package api

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"sync"
	"time"

	"go.sia.tech/jape"
	"lukechampine.com/frand"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	"go.sia.tech/walletd/syncer"
	"go.sia.tech/walletd/wallet"
)

type (
	// A ChainManager manages blockchain and txpool state.
	ChainManager interface {
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
		Peers() []*gateway.Peer
		PeerInfo(peer string) (syncer.PeerInfo, bool)
		Connect(addr string) (*gateway.Peer, error)
		BroadcastHeader(bh gateway.BlockHeader)
		BroadcastTransactionSet(txns []types.Transaction)
		BroadcastV2TransactionSet(index types.ChainIndex, txns []types.V2Transaction)
		BroadcastV2BlockOutline(bo gateway.V2BlockOutline)
	}

	// A WalletManager manages wallets, keyed by name.
	WalletManager interface {
		AddWallet(name string, info json.RawMessage) error
		DeleteWallet(name string) error
		Wallets() map[string]json.RawMessage
		SubscribeWallet(name string, startHeight uint64) error

		AddAddress(name string, addr types.Address, info json.RawMessage) error
		RemoveAddress(name string, addr types.Address) error
		Addresses(name string) (map[types.Address]json.RawMessage, error)
		Events(name string, offset, limit int) ([]wallet.Event, error)
		UnspentOutputs(name string) ([]types.SiacoinElement, []types.SiafundElement, error)
		Annotate(name string, pool []types.Transaction) ([]wallet.PoolTransaction, error)
	}
)

type server struct {
	cm ChainManager
	s  Syncer
	wm WalletManager

	// for walletsReserveHandler
	mu   sync.Mutex
	used map[types.Hash256]bool
}

func (s *server) consensusNetworkPrometheusHandler(jc jape.Context) {
	var buf bytes.Buffer
	text := `walletd_consensus_network{network="%s"} 1`
	fmt.Fprintf(&buf, text, s.cm.TipState().Network.Name)
	jc.ResponseWriter.Write(buf.Bytes())
}
func (s *server) consensusNetworkHandler(jc jape.Context) {
	jc.Encode(*s.cm.TipState().Network)
}
func (s *server) consensusTipPrometheusHandler(jc jape.Context) {
	var buf bytes.Buffer
	text := `walletd_consensus_tip_height %d`
	fmt.Fprintf(&buf, text, s.cm.TipState().Index.Height)
	jc.ResponseWriter.Write(buf.Bytes())
}

func (s *server) consensusTipHandler(jc jape.Context) {
	jc.Encode(s.cm.TipState().Index)
}

func (s *server) consensusTipStateHandler(jc jape.Context) {
	jc.Encode(s.cm.TipState())
}

func (s *server) syncerPeersPrometheusHandler(jc jape.Context) {
	p := s.s.Peers()
	resulttext := ""
	for i, peer := range p {
		synced_peer := fmt.Sprintf(`walletd_syncer_peer{address="%s", version="%s"} 1`, string(peer.Addr), peer.Version)
		if i != len(p)-1 {
			synced_peer = synced_peer + "\n"
		}
		resulttext = resulttext + synced_peer
	}
	var resultbuffer bytes.Buffer
	resultbuffer.WriteString(resulttext)
	jc.ResponseWriter.Write(resultbuffer.Bytes())
}
func (s *server) syncerPeersHandler(jc jape.Context) {
	var peers []GatewayPeer
	for _, p := range s.s.Peers() {
		info, ok := s.s.PeerInfo(p.Addr)
		if !ok {
			continue
		}
		peers = append(peers, GatewayPeer{
			Addr:    p.Addr,
			Inbound: p.Inbound,
			Version: p.Version,

			FirstSeen:      info.FirstSeen,
			ConnectedSince: info.LastConnect,
			SyncedBlocks:   info.SyncedBlocks,
			SyncDuration:   info.SyncDuration,
		})
	}
	jc.Encode(peers)
}

