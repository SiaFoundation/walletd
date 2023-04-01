package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.sia.tech/jape"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	"go.sia.tech/walletd/wallet"
)

type (
	// A ChainManager manages blockchain state.
	ChainManager interface {
		TipState() consensus.State
	}

	// A Syncer can connect to other peers and synchronize the blockchain.
	Syncer interface {
		Addr() string
		Peers() []*gateway.Peer
		Connect(addr string) (*gateway.Peer, error)
		BroadcastTransactionSet(txns []types.Transaction)
	}

	// A TransactionPool can validate and relay unconfirmed transactions.
	TransactionPool interface {
		RecommendedFee() types.Currency
		Transactions() []types.Transaction
		AddTransactionSet(txns []types.Transaction) error
		UnconfirmedParents(txn types.Transaction) []types.Transaction
	}

	// A Wallet can spend and receive siacoins.
	Wallet interface {
		AddAddress(addr types.Address, info json.RawMessage) error
		Addresses() (map[types.Address]json.RawMessage, error)
		Events(since time.Time, max int) ([]wallet.Event, error)
		UnspentOutputs() ([]wallet.SiacoinElement, []wallet.SiafundElement, error)
	}
)

type server struct {
	cm ChainManager
	tp TransactionPool
	s  Syncer
	w  Wallet

	// for walletReserveHandler
	mu   sync.Mutex
	used map[types.Hash256]bool
}

func (s *server) consensusTipHandler(jc jape.Context) {
	jc.Encode(s.cm.TipState().Index)
}

func (s *server) syncerPeersHandler(jc jape.Context) {
	var peers []GatewayPeer
	for _, p := range s.s.Peers() {
		peers = append(peers, GatewayPeer{
			Addr:    p.Addr,
			Inbound: p.Inbound,
			Version: p.Version,
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

func (s *server) txpoolTransactionsHandler(jc jape.Context) {
	jc.Encode(s.tp.Transactions())
}

func (s *server) txpoolBroadcastHandler(jc jape.Context) {
	var txnSet []types.Transaction
	if jc.Decode(&txnSet) != nil {
		return
	}
	if jc.Check("invalid transaction set", s.tp.AddTransactionSet(txnSet)) != nil {
		return
	}
	s.s.BroadcastTransactionSet(txnSet)
}

func (s *server) walletAddressHandlerPUT(jc jape.Context) {
	var addr types.Address
	if jc.DecodeParam("addr", &addr) != nil {
		return
	}
	var info json.RawMessage
	if jc.Decode(&info) != nil {
		return
	}
	if jc.Check("couldn't watch address", s.w.AddAddress(addr, info)) != nil {
		return
	}
}

func (s *server) walletAddressesHandler(jc jape.Context) {
	addrs, err := s.w.Addresses()
	if jc.Check("couldn't load addresses", err) != nil {
		return
	}
	jc.Encode(addrs)
}

func (s *server) walletBalanceHandler(jc jape.Context) {
	scos, sfos, err := s.w.UnspentOutputs()
	if jc.Check("couldn't load outputs", err) != nil {
		return
	}
	var sc types.Currency
	var sf uint64
	for _, sco := range scos {
		sc = sc.Add(sco.Value)
	}
	for _, sfo := range sfos {
		sf += sfo.Value
	}
	jc.Encode(WalletBalanceResponse{
		Siacoins: sc,
		Siafunds: sf,
	})
}

func (s *server) walletEventsHandler(jc jape.Context) {
	var since time.Time
	max := -1
	if jc.DecodeForm("since", (*paramTime)(&since)) != nil || jc.DecodeForm("max", &max) != nil {
		return
	}
	events, err := s.w.Events(since, max)
	if jc.Check("couldn't load events", err) != nil {
		return
	}
	jc.Encode(events)
}

func (s *server) walletOutputsHandler(jc jape.Context) {
	scos, sfos, err := s.w.UnspentOutputs()
	if jc.Check("couldn't load outputs", err) != nil {
		return
	}
	jc.Encode(WalletOutputsResponse{
		SiacoinOutputs: scos,
		SiafundOutputs: sfos,
	})
}

func (s *server) walletReserveHandler(jc jape.Context) {
	var wrr WalletReserveRequest
	if jc.Decode(&wrr) != nil {
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

	if wrr.Timeout == 0 {
		wrr.Timeout = 10 * time.Minute
	}
	time.AfterFunc(wrr.Timeout, func() {
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

// NewServer returns an HTTP handler that serves the walletd API.
func NewServer(cm ChainManager, s Syncer, tp TransactionPool, w Wallet) http.Handler {
	srv := server{
		cm: cm,
		s:  s,
		tp: tp,
		w:  w,
	}
	return jape.Mux(map[string]jape.Handler{
		"GET  /consensus/tip": srv.consensusTipHandler,

		"GET  /syncer/peers":   srv.syncerPeersHandler,
		"POST /syncer/connect": srv.syncerConnectHandler,

		"GET  /txpool/transactions": srv.txpoolTransactionsHandler,
		"POST /txpool/broadcast":    srv.txpoolBroadcastHandler,

		"PUT  /wallet/addresses/:addr": srv.walletAddressHandlerPUT,
		"GET  /wallet/addresses":       srv.walletAddressesHandler,
		"GET  /wallet/balance":         srv.walletBalanceHandler,
		"GET  /wallet/events":          srv.walletEventsHandler,
		"GET  /wallet/outputs":         srv.walletOutputsHandler,
		"POST /wallet/reserve":         srv.walletReserveHandler,
	})
}
