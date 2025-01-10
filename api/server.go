package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/pprof"
	"runtime"
	"sync"
	"time"

	"go.sia.tech/jape"
	"go.uber.org/zap"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/syncer"
	"go.sia.tech/walletd/build"
	"go.sia.tech/walletd/wallet"
)

// A ServerOption sets an optional parameter for the server.
type ServerOption func(*server)

// WithLogger sets the logger used by the server.
func WithLogger(log *zap.Logger) ServerOption {
	return func(s *server) {
		s.log = log
	}
}

// WithDebug enables debug endpoints.
func WithDebug() ServerOption {
	return func(s *server) {
		s.debugEnabled = true
	}
}

// WithPublicEndpoints sets whether the server should disable authentication
// on endpoints that are safe for use when running walletd as a service.
func WithPublicEndpoints(public bool) ServerOption {
	return func(s *server) {
		s.publicEndpoints = public
	}
}

// WithBasicAuth sets the password for basic authentication.
func WithBasicAuth(password string) ServerOption {
	return func(s *server) {
		s.password = password
	}
}

type (
	// A ChainManager manages blockchain and txpool state.
	ChainManager interface {
		UpdatesSince(types.ChainIndex, int) ([]chain.RevertUpdate, []chain.ApplyUpdate, error)

		Tip() types.ChainIndex
		BestIndex(height uint64) (types.ChainIndex, bool)
		TipState() consensus.State
		AddBlocks([]types.Block) error
		RecommendedFee() types.Currency
		PoolTransactions() []types.Transaction
		V2PoolTransactions() []types.V2Transaction
		AddPoolTransactions(txns []types.Transaction) (bool, error)
		AddV2PoolTransactions(index types.ChainIndex, txns []types.V2Transaction) (bool, error)
		UnconfirmedParents(txn types.Transaction) []types.Transaction
		UpdateV2TransactionSet(txns []types.V2Transaction, from types.ChainIndex, to types.ChainIndex) ([]types.V2Transaction, error)
	}

	// A Syncer can connect to other peers and synchronize the blockchain.
	Syncer interface {
		Addr() string
		Peers() []*syncer.Peer
		PeerInfo(addr string) (syncer.PeerInfo, error)
		Connect(ctx context.Context, addr string) (*syncer.Peer, error)
		BroadcastHeader(types.BlockHeader)
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
		WalletAddress(wallet.ID, types.Address) (wallet.Address, error)
		WalletEvents(id wallet.ID, offset, limit int) ([]wallet.Event, error)
		WalletUnconfirmedEvents(id wallet.ID) ([]wallet.Event, error)
		SelectSiacoinElements(walletID wallet.ID, amount types.Currency, useUnconfirmed bool) ([]types.SiacoinElement, types.ChainIndex, types.Currency, error)
		SelectSiafundElements(walletID wallet.ID, amount uint64) ([]types.SiafundElement, types.ChainIndex, uint64, error)
		UnspentSiacoinOutputs(id wallet.ID, offset, limit int) ([]types.SiacoinElement, types.ChainIndex, error)
		UnspentSiafundOutputs(id wallet.ID, offset, limit int) ([]types.SiafundElement, types.ChainIndex, error)
		WalletBalance(id wallet.ID) (wallet.Balance, error)

		AddressBalance(address types.Address) (wallet.Balance, error)
		AddressEvents(address types.Address, offset, limit int) ([]wallet.Event, error)
		AddressUnconfirmedEvents(address types.Address) ([]wallet.Event, error)
		AddressSiacoinOutputs(address types.Address, offset, limit int) ([]types.SiacoinElement, error)
		AddressSiafundOutputs(address types.Address, offset, limit int) ([]types.SiafundElement, error)

		Events(eventIDs []types.Hash256) ([]wallet.Event, error)

		SiacoinElement(types.SiacoinOutputID) (types.SiacoinElement, error)
		SiafundElement(types.SiafundOutputID) (types.SiafundElement, error)

		Reserve([]types.Hash256) error
		Release([]types.Hash256)
	}
)

type server struct {
	startTime       time.Time
	debugEnabled    bool
	publicEndpoints bool
	password        string

	log *zap.Logger
	cm  ChainManager
	s   Syncer
	wm  WalletManager

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
			Address: p.Addr(),
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
	if jc.Check("couldn't connect to peer", err) != nil {
		return
	}
	jc.EmptyResonse()
}

