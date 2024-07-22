package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"runtime"
	"sync"
	"time"

	"go.sia.tech/jape"
	"lukechampine.com/frand"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/syncer"
	"go.sia.tech/walletd/build"
	"go.sia.tech/walletd/wallet"
)

type (
	// A ChainManager manages blockchain and txpool state.
	ChainManager interface {
		UpdatesSince(types.ChainIndex, int) ([]chain.RevertUpdate, []chain.ApplyUpdate, error)
		BestIndex(height uint64) (types.ChainIndex, bool)
		TipState() consensus.State
		AddBlocks([]types.Block) error
		RecommendedFee() types.Currency
		PoolTransactions() []types.Transaction
		V2PoolTransactions() []types.V2Transaction
		AddPoolTransactions(txns []types.Transaction) (bool, error)
		AddV2PoolTransactions(index types.ChainIndex, txns []types.V2Transaction) (bool, error)
		UnconfirmedParents(txn types.Transaction) []types.Transaction
	}

	// A Syncer can connect to other peers and synchronize the blockchain.
	Syncer interface {
		Addr() string
		Peers() []*syncer.Peer
		PeerInfo(addr string) (syncer.PeerInfo, error)
		Connect(ctx context.Context, addr string) (*syncer.Peer, error)
		BroadcastHeader(bh gateway.BlockHeader)
		BroadcastTransactionSet(txns []types.Transaction)
		BroadcastV2TransactionSet(index types.ChainIndex, txns []types.V2Transaction)
		BroadcastV2BlockOutline(bo gateway.V2BlockOutline)
	}

	// A WalletManager manages wallets, keyed by name.
	WalletManager interface {
		IndexMode() wallet.IndexMode
		Tip() (types.ChainIndex, error)
		Scan(_ context.Context, index types.ChainIndex) error

		AddWallet(wallet.Wallet) (wallet.Wallet, error)
		UpdateWallet(wallet.Wallet) (wallet.Wallet, error)
		DeleteWallet(wallet.ID) error
		Wallets() ([]wallet.Wallet, error)

		AddAddress(id wallet.ID, addr wallet.Address) error
		RemoveAddress(id wallet.ID, addr types.Address) error
		Addresses(id wallet.ID) ([]wallet.Address, error)
		WalletEvents(id wallet.ID, offset, limit int) ([]wallet.Event, error)
		WalletUnconfirmedEvents(id wallet.ID) ([]wallet.Event, error)
		UnspentSiacoinOutputs(id wallet.ID, offset, limit int) ([]types.SiacoinElement, error)
		UnspentSiafundOutputs(id wallet.ID, offset, limit int) ([]types.SiafundElement, error)
		WalletBalance(id wallet.ID) (wallet.Balance, error)

		AddressBalance(address types.Address) (wallet.Balance, error)
		AddressEvents(address types.Address, offset, limit int) ([]wallet.Event, error)
		AddressUnconfirmedEvents(address types.Address) ([]wallet.Event, error)
		AddressSiacoinOutputs(address types.Address, offset, limit int) ([]types.SiacoinElement, error)
		AddressSiafundOutputs(address types.Address, offset, limit int) ([]types.SiafundElement, error)

		SiacoinElement(types.SiacoinOutputID) (types.SiacoinElement, error)
		SiafundElement(types.SiafundOutputID) (types.SiafundElement, error)

		Events(eventIDs []types.Hash256) ([]wallet.Event, error)
		Reserve(ids []types.Hash256, duration time.Duration) error
	}
)

type server struct {
	startTime time.Time
	chainMode string

	cm ChainManager
	s  Syncer
	wm WalletManager

	// for walletsReserveHandler
	mu   sync.Mutex
	used map[types.Hash256]bool

	scanMu         sync.Mutex // for resubscribe
	scanInProgress bool
	scanInfo       RescanResponse
}

func (s *server) stateHandler(jc jape.Context) {
	jc.Encode(StateResponse{
		Version:   build.Version(),
		Commit:    build.Commit(),
		OS:        runtime.GOOS,
		BuildTime: build.Time(),
		StartTime: s.startTime,
		IndexMode: s.wm.IndexMode(),
		ChainMode: s.chainMode,
	})
}

