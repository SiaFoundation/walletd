package api_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/syncer"
	"go.sia.tech/coreutils/testutil"
	"go.sia.tech/jape"
	"go.sia.tech/walletd/api"
	"go.sia.tech/walletd/persist/sqlite"
	"go.sia.tech/walletd/wallet"
	"go.uber.org/zap/zaptest"
	"lukechampine.com/frand"
)

func testNetwork() (*consensus.Network, types.Block) {
	// use a modified version of Zen
	n, genesisBlock := chain.TestnetZen()
	n.InitialTarget = types.BlockID{0xFF}
	n.HardforkDevAddr.Height = 1
	n.HardforkTax.Height = 1
	n.HardforkStorageProof.Height = 1
	n.HardforkOak.Height = 1
	n.HardforkASIC.Height = 1
	n.HardforkFoundation.Height = 1
	n.HardforkV2.AllowHeight = 5
	n.HardforkV2.RequireHeight = 10
	return n, genesisBlock
}

func runServer(t *testing.T, cm api.ChainManager, s api.Syncer, wm api.WalletManager) *api.Client {
	t.Helper()

	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal("failed to listen:", err)
	}
	t.Cleanup(func() { l.Close() })

	server := &http.Server{
		Handler:      api.NewServer(cm, s, wm, api.WithDebug(), api.WithLogger(zaptest.NewLogger(t))),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}
	t.Cleanup(func() { server.Close() })

	go server.Serve(l)
	return api.NewClient("http://"+l.Addr().String(), "password")
}

func waitForBlock(tb testing.TB, cm *chain.Manager, ws wallet.Store) {
	for i := 0; i < 1000; i++ {
		time.Sleep(10 * time.Millisecond)
		tip, _ := ws.LastCommittedIndex()
		if tip == cm.Tip() {
			return
		}
	}
	tb.Fatal("timed out waiting for block")
}

func TestWalletAdd(t *testing.T) {
	log := zaptest.NewLogger(t)

	n, genesisBlock := testNetwork()
	giftPrivateKey := types.GeneratePrivateKey()
	giftAddress := types.StandardUnlockHash(giftPrivateKey.PublicKey())
	genesisBlock.Transactions[0].SiacoinOutputs[0] = types.SiacoinOutput{
		Value:   types.Siacoins(1),
		Address: giftAddress,
	}

	// create wallets
	dbstore, tipState, err := chain.NewDBStore(chain.NewMemDB(), n, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(dbstore, tipState)

	ws, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "wallets.db"), log.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	wm, err := wallet.NewManager(cm, ws, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	c := runServer(t, cm, nil, wm)

	checkWalletResponse := func(wr api.WalletUpdateRequest, w wallet.Wallet, isUpdate bool) error {
		// check wallet
		if w.Name != wr.Name {
			return fmt.Errorf("expected wallet name to be %v, got %v", wr.Name, w.Name)
		} else if w.Description != wr.Description {
			return fmt.Errorf("expected wallet description to be %v, got %v", wr.Description, w.Description)
		} else if w.DateCreated.After(time.Now()) {
			return fmt.Errorf("expected wallet creation date to be in the past, got %v", w.DateCreated)
		} else if isUpdate && w.DateCreated == w.LastUpdated {
			return fmt.Errorf("expected wallet last updated date to be after creation %v, got %v", w.DateCreated, w.LastUpdated)
		}

		if wr.Metadata == nil && string(w.Metadata) == "null" { // zero value encodes as "null"
			return nil
		}

		// check metadata
		var am, bm map[string]any
		if err := json.Unmarshal(wr.Metadata, &am); err != nil {
			return fmt.Errorf("failed to unmarshal metadata a %q: %v", wr.Metadata, err)
		} else if err := json.Unmarshal(w.Metadata, &bm); err != nil {
			return fmt.Errorf("failed to unmarshal metadata b: %v", err)
		}

		if !reflect.DeepEqual(am, bm) { // not perfect, but probably enough for this test
			return fmt.Errorf("expected metadata to be equal %v, got %v", wr.Metadata, w.Metadata)
		}
		return nil
	}

	checkWallet := func(wa, wb wallet.Wallet) error {
		// check wallet
		if wa.Name != wb.Name {
			return fmt.Errorf("expected wallet name to be %v, got %v", wa.Name, wb.Name)
		} else if wa.Description != wb.Description {
			return fmt.Errorf("expected wallet description to be %v, got %v", wa.Description, wb.Description)
		} else if wa.DateCreated.Unix() != wb.DateCreated.Unix() {
			return fmt.Errorf("expected wallet creation date to be %v, got %v", wa.DateCreated, wb.DateCreated)
		} else if wa.LastUpdated.Unix() != wb.LastUpdated.Unix() {
			return fmt.Errorf("expected wallet last updated date to be %v, got %v", wa.LastUpdated, wb.LastUpdated)
		}

		if wa.Metadata == nil && string(wb.Metadata) == "null" { // zero value encodes as "null"
			return nil
		}

		// check metadata
		var am, bm map[string]any
		if err := json.Unmarshal(wa.Metadata, &am); err != nil {
			return fmt.Errorf("failed to unmarshal metadata a %q: %v", wa.Metadata, err)
		} else if err := json.Unmarshal(wb.Metadata, &bm); err != nil {
			return fmt.Errorf("failed to unmarshal metadata b %q: %v", wb.Metadata, err)
		}

		if !reflect.DeepEqual(am, bm) { // not perfect, but probably enough for this test
			return fmt.Errorf("expected metadata to be equal %v, got %v", wa.Metadata, wb.Metadata)
		}
		return nil
	}

	tests := []struct {
		Initial api.WalletUpdateRequest
		Update  api.WalletUpdateRequest
	}{
		{
			Initial: api.WalletUpdateRequest{Name: hex.EncodeToString(frand.Bytes(12))},
			Update:  api.WalletUpdateRequest{Name: hex.EncodeToString(frand.Bytes(12))},
		},
		{
			Initial: api.WalletUpdateRequest{Name: hex.EncodeToString(frand.Bytes(12)), Description: "hello, world!"},
			Update:  api.WalletUpdateRequest{Name: hex.EncodeToString(frand.Bytes(12)), Description: "goodbye, world!"},
		},
		{
			Initial: api.WalletUpdateRequest{Name: hex.EncodeToString(frand.Bytes(12)), Metadata: []byte(`{"foo": { "foo": "bar"}}`)},
			Update:  api.WalletUpdateRequest{Name: hex.EncodeToString(frand.Bytes(12)), Metadata: []byte(`{"foo": { "foo": "baz"}}`)},
		},
		{
			Initial: api.WalletUpdateRequest{Name: hex.EncodeToString(frand.Bytes(12)), Description: "hello, world!", Metadata: []byte(`{"foo": { "foo": "bar"}}`)},
			Update:  api.WalletUpdateRequest{Name: hex.EncodeToString(frand.Bytes(12)), Description: "goodbye, world!", Metadata: []byte(`{"foo": { "foo": "baz"}}`)},
		},
		{
			Initial: api.WalletUpdateRequest{Name: "constant name", Description: "constant description", Metadata: []byte(`{"foo": { "foo": "bar"}}`)},
			Update:  api.WalletUpdateRequest{Name: "constant name", Description: "constant description", Metadata: []byte(`{"foo": { "foo": "baz"}}`)},
		},
	}

	var expectedWallets []wallet.Wallet
	for i, test := range tests {
		w, err := c.AddWallet(test.Initial)
		if err != nil {
			t.Fatal(err)
		} else if err := checkWalletResponse(test.Initial, w, false); err != nil {
			t.Fatalf("test %v: %v", i, err)
		}

		expectedWallets = append(expectedWallets, w)
		// check that the wallet was added
		wallets, err := c.Wallets()
		if err != nil {
			t.Fatal(err)
		} else if len(wallets) != len(expectedWallets) {
			t.Fatalf("test %v: expected %v wallets, got %v", i, len(expectedWallets), len(wallets))
		}
		for j, w := range wallets {
			if err := checkWallet(expectedWallets[j], w); err != nil {
				t.Fatalf("test %v: wallet %v: %v", i, j, err)
			}
		}

		time.Sleep(time.Second) // ensure LastUpdated is different

		w, err = c.UpdateWallet(w.ID, test.Update)
		if err != nil {
			t.Fatal(err)
		} else if err := checkWalletResponse(test.Update, w, true); err != nil {
			t.Fatalf("test %v: %v", i, err)
		}

		// check that the wallet was updated
		expectedWallets[len(expectedWallets)-1] = w
		wallets, err = c.Wallets()
		if err != nil {
			t.Fatal(err)
		} else if len(wallets) != len(expectedWallets) {
			t.Fatalf("test %v: expected %v wallets, got %v", i, len(expectedWallets), len(wallets))
		}
		for j, w := range wallets {
			if err := checkWallet(expectedWallets[j], w); err != nil {
				t.Fatalf("test %v: wallet %v: %v", i, j, err)
			}
		}
	}
}

func TestWallet(t *testing.T) {
	log := zaptest.NewLogger(t)

	// create syncer
	syncerListener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer syncerListener.Close()

	// create chain manager
	n, genesisBlock := testNetwork()
	giftPrivateKey := types.GeneratePrivateKey()
	giftAddress := types.StandardUnlockHash(giftPrivateKey.PublicKey())
	genesisBlock.Transactions[0].SiacoinOutputs[0] = types.SiacoinOutput{
		Value:   types.Siacoins(1),
		Address: giftAddress,
	}

	dbstore, tipState, err := chain.NewDBStore(chain.NewMemDB(), n, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(dbstore, tipState)

	// create the sqlite store
	ws, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "wallets.db"), log.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	peerStore, err := sqlite.NewPeerStore(ws)
	if err != nil {
		t.Fatal(err)
	}

	// create the syncer
	s := syncer.New(syncerListener, cm, peerStore, gateway.Header{
		GenesisID:  genesisBlock.ID(),
		UniqueID:   gateway.GenerateUniqueID(),
		NetAddress: syncerListener.Addr().String(),
	})

	// create the wallet manager
	wm, err := wallet.NewManager(cm, ws, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	// create seed address vault
	sav := wallet.NewSeedAddressVault(wallet.NewSeed(), 0, 20)

	// run server
	c := runServer(t, cm, s, wm)
	w, err := c.AddWallet(api.WalletUpdateRequest{Name: "primary"})
	if err != nil {
		t.Fatal(err)
	} else if w.Name != "primary" {
		t.Fatalf("expected wallet name to be 'primary', got %v", w.Name)
	}
	wc := c.Wallet(w.ID)
	if err := c.Rescan(0); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, ws)

	balance, err := wc.Balance()
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.IsZero() || !balance.ImmatureSiacoins.IsZero() || balance.Siafunds != 0 {
		t.Fatal("balance should be 0")
	}

	// shouldn't have any events yet
	events, err := wc.Events(0, -1)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 0 {
		t.Fatal("event history should be empty")
	}

	// shouldn't have any addresses yet
	addresses, err := wc.Addresses()
	if err != nil {
		t.Fatal(err)
	} else if len(addresses) != 0 {
		t.Fatal("address list should be empty")
	}

	// create and add an address
	addr := sav.NewAddress("primary")
	if err := wc.AddAddress(addr); err != nil {
		t.Fatal(err)
	}

	// should have an address now
	addresses, err = wc.Addresses()
	if err != nil {
		t.Fatal(err)
	} else if len(addresses) != 1 {
		t.Fatal("address list should have one address")
	} else if addresses[0].Address != addr.Address {
		t.Fatalf("address should be %v, got %v", addr, addresses[0])
	}

	// send gift to wallet
	giftSCOID := genesisBlock.Transactions[0].SiacoinOutputID(0)
	txn := types.Transaction{
		SiacoinInputs: []types.SiacoinInput{{
			ParentID:         giftSCOID,
			UnlockConditions: types.StandardUnlockConditions(giftPrivateKey.PublicKey()),
		}},
		SiacoinOutputs: []types.SiacoinOutput{
			{Address: addr.Address, Value: types.Siacoins(1).Div64(2)},
			{Address: addr.Address, Value: types.Siacoins(1).Div64(2)},
		},
		Signatures: []types.TransactionSignature{{
			ParentID:      types.Hash256(giftSCOID),
			CoveredFields: types.CoveredFields{WholeTransaction: true},
		}},
	}
	sig := giftPrivateKey.SignHash(cm.TipState().WholeSigHash(txn, types.Hash256(giftSCOID), 0, 0, nil))
	txn.Signatures[0].Signature = sig[:]

	// broadcast the transaction to the transaction pool
	if err := c.TxpoolBroadcast(cm.Tip(), []types.Transaction{txn}, nil); err != nil {
		t.Fatal(err)
	}

	// shouldn't have any events yet
	events, err = wc.Events(0, -1)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 0 {
		t.Fatal("event history should be empty")
	}

	unconfirmed, err := wc.UnconfirmedEvents()
	if err != nil {
		t.Fatal(err)
	} else if len(unconfirmed) != 1 {
		t.Fatal("txpool should have one transaction")
	}

	cs := cm.TipState()
	b := types.Block{
		ParentID:     cs.Index.ID,
		Timestamp:    types.CurrentTimestamp(),
		MinerPayouts: []types.SiacoinOutput{{Address: types.VoidAddress, Value: cs.BlockReward()}},
		Transactions: []types.Transaction{txn},
	}

	for b.ID().CmpWork(cs.ChildTarget) < 0 {
		b.Nonce += cs.NonceFactor()
	}
	if err := cm.AddBlocks([]types.Block{b}); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, ws)

	// get new balance
	balance, err = wc.Balance()
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.Equals(types.Siacoins(1)) {
		t.Fatal("balance should be 1 SC, got", balance.Siacoins)
	} else if !balance.ImmatureSiacoins.IsZero() {
		t.Fatal("immature balance should be 0 SC, got", balance.ImmatureSiacoins)
	}

	// transaction should appear in history
	events, err = wc.Events(0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) == 0 {
		t.Fatal("transaction should appear in history")
	}

	outputs, basis, err := wc.SiacoinOutputs(0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(outputs) != 2 {
		t.Fatal("should have two UTXOs, got", len(outputs))
	} else if basis != cm.Tip() {
		t.Fatalf("basis should be %v, got %v", cm.Tip(), basis)
	}

	// mine a block to add an immature balance
	cs = cm.TipState()
	b = types.Block{
		ParentID:     cs.Index.ID,
		Timestamp:    types.CurrentTimestamp(),
		MinerPayouts: []types.SiacoinOutput{{Address: addr.Address, Value: cs.BlockReward()}},
	}
	for b.ID().CmpWork(cs.ChildTarget) < 0 {
		b.Nonce += cs.NonceFactor()
	}
	if err := cm.AddBlocks([]types.Block{b}); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, ws)

	// get new balance
	balance, err = wc.Balance()
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.Equals(types.Siacoins(1)) {
		t.Fatal("balance should be 1 SC, got", balance.Siacoins)
	} else if !balance.ImmatureSiacoins.Equals(b.MinerPayouts[0].Value) {
		t.Fatalf("immature balance should be %d SC, got %d SC", b.MinerPayouts[0].Value, balance.ImmatureSiacoins)
	}

	// mine enough blocks for the miner payout to mature
	expectedBalance := types.Siacoins(1).Add(b.MinerPayouts[0].Value)
	target := cs.MaturityHeight()
	for cs.Index.Height < target {
		cs = cm.TipState()
		b := types.Block{
			ParentID:     cs.Index.ID,
			Timestamp:    types.CurrentTimestamp(),
			MinerPayouts: []types.SiacoinOutput{{Address: types.VoidAddress, Value: cs.BlockReward()}},
		}
		for b.ID().CmpWork(cs.ChildTarget) < 0 {
			b.Nonce += cs.NonceFactor()
		}
		if err := cm.AddBlocks([]types.Block{b}); err != nil {
			t.Fatal(err)
		}
	}
	waitForBlock(t, cm, ws)

	// get new balance
	balance, err = wc.Balance()
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.Equals(expectedBalance) {
		t.Fatalf("balance should be %d, got %d", expectedBalance, balance.Siacoins)
	} else if !balance.ImmatureSiacoins.IsZero() {
		t.Fatal("immature balance should be 0 SC, got", balance.ImmatureSiacoins)
	}
}