func (s *server) syncerBroadcastBlockHandler(jc jape.Context) {
	var b types.Block
	if jc.Decode(&b) != nil {
		return
	} else if jc.Check("block is invalid", s.cm.AddBlocks([]types.Block{b})) != nil {
		return
	}
	if b.V2 == nil {
		s.s.BroadcastHeader(b.Header())
	} else {
		s.s.BroadcastV2BlockOutline(gateway.OutlineBlock(b, s.cm.PoolTransactions(), s.cm.V2PoolTransactions()))
	}
	jc.EmptyResonse()
}

func (s *server) txpoolParentsHandler(jc jape.Context) {
	var txn types.Transaction
	if jc.Decode(&txn) != nil {
		return
	}

	jc.Encode(s.cm.UnconfirmedParents(txn))
}

func (s *server) txpoolTransactionsHandler(jc jape.Context) {
	jc.Encode(TxpoolTransactionsResponse{
		Basis:          s.cm.Tip(),
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
		if err != nil {
			jc.Error(fmt.Errorf("invalid transaction set: %w", err), http.StatusBadRequest)
			return
		}
		s.s.BroadcastTransactionSet(tbr.Transactions)
	}
	if len(tbr.V2Transactions) != 0 {
		_, err := s.cm.AddV2PoolTransactions(tbr.Basis, tbr.V2Transactions)
		if err != nil {
			jc.Error(fmt.Errorf("invalid v2 transaction set: %w", err), http.StatusBadRequest)
			return
		}
		s.s.BroadcastV2TransactionSet(tbr.Basis, tbr.V2Transactions)
	}

	jc.EmptyResonse()
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
	jc.EmptyResonse()
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

	jc.EmptyResonse()
}

func (s *server) walletsAddressHandlerPUT(jc jape.Context) {
	var id wallet.ID
	var addr wallet.Address
	if jc.DecodeParam("id", &id) != nil || jc.Decode(&addr) != nil {
		return
	} else if jc.Check("couldn't add address", s.wm.AddAddress(id, addr)) != nil {
		return
	}
	jc.EmptyResonse()
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
	jc.EmptyResonse()
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

	scos, _, err := s.wm.UnspentSiacoinOutputs(id, offset, limit)
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

	sfos, _, err := s.wm.UnspentSiafundOutputs(id, offset, limit)
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

	if jc.Check("couldn't reserve outputs", s.wm.Reserve(ids)) != nil {
		return
	}
	jc.EmptyResonse()
}

func (s *server) walletsReleaseHandler(jc jape.Context) {
	var wrr WalletReleaseRequest
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
	s.wm.Release(ids)
	jc.EmptyResonse()
}

func (s *server) walletsFundHandler(jc jape.Context) {
	var id wallet.ID
	var wfr WalletFundRequest
	if jc.DecodeParam("id", &id) != nil || jc.Decode(&wfr) != nil {
		return
	}
	utxos, _, change, err := s.wm.SelectSiacoinElements(id, wfr.Amount, false)
	if jc.Check("couldn't get utxos to fund transaction", err) != nil {
		return
	}

	txn := wfr.Transaction
	if !change.IsZero() {
		if wfr.ChangeAddress == types.VoidAddress {
			jc.Error(errors.New("change address must be specified"), http.StatusBadRequest)
			return
		}

		txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.SiacoinOutput{
			Value:   change,
			Address: wfr.ChangeAddress,
		})
	}

	toSign := make([]types.Hash256, 0, len(utxos))
	for _, sce := range utxos {
		txn.SiacoinInputs = append(txn.SiacoinInputs, types.SiacoinInput{
			ParentID: sce.ID,
			// UnlockConditions left empty for client to fill in
		})
		toSign = append(toSign, types.Hash256(sce.ID))
	}

	jc.Encode(WalletFundResponse{
		Transaction: txn,
		ToSign:      toSign,
		DependsOn:   s.cm.UnconfirmedParents(txn),
	})
}