func (s *server) consensusNetworkHandler(jc jape.Context) {
	jc.Encode(*s.cm.TipState().Network)
}

func (s *server) consensusTipHandler(jc jape.Context) {
	jc.Encode(s.cm.TipState().Index)
}

func (s *server) consensusTipStateHandler(jc jape.Context) {
	jc.Encode(s.cm.TipState())
}

func (s *server) consensusIndexHeightHandler(jc jape.Context) {
	var height uint64
	if jc.DecodeParam("height", &height) != nil {
		return
	}
	index, ok := s.cm.BestIndex(height)
	if !ok {
		jc.Error(errors.New("height not found"), http.StatusNotFound)
		return
	}
	jc.Encode(index)
}

func (s *server) consensusUpdatesIndexHandler(jc jape.Context) {
	var index types.ChainIndex
	if jc.DecodeParam("index", &index) != nil {
		return
	}

	limit := 10
	if jc.DecodeForm("limit", &limit) != nil {
		return
	} else if limit <= 0 || limit > 100 {
		jc.Error(errors.New("limit must be between 0 and 100"), http.StatusBadRequest)
		return
	}

	reverted, applied, err := s.cm.UpdatesSince(index, limit)
	if jc.Check("couldn't get updates", err) != nil {
		return
	}

	var res ConsensusUpdatesResponse
	for _, ru := range reverted {
		res.Reverted = append(res.Reverted, RevertUpdate{
			Update: ru.RevertUpdate,
			State:  ru.State,
			Block:  ru.Block,
		})
	}
	for _, au := range applied {
		res.Applied = append(res.Applied, ApplyUpdate{
			Update: au.ApplyUpdate,
			State:  au.State,
			Block:  au.Block,
		})
	}
	jc.Encode(res)
}

func (s *server) syncerPeersHandler(jc jape.Context) {
	var peers []GatewayPeer
	for _, p := range s.s.Peers() {
		// create peer response with known fields
		peer := GatewayPeer{
			Addr:    p.Addr(),
			Inbound: p.Inbound,
			Version: p.Version(),
		}
		//  add more info if available
		info, err := s.s.PeerInfo(p.Addr())
		if err != nil && !errors.Is(err, syncer.ErrPeerNotFound) {
			jc.Error(err, http.StatusInternalServerError)
			return
		} else if err == nil {
			peer.FirstSeen = info.FirstSeen
			peer.ConnectedSince = info.LastConnect
			peer.SyncedBlocks = info.SyncedBlocks
			peer.SyncDuration = info.SyncDuration
		}
		peers = append(peers, peer)
	}
	jc.Encode(peers)
}

func (s *server) syncerConnectHandler(jc jape.Context) {
	var addr string
	if jc.Decode(&addr) != nil {
		return
	}
	_, err := s.s.Connect(jc.Request.Context(), addr)
	jc.Check("couldn't connect to peer", err)
}

func (s *server) syncerBroadcastBlockHandler(jc jape.Context) {
	var b types.Block
	if jc.Decode(&b) != nil {
		return
	} else if jc.Check("block is invalid", s.cm.AddBlocks([]types.Block{b})) != nil {
		return
	}
	if b.V2 == nil {
		s.s.BroadcastHeader(gateway.BlockHeader{
			ParentID:   b.ParentID,
			Nonce:      b.Nonce,
			Timestamp:  b.Timestamp,
			MerkleRoot: b.MerkleRoot(),
		})
	} else {
		s.s.BroadcastV2BlockOutline(gateway.OutlineBlock(b, s.cm.PoolTransactions(), s.cm.V2PoolTransactions()))
	}
}

func (s *server) txpoolTransactionsHandler(jc jape.Context) {
	jc.Encode(TxpoolTransactionsResponse{
		Transactions:   s.cm.PoolTransactions(),
		V2Transactions: s.cm.V2PoolTransactions(),
	})
}

func (s *server) txpoolFeeHandler(jc jape.Context) {
	jc.Encode(s.cm.RecommendedFee())
}