func TestAddresses(t *testing.T) {
	log := zaptest.NewLogger(t)

	n, genesisBlock := testNetwork()
	giftPrivateKey := types.GeneratePrivateKey()
	giftAddress := types.StandardUnlockHash(giftPrivateKey.PublicKey())
	genesisBlock.Transactions[0].SiacoinOutputs[0] = types.SiacoinOutput{
		Value:   types.Siacoins(1),
		Address: giftAddress,
	}

	// create wallets
	dbstore, tipState, err := chain.NewDBStore(chain.NewMemDB(), n, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(dbstore, tipState)

	ws, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "wallets.db"), log.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	wm, err := wallet.NewManager(cm, ws, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	sav := wallet.NewSeedAddressVault(wallet.NewSeed(), 0, 20)
	c := runServer(t, cm, nil, wm)
	w, err := c.AddWallet(api.WalletUpdateRequest{Name: "primary"})
	if err != nil {
		t.Fatal(err)
	} else if w.Name != "primary" {
		t.Fatalf("expected wallet name to be 'primary', got %v", w.Name)
	}
	wc := c.Wallet(w.ID)
	if err := c.Rescan(0); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, ws)

	balance, err := wc.Balance()
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.IsZero() || !balance.ImmatureSiacoins.IsZero() || balance.Siafunds != 0 {
		t.Fatal("balance should be 0")
	}

	// shouldn't have any events yet
	events, err := wc.Events(0, -1)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 0 {
		t.Fatal("event history should be empty")
	}

	// shouldn't have any addresses yet
	addresses, err := wc.Addresses()
	if err != nil {
		t.Fatal(err)
	} else if len(addresses) != 0 {
		t.Fatal("address list should be empty")
	}

	// create and add an address
	addr := sav.NewAddress("primary")
	if err := wc.AddAddress(addr); err != nil {
		t.Fatal(err)
	}

	// should have an address now
	addresses, err = wc.Addresses()
	if err != nil {
		t.Fatal(err)
	} else if len(addresses) != 1 {
		t.Fatal("address list should have one address")
	} else if addresses[0].Address != addr.Address {
		t.Fatalf("address should be %v, got %v", addr, addresses[0])
	}

	// send gift to wallet
	giftSCOID := genesisBlock.Transactions[0].SiacoinOutputID(0)
	txn := types.Transaction{
		SiacoinInputs: []types.SiacoinInput{{
			ParentID:         giftSCOID,
			UnlockConditions: types.StandardUnlockConditions(giftPrivateKey.PublicKey()),
		}},
		SiacoinOutputs: []types.SiacoinOutput{
			{Address: addr.Address, Value: types.Siacoins(1).Div64(2)},
			{Address: addr.Address, Value: types.Siacoins(1).Div64(2)},
		},
		Signatures: []types.TransactionSignature{{
			ParentID:      types.Hash256(giftSCOID),
			CoveredFields: types.CoveredFields{WholeTransaction: true},
		}},
	}
	sig := giftPrivateKey.SignHash(cm.TipState().WholeSigHash(txn, types.Hash256(giftSCOID), 0, 0, nil))
	txn.Signatures[0].Signature = sig[:]

	cs := cm.TipState()
	b := types.Block{
		ParentID:     cs.Index.ID,
		Timestamp:    types.CurrentTimestamp(),
		MinerPayouts: []types.SiacoinOutput{{Address: types.VoidAddress, Value: cs.BlockReward()}},
		Transactions: []types.Transaction{txn},
	}
	for b.ID().CmpWork(cs.ChildTarget) < 0 {
		b.Nonce += cs.NonceFactor()
	}
	if err := cm.AddBlocks([]types.Block{b}); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, ws)

	// get new balance
	balance, err = c.AddressBalance(addr.Address)
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.Equals(types.Siacoins(1)) {
		t.Fatal("balance should be 1 SC, got", balance.Siacoins)
	} else if !balance.ImmatureSiacoins.IsZero() {
		t.Fatal("immature balance should be 0 SC, got", balance.ImmatureSiacoins)
	}

	// transaction should appear in history
	events, err = c.AddressEvents(addr.Address, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) == 0 {
		t.Fatal("transaction should appear in history")
	}

	outputs, basis, err := c.AddressSiacoinOutputs(addr.Address, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(outputs) != 2 {
		t.Fatal("should have two UTXOs, got", len(outputs))
	} else if basis != cm.Tip() {
		t.Fatalf("basis should be %v, got %v", cm.Tip(), basis)
	}

	// mine a block to add an immature balance
	cs = cm.TipState()
	b = types.Block{
		ParentID:     cs.Index.ID,
		Timestamp:    types.CurrentTimestamp(),
		MinerPayouts: []types.SiacoinOutput{{Address: addr.Address, Value: cs.BlockReward()}},
	}
	for b.ID().CmpWork(cs.ChildTarget) < 0 {
		b.Nonce += cs.NonceFactor()
	}
	if err := cm.AddBlocks([]types.Block{b}); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, ws)

	// get new balance
	balance, err = c.AddressBalance(addr.Address)
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.Equals(types.Siacoins(1)) {
		t.Fatal("balance should be 1 SC, got", balance.Siacoins)
	} else if !balance.ImmatureSiacoins.Equals(b.MinerPayouts[0].Value) {
		t.Fatalf("immature balance should be %d SC, got %d SC", b.MinerPayouts[0].Value, balance.ImmatureSiacoins)
	}

	// mine enough blocks for the miner payout to mature
	expectedBalance := types.Siacoins(1).Add(b.MinerPayouts[0].Value)
	target := cs.MaturityHeight()
	for cs.Index.Height < target {
		cs = cm.TipState()
		b := types.Block{
			ParentID:     cs.Index.ID,
			Timestamp:    types.CurrentTimestamp(),
			MinerPayouts: []types.SiacoinOutput{{Address: types.VoidAddress, Value: cs.BlockReward()}},
		}
		for b.ID().CmpWork(cs.ChildTarget) < 0 {
			b.Nonce += cs.NonceFactor()
		}
		if err := cm.AddBlocks([]types.Block{b}); err != nil {
			t.Fatal(err)
		}
	}
	waitForBlock(t, cm, ws)

	// get new balance
	balance, err = c.AddressBalance(addr.Address)
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.Equals(expectedBalance) {
		t.Fatalf("balance should be %d, got %d", expectedBalance, balance.Siacoins)
	} else if !balance.ImmatureSiacoins.IsZero() {
		t.Fatal("immature balance should be 0 SC, got", balance.ImmatureSiacoins)
	}
}