func (s *server) syncerConnectHandler(jc jape.Context) {
	var addr string
	if jc.Decode(&addr) != nil {
		return
	}
	_, err := s.s.Connect(addr)
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

func (s *server) txpoolFeePrometheusHandler(jc jape.Context) {
	var buf bytes.Buffer
	text := `walletd_txpool_fee %s`
	fmt.Fprintf(&buf, text, s.cm.RecommendedFee().ExactString())
	jc.ResponseWriter.Write(buf.Bytes())
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

func (s *server) walletsHandler(jc jape.Context) {
	jc.Encode(s.wm.Wallets())
}

func (s *server) walletsNameHandlerPUT(jc jape.Context) {
	var name string
	var info json.RawMessage
	if jc.DecodeParam("name", &name) != nil || jc.Decode(&info) != nil {
		return
	} else if jc.Check("couldn't add wallet", s.wm.AddWallet(name, info)) != nil {
		return
	}
}

func (s *server) walletsNameHandlerDELETE(jc jape.Context) {
	var name string
	if jc.DecodeParam("name", &name) != nil {
		return
	} else if jc.Check("couldn't remove wallet", s.wm.DeleteWallet(name)) != nil {
		return
	}
}

func (s *server) walletsSubscribeHandler(jc jape.Context) {
	var name string
	var height uint64
	if jc.DecodeParam("name", &name) != nil || jc.Decode(&height) != nil {
		return
	} else if jc.Check("couldn't subscribe wallet", s.wm.SubscribeWallet(name, height)) != nil {
		return
	}
}

func (s *server) walletsAddressHandlerPUT(jc jape.Context) {
	var name string
	var addr types.Address
	var info json.RawMessage
	if jc.DecodeParam("name", &name) != nil || jc.DecodeParam("addr", &addr) != nil || jc.Decode(&info) != nil {
		return
	} else if jc.Check("couldn't add address", s.wm.AddAddress(name, addr, info)) != nil {
		return
	}
}

func (s *server) walletsAddressHandlerDELETE(jc jape.Context) {
	var name string
	var addr types.Address
	if jc.DecodeParam("name", &name) != nil || jc.DecodeParam("addr", &addr) != nil {
		return
	} else if jc.Check("couldn't remove address", s.wm.RemoveAddress(name, addr)) != nil {
		return
	}
}

func (s *server) walletsAddressesHandlerGET(jc jape.Context) {
	var name string
	if jc.DecodeParam("name", &name) != nil {
		return
	}
	addrs, err := s.wm.Addresses(name)
	if jc.Check("couldn't load addresses", err) != nil {
		return
	}
	jc.Encode(addrs)
}

func (s *server) walletsBalancePrometheusHandler(jc jape.Context) {
	var name string
	if jc.DecodeParam("name", &name) != nil {
		return
	}
	scos, sfos, err := s.wm.UnspentOutputs(name)
	if jc.Check("couldn't load outputs", err) != nil {
		return
	}
	var sc types.Currency
	var sf uint64
	for _, sco := range scos {
		sc = sc.Add(sco.SiacoinOutput.Value)
	}
	for _, sfo := range sfos {
		sf += sfo.SiafundOutput.Value
	}
	var buf bytes.Buffer
	text := `walletd_wallet_balance_siacoins{name="%s"} %s
walletd_wallet_balance_siafunds{name="%s"} %d`
	fmt.Fprintf(&buf, text, name, sc.ExactString(), name, sf)
	jc.ResponseWriter.Write(buf.Bytes())
}
func (s *server) walletsBalanceHandler(jc jape.Context) {
	var name string
	if jc.DecodeParam("name", &name) != nil {
		return
	}
	scos, sfos, err := s.wm.UnspentOutputs(name)
	if jc.Check("couldn't load outputs", err) != nil {
		return
	}
	height := s.cm.TipState().Index.Height
	var sc, immature types.Currency
	var sf uint64
	for _, sco := range scos {
		if height >= sco.MaturityHeight {
			sc = sc.Add(sco.SiacoinOutput.Value)
		} else {
			immature = immature.Add(sco.SiacoinOutput.Value)
		}
	}
	for _, sfo := range sfos {
		sf += sfo.SiafundOutput.Value
	}
	jc.Encode(WalletBalanceResponse{
		Siacoins:         sc,
		ImmatureSiacoins: immature,
		Siafunds:         sf,
	})
}

func (s *server) walletsEventsPrometheusHandler(jc jape.Context) {
	var name string
	offset, limit := 0, -1
	if jc.DecodeParam("name", &name) != nil || jc.DecodeForm("offset", &offset) != nil || jc.DecodeForm("limit", &limit) != nil {
		return
	}
	events, err := s.wm.Events(name, offset, limit)
	if jc.Check("couldn't load events", err) != nil {
		return
	}
	resulttext := ""
	for i, event := range events {
		event_txt := ""
		if e, ok := event.Val.(*wallet.EventTransaction); ok {
			event_txt = fmt.Sprintf(`walletd_wallet_event_transaction{name="%s", height="%d", timestamp="%s", txid="%s"} 1`,
				name,
				event.Index.Height,
				event.Timestamp.Format(time.RFC3339),
				hex.EncodeToString(e.ID[:]),
			)
		} else if e, ok := event.Val.(*wallet.EventMinerPayout); ok {
			event_txt = fmt.Sprintf(`walletd_wallet_event_minerpayout{name="%s", height="%d", timestamp="%s"} %s`,
				name,
				event.Index.Height,
				event.Timestamp.Format(time.RFC3339),
				e.SiacoinOutput.SiacoinOutput.Value.ExactString(),
			)
		} else if e, ok := event.Val.(*wallet.EventMissedFileContract); ok {
			event_txt = fmt.Sprintf(`walletd_wallet_event_missedfilecontract{name="%s", height="%d", timestamp="%s", outputs="%s"} %s`,
				name,
				event.Index.Height,
				event.Timestamp.Format(time.RFC3339),
				strconv.Itoa(len(e.MissedOutputs)),
				e.FileContract.FileContract.Payout.ExactString(),
			)
		}
		if i != len(events)-1 {
			event_txt = event_txt + "\n"
		}
		resulttext = resulttext + event_txt
	}
	var resultbuffer bytes.Buffer
	resultbuffer.WriteString(resulttext)
	jc.ResponseWriter.Write(resultbuffer.Bytes())
}
func (s *server) walletsEventsHandler(jc jape.Context) {
	var name string
	offset, limit := 0, -1
	if jc.DecodeParam("name", &name) != nil || jc.DecodeForm("offset", &offset) != nil || jc.DecodeForm("limit", &limit) != nil {
		return
	}
	events, err := s.wm.Events(name, offset, limit)
	if jc.Check("couldn't load events", err) != nil {
		return
	}
	jc.Encode(events)
}

func (s *server) walletsTxpoolHandler(jc jape.Context) {
	var name string
	if jc.DecodeParam("name", &name) != nil {
		return
	}
	pool, err := s.wm.Annotate(name, s.cm.PoolTransactions())
	if jc.Check("couldn't annotate pool", err) != nil {
		return
	}
	jc.Encode(pool)
}

func (s *server) walletsOutputsHandler(jc jape.Context) {
	var name string
	if jc.DecodeParam("name", &name) != nil {
		return
	}
	scos, sfos, err := s.wm.UnspentOutputs(name)
	if jc.Check("couldn't load outputs", err) != nil {
		return
	}
	jc.Encode(WalletOutputsResponse{
		SiacoinOutputs: scos,
		SiafundOutputs: sfos,
	})
}

func (s *server) walletsReserveHandler(jc jape.Context) {
	var name string
	var wrr WalletReserveRequest
	if jc.DecodeParam("name", &name) != nil || jc.Decode(&wrr) != nil {
		return
	}

	s.mu.Lock()
	for _, id := range wrr.SiacoinOutputs {
		if s.used[types.Hash256(id)] {
			s.mu.Unlock()
			jc.Error(fmt.Errorf("output %v is already reserved", id), http.StatusBadRequest)
			return
		}
		s.used[types.Hash256(id)] = true
	}
	for _, id := range wrr.SiafundOutputs {
		if s.used[types.Hash256(id)] {
			s.mu.Unlock()
			jc.Error(fmt.Errorf("output %v is already reserved", id), http.StatusBadRequest)
			return
		}
		s.used[types.Hash256(id)] = true
	}
	s.mu.Unlock()

	if wrr.Duration == 0 {
		wrr.Duration = 10 * time.Minute
	}
	time.AfterFunc(wrr.Duration, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, id := range wrr.SiacoinOutputs {
			delete(s.used, types.Hash256(id))
		}
		for _, id := range wrr.SiafundOutputs {
			delete(s.used, types.Hash256(id))
		}
	})
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

	var name string
	var wfr WalletFundRequest
	if jc.DecodeParam("name", &name) != nil || jc.Decode(&wfr) != nil {
		return
	}
	utxos, _, err := s.wm.UnspentOutputs(name)
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

	var name string
	var wfr WalletFundSFRequest
	if jc.DecodeParam("name", &name) != nil || jc.Decode(&wfr) != nil {
		return
	}
	_, utxos, err := s.wm.UnspentOutputs(name)
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

// NewServer returns an HTTP handler that serves the walletd API.
func NewServer(cm ChainManager, s Syncer, wm WalletManager) http.Handler {
	srv := server{
		cm:   cm,
		s:    s,
		wm:   wm,
		used: make(map[types.Hash256]bool),
	}
	return jape.Mux(map[string]jape.Handler{
		"GET    /consensus/network":             srv.consensusNetworkHandler,
		"GET    /consensus/tip":                 srv.consensusTipHandler,
		"GET    /consensus/tipstate":            srv.consensusTipStateHandler,
		"GET    /syncer/peers":                  srv.syncerPeersHandler,
		"POST   /syncer/connect":                srv.syncerConnectHandler,
		"POST   /syncer/broadcast/block":        srv.syncerBroadcastBlockHandler,
		"GET    /txpool/transactions":           srv.txpoolTransactionsHandler,
		"GET    /txpool/fee":                    srv.txpoolFeeHandler,
		"POST   /txpool/broadcast":              srv.txpoolBroadcastHandler,
		"GET    /wallets":                       srv.walletsHandler,
		"PUT    /wallets/:name":                 srv.walletsNameHandlerPUT,
		"DELETE /wallets/:name":                 srv.walletsNameHandlerDELETE,
		"POST   /wallets/:name/subscribe":       srv.walletsSubscribeHandler,
		"PUT    /wallets/:name/addresses/:addr": srv.walletsAddressHandlerPUT,
		"DELETE /wallets/:name/addresses/:addr": srv.walletsAddressHandlerDELETE,
		"GET    /wallets/:name/addresses":       srv.walletsAddressesHandlerGET,
		"GET    /wallets/:name/balance":         srv.walletsBalanceHandler,
		"GET    /wallets/:name/events":          srv.walletsEventsHandler,
		"GET    /wallets/:name/txpool":          srv.walletsTxpoolHandler,
		"GET    /wallets/:name/outputs":         srv.walletsOutputsHandler,
		"POST   /wallets/:name/reserve":         srv.walletsReserveHandler,
		"POST   /wallets/:name/release":         srv.walletsReleaseHandler,
		"POST   /wallets/:name/fund":            srv.walletsFundHandler,
		"POST   /wallets/:name/fundsf":          srv.walletsFundSFHandler,
	})
}

func NewPrometheusServer(cm ChainManager, s Syncer, wm WalletManager) http.Handler {
	srv := server{
		cm:   cm,
		s:    s,
		wm:   wm,
		used: make(map[types.Hash256]bool),
	}
	return jape.Mux(map[string]jape.Handler{
		"GET    /consensus/network": srv.consensusNetworkPrometheusHandler,
		"GET    /consensus/tip":     srv.consensusTipPrometheusHandler,
		// "GET    /consensus/tipstate": srv.consensusTipStateHandler, 				//intentionally left out
		"GET    /syncer/peers": srv.syncerPeersPrometheusHandler,
		// "GET    /txpool/transactions": srv.txpoolTransactionsHandler,
		"GET    /txpool/fee": srv.txpoolFeePrometheusHandler,
		// "GET    /wallets":    srv.walletsPrometheusHandler, 						//intentionally left out.
		// "GET    /wallets/:name/addresses":       srv.walletsAddressesHandlerGET, //intentionally left out.
		"GET    /wallets/:name/balance": srv.walletsBalancePrometheusHandler,
		"GET    /wallets/:name/events":  srv.walletsEventsPrometheusHandler,
		// "GET    /wallets/:name/txpool":          srv.walletsTxpoolHandler, 		//intentionally left out.
		// "GET    /wallets/:name/outputs":         srv.walletsOutputsHandler, 		//intentionally left out.
	})
}
