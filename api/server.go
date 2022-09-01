package api

import (
	"encoding/json"
	"math/big"
	"net/http"
	"strconv"
	"time"

	"go.sia.tech/siad/crypto"

	"github.com/julienschmidt/httprouter"
	"go.sia.tech/siad/types"
	"go.sia.tech/walletd/wallet"
)

type (
	// A ChainManager manages blockchain state.
	ChainManager interface {
		TipState() ConsensusState
	}

	// A Syncer can connect to other peers and synchronize the blockchain.
	Syncer interface {
		Addr() string
		Peers() []string
		Connect(addr string) error
		BroadcastTransaction(txn types.Transaction, dependsOn []types.Transaction)
	}

	// A TransactionPool can validate and relay unconfirmed transactions.
	TransactionPool interface {
		RecommendedFee() types.Currency
		Transactions() []types.Transaction
		AddTransactionSet(txns []types.Transaction) error
		UnconfirmedParents(txn types.Transaction) ([]types.Transaction, error)
	}

	// A Wallet can spend and receive siacoins.
	Wallet interface {
		Balance() (types.Currency, error)
		Address() (types.UnlockHash, error)
		Addresses() ([]types.UnlockHash, error)
		UnspentOutputs() ([]wallet.SiacoinElement, error)
		Transaction(id types.TransactionID) (wallet.Transaction, error)
		Transactions(since time.Time, max int) ([]wallet.Transaction, error)
		TransactionsByAddress(addr types.UnlockHash) ([]wallet.Transaction, error)
		SignTransaction(txn *types.Transaction, toSign []crypto.Hash) error
		FundTransaction(txn *types.Transaction, amount types.Currency) ([]crypto.Hash, func(), error)
	}
)

// WriteJSON writes the JSON encoded object to the http response.
func WriteJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.Encode(v)
}

// AuthMiddleware enforces HTTP Basic Authentication on the provided handler.
func AuthMiddleware(handler http.Handler, requiredPass string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if _, password, ok := req.BasicAuth(); !ok || password != requiredPass {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, req)
	})
}

type server struct {
	s  Syncer
	cm ChainManager
	tp TransactionPool
	w  Wallet
}

func (s *server) consensusTipHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	WriteJSON(w, s.cm.TipState().Index)
}

func (s *server) syncerPeersHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	ps := s.s.Peers()
	sps := make([]SyncerPeerResponse, len(ps))
	for i, peer := range ps {
		sps[i] = SyncerPeerResponse{
			NetAddress: peer,
		}
	}
	WriteJSON(w, sps)
}

func (s *server) syncerConnectHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	var scr SyncerConnectRequest
	if err := json.NewDecoder(req.Body).Decode(&scr); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.s.Connect(scr.NetAddress); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
}

func (s *server) txpoolTransactionsHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	WriteJSON(w, s.tp.Transactions())
}

func (s *server) txpoolBroadcastHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	var txnSet []types.Transaction
	if err := json.NewDecoder(req.Body).Decode(&txnSet); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.tp.AddTransactionSet(txnSet); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
}

func (s *server) walletBalanceHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	balance, err := s.w.Balance()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	WriteJSON(w, WalletBalanceResponse{
		Siacoins: balance,
	})
}

func (s *server) walletAddressHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	address, err := s.w.Address()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	WriteJSON(w, address)
}

func (s *server) walletAddressesHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	addresses, err := s.w.Addresses()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	WriteJSON(w, addresses)
}

func (s *server) walletTransactionsHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	var since time.Time
	if v := req.FormValue("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, "invalid since value: "+err.Error(), http.StatusBadRequest)
			return
		}
		since = t
	}
	max := -1
	if v := req.FormValue("max"); v != "" {
		t, err := strconv.Atoi(v)
		if err != nil {
			http.Error(w, "invalid max value: "+err.Error(), http.StatusBadRequest)
			return
		}
		max = t
	}
	txns, err := s.w.Transactions(since, max)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	WriteJSON(w, txns)
}

func (s *server) walletTransactionHandler(w http.ResponseWriter, req *http.Request, p httprouter.Params) {
	var id types.TransactionID
	if err := json.Unmarshal([]byte(`"`+p.ByName("id")+`"`), &id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	txn, err := s.w.Transaction(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	WriteJSON(w, txn)
}

func (s *server) walletTransactionsAddressHandler(w http.ResponseWriter, req *http.Request, p httprouter.Params) {
	var addr types.UnlockHash
	if err := json.Unmarshal([]byte(`"`+p.ByName("id")+`"`), &addr); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	txns, err := s.w.TransactionsByAddress(addr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	WriteJSON(w, txns)
}

func (s *server) walletOutputsHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	utxos, err := s.w.UnspentOutputs()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	WriteJSON(w, utxos)
}

func (s *server) walletSignHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	var wtsr WalletSignRequest
	if err := json.NewDecoder(req.Body).Decode(&wtsr); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.w.SignTransaction(&wtsr.Transaction, wtsr.ToSign); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, wtsr.Transaction)
}

func (s *server) walletSiacoinsHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	var amount types.Currency
	var txn types.Transaction
	if req.URL.Query().Get("amount") != "" && req.URL.Query().Get("destination") != "" {
		b, ok := new(big.Int).SetString(req.URL.Query().Get("amount"), 10)
		if !ok {
			http.Error(w, "invalid amount string", http.StatusBadRequest)
			return
		}
		amount = types.NewCurrency(b)

		var destination types.UnlockHash
		if err := destination.LoadString(req.URL.Query().Get("destination")); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		txn = types.Transaction{SiacoinOutputs: []types.SiacoinOutput{{amount, destination}}}
	} else if v := req.URL.Query().Get("outputs"); v != "" {
		var outputs []types.SiacoinOutput
		if err := json.Unmarshal([]byte(v), &outputs); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		txn = types.Transaction{SiacoinOutputs: outputs}
		for _, output := range outputs {
			amount = amount.Add(output.Value)
		}
	}

	ids, unclaim, err := s.w.FundTransaction(&txn, amount)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.w.SignTransaction(&txn, ids); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	txns := []types.Transaction{txn}
	if err := s.tp.AddTransactionSet(txns); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	unclaim()

	WriteJSON(w, WalletSiacoinsResponse{txns, ids})
}

// NewServer returns an HTTP handler that serves the walletd API.
func NewServer(cm ChainManager, s Syncer, tp TransactionPool, w Wallet) http.Handler {
	srv := server{
		cm: cm,
		s:  s,
		tp: tp,
		w:  w,
	}
	mux := httprouter.New()

	mux.GET("/consensus/tip", srv.consensusTipHandler)

	mux.GET("/syncer/peers", srv.syncerPeersHandler)
	mux.POST("/syncer/connect", srv.syncerConnectHandler)

	mux.GET("/txpool/transactions", srv.txpoolTransactionsHandler)
	mux.POST("/txpool/broadcast", srv.txpoolBroadcastHandler)

	mux.GET("/wallet/balance", srv.walletBalanceHandler)
	mux.GET("/wallet/address", srv.walletAddressHandler)
	mux.GET("/wallet/addresses", srv.walletAddressesHandler)
	mux.GET("/wallet/transaction/:id", srv.walletTransactionHandler)
	mux.POST("/wallet/sign", srv.walletSignHandler)
	mux.GET("/wallet/transactions", srv.walletTransactionsHandler)
	mux.GET("/wallet/transactions/:address", srv.walletTransactionsAddressHandler)
	mux.GET("/wallet/outputs", srv.walletOutputsHandler)
	mux.POST("/wallet/siacoins", srv.walletSiacoinsHandler)

	return mux
}
