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
	"go.sia.tech/walletd/syncer"
	"go.sia.tech/walletd/wallet"
)

type (
	// A ChainManager manages blockchain and txpool state.
	ChainManager interface {
		TipState() consensus.State

		RecommendedFee() types.Currency
		PoolTransactions() []types.Transaction
		AddPoolTransactions(txns []types.Transaction) error
		UnconfirmedParents(txn types.Transaction) []types.Transaction
	}

	// A Syncer can connect to other peers and synchronize the blockchain.
	Syncer interface {
		Addr() string
		Peers() []*gateway.Peer
		PeerInfo(peer string) (syncer.PeerInfo, bool)
		Connect(addr string) (*gateway.Peer, error)
		BroadcastTransactionSet(txns []types.Transaction)
	}

	// A WalletManager manages wallets, keyed by name.
	WalletManager interface {
		AddWallet(name string, info json.RawMessage) error
		Wallets() map[string]json.RawMessage
		SubscribeWallet(name string, startHeight uint64) error

		AddAddress(name string, addr types.Address, info json.RawMessage) error
		Addresses(name string) (map[types.Address]json.RawMessage, error)
		Events(name string, since time.Time, max int) ([]wallet.Event, error)
		UnspentOutputs(name string) ([]wallet.SiacoinElement, []wallet.SiafundElement, error)
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

func (s *server) consensusNetworkHandler(jc jape.Context) {
	jc.Encode(*s.cm.TipState().Network)
}

func (s *server) consensusTipHandler(jc jape.Context) {
	jc.Encode(s.cm.TipState().Index)
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

func (s *server) txpoolTransactionsHandler(jc jape.Context) {
	jc.Encode(s.cm.PoolTransactions())
}

func (s *server) txpoolFeeHandler(jc jape.Context) {
	jc.Encode(s.cm.RecommendedFee())
}

func (s *server) txpoolBroadcastHandler(jc jape.Context) {
	var txnSet []types.Transaction
	if jc.Decode(&txnSet) != nil {
		return
	}
	if jc.Check("invalid transaction set", s.cm.AddPoolTransactions(txnSet)) != nil {
		return
	}
	s.s.BroadcastTransactionSet(txnSet)
}

func (s *server) walletsHandler(jc jape.Context) {
	jc.Encode(s.wm.Wallets())
}

func (s *server) walletsNameHandler(jc jape.Context) {
	var name string
	var info json.RawMessage
	if jc.DecodeParam("name", &name) != nil || jc.Decode(&info) != nil {
		return
	} else if jc.Check("couldn't add wallet", s.wm.AddWallet(name, info)) != nil {
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
	} else if jc.Check("couldn't watch address", s.wm.AddAddress(name, addr, info)) != nil {
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

func (s *server) walletsBalanceHandler(jc jape.Context) {
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

func (s *server) walletsEventsHandler(jc jape.Context) {
	var name string
	var since time.Time
	max := -1
	if jc.DecodeParam("name", &name) != nil || jc.DecodeForm("since", (*paramTime)(&since)) != nil || jc.DecodeForm("max", &max) != nil {
		return
	}
	events, err := s.wm.Events(name, since, max)
	if jc.Check("couldn't load events", err) != nil {
		return
	}
	jc.Encode(events)
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

// NewServer returns an HTTP handler that serves the walletd API.
func NewServer(cm ChainManager, s Syncer, wm WalletManager) http.Handler {
	srv := server{
		cm: cm,
		s:  s,
		wm: wm,
	}
	return jape.Mux(map[string]jape.Handler{
		"GET  /consensus/network": srv.consensusNetworkHandler,
		"GET  /consensus/tip":     srv.consensusTipHandler,

		"GET  /syncer/peers":   srv.syncerPeersHandler,
		"POST /syncer/connect": srv.syncerConnectHandler,

		"GET  /txpool/transactions": srv.txpoolTransactionsHandler,
		"GET  /txpool/fee":          srv.txpoolFeeHandler,
		"POST /txpool/broadcast":    srv.txpoolBroadcastHandler,

		"GET  /wallets":                       srv.walletsHandler,
		"PUT  /wallets/:name":                 srv.walletsNameHandler,
		"POST /wallets/:name/subscribe":       srv.walletsSubscribeHandler,
		"PUT  /wallets/:name/addresses/:addr": srv.walletsAddressHandlerPUT,
		"GET  /wallets/:name/addresses":       srv.walletsAddressesHandlerGET,
		"GET  /wallets/:name/balance":         srv.walletsBalanceHandler,
		"GET  /wallets/:name/events":          srv.walletsEventsHandler,
		"GET  /wallets/:name/outputs":         srv.walletsOutputsHandler,
		"POST /wallets/:name/reserve":         srv.walletsReserveHandler,
		"POST /wallets/:name/release":         srv.walletsReleaseHandler,
	})
}