func (s *server) txpoolBroadcastHandler(jc jape.Context) {
	var tbr TxpoolBroadcastRequest
	if jc.Decode(&tbr) != nil {
		return
	}
	if len(tbr.Transactions) != 0 {
		_, err := s.cm.AddPoolTransactions(tbr.Transactions)
		if jc.Check("invalid transaction set", err) != nil {
			return
		}
		s.s.BroadcastTransactionSet(tbr.Transactions)
	}
	if len(tbr.V2Transactions) != 0 {
		index := s.cm.TipState().Index
		_, err := s.cm.AddV2PoolTransactions(index, tbr.V2Transactions)
		if jc.Check("invalid v2 transaction set", err) != nil {
			return
		}
		s.s.BroadcastV2TransactionSet(index, tbr.V2Transactions)
	}
}

func (s *server) txpoolParentsHandler(jc jape.Context) {
	var txn types.Transaction
	if jc.Decode(&txn) != nil {
		return
	}

	jc.Encode(s.cm.UnconfirmedParents(txn))
}

func (s *server) walletsHandler(jc jape.Context) {
	wallets, err := s.wm.Wallets()
	if jc.Check("couldn't load wallets", err) != nil {
		return
	}
	jc.Encode(wallets)
}

func (s *server) walletsHandlerPOST(jc jape.Context) {
	var req WalletUpdateRequest
	if jc.Decode(&req) != nil {
		return
	}
	w := wallet.Wallet{
		Name:        req.Name,
		Description: req.Description,
		Metadata:    req.Metadata,
	}

	w, err := s.wm.AddWallet(w)
	if jc.Check("couldn't add wallet", err) != nil {
		return
	}
	jc.Encode(w)
}

func (s *server) walletsIDHandlerPOST(jc jape.Context) {
	var id wallet.ID
	var req WalletUpdateRequest
	if jc.DecodeParam("id", &id) != nil || jc.Decode(&req) != nil {
		return
	}
	w := wallet.Wallet{
		ID:          id,
		Name:        req.Name,
		Description: req.Description,
		Metadata:    req.Metadata,
	}

	w, err := s.wm.UpdateWallet(w)
	if errors.Is(err, wallet.ErrNotFound) {
		jc.Error(err, http.StatusNotFound)
		return
	} else if jc.Check("couldn't update wallet", err) != nil {
		return
	}
	jc.Encode(w)
}

func (s *server) walletsIDHandlerDELETE(jc jape.Context) {
	var id wallet.ID
	if jc.DecodeParam("id", &id) != nil {
		return
	}
	err := s.wm.DeleteWallet(id)
	if errors.Is(err, wallet.ErrNotFound) {
		jc.Error(err, http.StatusNotFound)
	} else if jc.Check("couldn't remove wallet", err) != nil {
		return
	}
}

func (s *server) rescanHandlerGET(jc jape.Context) {
	index, err := s.wm.Tip()
	if jc.Check("couldn't get tip", err) != nil {
		return
	}

	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	s.scanInfo.Index = index
	jc.Encode(s.scanInfo)
}

func (s *server) rescanHandlerPOST(jc jape.Context) {
	var height uint64
	if jc.Decode(&height) != nil {
		return
	}

	s.scanMu.Lock()
	defer s.scanMu.Unlock()

	if s.scanInProgress {
		jc.Error(errors.New("scan already in progress"), http.StatusConflict)
		return
	}

	var index types.ChainIndex
	if height > 0 {
		var ok bool
		index, ok = s.cm.BestIndex(height)
		if !ok {
			jc.Error(errors.New("height not found"), http.StatusNotFound)
			return
		}
	}

	s.scanInProgress = true
	s.scanInfo = RescanResponse{
		StartIndex: index,
		Index:      index,
		StartTime:  time.Now(),
		Error:      nil,
	}

	go func() {
		err := s.wm.Scan(context.Background(), index)

		// update the scan state
		s.scanMu.Lock()
		defer s.scanMu.Unlock()
		s.scanInProgress = false
		if err != nil {
			msg := err.Error()
			s.scanInfo.Error = &msg
		}
	}()
}

func (s *server) walletsAddressHandlerPUT(jc jape.Context) {
	var id wallet.ID
	var addr wallet.Address
	if jc.DecodeParam("id", &id) != nil || jc.Decode(&addr) != nil {
		return
	} else if jc.Check("couldn't add address", s.wm.AddAddress(id, addr)) != nil {
		return
	}
}