func (s *server) walletsFundSFHandler(jc jape.Context) {
	var id wallet.ID
	var wfr WalletFundSFRequest
	if jc.DecodeParam("id", &id) != nil || jc.Decode(&wfr) != nil {
		return
	}
	utxos, _, change, err := s.wm.SelectSiafundElements(id, wfr.Amount)
	if jc.Check("couldn't get utxos to fund transaction", err) != nil {
		return
	}

	txn := wfr.Transaction
	if change > 0 {
		if wfr.ChangeAddress == types.VoidAddress {
			jc.Error(errors.New("change address must be specified"), http.StatusBadRequest)
			return
		}

		txn.SiafundOutputs = append(txn.SiafundOutputs, types.SiafundOutput{
			Value:   change,
			Address: wfr.ChangeAddress,
		})
	}

	toSign := make([]types.Hash256, 0, len(utxos))
	for _, sce := range utxos {
		txn.SiafundInputs = append(txn.SiafundInputs, types.SiafundInput{
			ParentID:     sce.ID,
			ClaimAddress: wfr.ChangeAddress,
			// UnlockConditions left empty for client to fill in
		})
		toSign = append(toSign, types.Hash256(sce.ID))
	}
	jc.Encode(WalletFundResponse{
		Transaction: txn,
		ToSign:      toSign,
		DependsOn:   s.cm.UnconfirmedParents(txn),
	})
}

func (s *server) walletsConstructHandler(jc jape.Context) {
	cs := s.cm.TipState()
	if cs.Index.Height >= cs.Network.HardforkV2.RequireHeight {
		jc.Error(errors.New("v1 transactions are not allowed after the v2 require height"), http.StatusBadRequest)
	}

	var walletID wallet.ID
	if err := jc.DecodeParam("id", &walletID); err != nil {
		return
	}

	_, err := s.wm.WalletBalance(walletID)
	if errors.Is(err, wallet.ErrNotFound) {
		jc.Error(err, http.StatusNotFound)
		return
	} else if jc.Check("failed to get wallet", err) != nil {
		return
	}

	var wcr WalletConstructRequest
	if err := jc.Decode(&wcr); err != nil {
		return
	}

	if wcr.ChangeAddress == types.VoidAddress {
		jc.Error(errors.New("change address must be specified"), http.StatusBadRequest)
		return
	}

	var siacoinInput types.Currency
	for i, sco := range wcr.Siacoins {
		switch {
		case sco.Value.IsZero():
			jc.Error(fmt.Errorf("siacoin output %d has zero value", i), http.StatusBadRequest)
			return
		case sco.Address == types.VoidAddress:
			jc.Error(fmt.Errorf("siacoin output %d has void address", i), http.StatusBadRequest)
			return
		}
		siacoinInput = siacoinInput.Add(sco.Value)
	}

	var siafundInput uint64
	for i, sfo := range wcr.Siafunds {
		switch {
		case sfo.Value == 0:
			jc.Error(fmt.Errorf("siafund output %d has zero value", i), http.StatusBadRequest)
			return
		case sfo.Address == types.VoidAddress:
			jc.Error(fmt.Errorf("siafund output %d has void address", i), http.StatusBadRequest)
			return
		}
		siafundInput += sfo.Value
	}

	if siacoinInput.IsZero() && siafundInput == 0 {
		jc.Error(errors.New("no inputs provided"), http.StatusBadRequest)
	}

	fee := s.cm.RecommendedFee().Mul64(2000) // use a const for simplicity

	var sent bool
	var locked []types.Hash256
	defer func() {
		if sent {
			return
		}
		s.wm.Release(locked)
	}()

	sces, basis, siacoinChange, err := s.wm.SelectSiacoinElements(walletID, siacoinInput.Add(fee), false)
	if err != nil {
		jc.Error(fmt.Errorf("failed to select siacoin elements: %w", err), http.StatusInternalServerError)
		return
	}
	for _, sce := range sces {
		locked = append(locked, types.Hash256(sce.ID))
	}

	if !siacoinChange.IsZero() {
		wcr.Siacoins = append(wcr.Siacoins, types.SiacoinOutput{
			Value:   siacoinChange,
			Address: wcr.ChangeAddress,
		})
	}

	sfes, _, siafundChange, err := s.wm.SelectSiafundElements(walletID, siafundInput)
	if err != nil {
		jc.Error(fmt.Errorf("failed to select siafund elements: %w", err), http.StatusInternalServerError)
		return
	}
	for _, sfe := range sfes {
		locked = append(locked, types.Hash256(sfe.ID))
	}

	if siafundChange > 0 {
		wcr.Siafunds = append(wcr.Siafunds, types.SiafundOutput{
			Value:   siafundChange,
			Address: wcr.ChangeAddress,
		})
	}

	knownAddresses := make(map[types.Address]wallet.Address)
	getAddress := func(addr types.Address) (wallet.Address, error) {
		if a, ok := knownAddresses[addr]; ok {
			return a, nil
		}
		a, err := s.wm.WalletAddress(walletID, addr)
		if err != nil {
			return wallet.Address{}, err
		}
		knownAddresses[addr] = a
		return a, nil
	}

	resp := WalletConstructResponse{
		Basis:        basis,
		EstimatedFee: fee,
	}

	txn := types.Transaction{
		MinerFees:      []types.Currency{fee},
		SiacoinInputs:  make([]types.SiacoinInput, 0, len(sces)),
		SiacoinOutputs: wcr.Siacoins,
		SiafundInputs:  make([]types.SiafundInput, 0, len(sfes)),
		SiafundOutputs: wcr.Siafunds,
	}

	for _, sce := range sces {
		addr, err := getAddress(sce.SiacoinOutput.Address)
		if err != nil {
			jc.Error(fmt.Errorf("failed to get address: %w", err), http.StatusInternalServerError)
			return
		}

		sci := types.SiacoinInput{
			ParentID: sce.ID,
		}

		if addr.SpendPolicy != nil {
			// best effort to fill unlock conditions
			uc, ok := addr.SpendPolicy.Type.(types.PolicyTypeUnlockConditions)
			if !ok {
				jc.Error(fmt.Errorf("address %q only unlock conditions are suppored in v1 transactions", addr.Address), http.StatusBadRequest)
				return
			}
			sci.UnlockConditions = types.UnlockConditions(uc)
		}

		txn.SiacoinInputs = append(txn.SiacoinInputs, sci)
		txn.Signatures = append(txn.Signatures, types.TransactionSignature{
			ParentID: types.Hash256(sce.ID),
			CoveredFields: types.CoveredFields{
				WholeTransaction: true,
			},
		})
	}

	for _, sfe := range sfes {
		addr, err := getAddress(sfe.SiafundOutput.Address)
		if err != nil {
			jc.Error(fmt.Errorf("failed to get address: %w", err), http.StatusInternalServerError)
			return
		}

		sfi := types.SiafundInput{
			ParentID:     sfe.ID,
			ClaimAddress: wcr.ChangeAddress,
		}
		if addr.SpendPolicy != nil {
			// best effort to fill unlock conditions
			uc, ok := addr.SpendPolicy.Type.(types.PolicyTypeUnlockConditions)
			if !ok {
				jc.Error(fmt.Errorf("address %q only unlock conditions are suppored in v1 transactions", addr.Address), http.StatusBadRequest)
				return
			}
			sfi.UnlockConditions = types.UnlockConditions(uc)
		}
		txn.SiafundInputs = append(txn.SiafundInputs, sfi)
		txn.Signatures = append(txn.Signatures, types.TransactionSignature{
			ParentID: types.Hash256(sfe.ID),
			CoveredFields: types.CoveredFields{
				WholeTransaction: true,
			},
		})
	}

	resp.ID = txn.ID()
	resp.Transaction = txn
	sent = true // locks are released in defer
	jc.Encode(resp)
}