func TestV2(t *testing.T) {
	log := zaptest.NewLogger(t)

	n, genesisBlock := testNetwork()
	// gift primary wallet some coins
	primaryPrivateKey := types.GeneratePrivateKey()
	primaryAddress := types.StandardUnlockHash(primaryPrivateKey.PublicKey())
	genesisBlock.Transactions[0].SiacoinOutputs[0].Address = primaryAddress
	// secondary wallet starts with nothing
	secondaryPrivateKey := types.GeneratePrivateKey()
	secondaryAddress := types.StandardUnlockHash(secondaryPrivateKey.PublicKey())

	// create wallets
	dbstore, tipState, err := chain.NewDBStore(chain.NewMemDB(), n, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(dbstore, tipState)
	ws, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "wallets.db"), log.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	wm, err := wallet.NewManager(cm, ws, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	c := runServer(t, cm, nil, wm)
	primaryWallet, err := c.AddWallet(api.WalletUpdateRequest{Name: "primary"})
	if err != nil {
		t.Fatal(err)
	}
	primary := c.Wallet(primaryWallet.ID)
	if err := primary.AddAddress(wallet.Address{Address: primaryAddress}); err != nil {
		t.Fatal(err)
	}
	secondaryWallet, err := c.AddWallet(api.WalletUpdateRequest{Name: "secondary"})
	if err != nil {
		t.Fatal(err)
	}
	secondary := c.Wallet(secondaryWallet.ID)
	if err := secondary.AddAddress(wallet.Address{Address: secondaryAddress}); err != nil {
		t.Fatal(err)
	}

	if err := c.Rescan(0); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm, ws)

	// define some helper functions
	addBlock := func(txns []types.Transaction, v2txns []types.V2Transaction) error {
		cs := cm.TipState()
		b := types.Block{
			ParentID:     cs.Index.ID,
			Timestamp:    types.CurrentTimestamp(),
			MinerPayouts: []types.SiacoinOutput{{Address: types.VoidAddress, Value: cs.BlockReward()}},
			Transactions: txns,
		}
		if v2txns != nil {
			b.V2 = &types.V2BlockData{
				Height:       cs.Index.Height + 1,
				Transactions: v2txns,
			}
			b.V2.Commitment = cs.Commitment(cs.TransactionsCommitment(b.Transactions, b.V2Transactions()), b.MinerPayouts[0].Address)
		}
		for b.ID().CmpWork(cs.ChildTarget) < 0 {
			b.Nonce += cs.NonceFactor()
		}
		return cm.AddBlocks([]types.Block{b})
	}
	checkBalances := func(p, s types.Currency) {
		t.Helper()
		waitForBlock(t, cm, ws)
		if primaryBalance, err := primary.Balance(); err != nil {
			t.Fatal(err)
		} else if !primaryBalance.Siacoins.Equals(p) {
			t.Fatalf("primary should have balance of %v, got %v", p, primaryBalance.Siacoins)
		}
		if secondaryBalance, err := secondary.Balance(); err != nil {
			t.Fatal(err)
		} else if !secondaryBalance.Siacoins.Equals(s) {
			t.Fatalf("secondary should have balance of %v, got %v", s, secondaryBalance.Siacoins)
		}
	}
	sendV1 := func() error {
		t.Helper()
		waitForBlock(t, cm, ws)

		// which wallet is sending?
		key := primaryPrivateKey
		dest := secondaryAddress
		pbal, sbal := types.ZeroCurrency, types.ZeroCurrency
		sces, basis, err := primary.SiacoinOutputs(0, 100)
		if err != nil {
			t.Fatal(err)
		} else if basis != cm.Tip() {
			t.Fatalf("basis should be %v, got %v", cm.Tip(), basis)
		}
		if len(sces) == 0 {
			sces, basis, err = secondary.SiacoinOutputs(0, 100)
			if err != nil {
				t.Fatal(err)
			} else if basis != cm.Tip() {
				t.Fatalf("basis should be %v, got %v", cm.Tip(), basis)
			}
			key = secondaryPrivateKey
			dest = primaryAddress
			pbal = sces[0].SiacoinOutput.Value
		} else {
			sbal = sces[0].SiacoinOutput.Value
		}
		sce := sces[0]

		txn := types.Transaction{
			SiacoinInputs: []types.SiacoinInput{{
				ParentID:         types.SiacoinOutputID(sce.ID),
				UnlockConditions: types.StandardUnlockConditions(key.PublicKey()),
			}},
			SiacoinOutputs: []types.SiacoinOutput{{
				Address: dest,
				Value:   sce.SiacoinOutput.Value,
			}},
			Signatures: []types.TransactionSignature{{
				ParentID:      types.Hash256(sce.ID),
				CoveredFields: types.CoveredFields{WholeTransaction: true},
			}},
		}
		sig := key.SignHash(cm.TipState().WholeSigHash(txn, types.Hash256(sce.ID), 0, 0, nil))
		txn.Signatures[0].Signature = sig[:]
		if err := addBlock([]types.Transaction{txn}, nil); err != nil {
			return err
		}
		checkBalances(pbal, sbal)
		return nil
	}
	sendV2 := func() error {
		t.Helper()
		waitForBlock(t, cm, ws)

		// which wallet is sending?
		key := primaryPrivateKey
		dest := secondaryAddress
		pbal, sbal := types.ZeroCurrency, types.ZeroCurrency
		sces, basis, err := primary.SiacoinOutputs(0, 100)
		if err != nil {
			t.Fatal(err)
		} else if basis != cm.Tip() {
			t.Fatalf("basis should be %v, got %v", cm.Tip(), basis)
		}
		if len(sces) == 0 {
			sces, _, err = secondary.SiacoinOutputs(0, 100)
			if err != nil {
				t.Fatal(err)
			}
			key = secondaryPrivateKey
			dest = primaryAddress
			pbal = sces[0].SiacoinOutput.Value
		} else {
			sbal = sces[0].SiacoinOutput.Value
		}
		sce := sces[0]

		txn := types.V2Transaction{
			SiacoinInputs: []types.V2SiacoinInput{{
				Parent: sce,
				SatisfiedPolicy: types.SatisfiedPolicy{
					Policy: types.SpendPolicy{Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(key.PublicKey()))},
				},
			}},
			SiacoinOutputs: []types.SiacoinOutput{{
				Address: dest,
				Value:   sce.SiacoinOutput.Value,
			}},
		}
		txn.SiacoinInputs[0].SatisfiedPolicy.Signatures = []types.Signature{key.SignHash(cm.TipState().InputSigHash(txn))}
		if err := addBlock(nil, []types.V2Transaction{txn}); err != nil {
			return err
		}
		checkBalances(pbal, sbal)
		return nil
	}

	// attempt to send primary->secondary with a v2 txn; should fail
	if err := sendV2(); err == nil {
		t.Fatal("expected v2 txn to be rejected")
	}
	// use a v1 transaction instead
	if err := sendV1(); err != nil {
		t.Fatal(err)
	}

	// mine past v2 allow height
	for cm.Tip().Height <= n.HardforkV2.AllowHeight {
		if err := addBlock(nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	// now send coins back with a v2 transaction
	if err := sendV2(); err != nil {
		t.Fatal(err)
	}
	// v1 transactions should also still work
	if err := sendV1(); err != nil {
		t.Fatal(err)
	}

	// mine past v2 require height
	for cm.Tip().Height <= n.HardforkV2.RequireHeight {
		if err := addBlock(nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	// v1 transactions should no longer work
	if err := sendV1(); err == nil {
		t.Fatal("expected v1 txn to be rejected")
	}
	// use a v2 transaction instead
	if err := sendV2(); err != nil {
		t.Fatal(err)
	}
}

func TestP2P(t *testing.T) {
	t.Skip("flaky test") // TODO refactor

	logger := zaptest.NewLogger(t)
	n, genesisBlock := testNetwork()
	// gift primary wallet some coins
	primaryPrivateKey := types.GeneratePrivateKey()
	primaryAddress := types.StandardUnlockHash(primaryPrivateKey.PublicKey())
	genesisBlock.Transactions[0].SiacoinOutputs[0].Address = primaryAddress
	// secondary wallet starts with nothing
	secondaryPrivateKey := types.GeneratePrivateKey()
	secondaryAddress := types.StandardUnlockHash(secondaryPrivateKey.PublicKey())

	// create wallets
	dbstore1, tipState, err := chain.NewDBStore(chain.NewMemDB(), n, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	log1 := logger.Named("one")
	cm1 := chain.NewManager(dbstore1, tipState)
	store1, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "wallets.db"), log1.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store1.Close()

	peerStore, err := sqlite.NewPeerStore(store1)
	if err != nil {
		t.Fatal(err)
	}

	wm1, err := wallet.NewManager(cm1, store1, wallet.WithLogger(log1.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm1.Close()

	l1, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer l1.Close()
	s1 := syncer.New(l1, cm1, peerStore, gateway.Header{
		GenesisID:  genesisBlock.ID(),
		UniqueID:   gateway.GenerateUniqueID(),
		NetAddress: l1.Addr().String(),
	})
	go s1.Run()
	defer s1.Close()
	c1 := runServer(t, cm1, s1, wm1)
	w1, err := c1.AddWallet(api.WalletUpdateRequest{Name: "primary"})
	if err != nil {
		t.Fatal(err)
	}
	primary := c1.Wallet(w1.ID)
	if err := primary.AddAddress(wallet.Address{Address: primaryAddress}); err != nil {
		t.Fatal(err)
	}
	if err := c1.Rescan(0); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm1, store1)

	dbstore2, tipState, err := chain.NewDBStore(chain.NewMemDB(), n, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	log2 := logger.Named("two")
	cm2 := chain.NewManager(dbstore2, tipState)
	store2, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "wallets.db"), log2.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	wm2, err := wallet.NewManager(cm2, store2, wallet.WithLogger(log2.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm2.Close()

	l2, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	s2 := syncer.New(l2, cm2, peerStore, gateway.Header{
		GenesisID:  genesisBlock.ID(),
		UniqueID:   gateway.GenerateUniqueID(),
		NetAddress: l2.Addr().String(),
	}, syncer.WithLogger(zaptest.NewLogger(t)))
	go s2.Run()
	defer s2.Close()
	c2 := runServer(t, cm2, s2, wm2)

	w2, err := c2.AddWallet(api.WalletUpdateRequest{Name: "secondary"})
	if err != nil {
		t.Fatal(err)
	}
	secondary := c2.Wallet(w2.ID)
	if err := secondary.AddAddress(wallet.Address{Address: secondaryAddress}); err != nil {
		t.Fatal(err)
	}
	if err := c2.Rescan(0); err != nil {
		t.Fatal(err)
	}
	waitForBlock(t, cm2, store2)

	// define some helper functions
	addBlock := func() error {
		// choose a client at random
		c := c1
		if frand.Intn(2) == 0 {
			c = c2
		}

		cs, err := c.ConsensusTipState()
		if err != nil {
			return err
		}

		_, txns, v2txns, err := c.TxpoolTransactions()
		if err != nil {
			return err
		}
		b := types.Block{
			ParentID:     cs.Index.ID,
			Timestamp:    types.CurrentTimestamp(),
			MinerPayouts: []types.SiacoinOutput{{Address: types.VoidAddress, Value: cs.BlockReward()}},
			Transactions: txns,
		}
		if len(v2txns) > 0 {
			b.V2 = &types.V2BlockData{
				Height:       cs.Index.Height + 1,
				Transactions: v2txns,
			}
			b.V2.Commitment = cs.Commitment(cs.TransactionsCommitment(b.Transactions, b.V2Transactions()), b.MinerPayouts[0].Address)
		}
		for b.ID().CmpWork(cs.ChildTarget) < 0 {
			b.Nonce += cs.NonceFactor()
		}
		if err := c.SyncerBroadcastBlock(b); err != nil {
			return err
		}
		// wait for tips to update
	again:
		time.Sleep(10 * time.Millisecond)
		if tip1, err := c1.ConsensusTip(); err != nil {
			return err
		} else if tip2, err := c2.ConsensusTip(); err != nil {
			return err
		} else if tip1 == cs.Index || tip2 == cs.Index {
			goto again
		}
		return nil
	}
	checkBalances := func(p, s types.Currency) {
		t.Helper()
		waitForBlock(t, cm1, store1)
		waitForBlock(t, cm2, store2)
		if primaryBalance, err := primary.Balance(); err != nil {
			t.Fatal(err)
		} else if !primaryBalance.Siacoins.Equals(p) {
			t.Fatalf("primary should have balance of %v, got %v", p, primaryBalance.Siacoins)
		}
		if secondaryBalance, err := secondary.Balance(); err != nil {
			t.Fatal(err)
		} else if !secondaryBalance.Siacoins.Equals(s) {
			t.Fatalf("secondary should have balance of %v, got %v", s, secondaryBalance.Siacoins)
		}
	}
	sendV1 := func() error {
		t.Helper()

		// which wallet is sending?
		c := c1
		key := primaryPrivateKey
		dest := secondaryAddress
		pbal, sbal := types.ZeroCurrency, types.ZeroCurrency
		sces, _, err := primary.SiacoinOutputs(0, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(sces) == 0 {
			c = c2
			key = secondaryPrivateKey
			dest = primaryAddress
			sces, _, err = secondary.SiacoinOutputs(0, 100)
			if err != nil {
				t.Fatal(err)
			}
			pbal = sces[0].SiacoinOutput.Value
		} else {
			sbal = sces[0].SiacoinOutput.Value
		}
		sce := sces[0]

		txn := types.Transaction{
			SiacoinInputs: []types.SiacoinInput{{
				ParentID:         types.SiacoinOutputID(sce.ID),
				UnlockConditions: types.StandardUnlockConditions(key.PublicKey()),
			}},
			SiacoinOutputs: []types.SiacoinOutput{{
				Address: dest,
				Value:   sce.SiacoinOutput.Value,
			}},
			Signatures: []types.TransactionSignature{{
				ParentID:      types.Hash256(sce.ID),
				CoveredFields: types.CoveredFields{WholeTransaction: true},
			}},
		}
		cs, err := c.ConsensusTipState()
		if err != nil {
			return err
		}
		sig := key.SignHash(cs.WholeSigHash(txn, types.Hash256(sce.ID), 0, 0, nil))
		txn.Signatures[0].Signature = sig[:]
		if err := c.TxpoolBroadcast(cs.Index, []types.Transaction{txn}, nil); err != nil {
			return err
		} else if err := addBlock(); err != nil {
			return err
		}
		checkBalances(pbal, sbal)
		return nil
	}
	sendV2 := func() error {
		t.Helper()

		// which wallet is sending?
		c := c1
		key := primaryPrivateKey
		dest := secondaryAddress
		pbal, sbal := types.ZeroCurrency, types.ZeroCurrency
		sces, _, err := primary.SiacoinOutputs(0, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(sces) == 0 {
			c = c2
			key = secondaryPrivateKey
			dest = primaryAddress
			sces, _, err = secondary.SiacoinOutputs(0, 100)
			if err != nil {
				t.Fatal(err)
			}
			pbal = sces[0].SiacoinOutput.Value
		} else {
			sbal = sces[0].SiacoinOutput.Value
		}
		sce := sces[0]

		txn := types.V2Transaction{
			SiacoinInputs: []types.V2SiacoinInput{{
				Parent: sce,
				SatisfiedPolicy: types.SatisfiedPolicy{
					Policy: types.SpendPolicy{Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(key.PublicKey()))},
				},
			}},
			SiacoinOutputs: []types.SiacoinOutput{{
				Address: dest,
				Value:   sce.SiacoinOutput.Value,
			}},
		}
		cs, err := c.ConsensusTipState()
		if err != nil {
			return err
		}
		txn.SiacoinInputs[0].SatisfiedPolicy.Signatures = []types.Signature{key.SignHash(cs.InputSigHash(txn))}
		if err := c.TxpoolBroadcast(cs.Index, nil, []types.V2Transaction{txn}); err != nil {
			return err
		} else if err := addBlock(); err != nil {
			return err
		}
		checkBalances(pbal, sbal)
		return nil
	}

	// connect the syncers
	if _, err := s1.Connect(context.Background(), s2.Addr()); err != nil {
		t.Fatal(err)
	}

	// attempt to send primary->secondary with a v2 txn; should fail
	if err := sendV2(); err == nil {
		t.Fatal("expected v2 txn to be rejected")
	}
	// use a v1 transaction instead
	if err := sendV1(); err != nil {
		t.Fatal(err)
	}

	// mine past v2 allow height
	for cm1.Tip().Height <= n.HardforkV2.AllowHeight {
		if err := addBlock(); err != nil {
			t.Fatal(err)
		}
	}
	waitForBlock(t, cm1, store1)
	// now send coins back with a v2 transaction
	if err := sendV2(); err != nil {
		t.Fatal(err)
	}
	// v1 transactions should also still work
	if err := sendV1(); err != nil {
		t.Fatal(err)
	}

	// mine past v2 require height
	for cm1.Tip().Height <= n.HardforkV2.RequireHeight {
		if err := addBlock(); err != nil {
			t.Fatal(err)
		}
	}
	waitForBlock(t, cm1, store1)
	// v1 transactions should no longer work
	if err := sendV1(); err == nil {
		t.Fatal("expected v1 txn to be rejected")
	}
	// use a v2 transaction instead
	if err := sendV2(); err != nil {
		t.Fatal(err)
	}
}

func TestConsensusUpdates(t *testing.T) {
	log := zaptest.NewLogger(t)

	n, genesisBlock := testNetwork()
	giftPrivateKey := types.GeneratePrivateKey()
	giftAddress := types.StandardUnlockHash(giftPrivateKey.PublicKey())
	genesisBlock.Transactions[0].SiacoinOutputs[0] = types.SiacoinOutput{
		Value:   types.Siacoins(1),
		Address: giftAddress,
	}

	// create wallets
	dbstore, tipState, err := chain.NewDBStore(chain.NewMemDB(), n, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(dbstore, tipState)

	ws, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "wallets.db"), log.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	wm, err := wallet.NewManager(cm, ws, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	c := runServer(t, cm, nil, wm)

	for i := 0; i < 10; i++ {
		b, ok := coreutils.MineBlock(cm, types.VoidAddress, time.Second)
		if !ok {
			t.Fatal("failed to mine block")
		} else if err := cm.AddBlocks([]types.Block{b}); err != nil {
			t.Fatal(err)
		}
	}

	waitForBlock(t, cm, ws)

	reverted, applied, err := c.ConsensusUpdates(types.ChainIndex{}, 10)
	if err != nil {
		t.Fatal(err)
	} else if len(reverted) != 0 {
		t.Fatal("expected no reverted blocks")
	} else if len(applied) != 11 { // genesis + 10 mined blocks (chain manager off-by-one)
		t.Fatalf("expected 11 applied blocks, got %v", len(applied))
	}

	for i, cau := range applied {
		// using i for height since we're testing the update contents
		expected, ok := cm.BestIndex(uint64(i))
		if !ok {
			t.Fatalf("failed to get expected index for block %v", i)
		} else if cau.State.Index != expected {
			t.Fatalf("expected index %v, got %v", expected, cau.State.Index)
		} else if cau.State.Network.Name != n.Name { // TODO: better comparison. reflect.DeepEqual is failing in CI, but passing local.
			t.Fatalf("expected network to be %q, got %q", n.Name, cau.State.Network.Name)
		}
	}
}

func TestConstructSiacoins(t *testing.T) {
	log := zaptest.NewLogger(t)

	n, genesisBlock := testNetwork()
	senderPrivateKey := types.GeneratePrivateKey()
	senderPolicy := types.SpendPolicy{Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(senderPrivateKey.PublicKey()))}
	senderAddr := senderPolicy.Address()

	receiverPrivateKey := types.GeneratePrivateKey()
	receiverPolicy := types.SpendPolicy{Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(receiverPrivateKey.PublicKey()))}
	receiverAddr := receiverPolicy.Address()

	genesisBlock.Transactions[0].SiacoinOutputs[0] = types.SiacoinOutput{
		Value:   types.Siacoins(100),
		Address: senderAddr,
	}

	// create wallets
	dbstore, tipState, err := chain.NewDBStore(chain.NewMemDB(), n, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(dbstore, tipState)

	ws, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "wallets.db"), log.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	peerStore, err := sqlite.NewPeerStore(ws)
	if err != nil {
		t.Fatal(err)
	}

	syncerListener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer syncerListener.Close()

	// create the syncer
	s := syncer.New(syncerListener, cm, peerStore, gateway.Header{
		GenesisID:  genesisBlock.ID(),
		UniqueID:   gateway.GenerateUniqueID(),
		NetAddress: syncerListener.Addr().String(),
	})

	wm, err := wallet.NewManager(cm, ws, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	c := runServer(t, cm, s, wm)

	w, err := c.AddWallet(api.WalletUpdateRequest{
		Name: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}

	wc := c.Wallet(w.ID)
	// add an address with no spend policy
	err = wc.AddAddress(wallet.Address{
		Address: senderAddr,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := c.Rescan(0); err != nil {
		t.Fatal(err)
	}

	testutil.MineBlocks(t, cm, types.VoidAddress, 1)
	waitForBlock(t, cm, ws)

	// try to construct a valid transaction with no spend policy
	_, err = wc.Construct([]types.SiacoinOutput{
		{Value: types.Siacoins(1), Address: receiverAddr},
	}, nil, senderAddr)
	if !strings.Contains(err.Error(), "no spend policy") {
		t.Fatalf("expected error to contain %q, got %q", "no spend policy", err)
	}

	// add the spend policy
	err = wc.AddAddress(wallet.Address{
		Address: senderAddr,
		SpendPolicy: &types.SpendPolicy{
			Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(senderPrivateKey.PublicKey())),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// try to construct a transaction with more siafunds than the wallet holds.
	// this will lock all of the wallet's siacoins
	resp, err := wc.Construct([]types.SiacoinOutput{
		{Value: types.Siacoins(1), Address: receiverAddr},
	}, []types.SiafundOutput{
		{Value: 100000, Address: senderAddr},
	}, senderAddr)
	if !strings.Contains(err.Error(), "insufficient funds") {
		t.Fatal(err)
	}

	// construct a transaction with a single siacoin output
	// this will fail if the utxos were not unlocked
	resp, err = wc.Construct([]types.SiacoinOutput{
		{Value: types.Siacoins(1), Address: receiverAddr},
	}, nil, senderAddr)
	if err != nil {
		t.Fatal(err)
	}

	switch {
	case resp.Transaction.SiacoinOutputs[0].Address != receiverAddr:
		t.Fatalf("expected transaction to have output address %q, got %q", receiverAddr, resp.Transaction.SiacoinOutputs[0].Address)
	case !resp.Transaction.SiacoinOutputs[0].Value.Equals(types.Siacoins(1)):
		t.Fatalf("expected transaction to have output value of %v, got %v", types.Siacoins(1), resp.Transaction.SiacoinOutputs[0].Value)
	case resp.Transaction.SiacoinOutputs[1].Address != senderAddr:
		t.Fatalf("expected transaction to have change address %q, got %q", senderAddr, resp.Transaction.SiacoinOutputs[1].Address)
	case !resp.Transaction.SiacoinOutputs[1].Value.Equals(types.Siacoins(99).Sub(resp.EstimatedFee)):
		t.Fatalf("expected transaction to have change value of %v, got %v", types.Siacoins(99).Sub(resp.EstimatedFee), resp.Transaction.SiacoinOutputs[1].Value)
	}

	cs, err := c.ConsensusTipState()
	if err != nil {
		t.Fatal(err)
	}

	// sign the transaction
	for i, sig := range resp.Transaction.Signatures {
		sigHash := cs.WholeSigHash(resp.Transaction, sig.ParentID, 0, 0, nil)
		sig := senderPrivateKey.SignHash(sigHash)
		resp.Transaction.Signatures[i].Signature = sig[:]
	}

	if err := c.TxpoolBroadcast(resp.Basis, []types.Transaction{resp.Transaction}, nil); err != nil {
		t.Fatal(err)
	}

	unconfirmed, err := wc.UnconfirmedEvents()
	if err != nil {
		t.Fatal(err)
	} else if len(unconfirmed) != 1 {
		t.Fatalf("expected 1 unconfirmed event, got %v", len(unconfirmed))
	}
	expectedValue := types.Siacoins(1).Add(resp.EstimatedFee)
	sent := unconfirmed[0]
	switch {
	case types.TransactionID(sent.ID) != resp.ID:
		t.Fatalf("expected unconfirmed event to have transaction ID %q, got %q", resp.ID, sent.ID)
	case sent.Type != wallet.EventTypeV1Transaction:
		t.Fatalf("expected unconfirmed event to have type %q, got %q", wallet.EventTypeV1Transaction, sent.Type)
	case !sent.SiacoinOutflow().Sub(sent.SiacoinInflow()).Equals(expectedValue):
		t.Fatalf("expected unconfirmed event to have outflow of %v, got %v", expectedValue, sent.SiacoinOutflow().Sub(sent.SiacoinInflow()))
	}

	testutil.MineBlocks(t, cm, types.VoidAddress, 1)
	waitForBlock(t, cm, ws)

	confirmed, err := wc.Events(0, 5)
	if err != nil {
		t.Fatal(err)
	} else if len(confirmed) != 2 {
		t.Fatalf("expected 2 confirmed events, got %v", len(confirmed)) // initial gift + sent transaction
	}
	sent = confirmed[0]
	switch {
	case types.TransactionID(sent.ID) != resp.ID:
		t.Fatalf("expected confirmed event to have transaction ID %q, got %q", resp.ID, sent.ID)
	case sent.Type != wallet.EventTypeV1Transaction:
		t.Fatalf("expected confirmed event to have type %q, got %q", wallet.EventTypeV1Transaction, sent.Type)
	case !sent.SiacoinOutflow().Sub(sent.SiacoinInflow()).Equals(expectedValue):
		t.Fatalf("expected confirmed event to have outflow of %v, got %v", expectedValue, sent.SiacoinOutflow().Sub(sent.SiacoinInflow()))
	}
}

func TestConstructSiafunds(t *testing.T) {
	log := zaptest.NewLogger(t)

	n, genesisBlock := testNetwork()
	senderPrivateKey := types.GeneratePrivateKey()
	senderPolicy := types.SpendPolicy{Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(senderPrivateKey.PublicKey()))}
	senderAddr := senderPolicy.Address()

	receiverPrivateKey := types.GeneratePrivateKey()
	receiverPolicy := types.SpendPolicy{Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(receiverPrivateKey.PublicKey()))}
	receiverAddr := receiverPolicy.Address()

	genesisBlock.Transactions[0].SiacoinOutputs[0] = types.SiacoinOutput{
		Value:   types.Siacoins(100),
		Address: senderAddr,
	}
	genesisBlock.Transactions[0].SiafundOutputs[0].Address = senderAddr

	// create wallets
	dbstore, tipState, err := chain.NewDBStore(chain.NewMemDB(), n, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(dbstore, tipState)

	ws, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "wallets.db"), log.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	peerStore, err := sqlite.NewPeerStore(ws)
	if err != nil {
		t.Fatal(err)
	}

	syncerListener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer syncerListener.Close()

	// create the syncer
	s := syncer.New(syncerListener, cm, peerStore, gateway.Header{
		GenesisID:  genesisBlock.ID(),
		UniqueID:   gateway.GenerateUniqueID(),
		NetAddress: syncerListener.Addr().String(),
	})

	wm, err := wallet.NewManager(cm, ws, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	c := runServer(t, cm, s, wm)

	w, err := c.AddWallet(api.WalletUpdateRequest{
		Name: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}

	wc := c.Wallet(w.ID)
	err = wc.AddAddress(wallet.Address{
		Address:     senderAddr,
		SpendPolicy: &senderPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := c.Rescan(0); err != nil {
		t.Fatal(err)
	}

	testutil.MineBlocks(t, cm, types.VoidAddress, 1)
	waitForBlock(t, cm, ws)

	resp, err := wc.Construct(nil, []types.SiafundOutput{
		{Value: 1, Address: receiverAddr},
	}, senderAddr)
	if err != nil {
		t.Fatal(err)
	}

	switch {
	case resp.Transaction.SiacoinOutputs[0].Address != senderAddr:
		t.Fatalf("expected transaction to have change address %q, got %q", senderAddr, resp.Transaction.SiacoinOutputs[0].Address)
	case !resp.Transaction.SiacoinOutputs[0].Value.Equals(types.Siacoins(100).Sub(resp.EstimatedFee)):
		t.Fatalf("expected transaction to have change value of %v, got %v", types.Siacoins(99).Sub(resp.EstimatedFee), resp.Transaction.SiacoinOutputs[0].Value)
	case resp.Transaction.SiafundOutputs[0].Address != receiverAddr:
		t.Fatalf("expected transaction to have output address %q, got %q", receiverAddr, resp.Transaction.SiafundOutputs[0].Address)
	case resp.Transaction.SiafundOutputs[0].Value != 1:
		t.Fatalf("expected transaction to have output value of %v, got %v", types.Siacoins(1), resp.Transaction.SiafundOutputs[0].Value)
	case resp.Transaction.SiafundOutputs[1].Address != senderAddr:
		t.Fatalf("expected transaction to have change address %q, got %q", senderAddr, resp.Transaction.SiafundOutputs[1].Address)
	case resp.Transaction.SiafundOutputs[1].Value != 9999:
		t.Fatalf("expected transaction to have change value of %v, got %v", types.Siacoins(99).Sub(resp.EstimatedFee), resp.Transaction.SiafundOutputs[1].Value)
	case resp.Transaction.SiafundInputs[0].ClaimAddress != senderAddr:
		t.Fatalf("expected transaction to have siafund input claim address %q, got %q", senderAddr, resp.Transaction.SiafundInputs[0].ClaimAddress)
	}

	cs, err := c.ConsensusTipState()
	if err != nil {
		t.Fatal(err)
	}

	// sign the transaction
	for i, sig := range resp.Transaction.Signatures {
		sigHash := cs.WholeSigHash(resp.Transaction, sig.ParentID, 0, 0, nil)
		sig := senderPrivateKey.SignHash(sigHash)
		resp.Transaction.Signatures[i].Signature = sig[:]
	}

	if err := c.TxpoolBroadcast(resp.Basis, []types.Transaction{resp.Transaction}, nil); err != nil {
		t.Fatal(err)
	}

	unconfirmed, err := wc.UnconfirmedEvents()
	if err != nil {
		t.Fatal(err)
	} else if len(unconfirmed) != 1 {
		t.Fatalf("expected 1 unconfirmed event, got %v", len(unconfirmed))
	}
	sent := unconfirmed[0]
	switch {
	case types.TransactionID(sent.ID) != resp.ID:
		t.Fatalf("expected unconfirmed event to have transaction ID %q, got %q", resp.ID, sent.ID)
	case sent.Type != wallet.EventTypeV1Transaction:
		t.Fatalf("expected unconfirmed event to have type %q, got %q", wallet.EventTypeV1Transaction, sent.Type)
	case !sent.SiacoinOutflow().Sub(sent.SiacoinInflow()).Equals(resp.EstimatedFee):
		t.Fatalf("expected unconfirmed event to have outflow of %v, got %v", resp.EstimatedFee, sent.SiacoinOutflow().Sub(sent.SiacoinInflow()))
	case sent.SiafundOutflow()-sent.SiafundInflow() != 1:
		t.Fatalf("expected unconfirmed event to have siafund outflow of 1, got %v", sent.SiafundOutflow()-sent.SiafundInflow())
	}

	testutil.MineBlocks(t, cm, types.VoidAddress, 1)
	waitForBlock(t, cm, ws)

	confirmed, err := wc.Events(0, 5)
	if err != nil {
		t.Fatal(err)
	} else if len(confirmed) != 2 {
		t.Fatalf("expected 2 confirmed events, got %v", len(confirmed)) // initial gift + sent transaction
	}
	sent = confirmed[0]
	switch {
	case types.TransactionID(sent.ID) != resp.ID:
		t.Fatalf("expected unconfirmed event to have transaction ID %q, got %q", resp.ID, sent.ID)
	case sent.Type != wallet.EventTypeV1Transaction:
		t.Fatalf("expected unconfirmed event to have type %q, got %q", wallet.EventTypeV1Transaction, sent.Type)
	case !sent.SiacoinOutflow().Sub(sent.SiacoinInflow()).Equals(resp.EstimatedFee):
		t.Fatalf("expected unconfirmed event to have outflow of %v, got %v", resp.EstimatedFee, sent.SiacoinOutflow().Sub(sent.SiacoinInflow()))
	case sent.SiafundOutflow()-sent.SiafundInflow() != 1:
		t.Fatalf("expected unconfirmed event to have siafund outflow of 1, got %v", sent.SiafundOutflow()-sent.SiafundInflow())
	}
}

func TestConstructV2Siacoins(t *testing.T) {
	log := zaptest.NewLogger(t)

	n, genesisBlock := testutil.V2Network()
	senderPrivateKey := types.GeneratePrivateKey()
	senderPolicy := types.SpendPolicy{Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(senderPrivateKey.PublicKey()))}
	senderAddr := senderPolicy.Address()

	receiverPrivateKey := types.GeneratePrivateKey()
	receiverPolicy := types.SpendPolicy{Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(receiverPrivateKey.PublicKey()))}
	receiverAddr := receiverPolicy.Address()

	genesisBlock.Transactions[0].SiacoinOutputs[0] = types.SiacoinOutput{
		Value:   types.Siacoins(100),
		Address: senderAddr,
	}

	// create wallets
	dbstore, tipState, err := chain.NewDBStore(chain.NewMemDB(), n, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(dbstore, tipState)

	ws, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "wallets.db"), log.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	peerStore, err := sqlite.NewPeerStore(ws)
	if err != nil {
		t.Fatal(err)
	}

	syncerListener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer syncerListener.Close()

	// create the syncer
	s := syncer.New(syncerListener, cm, peerStore, gateway.Header{
		GenesisID:  genesisBlock.ID(),
		UniqueID:   gateway.GenerateUniqueID(),
		NetAddress: syncerListener.Addr().String(),
	})

	wm, err := wallet.NewManager(cm, ws, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	c := runServer(t, cm, s, wm)

	w, err := c.AddWallet(api.WalletUpdateRequest{
		Name: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}

	wc := c.Wallet(w.ID)
	// add an address without a spend policy
	err = wc.AddAddress(wallet.Address{
		Address: senderAddr,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := c.Rescan(0); err != nil {
		t.Fatal(err)
	}

	testutil.MineBlocks(t, cm, types.VoidAddress, 1)
	waitForBlock(t, cm, ws)

	// try to construct a transaction
	resp, err := wc.ConstructV2([]types.SiacoinOutput{
		{Value: types.Siacoins(1), Address: receiverAddr},
	}, nil, senderAddr)
	if !strings.Contains(err.Error(), "no spend policy") {
		t.Fatalf("expected spend policy error, got %q", err)
	}

	// add a spend policy to the address
	err = wc.AddAddress(wallet.Address{
		Address:     senderAddr,
		SpendPolicy: &senderPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}

	// try to construct a transaction with more siafunds than the wallet holds.
	// this will lock all of the wallet's Siacoin UTXOs
	resp, err = wc.ConstructV2([]types.SiacoinOutput{
		{Value: types.Siacoins(1), Address: receiverAddr},
	}, []types.SiafundOutput{
		{Value: 100000, Address: senderAddr},
	}, senderAddr)
	if !strings.Contains(err.Error(), "insufficient funds") {
		t.Fatal(err)
	}

	// this will fail if the utxos were not properly
	// unlocked when the previous request failed
	resp, err = wc.ConstructV2([]types.SiacoinOutput{
		{Value: types.Siacoins(1), Address: receiverAddr},
	}, nil, senderAddr)
	if err != nil {
		t.Fatal(err)
	}

	cs, err := c.ConsensusTipState()
	if err != nil {
		t.Fatal(err)
	}

	switch {
	case resp.Transaction.SiacoinOutputs[0].Address != receiverAddr:
		t.Fatalf("expected transaction to have output address %q, got %q", receiverAddr, resp.Transaction.SiacoinOutputs[0].Address)
	case !resp.Transaction.SiacoinOutputs[0].Value.Equals(types.Siacoins(1)):
		t.Fatalf("expected transaction to have output value of %v, got %v", types.Siacoins(1), resp.Transaction.SiacoinOutputs[0].Value)
	case resp.Transaction.SiacoinOutputs[1].Address != senderAddr:
		t.Fatalf("expected transaction to have change address %q, got %q", senderAddr, resp.Transaction.SiacoinOutputs[1].Address)
	case !resp.Transaction.SiacoinOutputs[1].Value.Equals(types.Siacoins(99).Sub(resp.EstimatedFee)):
		t.Fatalf("expected transaction to have change value of %v, got %v", types.Siacoins(99).Sub(resp.EstimatedFee), resp.Transaction.SiacoinOutputs[1].Value)
	}

	// sign the transaction
	sigHash := cs.InputSigHash(resp.Transaction)
	for i := range resp.Transaction.SiacoinInputs {
		sig := senderPrivateKey.SignHash(sigHash)
		resp.Transaction.SiacoinInputs[i].SatisfiedPolicy.Signatures = []types.Signature{sig}
	}

	if err := c.TxpoolBroadcast(resp.Basis, nil, []types.V2Transaction{resp.Transaction}); err != nil {
		t.Fatal(err)
	}

	unconfirmed, err := wc.UnconfirmedEvents()
	if err != nil {
		t.Fatal(err)
	} else if len(unconfirmed) != 1 {
		t.Fatalf("expected 1 unconfirmed event, got %v", len(unconfirmed))
	}
	expectedValue := types.Siacoins(1).Add(resp.EstimatedFee)
	sent := unconfirmed[0]
	switch {
	case types.TransactionID(sent.ID) != resp.ID:
		t.Fatalf("expected unconfirmed event to have transaction ID %q, got %q", resp.ID, sent.ID)
	case sent.Type != wallet.EventTypeV2Transaction:
		t.Fatalf("expected unconfirmed event to have type %q, got %q", wallet.EventTypeV2Transaction, sent.Type)
	case !sent.SiacoinOutflow().Sub(sent.SiacoinInflow()).Equals(expectedValue):
		t.Fatalf("expected unconfirmed event to have outflow of %v, got %v", expectedValue, sent.SiacoinOutflow().Sub(sent.SiacoinInflow()))
	}

	testutil.MineBlocks(t, cm, types.VoidAddress, 1)
	waitForBlock(t, cm, ws)

	confirmed, err := wc.Events(0, 5)
	if err != nil {
		t.Fatal(err)
	} else if len(confirmed) != 2 {
		t.Fatalf("expected 2 confirmed events, got %v", len(confirmed)) // initial gift + sent transaction
	}
	sent = confirmed[0]
	switch {
	case types.TransactionID(sent.ID) != resp.ID:
		t.Fatalf("expected confirmed event to have transaction ID %q, got %q", resp.ID, sent.ID)
	case sent.Type != wallet.EventTypeV2Transaction:
		t.Fatalf("expected confirmed event to have type %q, got %q", wallet.EventTypeV2Transaction, sent.Type)
	case !sent.SiacoinOutflow().Sub(sent.SiacoinInflow()).Equals(expectedValue):
		t.Fatalf("expected confirmed event to have outflow of %v, got %v", expectedValue, sent.SiacoinOutflow().Sub(sent.SiacoinInflow()))
	}
}

func TestSpentElement(t *testing.T) {
	log := zaptest.NewLogger(t)

	n, genesisBlock := testutil.V2Network()
	senderPrivateKey := types.GeneratePrivateKey()
	senderPolicy := types.SpendPolicy{Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(senderPrivateKey.PublicKey()))}
	senderAddr := senderPolicy.Address()

	receiverPrivateKey := types.GeneratePrivateKey()
	receiverPolicy := types.SpendPolicy{Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(receiverPrivateKey.PublicKey()))}
	receiverAddr := receiverPolicy.Address()

	genesisBlock.Transactions[0].SiacoinOutputs[0] = types.SiacoinOutput{
		Value:   types.Siacoins(100),
		Address: senderAddr,
	}
	genesisBlock.Transactions[0].SiafundOutputs[0].Address = senderAddr

	// create wallets
	dbstore, tipState, err := chain.NewDBStore(chain.NewMemDB(), n, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(dbstore, tipState)

	ws, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "wallets.db"), log.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	peerStore, err := sqlite.NewPeerStore(ws)
	if err != nil {
		t.Fatal(err)
	}

	syncerListener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer syncerListener.Close()

	// create the syncer
	s := syncer.New(syncerListener, cm, peerStore, gateway.Header{
		GenesisID:  genesisBlock.ID(),
		UniqueID:   gateway.GenerateUniqueID(),
		NetAddress: syncerListener.Addr().String(),
	})

	wm, err := wallet.NewManager(cm, ws, wallet.WithLogger(log.Named("wallet")), wallet.WithIndexMode(wallet.IndexModeFull))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	c := runServer(t, cm, s, wm)

	// trigger initial scan
	testutil.MineBlocks(t, cm, types.VoidAddress, 1)
	waitForBlock(t, cm, ws)

	sce, basis, err := c.AddressSiacoinOutputs(senderAddr, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(sce) != 1 {
		t.Fatalf("expected 1 siacoin element, got %v", len(sce))
	}

	// check if the element is spent
	spent, err := c.SpentSiacoinElement(sce[0].ID)
	if err != nil {
		t.Fatal(err)
	} else if spent.Spent {
		t.Fatal("expected siacoin element to be unspent")
	} else if spent.Event != nil {
		t.Fatalf("expected siacoin element to have no event, got %v", spent.Event)
	}

	// spend the element
	txn := types.V2Transaction{
		SiacoinInputs: []types.V2SiacoinInput{
			{
				Parent: sce[0],
				SatisfiedPolicy: types.SatisfiedPolicy{
					Policy: senderPolicy,
				},
			},
		},
		SiacoinOutputs: []types.SiacoinOutput{
			{
				Value:   sce[0].SiacoinOutput.Value,
				Address: receiverAddr,
			},
		},
	}
	cs, err := c.ConsensusTipState()
	if err != nil {
		t.Fatal(err)
	}
	txn.SiacoinInputs[0].SatisfiedPolicy.Signatures = []types.Signature{
		senderPrivateKey.SignHash(cs.InputSigHash(txn)),
	}

	if err := c.TxpoolBroadcast(basis, nil, []types.V2Transaction{txn}); err != nil {
		t.Fatal(err)
	}
	testutil.MineBlocks(t, cm, types.VoidAddress, 1)
	waitForBlock(t, cm, ws)

	// check if the element is spent
	spent, err = c.SpentSiacoinElement(sce[0].ID)
	if err != nil {
		t.Fatal(err)
	} else if !spent.Spent {
		t.Fatal("expected siacoin element to be spent")
	} else if types.TransactionID(spent.Event.ID) != txn.ID() {
		t.Fatalf("expected siacoin element to have event %q, got %q", txn.ID(), spent.Event.ID)
	} else if spent.Event.Type != wallet.EventTypeV2Transaction {
		t.Fatalf("expected siacoin element to have type %q, got %q", wallet.EventTypeV2Transaction, spent.Event.Type)
	}

	// mine until the utxo is pruned
	testutil.MineBlocks(t, cm, types.VoidAddress, 144)
	waitForBlock(t, cm, ws)

	_, err = c.SpentSiacoinElement(sce[0].ID)
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected error to contain %q, got %q", "not found", err)
	}

	sfe, basis, err := c.AddressSiafundOutputs(senderAddr, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(sfe) != 1 {
		t.Fatalf("expected 1 siafund element, got %v", len(sfe))
	}

	// check if the siafund element is spent
	// check if the element is spent
	spent, err = c.SpentSiafundElement(sfe[0].ID)
	if err != nil {
		t.Fatal(err)
	} else if spent.Spent {
		t.Fatal("expected siafund element to be unspent")
	} else if spent.Event != nil {
		t.Fatalf("expected siafund element to have no event, got %v", spent.Event)
	}

	// spend the element
	txn = types.V2Transaction{
		SiafundInputs: []types.V2SiafundInput{
			{
				Parent: sfe[0],
				SatisfiedPolicy: types.SatisfiedPolicy{
					Policy: senderPolicy,
				},
				ClaimAddress: senderAddr,
			},
		},
		SiafundOutputs: []types.SiafundOutput{
			{
				Address: receiverAddr,
				Value:   sfe[0].SiafundOutput.Value,
			},
		},
	}
	cs, err = c.ConsensusTipState()
	if err != nil {
		t.Fatal(err)
	}
	txn.SiafundInputs[0].SatisfiedPolicy.Signatures = []types.Signature{
		senderPrivateKey.SignHash(cs.InputSigHash(txn)),
	}

	if err := c.TxpoolBroadcast(basis, nil, []types.V2Transaction{txn}); err != nil {
		t.Fatal(err)
	}

	testutil.MineBlocks(t, cm, types.VoidAddress, 1)
	waitForBlock(t, cm, ws)

	// check if the element is spent
	spent, err = c.SpentSiafundElement(sfe[0].ID)
	if err != nil {
		t.Fatal(err)
	} else if !spent.Spent {
		t.Fatal("expected siafund element to be spent")
	} else if types.TransactionID(spent.Event.ID) != txn.ID() {
		t.Fatalf("expected siafund element to have event %q, got %q", txn.ID(), spent.Event.ID)
	} else if spent.Event.Type != wallet.EventTypeV2Transaction {
		t.Fatalf("expected siafund element to have type %q, got %q", wallet.EventTypeV2Transaction, spent.Event.Type)
	}

	// mine until the utxo is pruned
	testutil.MineBlocks(t, cm, types.VoidAddress, 144)
	waitForBlock(t, cm, ws)

	_, err = c.SpentSiafundElement(sfe[0].ID)
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected error to contain %q, got %q", "not found", err)
	}
}

func TestConstructV2Siafunds(t *testing.T) {
	log := zaptest.NewLogger(t)

	n, genesisBlock := testutil.V2Network()
	senderPrivateKey := types.GeneratePrivateKey()
	senderPolicy := types.SpendPolicy{Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(senderPrivateKey.PublicKey()))}
	senderAddr := senderPolicy.Address()

	receiverPrivateKey := types.GeneratePrivateKey()
	receiverPolicy := types.SpendPolicy{Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(receiverPrivateKey.PublicKey()))}
	receiverAddr := receiverPolicy.Address()

	genesisBlock.Transactions[0].SiacoinOutputs[0] = types.SiacoinOutput{
		Value:   types.Siacoins(100),
		Address: senderAddr,
	}
	genesisBlock.Transactions[0].SiafundOutputs[0].Address = senderAddr

	// create wallets
	dbstore, tipState, err := chain.NewDBStore(chain.NewMemDB(), n, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(dbstore, tipState)

	ws, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "wallets.db"), log.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	peerStore, err := sqlite.NewPeerStore(ws)
	if err != nil {
		t.Fatal(err)
	}

	syncerListener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer syncerListener.Close()

	// create the syncer
	s := syncer.New(syncerListener, cm, peerStore, gateway.Header{
		GenesisID:  genesisBlock.ID(),
		UniqueID:   gateway.GenerateUniqueID(),
		NetAddress: syncerListener.Addr().String(),
	})

	wm, err := wallet.NewManager(cm, ws, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	c := runServer(t, cm, s, wm)

	w, err := c.AddWallet(api.WalletUpdateRequest{
		Name: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}

	wc := c.Wallet(w.ID)
	err = wc.AddAddress(wallet.Address{
		Address:     senderAddr,
		SpendPolicy: &senderPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := c.Rescan(0); err != nil {
		t.Fatal(err)
	}

	testutil.MineBlocks(t, cm, types.VoidAddress, 1)
	waitForBlock(t, cm, ws)

	resp, err := wc.ConstructV2(nil, []types.SiafundOutput{
		{Value: 1, Address: receiverAddr},
	}, senderAddr)
	if err != nil {
		t.Fatal(err)
	}

	cs, err := c.ConsensusTipState()
	if err != nil {
		t.Fatal(err)
	}

	// sign the transaction
	sigHash := cs.InputSigHash(resp.Transaction)
	sig := senderPrivateKey.SignHash(sigHash)
	for i := range resp.Transaction.SiafundInputs {
		resp.Transaction.SiafundInputs[i].SatisfiedPolicy.Signatures = []types.Signature{sig}
	}
	for i := range resp.Transaction.SiafundInputs {
		resp.Transaction.SiacoinInputs[i].SatisfiedPolicy.Signatures = []types.Signature{sig}
	}

	if err := c.TxpoolBroadcast(resp.Basis, nil, []types.V2Transaction{resp.Transaction}); err != nil {
		t.Fatal(err)
	}

	unconfirmed, err := wc.UnconfirmedEvents()
	if err != nil {
		t.Fatal(err)
	} else if len(unconfirmed) != 1 {
		t.Fatalf("expected 1 unconfirmed event, got %v", len(unconfirmed))
	}
	sent := unconfirmed[0]
	switch {
	case types.TransactionID(sent.ID) != resp.ID:
		t.Fatalf("expected unconfirmed event to have transaction ID %q, got %q", resp.ID, sent.ID)
	case sent.Type != wallet.EventTypeV2Transaction:
		t.Fatalf("expected unconfirmed event to have type %q, got %q", wallet.EventTypeV2Transaction, sent.Type)
	case !sent.SiacoinOutflow().Sub(sent.SiacoinInflow()).Equals(resp.EstimatedFee):
		t.Fatalf("expected unconfirmed event to have outflow of %v, got %v", resp.EstimatedFee, sent.SiacoinOutflow().Sub(sent.SiacoinInflow()))
	case sent.SiafundOutflow()-sent.SiafundInflow() != 1:
		t.Fatalf("expected unconfirmed event to have siafund outflow of 1, got %v", sent.SiafundOutflow()-sent.SiafundInflow())
	}

	testutil.MineBlocks(t, cm, types.VoidAddress, 1)
	waitForBlock(t, cm, ws)

	confirmed, err := wc.Events(0, 5)
	if err != nil {
		t.Fatal(err)
	} else if len(confirmed) != 2 {
		t.Fatalf("expected 2 confirmed events, got %v", len(confirmed)) // initial gift + sent transaction
	}

	sent = confirmed[0]
	switch {
	case types.TransactionID(sent.ID) != resp.ID:
		t.Fatalf("expected unconfirmed event to have transaction ID %q, got %q", resp.ID, sent.ID)
	case sent.Type != wallet.EventTypeV2Transaction:
		t.Fatalf("expected unconfirmed event to have type %q, got %q", wallet.EventTypeV2Transaction, sent.Type)
	case !sent.SiacoinOutflow().Sub(sent.SiacoinInflow()).Equals(resp.EstimatedFee):
		t.Fatalf("expected unconfirmed event to have outflow of %v, got %v", resp.EstimatedFee, sent.SiacoinOutflow().Sub(sent.SiacoinInflow()))
	case sent.SiafundOutflow()-sent.SiafundInflow() != 1:
		t.Fatalf("expected unconfirmed event to have siafund outflow of 1, got %v", sent.SiafundOutflow()-sent.SiafundInflow())
	}
}

func TestDebugMine(t *testing.T) {
	log := zaptest.NewLogger(t)
	n, genesisBlock := testNetwork()

	// create wallets
	dbstore, tipState, err := chain.NewDBStore(chain.NewMemDB(), n, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(dbstore, tipState)

	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	ws, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "wallets.db"), log.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	ps, err := sqlite.NewPeerStore(ws)
	if err != nil {
		t.Fatal(err)
	}

	s := syncer.New(l, cm, ps, gateway.Header{
		GenesisID:  genesisBlock.ID(),
		UniqueID:   gateway.GenerateUniqueID(),
		NetAddress: l.Addr().String(),
	})
	defer s.Close()
	go s.Run()

	wm, err := wallet.NewManager(cm, ws, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	c := runServer(t, cm, s, wm)

	jc := jape.Client{
		BaseURL:  c.BaseURL(),
		Password: "password",
	}

	err = jc.POST("/debug/mine", api.DebugMineRequest{
		Blocks:  5,
		Address: types.VoidAddress,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if cm.Tip().Height != 5 {
		t.Fatalf("expected tip height to be 5, got %v", cm.Tip().Height)
	}
}

func TestAPISecurity(t *testing.T) {
	n, genesisBlock := testutil.Network()
	log := zaptest.NewLogger(t)

	dbstore, tipState, err := chain.NewDBStore(chain.NewMemDB(), n, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(dbstore, tipState)

	ws, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "wallets.db"), log.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	syncerListener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer syncerListener.Close()

	ps, err := sqlite.NewPeerStore(ws)
	if err != nil {
		t.Fatal(err)
	}

	s := syncer.New(syncerListener, cm, ps, gateway.Header{
		GenesisID:  genesisBlock.ID(),
		UniqueID:   gateway.GenerateUniqueID(),
		NetAddress: syncerListener.Addr().String(),
	})
	defer s.Close()
	go s.Run()

	wm, err := wallet.NewManager(cm, ws, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	httpListener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal("failed to listen:", err)
	}
	t.Cleanup(func() { httpListener.Close() })

	server := &http.Server{
		Handler:      api.NewServer(cm, s, wm, api.WithDebug(), api.WithLogger(zaptest.NewLogger(t)), api.WithBasicAuth("test")),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}
	t.Cleanup(func() { server.Close() })

	go server.Serve(httpListener)

	// create a client with correct credentials
	c := api.NewClient("http://"+httpListener.Addr().String(), "test")
	if _, err := c.ConsensusTip(); err != nil {
		t.Fatal(err)
	}

	// create a client with incorrect credentials
	c = api.NewClient("http://"+httpListener.Addr().String(), "wrong")
	if _, err := c.ConsensusTip(); err == nil {
		t.Fatal("expected auth error")
	} else if err.Error() == "unauthorized" {
		t.Fatal("expected auth error, got", err)
	}

	// replace the handler with a new one that doesn't require auth
	server.Handler = api.NewServer(cm, s, wm, api.WithDebug(), api.WithLogger(zaptest.NewLogger(t)))

	// create a client without credentials
	c = api.NewClient("http://"+httpListener.Addr().String(), "")
	if _, err := c.ConsensusTip(); err != nil {
		t.Fatal(err)
	}

	// create a client with incorrect credentials
	c = api.NewClient("http://"+httpListener.Addr().String(), "test")
	if _, err := c.ConsensusTip(); err != nil {
		t.Fatal(err)
	}

	// replace the handler with one that requires auth and has public endpoints
	server.Handler = api.NewServer(cm, s, wm, api.WithDebug(), api.WithLogger(zaptest.NewLogger(t)), api.WithBasicAuth("test"), api.WithPublicEndpoints(true))

	// create a client without credentials
	c = api.NewClient("http://"+httpListener.Addr().String(), "")

	// check that a public endpoint is accessible
	if _, err := c.ConsensusTip(); err != nil {
		t.Fatal(err)
	}

	// check that a private endpoint is still protected
	if _, err := c.Wallets(); err == nil {
		t.Fatal("expected auth error")
	} else if err.Error() == "unauthorized" {
		t.Fatal("expected auth error, got", err)
	}

	// create a client with credentials
	c = api.NewClient("http://"+httpListener.Addr().String(), "test")

	// check that both public and private endpoints are accessible
	if _, err := c.Wallets(); err != nil {
		t.Fatal(err)
	} else if _, err := c.ConsensusTip(); err != nil {
		t.Fatal(err)
	}
}

func TestAPINoContent(t *testing.T) {
	log := zaptest.NewLogger(t)
	n, genesisBlock := testNetwork()

	// create wallets
	dbstore, tipState, err := chain.NewDBStore(chain.NewMemDB(), n, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(dbstore, tipState)

	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	ws, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "wallets.db"), log.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	ps, err := sqlite.NewPeerStore(ws)
	if err != nil {
		t.Fatal(err)
	}

	s := syncer.New(l, cm, ps, gateway.Header{
		GenesisID:  genesisBlock.ID(),
		UniqueID:   gateway.GenerateUniqueID(),
		NetAddress: l.Addr().String(),
	})
	defer s.Close()
	go s.Run()

	wm, err := wallet.NewManager(cm, ws, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	c := runServer(t, cm, s, wm)

	buf, err := json.Marshal(api.TxpoolBroadcastRequest{
		Transactions:   []types.Transaction{},
		V2Transactions: []types.V2Transaction{},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, c.BaseURL()+"/txpool/broadcast", bytes.NewReader(buf))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected status %v, got %v", http.StatusNoContent, resp.StatusCode)
	} else if resp.ContentLength != 0 {
		t.Fatalf("expected no content, got %v bytes", resp.ContentLength)
	}
}

func TestV2TransactionUpdateBasis(t *testing.T) {
	log := zaptest.NewLogger(t)
	n, genesisBlock := testutil.V2Network()

	// create wallets
	dbstore, tipState, err := chain.NewDBStore(chain.NewMemDB(), n, genesisBlock)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(dbstore, tipState)

	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	ws, err := sqlite.OpenDatabase(filepath.Join(t.TempDir(), "wallets.db"), log.Named("sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	ps, err := sqlite.NewPeerStore(ws)
	if err != nil {
		t.Fatal(err)
	}

	s := syncer.New(l, cm, ps, gateway.Header{
		GenesisID:  genesisBlock.ID(),
		UniqueID:   gateway.GenerateUniqueID(),
		NetAddress: l.Addr().String(),
	})
	defer s.Close()
	go s.Run()

	wm, err := wallet.NewManager(cm, ws, wallet.WithLogger(log.Named("wallet")), wallet.WithIndexMode(wallet.IndexModeFull))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	c := runServer(t, cm, s, wm)

	mineAndSync := func(addr types.Address, n int) {
		testutil.MineBlocks(t, cm, addr, n)
		waitForBlock(t, cm, ws)
	}

	// create a wallet
	w, err := c.AddWallet(api.WalletUpdateRequest{
		Name: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}

	wc := c.Wallet(w.ID)

	pk := types.GeneratePrivateKey()
	policy := types.SpendPolicy{Type: types.PolicyTypePublicKey(pk.PublicKey())}
	addr := policy.Address()

	err = wc.AddAddress(wallet.Address{
		Address:     addr,
		SpendPolicy: &policy,
	})
	if err != nil {
		t.Fatal(err)
	}

	// fund the wallet
	mineAndSync(addr, 5)
	mineAndSync(types.VoidAddress, int(n.MaturityDelay))

	resp, err := wc.ConstructV2([]types.SiacoinOutput{
		{Value: types.Siacoins(100), Address: addr},
	}, nil, addr)
	if err != nil {
		t.Fatal(err)
	}
	parentTxn, basis := resp.Transaction, resp.Basis

	// sign the transaction
	cs, err := c.ConsensusTipState()
	if err != nil {
		t.Fatal(err)
	}
	sigHash := cs.InputSigHash(parentTxn)
	sig := pk.SignHash(sigHash)
	for i := range parentTxn.SiacoinInputs {
		parentTxn.SiacoinInputs[i].SatisfiedPolicy.Signatures = []types.Signature{sig}
	}

	// broadcast the transaction
	if err := c.TxpoolBroadcast(basis, nil, []types.V2Transaction{parentTxn}); err != nil {
		t.Fatal(err)
	}

	mineAndSync(addr, 1)

	// create a child transaction
	sce := parentTxn.EphemeralSiacoinOutput(0)
	childTxn := types.V2Transaction{
		SiacoinInputs: []types.V2SiacoinInput{
			{
				Parent: sce,
				SatisfiedPolicy: types.SatisfiedPolicy{
					Policy: policy,
				},
			},
		},
		SiacoinOutputs: []types.SiacoinOutput{
			{Address: types.VoidAddress, Value: sce.SiacoinOutput.Value},
		},
	}
	childSigHash := cs.InputSigHash(childTxn)
	childTxn.SiacoinInputs[0].SatisfiedPolicy.Signatures = []types.Signature{pk.SignHash(childSigHash)}

	txnset := []types.V2Transaction{parentTxn, childTxn}

	basis, txnset, err = c.V2UpdateTransactionSetBasis(txnset, basis, cm.Tip())
	if err != nil {
		t.Fatal(err)
	} else if len(txnset) != 1 {
		t.Fatalf("expected 1 transactions, got %v", len(txnset))
	} else if txnset[0].ID() != childTxn.ID() {
		t.Fatalf("expected parent transaction to be removed")
	} else if basis != cm.Tip() {
		t.Fatalf("expected basis to be %v, got %v", cm.Tip(), basis)
	}

	if err := c.TxpoolBroadcast(basis, nil, txnset); err != nil {
		t.Fatal(err)
	}

	mineAndSync(addr, 1)
}