func (s *server) walletsAddressHandlerDELETE(jc jape.Context) {
	var id wallet.ID
	var addr types.Address
	if jc.DecodeParam("id", &id) != nil || jc.DecodeParam("addr", &addr) != nil {
		return
	}

	err := s.wm.RemoveAddress(id, addr)
	if errors.Is(err, wallet.ErrNotFound) {
		jc.Error(err, http.StatusNotFound)
	} else if jc.Check("couldn't remove address", err) != nil {
		return
	}
}

func (s *server) walletsAddressesHandlerGET(jc jape.Context) {
	var id wallet.ID
	if jc.DecodeParam("id", &id) != nil {
		return
	}
	addrs, err := s.wm.Addresses(id)
	if jc.Check("couldn't load addresses", err) != nil {
		return
	}
	jc.Encode(addrs)
}

func (s *server) walletsBalanceHandler(jc jape.Context) {
	var id wallet.ID
	if jc.DecodeParam("id", &id) != nil {
		return
	}

	b, err := s.wm.WalletBalance(id)
	if errors.Is(err, wallet.ErrNotFound) {
		jc.Error(err, http.StatusNotFound)
		return
	} else if jc.Check("couldn't load balance", err) != nil {
		return
	}
	jc.Encode(BalanceResponse(b))
}

func (s *server) walletsEventsHandler(jc jape.Context) {
	var id wallet.ID
	offset, limit := 0, 500
	if jc.DecodeParam("id", &id) != nil || jc.DecodeForm("offset", &offset) != nil || jc.DecodeForm("limit", &limit) != nil {
		return
	}
	events, err := s.wm.WalletEvents(id, offset, limit)
	if errors.Is(err, wallet.ErrNotFound) {
		jc.Error(err, http.StatusNotFound)
		return
	} else if jc.Check("couldn't load events", err) != nil {
		return
	}
	jc.Encode(events)
}

func (s *server) walletsEventsUnconfirmedHandlerGET(jc jape.Context) {
	var id wallet.ID
	if jc.DecodeParam("id", &id) != nil {
		return
	}

	events, err := s.wm.WalletUnconfirmedEvents(id)
	if errors.Is(err, wallet.ErrNotFound) {
		jc.Error(err, http.StatusNotFound)
		return
	} else if err != nil {
		jc.Error(err, http.StatusInternalServerError)
		return
	}
	jc.Encode(events)
}

func (s *server) walletsOutputsSiacoinHandler(jc jape.Context) {
	var id wallet.ID
	if jc.DecodeParam("id", &id) != nil {
		return
	}

	offset, limit := 0, 1000
	if jc.DecodeForm("offset", &offset) != nil || jc.DecodeForm("limit", &limit) != nil {
		return
	}

	scos, err := s.wm.UnspentSiacoinOutputs(id, offset, limit)
	if jc.Check("couldn't load siacoin outputs", err) != nil {
		return
	}

	jc.Encode(scos)
}

func (s *server) walletsOutputsSiafundHandler(jc jape.Context) {
	var id wallet.ID
	if jc.DecodeParam("id", &id) != nil {
		return
	}

	offset, limit := 0, 1000
	if jc.DecodeForm("offset", &offset) != nil || jc.DecodeForm("limit", &limit) != nil {
		return
	}

	sfos, err := s.wm.UnspentSiafundOutputs(id, offset, limit)
	if jc.Check("couldn't load siacoin outputs", err) != nil {
		return
	}
	jc.Encode(sfos)
}

func (s *server) walletsReserveHandler(jc jape.Context) {
	var wrr WalletReserveRequest
	if jc.Decode(&wrr) != nil {
		return
	}

	ids := make([]types.Hash256, 0, len(wrr.SiacoinOutputs)+len(wrr.SiafundOutputs))
	for _, id := range wrr.SiacoinOutputs {
		ids = append(ids, types.Hash256(id))
	}

	for _, id := range wrr.SiafundOutputs {
		ids = append(ids, types.Hash256(id))
	}

	if jc.Check("couldn't reserve outputs", s.wm.Reserve(ids, wrr.Duration)) != nil {
		return
	}
}

