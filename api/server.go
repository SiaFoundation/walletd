package api

import (
	"errors"
	"math/big"
	"net/http"
	"time"

	"gitlab.com/NebulousLabs/encoding"
	"go.sia.tech/jape"
	"go.sia.tech/siad/crypto"

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
		Balance() (types.Currency, types.Currency, error)
		Address() (types.UnlockHash, error)
		Addresses() ([]types.UnlockHash, error)
		AddressInfo(addr types.UnlockHash) (wallet.SeedAddressInfo, error)
		UnspentSiacoinOutputs() ([]wallet.SiacoinElement, error)
		UnspentSiafundOutputs() ([]wallet.SiafundElement, error)
		Transaction(id types.TransactionID) (wallet.Transaction, error)
		Transactions(since time.Time, max int) ([]wallet.Transaction, error)
		TransactionsByAddress(addr types.UnlockHash) ([]wallet.Transaction, error)
		SignTransaction(txn *types.Transaction, toSign []crypto.Hash) error
		FundTransaction(txn *types.Transaction, amountSC types.Currency, amountSF types.Currency) ([]crypto.Hash, func(), error)
	}
)

type server struct {
	s  Syncer
	cm ChainManager
	tp TransactionPool
	w  Wallet
}

func (s *server) consensusTipHandler(jc jape.Context) {
	jc.Encode(s.cm.TipState().Index)
}

func (s *server) syncerPeersHandler(jc jape.Context) {
	jc.Encode(s.s.Peers())
}

func (s *server) syncerConnectHandler(jc jape.Context) {
	var addr string
	if jc.Decode(&addr) == nil {
		jc.Check("couldn't connect to peer", s.s.Connect(addr))
	}
}

func (s *server) txpoolTransactionsHandler(jc jape.Context) {
	jc.Encode(s.tp.Transactions())
}

func (s *server) txpoolBroadcastHandler(jc jape.Context) {
	var txnSet []types.Transaction
	if jc.Decode(&txnSet) == nil {
		jc.Check("couldn't broadcast transaction set", s.tp.AddTransactionSet(txnSet))
	}
}

func (s *server) walletBalanceHandler(jc jape.Context) {
	siacoins, siafunds, err := s.w.Balance()
	if jc.Check("couldn't load balance", err) == nil {
		jc.Encode(WalletBalanceResponse{
			Siacoins: siacoins,
			Siafunds: siafunds,
		})
	}
}

func (s *server) walletAddressHandler(jc jape.Context) {
	address, err := s.w.Address()
	if jc.Check("couldn't load address", err) == nil {
		jc.Encode(address)
	}
}

func (s *server) walletAddressesHandler(jc jape.Context) {
	addresses, err := s.w.Addresses()
	if jc.Check("couldn't load addresses", err) == nil {
		jc.Encode(addresses)
	}
}

func (s *server) walletTransactionsHandler(jc jape.Context) {
	var since time.Time
	max := -1
	if jc.DecodeForm("since", (*paramTime)(&since)) != nil || jc.DecodeForm("max", &max) != nil {
		return
	}
	txns, err := s.w.Transactions(since, max)
	if jc.Check("couldn't load transactions", err) == nil {
		jc.Encode(txns)
	}
}

func (s *server) walletTransactionHandler(jc jape.Context) {
	var id types.TransactionID
	if jc.DecodeParam("id", &id) != nil {
		return
	}

	txn, err := s.w.Transaction(id)
	if jc.Check("couldn't load transaction", err) == nil {
		jc.Encode(txn)
	}
}

func (s *server) walletTransactionsAddressHandler(jc jape.Context) {
	var addr types.UnlockHash
	if jc.DecodeParam("address", &addr) != nil {
		return
	}

	txns, err := s.w.TransactionsByAddress(addr)
	if jc.Check("couldn't load transactions", err) == nil {
		jc.Encode(txns)
		return
	}
}

func (s *server) walletOutputsHandler(jc jape.Context) {
	utxos, err := s.w.UnspentSiacoinOutputs()
	if jc.Check("couldn't load unspent siacoin outputs", err) == nil {
		jc.Encode(utxos)
	}
}

func (s *server) walletSignHandler(jc jape.Context) {
	var wtsr WalletSignRequest
	if jc.Decode(&wtsr) != nil {
		return
	}

	if jc.Check("failed to sign transaction", s.w.SignTransaction(&wtsr.Transaction, wtsr.ToSign)) != nil {
		return
	}
	jc.Encode(wtsr.Transaction)
}