func (s *server) walletsConstructV2Handler(jc jape.Context) {
	cs := s.cm.TipState()
	if cs.Index.Height < cs.Network.HardforkV2.AllowHeight {
		jc.Error(errors.New("v2 transactions are not allowed before the v2 allow height"), http.StatusBadRequest)
	}

	var walletID wallet.ID
	if err := jc.DecodeParam("id", &walletID); err != nil {
		return
	}

	_, err := s.wm.WalletBalance(walletID)
	if errors.Is(err, wallet.ErrNotFound) {
		jc.Error(err, http.StatusNotFound)
		return
	} else if jc.Check("failed to get wallet", err) != nil {
		return
	}

	var wcr WalletConstructRequest
	if err := jc.Decode(&wcr); err != nil {
		return
	}

	if wcr.ChangeAddress == types.VoidAddress {
		jc.Error(errors.New("change address must be specified"), http.StatusBadRequest)
		return
	}

	var siacoinInput types.Currency
	for i, sco := range wcr.Siacoins {
		switch {
		case sco.Value.IsZero():
			jc.Error(fmt.Errorf("siacoin output %d has zero value", i), http.StatusBadRequest)
			return
		case sco.Address == types.VoidAddress:
			jc.Error(fmt.Errorf("siacoin output %d has void address", i), http.StatusBadRequest)
			return
		}
		siacoinInput = siacoinInput.Add(sco.Value)
	}

	var siafundInput uint64
	for i, sfo := range wcr.Siafunds {
		switch {
		case sfo.Value == 0:
			jc.Error(fmt.Errorf("siafund output %d has zero value", i), http.StatusBadRequest)
			return
		case sfo.Address == types.VoidAddress:
			jc.Error(fmt.Errorf("siafund output %d has void address", i), http.StatusBadRequest)
			return
		}
		siafundInput += sfo.Value
	}

	if siacoinInput.IsZero() && siafundInput == 0 {
		jc.Error(errors.New("no inputs provided"), http.StatusBadRequest)
	}

	fee := s.cm.RecommendedFee().Mul64(2000) // use a const for simplicity

	var sent bool
	var locked []types.Hash256
	defer func() {
		if sent {
			return
		}
		s.wm.Release(locked)
	}()

	sces, basis, siacoinChange, err := s.wm.SelectSiacoinElements(walletID, siacoinInput.Add(fee), false)
	if err != nil {
		jc.Error(fmt.Errorf("failed to select siacoin elements: %w", err), http.StatusInternalServerError)
		return
	}
	for _, sce := range sces {
		locked = append(locked, types.Hash256(sce.ID))
	}

	if !siacoinChange.IsZero() {
		wcr.Siacoins = append(wcr.Siacoins, types.SiacoinOutput{
			Value:   siacoinChange,
			Address: wcr.ChangeAddress,
		})
	}

	sfes, sfBasis, siafundChange, err := s.wm.SelectSiafundElements(walletID, siafundInput)
	if err != nil {
		jc.Error(fmt.Errorf("failed to select siafund elements: %w", err), http.StatusInternalServerError)
		return
	}
	for _, sfe := range sfes {
		locked = append(locked, types.Hash256(sfe.ID))
	}

	if siafundChange > 0 {
		wcr.Siafunds = append(wcr.Siafunds, types.SiafundOutput{
			Value:   siafundChange,
			Address: wcr.ChangeAddress,
		})
	}

	knownAddresses := make(map[types.Address]wallet.Address)
	getAddress := func(addr types.Address) (wallet.Address, error) {
		if a, ok := knownAddresses[addr]; ok {
			return a, nil
		}
		a, err := s.wm.WalletAddress(walletID, addr)
		if err != nil {
			return wallet.Address{}, err
		}
		knownAddresses[addr] = a
		return a, nil
	}

	resp := WalletConstructV2Response{
		Basis:        basis,
		EstimatedFee: fee,
	}

	txn := types.V2Transaction{
		MinerFee:       fee,
		SiacoinInputs:  make([]types.V2SiacoinInput, 0, len(sces)),
		SiacoinOutputs: wcr.Siacoins,
		SiafundInputs:  make([]types.V2SiafundInput, 0, len(sfes)),
		SiafundOutputs: wcr.Siafunds,
	}

	// the siafund elements are added to the transaction first because `UpdateV2TransactionSet` takes
	// a V2 transaction as an argument. The Siacoin basis is our target because the transaction is
	// guaranteed to have a non-zero Siacoin basis while the Siafund basis will be zero when not
	// sending Siafunds.
	for _, sfe := range sfes {
		addr, err := getAddress(sfe.SiafundOutput.Address)
		if err != nil {
			jc.Error(fmt.Errorf("failed to get address: %w", err), http.StatusInternalServerError)
			return
		}

		sfi := types.V2SiafundInput{
			Parent:       sfe,
			ClaimAddress: wcr.ChangeAddress,
		}

		if addr.SpendPolicy != nil {
			// best effort to fill spend policy
			sfi.SatisfiedPolicy = types.SatisfiedPolicy{
				Policy: *addr.SpendPolicy,
			}
		}
		txn.SiafundInputs = append(txn.SiafundInputs, sfi)
	}

	if len(sfes) > 0 && basis != sfBasis {
		txnset, err := s.cm.UpdateV2TransactionSet([]types.V2Transaction{txn}, sfBasis, basis)
		if err != nil {
			jc.Error(fmt.Errorf("failed to update transaction set: %w", err), http.StatusInternalServerError)
			return
		}
		txn = txnset[0]
	}

	for _, sce := range sces {
		addr, err := getAddress(sce.SiacoinOutput.Address)
		if err != nil {
			jc.Error(fmt.Errorf("failed to get address: %w", err), http.StatusInternalServerError)
			return
		}

		sci := types.V2SiacoinInput{
			Parent: sce,
		}

		if addr.SpendPolicy != nil {
			// best effort to fill spend policy
			sci.SatisfiedPolicy = types.SatisfiedPolicy{
				Policy: *addr.SpendPolicy,
			}
		}
		txn.SiacoinInputs = append(txn.SiacoinInputs, sci)
	}

	resp.ID = txn.ID()
	resp.Transaction = txn
	sent = true // locks are released in defer
	jc.Encode(resp)
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

func (s *server) debugMineHandler(jc jape.Context) {
	var req DebugMineRequest
	if jc.Decode(&req) != nil {
		return
	}

	log := s.log.Named("miner")
	ctx := jc.Request.Context()

	for n := req.Blocks; n > 0; {
		b, err := mineBlock(ctx, s.cm, req.Address)
		if errors.Is(err, context.Canceled) {
			return
		} else if err != nil {
			log.Warn("failed to mine block", zap.Error(err))
		} else if err := s.cm.AddBlocks([]types.Block{b}); err != nil {
			log.Warn("failed to add block", zap.Error(err))
		}

		if b.V2 == nil {
			s.s.BroadcastHeader(b.Header())
		} else {
			s.s.BroadcastV2BlockOutline(gateway.OutlineBlock(b, s.cm.PoolTransactions(), s.cm.V2PoolTransactions()))
		}

		log.Debug("mined block", zap.Stringer("blockID", b.ID()))
		n--
	}
	jc.EmptyResonse()
}

func (s *server) pprofHandler(jc jape.Context) {
	var handler string
	if err := jc.DecodeParam("handler", &handler); err != nil {
		return
	}

	switch handler {
	case "cmdline":
		pprof.Cmdline(jc.ResponseWriter, jc.Request)
	case "profile":
		pprof.Profile(jc.ResponseWriter, jc.Request)
	case "symbol":
		pprof.Symbol(jc.ResponseWriter, jc.Request)
	case "trace":
		pprof.Trace(jc.ResponseWriter, jc.Request)
	default:
		pprof.Index(jc.ResponseWriter, jc.Request)
	}
	pprof.Index(jc.ResponseWriter, jc.Request)
}

// NewServer returns an HTTP handler that serves the walletd API.
func NewServer(cm ChainManager, s Syncer, wm WalletManager, opts ...ServerOption) http.Handler {
	srv := server{
		log:             zap.NewNop(),
		debugEnabled:    false,
		publicEndpoints: false,
		startTime:       time.Now(),

		cm: cm,
		s:  s,
		wm: wm,
	}
	for _, opt := range opts {
		opt(&srv)
	}

	// checkAuth checks the request for basic authentication.
	checkAuth := func(jc jape.Context) bool {
		if srv.password == "" {
			// unset password is equivalent to no auth
			return true
		}

		// verify auth header
		_, pass, ok := jc.Request.BasicAuth()
		if ok && pass == srv.password {
			return true
		}

		jc.Error(errors.New("unauthorized"), http.StatusUnauthorized)
		return false
	}

	// wrapAuthHandler wraps a jape handler with an authentication check.
	wrapAuthHandler := func(h jape.Handler) jape.Handler {
		return func(jc jape.Context) {
			if !checkAuth(jc) {
				return
			}
			h(jc)
		}
	}

	// wrapPublicAuthHandler wraps a jape handler with an authentication check
	// unless publicEndpoints is true.
	wrapPublicAuthHandler := func(h jape.Handler) jape.Handler {
		return func(jc jape.Context) {
			if !srv.publicEndpoints && !checkAuth(jc) {
				return
			}
			h(jc)
		}
	}

	handlers := map[string]jape.Handler{
		"GET /state": wrapPublicAuthHandler(srv.stateHandler),

		"GET /consensus/network":        wrapPublicAuthHandler(srv.consensusNetworkHandler),
		"GET /consensus/tip":            wrapPublicAuthHandler(srv.consensusTipHandler),
		"GET /consensus/tipstate":       wrapPublicAuthHandler(srv.consensusTipStateHandler),
		"GET /consensus/updates/:index": wrapPublicAuthHandler(srv.consensusUpdatesIndexHandler),
		"GET /consensus/index/:height":  wrapPublicAuthHandler(srv.consensusIndexHeightHandler),

		"POST /syncer/connect":         wrapAuthHandler(srv.syncerConnectHandler),
		"GET /syncer/peers":            wrapPublicAuthHandler(srv.syncerPeersHandler),
		"POST /syncer/broadcast/block": wrapPublicAuthHandler(srv.syncerBroadcastBlockHandler),

		"GET /txpool/transactions": wrapPublicAuthHandler(srv.txpoolTransactionsHandler),
		"GET /txpool/fee":          wrapPublicAuthHandler(srv.txpoolFeeHandler),
		"POST /txpool/parents":     wrapPublicAuthHandler(srv.txpoolParentsHandler),
		"POST /txpool/broadcast":   wrapPublicAuthHandler(srv.txpoolBroadcastHandler),

		"GET /addresses/:addr/balance":            wrapPublicAuthHandler(srv.addressesAddrBalanceHandler),
		"GET /addresses/:addr/events":             wrapPublicAuthHandler(srv.addressesAddrEventsHandlerGET),
		"GET /addresses/:addr/events/unconfirmed": wrapPublicAuthHandler(srv.addressesAddrEventsUnconfirmedHandlerGET),
		"GET /addresses/:addr/outputs/siacoin":    wrapPublicAuthHandler(srv.addressesAddrOutputsSCHandler),
		"GET /addresses/:addr/outputs/siafund":    wrapPublicAuthHandler(srv.addressesAddrOutputsSFHandler),

		"GET /outputs/siacoin/:id": wrapPublicAuthHandler(srv.outputsSiacoinHandlerGET),
		"GET /outputs/siafund/:id": wrapPublicAuthHandler(srv.outputsSiafundHandlerGET),

		"GET /events/:id": wrapPublicAuthHandler(srv.eventsHandlerGET),

		"GET /rescan":  wrapAuthHandler(srv.rescanHandlerGET),
		"POST /rescan": wrapAuthHandler(srv.rescanHandlerPOST),

		"GET /wallets":                               wrapAuthHandler(srv.walletsHandler),
		"POST /wallets":                              wrapAuthHandler(srv.walletsHandlerPOST),
		"POST /wallets/:id":                          wrapAuthHandler(srv.walletsIDHandlerPOST),
		"DELETE	/wallets/:id":                        wrapAuthHandler(srv.walletsIDHandlerDELETE),
		"PUT /wallets/:id/addresses":                 wrapAuthHandler(srv.walletsAddressHandlerPUT),
		"DELETE /wallets/:id/addresses/:addr":        wrapAuthHandler(srv.walletsAddressHandlerDELETE),
		"GET /wallets/:id/addresses":                 wrapAuthHandler(srv.walletsAddressesHandlerGET),
		"GET /wallets/:id/balance":                   wrapAuthHandler(srv.walletsBalanceHandler),
		"GET /wallets/:id/events":                    wrapAuthHandler(srv.walletsEventsHandler),
		"POST /wallets/:id/construct/transaction":    wrapAuthHandler(srv.walletsConstructHandler),
		"POST /wallets/:id/construct/v2/transaction": wrapAuthHandler(srv.walletsConstructV2Handler),
		"GET /wallets/:id/events/unconfirmed":        wrapAuthHandler(srv.walletsEventsUnconfirmedHandlerGET),
		"GET /wallets/:id/outputs/siacoin":           wrapAuthHandler(srv.walletsOutputsSiacoinHandler),
		"GET /wallets/:id/outputs/siafund":           wrapAuthHandler(srv.walletsOutputsSiafundHandler),
		"POST /wallets/:id/reserve":                  wrapAuthHandler(srv.walletsReserveHandler),
		"POST /wallets/:id/release":                  wrapAuthHandler(srv.walletsReleaseHandler),
		"POST /wallets/:id/fund":                     wrapAuthHandler(srv.walletsFundHandler),
		"POST /wallets/:id/fundsf":                   wrapAuthHandler(srv.walletsFundSFHandler),
	}

	if srv.debugEnabled {
		handlers["POST /debug/mine"] = wrapAuthHandler(srv.debugMineHandler)
		handlers["GET /debug/pprof/:handler"] = wrapAuthHandler(srv.pprofHandler)
	}
	return jape.Mux(handlers)
}