func (s *server) walletsReleaseHandler(jc jape.Context) {
	var name string
	var wrr WalletReleaseRequest
	if jc.DecodeParam("name", &name) != nil || jc.Decode(&wrr) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range wrr.SiacoinOutputs {
		delete(s.used, types.Hash256(id))
	}
	for _, id := range wrr.SiafundOutputs {
		delete(s.used, types.Hash256(id))
	}
}

func (s *server) walletsFundHandler(jc jape.Context) {
	fundTxn := func(txn *types.Transaction, amount types.Currency, utxos []types.SiacoinElement, changeAddr types.Address, pool []types.Transaction) ([]types.Hash256, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if amount.IsZero() {
			return nil, nil
		}
		inPool := make(map[types.Hash256]bool)
		for _, ptxn := range pool {
			for _, in := range ptxn.SiacoinInputs {
				inPool[types.Hash256(in.ParentID)] = true
			}
		}
		frand.Shuffle(len(utxos), reflect.Swapper(utxos))
		var outputSum types.Currency
		var fundingElements []types.SiacoinElement
		for _, sce := range utxos {
			if s.used[types.Hash256(sce.ID)] || inPool[types.Hash256(sce.ID)] {
				continue
			}
			fundingElements = append(fundingElements, sce)
			outputSum = outputSum.Add(sce.SiacoinOutput.Value)
			if outputSum.Cmp(amount) >= 0 {
				break
			}
		}
		if outputSum.Cmp(amount) < 0 {
			return nil, errors.New("insufficient balance")
		} else if outputSum.Cmp(amount) > 0 {
			if changeAddr == types.VoidAddress {
				return nil, errors.New("change address must be specified")
			}
			txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.SiacoinOutput{
				Value:   outputSum.Sub(amount),
				Address: changeAddr,
			})
		}

		toSign := make([]types.Hash256, len(fundingElements))
		for i, sce := range fundingElements {
			txn.SiacoinInputs = append(txn.SiacoinInputs, types.SiacoinInput{
				ParentID: types.SiacoinOutputID(sce.ID),
				// UnlockConditions left empty for client to fill in
			})
			toSign[i] = types.Hash256(sce.ID)
			s.used[types.Hash256(sce.ID)] = true
		}

		return toSign, nil
	}

	var id wallet.ID
	var wfr WalletFundRequest
	if jc.DecodeParam("id", &id) != nil || jc.Decode(&wfr) != nil {
		return
	}
	utxos, err := s.wm.UnspentSiacoinOutputs(id, 0, 1000)
	if jc.Check("couldn't get utxos to fund transaction", err) != nil {
		return
	}

	txn := wfr.Transaction
	toSign, err := fundTxn(&txn, wfr.Amount, utxos, wfr.ChangeAddress, s.cm.PoolTransactions())
	if jc.Check("couldn't fund transaction", err) != nil {
		return
	}
	jc.Encode(WalletFundResponse{
		Transaction: txn,
		ToSign:      toSign,
		DependsOn:   s.cm.UnconfirmedParents(txn),
	})
}