func (s *server) walletFundHandler(jc jape.Context) {
	var wfr WalletFundRequest
	if jc.Decode(&wfr) != nil {
		return
	}
	txn := wfr.Transaction
	fee := s.tp.RecommendedFee().Mul64(uint64(len(encoding.Marshal(txn))))
	txn.MinerFees = []types.Currency{fee}
	toSign, unclaim, err := s.w.FundTransaction(&wfr.Transaction, wfr.Siacoins.Add(txn.MinerFees[0]), wfr.Siafunds)
	if jc.Check("failed to fund transaction", err) != nil {
		return
	}
	parents, err := s.tp.UnconfirmedParents(txn)
	if err != nil {
		unclaim()
		jc.Check("couldn't load parents", err)
		return
	}
	jc.Encode(WalletFundResponse{
		Transaction: txn,
		ToSign:      toSign,
		DependsOn:   parents,
	})
}

func (s *server) walletSplitHandler(jc jape.Context) {
	var wsr WalletSplitRequest
	if jc.Decode(&wsr) != nil {
		return
	}

	utxos, err := s.w.UnspentSiacoinOutputs()
	if jc.Check("couldn't load unspent siacoin outputs", err) != nil {
		return
	}

	ins, fee, _ := wallet.DistributeFunds(utxos, wsr.Outputs, wsr.Amount, s.tp.RecommendedFee())
	if jc.Check("couldn't distribute funds", err) != nil {
		return
	}

	txn := types.Transaction{
		SiacoinInputs:  make([]types.SiacoinInput, len(ins)),
		SiacoinOutputs: make([]types.SiacoinOutput, wsr.Outputs),
		MinerFees:      []types.Currency{fee},
	}

	addr, err := s.w.Address()
	if jc.Check("couldn't get wallet address", err) != nil {
		return
	}

	for i := range ins {
		addr, err := s.w.AddressInfo(ins[i].UnlockHash)
		if jc.Check("couldn't load address info", err) != nil {
			return
		}
		txn.SiacoinInputs[i] = types.SiacoinInput{
			ParentID:         ins[i].ID,
			UnlockConditions: addr.UnlockConditions,
		}
	}

	for i := range txn.SiacoinOutputs {
		txn.SiacoinOutputs[i] = types.SiacoinOutput{
			Value:      wsr.Amount,
			UnlockHash: addr,
		}
	}

	jc.Encode(txn)
}

func (s *server) walletSendHandler(jc jape.Context) {
	b, ok := new(big.Int).SetString(jc.Request.FormValue("amount"), 10)
	if !ok {
		jc.Error(errors.New("invalid amount string"), http.StatusBadRequest)
		return
	}

	var destination types.UnlockHash
	if jc.Check("failed to parse destination address", destination.LoadString(jc.Request.FormValue("destination"))) != nil {
		return
	}

	var txn types.Transaction
	var amountSC, amountSF types.Currency
	if jc.Request.FormValue("type") == "siacoin" {
		amountSC = types.NewCurrency(b)
		txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.SiacoinOutput{amountSC, destination})
	} else if jc.Request.FormValue("type") == "siafund" {
		amountSF = types.NewCurrency(b)
		txn.SiafundOutputs = append(txn.SiafundOutputs, types.SiafundOutput{amountSF, destination, types.ZeroCurrency})
	} else {
		jc.Error(errors.New("specify either siacoin or siafund as the type"), http.StatusBadRequest)
		return
	}
	fee := s.tp.RecommendedFee().Mul64(uint64(len(encoding.Marshal(txn))))
	txn.MinerFees = []types.Currency{fee}
	amountSC = amountSC.Add(fee)

	toSign, unclaim, err := s.w.FundTransaction(&txn, amountSC, amountSF)
	if jc.Check("failed to fund transaction", err) != nil {
		return
	}
	defer unclaim()

	if jc.Check("failed to fund transaction", s.w.SignTransaction(&txn, toSign)) != nil {
		return
	}
	if jc.Check("failed to add transaction to transaction pool", s.tp.AddTransactionSet([]types.Transaction{txn})) != nil {
		return
	}
	jc.Encode(WalletSendResponse{txn.ID(), txn})
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

		"GET  /wallet/balance":               srv.walletBalanceHandler,
		"GET  /wallet/address":               srv.walletAddressHandler,
		"GET  /wallet/addresses":             srv.walletAddressesHandler,
		"GET  /wallet/transaction/:id":       srv.walletTransactionHandler,
		"POST /wallet/sign":                  srv.walletSignHandler,
		"POST /wallet/fund":                  srv.walletFundHandler,
		"POST /wallet/split":                 srv.walletSplitHandler,
		"POST /wallet/send":                  srv.walletSendHandler,
		"GET  /wallet/transactions":          srv.walletTransactionsHandler,
		"GET  /wallet/transactions/:address": srv.walletTransactionsAddressHandler,
		"GET  /wallet/outputs":               srv.walletOutputsHandler,
	})
}
