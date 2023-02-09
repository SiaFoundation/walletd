package main

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"

	"gitlab.com/NebulousLabs/encoding"
	ctypes "go.sia.tech/core/types"
	"go.sia.tech/jape"
	"go.sia.tech/siad/modules"
	stypes "go.sia.tech/siad/types"
	"go.sia.tech/walletd/api"
)

func coreConvertToSiad(from ctypes.EncoderTo, to interface{}) {
	var buf bytes.Buffer
	e := ctypes.NewEncoder(&buf)
	from.EncodeTo(e)
	e.Flush()
	if err := encoding.Unmarshal(buf.Bytes(), to); err != nil {
		panic(fmt.Sprintf("type conversion failed (%T->%T): %v", from, to, err))
	}
}

func siadConvertToCore(from interface{}, to ctypes.DecoderFrom) {
	d := ctypes.NewBufDecoder(encoding.Marshal(from))
	to.DecodeFrom(d)
	if err := d.Err(); err != nil {
		panic(fmt.Sprintf("type conversion failed (%T->%T): %v", from, to, err))
	}
}

//go:embed dist
var dist embed.FS

type clientRouterFS struct {
	fs fs.FS
}

func (cr *clientRouterFS) Open(name string) (fs.File, error) {
	f, err := cr.fs.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return cr.fs.Open("index.html")
	}
	return f, err
}

func createUIHandler() http.Handler {
	assets, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(&clientRouterFS{fs: assets}))
}

type chainManager struct {
	cs modules.ConsensusSet
}

func (cm chainManager) TipState() api.ConsensusState {
	var id ctypes.BlockID
	siadConvertToCore(cm.cs.CurrentBlock().ID(), &id)
	return api.ConsensusState{
		Index: api.ChainIndex{
			Height: uint64(cm.cs.Height()),
			ID:     id,
		},
	}
}

type syncer struct {
	g  modules.Gateway
	tp modules.TransactionPool
}

func (s syncer) Addr() string {
	return string(s.g.Address())
}

func (s syncer) Peers() []string {
	var peers []string
	for _, p := range s.g.Peers() {
		peers = append(peers, string(p.NetAddress))
	}
	return peers
}

func (s syncer) Connect(addr string) error {
	return s.g.Connect(modules.NetAddress(addr))
}

func (s syncer) BroadcastTransaction(txn ctypes.Transaction, dependsOn []ctypes.Transaction) {
	var sTxn stypes.Transaction
	coreConvertToSiad(txn, &sTxn)

	var sDependsOn []stypes.Transaction
	for _, depend := range dependsOn {
		var sDepend stypes.Transaction
		coreConvertToSiad(depend, &sDependsOn)
		sDependsOn = append(sDependsOn, sDepend)
	}

	s.tp.Broadcast(append(sDependsOn, sTxn))
}

type txpool struct {
	tp modules.TransactionPool
}

func (tp txpool) RecommendedFee() ctypes.Currency {
	min, _ := tp.tp.FeeEstimation()

	var cMin ctypes.Currency
	siadConvertToCore(min, &cMin)

	return cMin
}

func (tp txpool) Transactions() (cTxns []ctypes.Transaction) {
	for _, txn := range tp.tp.Transactions() {
		var cTxn ctypes.Transaction
		siadConvertToCore(txn, &cTxn)
		cTxns = append(cTxns, cTxn)
	}
	return
}

func (tp txpool) AddTransactionSet(txns []ctypes.Transaction) error {
	var sTxns []stypes.Transaction
	for _, txn := range txns {
		var sTxn stypes.Transaction
		coreConvertToSiad(txn, &sTxn)
		sTxns = append(sTxns, sTxn)
	}
	return tp.tp.AcceptTransactionSet(sTxns)
}

func (tp txpool) UnconfirmedParents(txn ctypes.Transaction) ([]ctypes.Transaction, error) {
	pool := tp.Transactions()
	outputToParent := make(map[ctypes.SiacoinOutputID]*ctypes.Transaction)
	for i, txn := range pool {
		for j := range txn.SiacoinOutputs {
			scoid := txn.SiacoinOutputID(j)
			outputToParent[scoid] = &pool[i]
		}
	}
	var parents []ctypes.Transaction
	seen := make(map[ctypes.TransactionID]bool)
	for _, sci := range txn.SiacoinInputs {
		if parent, ok := outputToParent[sci.ParentID]; ok {
			if txid := parent.ID(); !seen[txid] {
				seen[txid] = true
				parents = append(parents, *parent)
			}
		}
	}
	return parents, nil
}

func startWeb(l net.Listener, node *node, password string) error {
	renter := api.NewServer(&chainManager{node.cm}, &syncer{node.g, node.tp}, txpool{node.tp}, node.w)
	api := jape.AuthMiddleware(renter, password)
	web := createUIHandler()
	return http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api") {
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api")
			api.ServeHTTP(w, r)
			return
		}
		web.ServeHTTP(w, r)
	}))
}