func (s *server) walletsFundSFHandler(jc jape.Context) {
	fundTxn := func(txn *types.Transaction, amount uint64, utxos []types.SiafundElement, changeAddr, claimAddr types.Address, pool []types.Transaction) ([]types.Hash256, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if amount == 0 {
			return nil, nil
		}
		inPool := make(map[types.Hash256]bool)
		for _, ptxn := range pool {
			for _, in := range ptxn.SiafundInputs {
				inPool[types.Hash256(in.ParentID)] = true
			}
		}
		frand.Shuffle(len(utxos), reflect.Swapper(utxos))
		var outputSum uint64
		var fundingElements []types.SiafundElement
		for _, sfe := range utxos {
			if s.used[types.Hash256(sfe.ID)] || inPool[types.Hash256(sfe.ID)] {
				continue
			}
			fundingElements = append(fundingElements, sfe)
			outputSum += sfe.SiafundOutput.Value
			if outputSum >= amount {
				break
			}
		}
		if outputSum < amount {
			return nil, errors.New("insufficient balance")
		} else if outputSum > amount {
			if changeAddr == types.VoidAddress {
				return nil, errors.New("change address must be specified")
			}
			txn.SiafundOutputs = append(txn.SiafundOutputs, types.SiafundOutput{
				Value:   outputSum - amount,
				Address: changeAddr,
			})
		}

		toSign := make([]types.Hash256, len(fundingElements))
		for i, sfe := range fundingElements {
			txn.SiafundInputs = append(txn.SiafundInputs, types.SiafundInput{
				ParentID:     types.SiafundOutputID(sfe.ID),
				ClaimAddress: claimAddr,
				// UnlockConditions left empty for client to fill in
			})
			toSign[i] = types.Hash256(sfe.ID)
			s.used[types.Hash256(sfe.ID)] = true
		}

		return toSign, nil
	}

	var id wallet.ID
	var wfr WalletFundSFRequest
	if jc.DecodeParam("id", &id) != nil || jc.Decode(&wfr) != nil {
		return
	}
	utxos, err := s.wm.UnspentSiafundOutputs(id, 0, 1000)
	if jc.Check("couldn't get utxos to fund transaction", err) != nil {
		return
	}

	txn := wfr.Transaction
	toSign, err := fundTxn(&txn, wfr.Amount, utxos, wfr.ChangeAddress, wfr.ClaimAddress, s.cm.PoolTransactions())
	if jc.Check("couldn't fund transaction", err) != nil {
		return
	}
	jc.Encode(WalletFundResponse{
		Transaction: txn,
		ToSign:      toSign,
		DependsOn:   s.cm.UnconfirmedParents(txn),
	})
}

func (s *server) addressesAddrBalanceHandler(jc jape.Context) {
	var addr types.Address
	if jc.DecodeParam("addr", &addr) != nil {
		return
	}
	b, err := s.wm.AddressBalance(addr)
	if jc.Check("couldn't load balance", err) != nil {
		return
	}
	jc.Encode(BalanceResponse(b))
}

func (s *server) addressesAddrEventsHandlerGET(jc jape.Context) {
	var addr types.Address
	if jc.DecodeParam("addr", &addr) != nil {
		return
	}

	offset, limit := 0, 1000
	if jc.DecodeForm("offset", &offset) != nil || jc.DecodeForm("limit", &limit) != nil {
		return
	}

	events, err := s.wm.AddressEvents(addr, offset, limit)
	if jc.Check("couldn't load events", err) != nil {
		return
	}
	jc.Encode(events)
}

func (s *server) addressesAddrEventsUnconfirmedHandlerGET(jc jape.Context) {
	var addr types.Address
	if jc.DecodeParam("addr", &addr) != nil {
		return
	}

	events, err := s.wm.AddressUnconfirmedEvents(addr)
	if jc.Check("couldn't load events", err) != nil {
		return
	}
	jc.Encode(events)
}

func (s *server) addressesAddrOutputsSCHandler(jc jape.Context) {
	var addr types.Address
	if jc.DecodeParam("addr", &addr) != nil {
		return
	}

	offset, limit := 0, 1000
	if jc.DecodeForm("offset", &offset) != nil || jc.DecodeForm("limit", &limit) != nil {
		return
	}

	utxos, err := s.wm.AddressSiacoinOutputs(addr, offset, limit)
	if jc.Check("couldn't load utxos", err) != nil {
		return
	}
	jc.Encode(utxos)
}

func (s *server) addressesAddrOutputsSFHandler(jc jape.Context) {
	var addr types.Address
	if jc.DecodeParam("addr", &addr) != nil {
		return
	}

	offset, limit := 0, 1000
	if jc.DecodeForm("offset", &offset) != nil || jc.DecodeForm("limit", &limit) != nil {
		return
	}

	utxos, err := s.wm.AddressSiafundOutputs(addr, offset, limit)
	if jc.Check("couldn't load utxos", err) != nil {
		return
	}
	jc.Encode(utxos)
}

func (s *server) eventsHandlerGET(jc jape.Context) {
	var eventID types.Hash256
	if jc.DecodeParam("id", &eventID) != nil {
		return
	}
	events, err := s.wm.Events([]types.Hash256{eventID})
	if jc.Check("couldn't load events", err) != nil {
		return
	} else if len(events) == 0 {
		jc.Error(errors.New("event not found"), http.StatusNotFound)
		return
	}
	jc.Encode(events[0])
}

func (s *server) outputsSiacoinHandlerGET(jc jape.Context) {
	var outputID types.SiacoinOutputID
	if jc.DecodeParam("id", &outputID) != nil {
		return
	}

	output, err := s.wm.SiacoinElement(outputID)
	if jc.Check("couldn't load output", err) != nil {
		return
	}
	jc.Encode(output)
}

func (s *server) outputsSiafundHandlerGET(jc jape.Context) {
	var outputID types.SiafundOutputID
	if jc.DecodeParam("id", &outputID) != nil {
		return
	}

	output, err := s.wm.SiafundElement(outputID)
	if jc.Check("couldn't load output", err) != nil {
		return
	}
	jc.Encode(output)
}

// NewServer returns an HTTP handler that serves the walletd API.
func NewServer(opts ...ServerOption) http.Handler {
	srv := &server{
		startTime: time.Now(),
		used:      make(map[types.Hash256]bool),
	}

	for _, opt := range opts {
		opt(srv)
	}

	return jape.Mux(map[string]jape.Handler{
		"GET /state": srv.stateHandler,

		"GET /consensus/network":        srv.consensusNetworkHandler,
		"GET /consensus/tip":            srv.consensusTipHandler,
		"GET /consensus/tipstate":       srv.consensusTipStateHandler,
		"GET /consensus/updates/:index": srv.consensusUpdatesIndexHandler,
		"GET /consensus/index/:height":  srv.consensusIndexHeightHandler,

		"GET /syncer/peers":            srv.syncerPeersHandler,
		"POST /syncer/connect":         srv.syncerConnectHandler,
		"POST /syncer/broadcast/block": srv.syncerBroadcastBlockHandler,

		"GET /txpool/transactions": srv.txpoolTransactionsHandler,
		"GET /txpool/fee":          srv.txpoolFeeHandler,
		"POST /txpool/broadcast":   srv.txpoolBroadcastHandler,
		"POST /txpool/parents":     srv.txpoolParentsHandler,

		"GET /rescan":  srv.rescanHandlerGET,
		"POST /rescan": srv.rescanHandlerPOST,

		"GET /wallets":                        srv.walletsHandler,
		"POST /wallets":                       srv.walletsHandlerPOST,
		"POST /wallets/:id":                   srv.walletsIDHandlerPOST,
		"DELETE	/wallets/:id":                 srv.walletsIDHandlerDELETE,
		"PUT /wallets/:id/addresses":          srv.walletsAddressHandlerPUT,
		"DELETE /wallets/:id/addresses/:addr": srv.walletsAddressHandlerDELETE,
		"GET /wallets/:id/addresses":          srv.walletsAddressesHandlerGET,
		"GET /wallets/:id/balance":            srv.walletsBalanceHandler,
		"GET /wallets/:id/events":             srv.walletsEventsHandler,
		"GET /wallets/:id/events/unconfirmed": srv.walletsEventsUnconfirmedHandlerGET,
		"GET /wallets/:id/outputs/siacoin":    srv.walletsOutputsSiacoinHandler,
		"GET /wallets/:id/outputs/siafund":    srv.walletsOutputsSiafundHandler,
		"POST /wallets/:id/reserve":           srv.walletsReserveHandler,
		"POST /wallets/:id/release":           srv.walletsReleaseHandler,
		"POST /wallets/:id/fund":              srv.walletsFundHandler,
		"POST /wallets/:id/fundsf":            srv.walletsFundSFHandler,

		"GET /addresses/:addr/balance":            srv.addressesAddrBalanceHandler,
		"GET /addresses/:addr/events":             srv.addressesAddrEventsHandlerGET,
		"GET /addresses/:addr/events/unconfirmed": srv.addressesAddrEventsUnconfirmedHandlerGET,
		"GET /addresses/:addr/outputs/siacoin":    srv.addressesAddrOutputsSCHandler,
		"GET /addresses/:addr/outputs/siafund":    srv.addressesAddrOutputsSFHandler,

		"GET /outputs/siacoin/:id": srv.outputsSiacoinHandlerGET,
		"GET /outputs/siafund/:id": srv.outputsSiafundHandlerGET,

		"GET /events/:id": srv.eventsHandlerGET,
	})
}
