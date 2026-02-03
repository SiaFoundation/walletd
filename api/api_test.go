package api_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils"
	"go.sia.tech/jape"
	"go.sia.tech/walletd/v2/api"
	"go.sia.tech/walletd/v2/internal/testutil"
	"go.sia.tech/walletd/v2/wallet"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	"lukechampine.com/frand"
)

// testNode wraps a ConsensusNode with additional fields.
type testNode struct {
	*testutil.ConsensusNode
	network *consensus.Network
	client  *api.Client
	genesis types.Block
	pk      types.PrivateKey
}

func (tn *testNode) fundingAddr() types.Address {
	return types.StandardUnlockHash(tn.pk.PublicKey())
}

// newCustomTestNode creates a test node with the given network and genesis block.
// If sf is true, also assigns genesis siafunds to the funding address.
func newCustomTestNode(tb testing.TB, log *zap.Logger, n *consensus.Network, genesisBlock types.Block, sc types.Currency, sf bool, walletOpts ...wallet.Option) *testNode {
	tb.Helper()

	fundingKey := types.GeneratePrivateKey()
	fundingAddr := types.StandardUnlockHash(fundingKey.PublicKey())
	genesisBlock.Transactions[0].SiacoinOutputs[0] = types.SiacoinOutput{
		Value:   sc,
		Address: fundingAddr,
	}
	if sf {
		genesisBlock.Transactions[0].SiafundOutputs[0].Address = fundingAddr
	}
	cn := testutil.NewConsensusNode(tb, n, genesisBlock, log)
	c := startWalletServer(tb, cn, log, walletOpts...)
	return &testNode{
		ConsensusNode: cn,
		network:       n,
		client:        c,
		genesis:       genesisBlock,
		pk:            fundingKey,
	}
}

// newV1TestNode creates a V1 network test node with initial siacoin funding.
// If sf is true, also assigns genesis siafunds to the funding address.
func newV1TestNode(tb testing.TB, log *zap.Logger, sc types.Currency, sf bool, walletOpts ...wallet.Option) *testNode {
	tb.Helper()
	n, genesisBlock := testutil.V1Network()
	return newCustomTestNode(tb, log, n, genesisBlock, sc, sf, walletOpts...)
}

// newV2TestNode creates a V2 network test node with initial siacoin funding.
// If sf is true, also assigns genesis siafunds to the funding address.
func newV2TestNode(tb testing.TB, log *zap.Logger, sc types.Currency, sf bool, walletOpts ...wallet.Option) *testNode {
	tb.Helper()
	n, genesisBlock := testutil.V2Network()
	return newCustomTestNode(tb, log, n, genesisBlock, sc, sf, walletOpts...)
}

// signV1Txn signs all signatures in a V1 transaction.
func signV1Txn(cs consensus.State, txn *types.Transaction, pk types.PrivateKey) {
	for i, sig := range txn.Signatures {
		sigHash := cs.WholeSigHash(*txn, sig.ParentID, 0, 0, nil)
		s := pk.SignHash(sigHash)
		txn.Signatures[i].Signature = s[:]
	}
}

// signV2Txn signs all siacoin and siafund inputs in a V2 transaction.
func signV2Txn(cs consensus.State, txn *types.V2Transaction, pk types.PrivateKey) {
	sigHash := cs.InputSigHash(*txn)
	sig := pk.SignHash(sigHash)
	for i := range txn.SiacoinInputs {
		txn.SiacoinInputs[i].SatisfiedPolicy.Signatures = []types.Signature{sig}
	}
	for i := range txn.SiafundInputs {
		txn.SiafundInputs[i].SatisfiedPolicy.Signatures = []types.Signature{sig}
	}
}

func startWalletServer(tb testing.TB, cn *testutil.ConsensusNode, log *zap.Logger, walletOpts ...wallet.Option) *api.Client {
	tb.Helper()

	l, err := net.Listen("tcp", ":0")
	if err != nil {
		tb.Fatal("failed to listen:", err)
	}
	tb.Cleanup(func() { l.Close() })

	wm, err := wallet.NewManager(cn.Chain, cn.Store, append([]wallet.Option{wallet.WithLogger(log.Named("wallet"))}, walletOpts...)...)
	if err != nil {
		tb.Fatal("failed to create wallet manager:", err)
	}
	tb.Cleanup(func() { wm.Close() })

	server := &http.Server{
		Handler:      api.NewServer(cn.Store, cn.Chain, cn.Syncer, wm, api.WithDebug(), api.WithLogger(log)),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}
	tb.Cleanup(func() { server.Close() })

	go server.Serve(l)
	return api.NewClient("http://"+l.Addr().String(), "password")
}

func TestWalletAdd(t *testing.T) {
	log := zaptest.NewLogger(t)
	tn := newV1TestNode(t, log, types.Siacoins(1), false)
	c := tn.client

	checkWalletResponse := func(wr api.WalletUpdateRequest, w wallet.Wallet, isUpdate bool) error {
		// check wallet
		if w.Name != wr.Name {
			return fmt.Errorf("expected wallet name to be %v, got %v", wr.Name, w.Name)
		} else if w.Description != wr.Description {
			return fmt.Errorf("expected wallet description to be %v, got %v", wr.Description, w.Description)
		} else if w.DateCreated.After(time.Now()) {
			return fmt.Errorf("expected wallet creation date to be in the past, got %v", w.DateCreated)
		} else if isUpdate && w.DateCreated.Equal(w.LastUpdated) {
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
	tn := newV1TestNode(t, log, types.Siacoins(1), false)
	c := tn.client

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
	tn.WaitForSync(t)

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
	sk2 := types.GeneratePrivateKey()
	addr := types.StandardUnlockHash(sk2.PublicKey())
	err = wc.AddAddress(wallet.Address{
		Address: addr,
	})
	if err != nil {
		t.Fatal(err)
	}

	// should have an address now
	addresses, err = wc.Addresses()
	if err != nil {
		t.Fatal(err)
	} else if len(addresses) != 1 {
		t.Fatal("address list should have one address")
	} else if addresses[0].Address != addr {
		t.Fatalf("address should be %v, got %v", addr, addresses[0])
	}

	// send gift to wallet
	giftSCOID := tn.genesis.Transactions[0].SiacoinOutputID(0)
	txn := types.Transaction{
		SiacoinInputs: []types.SiacoinInput{{
			ParentID:         giftSCOID,
			UnlockConditions: types.StandardUnlockConditions(tn.pk.PublicKey()),
		}},
		SiacoinOutputs: []types.SiacoinOutput{
			{Address: addr, Value: types.Siacoins(1).Div64(2)},
			{Address: addr, Value: types.Siacoins(1).Div64(2)},
		},
		Signatures: []types.TransactionSignature{{
			ParentID:      types.Hash256(giftSCOID),
			CoveredFields: types.CoveredFields{WholeTransaction: true},
		}},
	}

	cs, err := c.ConsensusTipState()
	if err != nil {
		t.Fatal(err)
	}

	sig := tn.pk.SignHash(cs.WholeSigHash(txn, types.Hash256(giftSCOID), 0, 0, nil))
	txn.Signatures[0].Signature = sig[:]

	// broadcast the transaction to the transaction pool
	if _, err := c.TxpoolBroadcast(cs.Index, []types.Transaction{txn}, nil); err != nil {
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
	// confirm the transaction
	tn.MineBlocks(t, types.VoidAddress, 1)

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
	} else if basis != tn.Chain.Tip() {
		t.Fatalf("basis should be %v, got %v", tn.Chain.Tip(), basis)
	} else if outputs[0].Confirmations != 1 {
		t.Fatalf("expected 1 confirmation, got %v", outputs[0].Confirmations)
	}

	// mine a block to add an immature balance
	expectedPayout := tn.Chain.TipState().BlockReward()
	tn.MineBlocks(t, addr, 1)

	// get new balance
	balance, err = wc.Balance()
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.Equals(types.Siacoins(1)) {
		t.Fatal("balance should be 1 SC, got", balance.Siacoins)
	} else if !balance.ImmatureSiacoins.Equals(expectedPayout) {
		t.Fatalf("immature balance should be %d SC, got %d SC", expectedPayout, balance.ImmatureSiacoins)
	}

	// mine enough blocks for the miner payout to mature
	expectedBalance := types.Siacoins(1).Add(expectedPayout)
	tn.MineBlocks(t, types.VoidAddress, int(tn.network.MaturityDelay))

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
	tn := newV1TestNode(t, log, types.Siacoins(1), false)
	c := tn.client

	sk2 := types.GeneratePrivateKey()
	addr := types.StandardUnlockHash(sk2.PublicKey())

	// personal index mode requires a wallet for indexing
	w, err := c.AddWallet(api.WalletUpdateRequest{Name: "primary"})
	if err != nil {
		t.Fatal(err)
	}
	wc := c.Wallet(w.ID)
	err = wc.AddAddress(wallet.Address{Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	// send gift to wallet
	giftSCOID := tn.genesis.Transactions[0].SiacoinOutputID(0)
	txn := types.Transaction{
		SiacoinInputs: []types.SiacoinInput{{
			ParentID:         giftSCOID,
			UnlockConditions: types.StandardUnlockConditions(tn.pk.PublicKey()),
		}},
		SiacoinOutputs: []types.SiacoinOutput{
			{Address: addr, Value: types.Siacoins(1).Div64(2)},
			{Address: addr, Value: types.Siacoins(1).Div64(2)},
		},
		Signatures: []types.TransactionSignature{{
			ParentID:      types.Hash256(giftSCOID),
			CoveredFields: types.CoveredFields{WholeTransaction: true},
		}},
	}

	cs, err := c.ConsensusTipState()
	if err != nil {
		t.Fatal(err)
	}

	sig := tn.pk.SignHash(cs.WholeSigHash(txn, types.Hash256(giftSCOID), 0, 0, nil))
	txn.Signatures[0].Signature = sig[:]

	// broadcast the transaction to the transaction pool
	if _, err := c.TxpoolBroadcast(cs.Index, []types.Transaction{txn}, nil); err != nil {
		t.Fatal(err)
	}
	tn.MineBlocks(t, types.VoidAddress, 1)

	// get new balance
	balance, err := c.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.Equals(types.Siacoins(1)) {
		t.Fatal("balance should be 1 SC, got", balance.Siacoins)
	} else if !balance.ImmatureSiacoins.IsZero() {
		t.Fatal("immature balance should be 0 SC, got", balance.ImmatureSiacoins)
	}

	// transaction should appear in history
	events, err := c.AddressEvents(addr, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) == 0 {
		t.Fatal("transaction should appear in history")
	}

	outputs, basis, err := c.AddressSiacoinOutputs(addr, false, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(outputs) != 2 {
		t.Fatal("should have two UTXOs, got", len(outputs))
	} else if basis != tn.Chain.Tip() {
		t.Fatalf("basis should be %v, got %v", tn.Chain.Tip(), basis)
	}

	// mine a block to add an immature balance
	expectedPayout := tn.Chain.TipState().BlockReward()
	tn.MineBlocks(t, addr, 1)

	// get new balance
	balance, err = c.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.Equals(types.Siacoins(1)) {
		t.Fatal("balance should be 1 SC, got", balance.Siacoins)
	} else if !balance.ImmatureSiacoins.Equals(expectedPayout) {
		t.Fatalf("immature balance should be %d SC, got %d SC", expectedPayout, balance.ImmatureSiacoins)
	}

	// mine enough blocks for the miner payout to mature
	expectedBalance := types.Siacoins(1).Add(expectedPayout)
	tn.MineBlocks(t, types.VoidAddress, int(tn.network.MaturityDelay))

	// get new balance
	balance, err = c.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.Equals(expectedBalance) {
		t.Fatalf("balance should be %d, got %d", expectedBalance, balance.Siacoins)
	} else if !balance.ImmatureSiacoins.IsZero() {
		t.Fatal("immature balance should be 0 SC, got", balance.ImmatureSiacoins)
	}

	// create new wallet
	w, err = c.AddWallet(api.WalletUpdateRequest{Name: t.Name()})
	if err != nil {
		t.Fatal(err)
	}
	wc = c.Wallet(w.ID)

	// create two addresses
	pk1 := types.GeneratePrivateKey()
	pk2 := types.GeneratePrivateKey()
	addr1 := types.StandardUnlockHash(pk1.PublicKey())
	addr2 := types.StandardUnlockHash(pk2.PublicKey())

	// assert multiple addresses can be added to a wallet
	if err := wc.AddAddresses([]wallet.Address{{Address: addr1}, {Address: addr2}}); err != nil {
		t.Fatal(err)
	} else if addrs, err := wc.Addresses(); err != nil {
		t.Fatal(err)
	} else if len(addrs) != 2 {
		t.Fatalf("expected 2 addresses, got %d", len(addrs))
	}
}

func TestConsensus(t *testing.T) {
	log := zaptest.NewLogger(t)
	tn := newV2TestNode(t, log, types.Siacoins(1), false)
	c := tn.client

	// mine a block
	minedBlock, ok := coreutils.MineBlock(tn.Chain, types.Address{}, time.Minute)
	if !ok {
		t.Fatal("no block found")
	} else if err := tn.Chain.AddBlocks([]types.Block{minedBlock}); err != nil {
		t.Fatal(err)
	}

	// block should be tip now
	ci, err := c.ConsensusTip()
	if err != nil {
		t.Fatal(err)
	} else if ci.ID != minedBlock.ID() {
		t.Fatalf("expected consensus tip to be %v, got %v", minedBlock.ID(), ci.ID)
	}

	// fetch block
	b, err := c.ConsensusBlocksID(minedBlock.ID())
	if err != nil {
		t.Fatal(err)
	} else if b.ID != minedBlock.ID() {
		t.Fatal("mismatch")
	}
}

func TestConsensusCheckpoint(t *testing.T) {
	log := zaptest.NewLogger(t)
	tn := newV2TestNode(t, log, types.Siacoins(1), false)
	c := tn.client

	// mine a block
	minedBlock, ok := coreutils.MineBlock(tn.Chain, types.Address{}, time.Minute)
	if !ok {
		t.Fatal("no block found")
	} else if err := tn.Chain.AddBlocks([]types.Block{minedBlock}); err != nil {
		t.Fatal(err)
	}

	// block should be tip now
	ci, err := c.ConsensusTip()
	if err != nil {
		t.Fatal(err)
	} else if ci.ID != minedBlock.ID() {
		t.Fatalf("expected consensus tip to be %v, got %v", minedBlock.ID(), ci.ID)
	}

	// fetch block
	resp, err := c.ConsensusCheckpointID(minedBlock.ID())
	if err != nil {
		t.Fatal(err)
	} else if resp.Block.ID() != minedBlock.ID() {
		t.Fatal("mismatch")
	} else if resp.State.Index != tn.Chain.Tip() {
		t.Fatal("mismatch tip")
	}

	heightResp, err := c.ConsensusCheckpointHeight(tn.Chain.Tip().Height)
	if err != nil {
		t.Fatal(err)
	} else if heightResp.Block.ID() != minedBlock.ID() {
		t.Fatal("mismatch")
	} else if heightResp.State.Index != tn.Chain.Tip() {
		t.Fatal("mismatch tip")
	}
}

func TestConsensusUpdates(t *testing.T) {
	log := zaptest.NewLogger(t)
	tn := newV1TestNode(t, log, types.Siacoins(1), false)
	c := tn.client
	tn.MineBlocks(t, types.VoidAddress, 10)

	reverted, applied, err := c.ConsensusUpdates(types.ChainIndex{}, 10)
	if err != nil {
		t.Fatal(err)
	} else if len(reverted) != 0 {
		t.Fatal("expected no reverted blocks")
	} else if len(applied) != 10 { // genesis + 10 mined blocks
		t.Fatalf("expected 10 applied blocks, got %v", len(applied))
	}

	for i, cau := range applied {
		// using i for height since we're testing the update contents
		expected, ok := tn.Chain.BestIndex(uint64(i))
		if !ok {
			t.Fatalf("failed to get expected index for block %v", i)
		} else if cau.State.Index != expected {
			t.Fatalf("expected index %v, got %v", expected, cau.State.Index)
		} else if cau.State.Network.Name != tn.network.Name { // TODO: better comparison. reflect.DeepEqual is failing in CI, but passing local.
			t.Fatalf("expected network to be %q, got %q", tn.network.Name, cau.State.Network.Name)
		}
	}
}

func TestConstructSiacoins(t *testing.T) {
	log := zaptest.NewLogger(t)
	tn := newV1TestNode(t, log, types.Siacoins(100), false)
	c := tn.client

	senderPrivateKey := tn.pk
	senderPolicy := types.SpendPolicy{Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(senderPrivateKey.PublicKey()))}
	senderAddr := tn.fundingAddr()

	receiverAddr := types.StandardUnlockHash(types.GeneratePrivateKey().PublicKey())

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
	tn.MineBlocks(t, types.VoidAddress, 1)

	// try to construct a valid transaction with no spend policy
	_, err = wc.Construct([]types.SiacoinOutput{
		{Value: types.Siacoins(1), Address: receiverAddr},
	}, nil, senderAddr)
	if !strings.Contains(err.Error(), "no spend policy") {
		t.Fatalf("expected error to contain %q, got %q", "no spend policy", err)
	}

	// add the spend policy
	err = wc.AddAddress(wallet.Address{
		Address:     senderAddr,
		SpendPolicy: &senderPolicy,
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
	signV1Txn(cs, &resp.Transaction, senderPrivateKey)

	if broadcastResp, err := c.TxpoolBroadcast(resp.Basis, []types.Transaction{resp.Transaction}, nil); err != nil {
		t.Fatal(err)
	} else if len(broadcastResp.Transactions) != 1 || len(broadcastResp.V2Transactions) != 0 {
		t.Fatalf("expected 1 v1 ID and 0 v2 IDs, got %v and %v", len(broadcastResp.Transactions), len(broadcastResp.V2Transactions))
	} else if broadcastResp.Transactions[0].ID() != resp.ID {
		t.Fatalf("expected v1 ID to be %v, got %v", resp.ID, broadcastResp.Transactions[0].ID())
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
	tn.MineBlocks(t, types.VoidAddress, 1)

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
	tn := newV1TestNode(t, log, types.Siacoins(100), true) // siafunds=true
	c := tn.client

	senderPrivateKey := tn.pk
	senderPolicy := types.SpendPolicy{Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(senderPrivateKey.PublicKey()))}
	senderAddr := tn.fundingAddr()

	receiverAddr := types.StandardUnlockHash(types.GeneratePrivateKey().PublicKey())

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
	tn.MineBlocks(t, types.VoidAddress, 1)

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
	signV1Txn(cs, &resp.Transaction, senderPrivateKey)

	if _, err := c.TxpoolBroadcast(resp.Basis, []types.Transaction{resp.Transaction}, nil); err != nil {
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
	tn.MineBlocks(t, types.VoidAddress, 1)

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
	tn := newV2TestNode(t, log, types.Siacoins(100), false)
	c := tn.client

	senderPrivateKey := tn.pk
	senderPolicy := types.SpendPolicy{Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(senderPrivateKey.PublicKey()))}
	senderAddr := tn.fundingAddr()

	receiverAddr := types.StandardUnlockHash(types.GeneratePrivateKey().PublicKey())

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
	tn.MineBlocks(t, types.VoidAddress, 1)

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
	signV2Txn(cs, &resp.Transaction, senderPrivateKey)

	if broadcastResp, err := c.TxpoolBroadcast(resp.Basis, nil, []types.V2Transaction{resp.Transaction}); err != nil {
		t.Fatal(err)
	} else if len(broadcastResp.Transactions) != 0 || len(broadcastResp.V2Transactions) != 1 {
		t.Fatalf("expected 1 v1 ID and 0 v2 IDs, got %v and %v", len(broadcastResp.Transactions), len(broadcastResp.V2Transactions))
	} else if broadcastResp.V2Transactions[0].ID() != resp.ID {
		t.Fatalf("expected v2 ID to be %v, got %v", resp.ID, broadcastResp.V2Transactions[0].ID())
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

	unconfirmed, err = c.TPoolEvents()
	if err != nil {
		t.Fatal(err)
	} else if len(unconfirmed) != 1 {
		t.Fatalf("expected 1 unconfirmed event, got %v", len(unconfirmed))
	} else if unconfirmed[0].Type != wallet.EventTypeV2Transaction {
		t.Fatalf("expected unconfirmed event to have type %q, got %q", wallet.EventTypeV2Transaction, unconfirmed[0].Type)
	} else if unconfirmed[0].ID != sent.ID {
		t.Fatalf("expected unconfirmed event to have ID %q, got %q", sent.ID, unconfirmed[0].ID)
	}

	tn.MineBlocks(t, types.VoidAddress, 1)

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

func TestConstructV2Siafunds(t *testing.T) {
	log := zaptest.NewLogger(t)
	tn := newV2TestNode(t, log, types.Siacoins(100), true) // siafunds=true
	c := tn.client

	senderPrivateKey := tn.pk
	senderPolicy := types.SpendPolicy{Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(senderPrivateKey.PublicKey()))}
	senderAddr := tn.fundingAddr()

	receiverAddr := types.StandardUnlockHash(types.GeneratePrivateKey().PublicKey())

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
	tn.MineBlocks(t, types.VoidAddress, 1)

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
	signV2Txn(cs, &resp.Transaction, senderPrivateKey)

	if _, err := c.TxpoolBroadcast(resp.Basis, nil, []types.V2Transaction{resp.Transaction}); err != nil {
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
	tn.MineBlocks(t, types.VoidAddress, 1)

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

func TestSpentElement(t *testing.T) {
	log := zaptest.NewLogger(t)
	tn := newV2TestNode(t, log, types.Siacoins(100), true, wallet.WithIndexMode(wallet.IndexModeFull))
	c := tn.client

	senderPrivateKey := tn.pk
	senderPolicy := types.SpendPolicy{Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(senderPrivateKey.PublicKey()))}
	senderAddr := tn.fundingAddr()

	receiverAddr := types.StandardUnlockHash(types.GeneratePrivateKey().PublicKey())

	// trigger initial scan
	tn.MineBlocks(t, types.VoidAddress, 1)

	sce, basis, err := c.AddressSiacoinOutputs(senderAddr, false, 0, 100)
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
				Parent: sce[0].SiacoinElement,
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

	if _, err := c.TxpoolBroadcast(basis, nil, []types.V2Transaction{txn}); err != nil {
		t.Fatal(err)
	}
	tn.MineBlocks(t, types.VoidAddress, 1)

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

	sfe, basis, err := c.AddressSiafundOutputs(senderAddr, false, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(sfe) != 1 {
		t.Fatalf("expected 1 siafund element, got %v", len(sfe))
	}

	// check if the siafund element is spent
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
				Parent: sfe[0].SiafundElement,
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

	if _, err := c.TxpoolBroadcast(basis, nil, []types.V2Transaction{txn}); err != nil {
		t.Fatal(err)
	}
	tn.MineBlocks(t, types.VoidAddress, 1)

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
}

func TestDebugMine(t *testing.T) {
	log := zaptest.NewLogger(t)
	tn := newV1TestNode(t, log, types.ZeroCurrency, false)

	jc := jape.Client{
		BaseURL:  tn.client.BaseURL(),
		Password: "password",
	}

	err := jc.POST(context.Background(), "/debug/mine", api.DebugMineRequest{
		Blocks:  5,
		Address: types.VoidAddress,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tn.WaitForSync(t)

	tip, err := tn.client.ConsensusTip()
	if err != nil {
		t.Fatal(err)
	} else if tip.Height != 5 {
		t.Fatalf("expected tip height to be 5, got %v", tip.Height)
	}
}

func TestAPISecurity(t *testing.T) {
	n, genesisBlock := testutil.V1Network()
	log := zaptest.NewLogger(t)

	cn := testutil.NewConsensusNode(t, n, genesisBlock, log)
	wm, err := wallet.NewManager(cn.Chain, cn.Store, wallet.WithLogger(log.Named("wallet")))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	httpListener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal("failed to listen:", err)
	}
	defer httpListener.Close()

	server := &http.Server{
		Handler:      api.NewServer(cn.Store, cn.Chain, cn.Syncer, wm, api.WithDebug(), api.WithLogger(zaptest.NewLogger(t)), api.WithBasicAuth("test")),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}
	defer server.Close()
	go server.Serve(httpListener)

	replaceHandler := func(apiOpts ...api.ServerOption) {
		server.Handler = api.NewServer(cn.Store, cn.Chain, cn.Syncer, wm, apiOpts...)
	}

	// create a client with correct credentials
	c := api.NewClient("http://"+httpListener.Addr().String(), "test")
	if _, err := c.ConsensusTip(); err != nil {
		t.Fatal(err)
	}

	// create a client with incorrect credentials
	c = api.NewClient("http://"+httpListener.Addr().String(), "wrong")
	if _, err := c.ConsensusTip(); err == nil {
		t.Fatal("expected auth error")
	} else if err.Error() != "unauthorized" {
		t.Fatal("expected auth error, got", err)
	}

	// replace the handler with a new one that doesn't require auth
	replaceHandler()

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
	replaceHandler(api.WithBasicAuth("test"), api.WithPublicEndpoints(true))

	// create a client without credentials
	c = api.NewClient("http://"+httpListener.Addr().String(), "")

	// check that a public endpoint is accessible
	if _, err := c.ConsensusTip(); err != nil {
		t.Fatal(err)
	}

	// check that a private endpoint is still protected
	if _, err := c.Wallets(); err == nil {
		t.Fatal("expected auth error")
	} else if err.Error() != "unauthorized" {
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
	tn := newV1TestNode(t, log, types.ZeroCurrency, false)
	c := tn.client

	buf, err := json.Marshal(tn.Chain.Tip().Height)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, c.BaseURL()+"/rescan", bytes.NewReader(buf))
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
	tn := newV2TestNode(t, log, types.ZeroCurrency, false)
	c := tn.client

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
	tn.MineBlocks(t, addr, 5+int(tn.network.MaturityDelay))

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
	if _, err := c.TxpoolBroadcast(basis, nil, []types.V2Transaction{parentTxn}); err != nil {
		t.Fatal(err)
	}
	tn.MineBlocks(t, types.VoidAddress, 1)

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
			{Address: frand.Entropy256(), Value: sce.SiacoinOutput.Value},
		},
	}
	childSigHash := cs.InputSigHash(childTxn)
	childTxn.SiacoinInputs[0].SatisfiedPolicy.Signatures = []types.Signature{pk.SignHash(childSigHash)}

	txnset := []types.V2Transaction{parentTxn, childTxn}

	tip, err := c.ConsensusTip()
	if err != nil {
		t.Fatal(err)
	}

	basis, txnset, err = c.V2UpdateTransactionSetBasis(txnset, basis, tip)
	if err != nil {
		t.Fatal(err)
	} else if len(txnset) != 1 {
		t.Fatalf("expected 1 transactions, got %v", len(txnset))
	} else if txnset[0].ID() != childTxn.ID() {
		t.Fatalf("expected parent transaction to be removed")
	} else if basis != tip {
		t.Fatalf("expected basis to be %v, got %v", tip, basis)
	}

	if _, err := c.TxpoolBroadcast(basis, nil, txnset); err != nil {
		t.Fatal(err)
	}
	tn.MineBlocks(t, types.VoidAddress, 1)
}

func TestAddressTPool(t *testing.T) {
	log := zaptest.NewLogger(t)
	tn := newV2TestNode(t, log, types.Siacoins(100), false, wallet.WithIndexMode(wallet.IndexModeFull))
	c := tn.client

	pk := tn.pk
	uc := types.StandardUnlockConditions(pk.PublicKey())
	addr1 := tn.fundingAddr()

	assertSiacoinElement := func(t *testing.T, id types.SiacoinOutputID, value types.Currency, confirmations uint64) {
		t.Helper()

		utxos, _, err := c.AddressSiacoinOutputs(addr1, true, 0, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, sce := range utxos {
			if sce.ID == id {
				if !sce.SiacoinOutput.Value.Equals(value) {
					t.Fatalf("expected value %v, got %v", value, sce.SiacoinOutput.Value)
				} else if sce.Confirmations != confirmations {
					t.Fatalf("expected confirmations %d, got %d", confirmations, sce.Confirmations)
				}
				return
			}
		}
		t.Fatalf("expected siacoin element with ID %q not found", id)
	}

	tn.MineBlocks(t, types.VoidAddress, 1)

	airdropID := tn.genesis.Transactions[0].SiacoinOutputID(0)
	assertSiacoinElement(t, airdropID, types.Siacoins(100), 2)

	utxos, basis, err := c.AddressSiacoinOutputs(addr1, true, 0, 100)
	if err != nil {
		t.Fatal(err)
	}

	cs, err := c.ConsensusTipState()
	if err != nil {
		t.Fatal(err)
	}
	txn := types.V2Transaction{
		SiacoinInputs: []types.V2SiacoinInput{
			{
				Parent: utxos[0].SiacoinElement,
				SatisfiedPolicy: types.SatisfiedPolicy{
					Policy: types.SpendPolicy{
						Type: types.PolicyTypeUnlockConditions(uc),
					},
				},
			},
		},
		SiacoinOutputs: []types.SiacoinOutput{
			{
				Address: frand.Entropy256(),
				Value:   types.Siacoins(25),
			},
			{
				Address: addr1,
				Value:   types.Siacoins(75),
			},
		},
	}
	sigHash := cs.InputSigHash(txn)
	txn.SiacoinInputs[0].SatisfiedPolicy.Signatures = []types.Signature{
		pk.SignHash(sigHash),
	}

	if _, err := c.TxpoolBroadcast(basis, nil, []types.V2Transaction{txn}); err != nil {
		t.Fatal(err)
	}

	assertSiacoinElement(t, txn.SiacoinOutputID(txn.ID(), 1), types.Siacoins(75), 0)
	tn.MineBlocks(t, types.VoidAddress, 1)
	assertSiacoinElement(t, txn.SiacoinOutputID(txn.ID(), 1), types.Siacoins(75), 1)
}

func TestEphemeralTransactions(t *testing.T) {
	log := zaptest.NewLogger(t)
	tn := newV2TestNode(t, log, types.Siacoins(100), false, wallet.WithIndexMode(wallet.IndexModeFull))
	c := tn.client

	pk := tn.pk
	sp := types.SpendPolicy{
		Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(pk.PublicKey())),
	}
	addr1 := tn.fundingAddr()

	tn.MineBlocks(t, types.VoidAddress, 1)

	sces, basis, err := c.AddressSiacoinOutputs(addr1, true, 0, 100)
	if err != nil {
		t.Fatal(err)
	}

	txn := types.V2Transaction{
		SiacoinInputs: []types.V2SiacoinInput{

			{
				Parent: sces[0].SiacoinElement,
				SatisfiedPolicy: types.SatisfiedPolicy{
					Policy: sp,
				},
			},
		},
		SiacoinOutputs: []types.SiacoinOutput{
			{
				Address: frand.Entropy256(),
				Value:   types.Siacoins(50),
			},
			{
				Address: addr1,
				Value:   types.Siacoins(50),
			},
		},
	}
	cs, err := c.ConsensusTipState()
	if err != nil {
		t.Fatal(err)
	}
	sigHash := cs.InputSigHash(txn)
	txn.SiacoinInputs[0].SatisfiedPolicy.Signatures = []types.Signature{pk.SignHash(sigHash)}
	expectedOutputID := txn.SiacoinOutputID(txn.ID(), 1)

	if _, err := c.TxpoolBroadcast(basis, nil, []types.V2Transaction{txn}); err != nil {
		t.Fatal(err)
	}

	sces, basis, err = c.AddressSiacoinOutputs(addr1, true, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(sces) != 1 {
		t.Fatalf("expected 1 siacoin element, got %v", len(sces))
	} else if sces[0].ID != expectedOutputID {
		t.Fatalf("expected siacoin element ID %q, got %q", expectedOutputID, sces[0].ID)
	} else if sces[0].StateElement.LeafIndex != types.UnassignedLeafIndex {
		t.Fatalf("expected siacoin element to have unassigned leaf index, got %v", sces[0].StateElement.LeafIndex)
	}

	txn2 := types.V2Transaction{
		SiacoinInputs: []types.V2SiacoinInput{
			{
				Parent: sces[0].SiacoinElement,
				SatisfiedPolicy: types.SatisfiedPolicy{
					Policy: sp,
				},
			},
		},
		SiacoinOutputs: []types.SiacoinOutput{
			{
				Address: frand.Entropy256(),
				Value:   sces[0].SiacoinOutput.Value,
			},
		},
	}
	sigHash = cs.InputSigHash(txn2)
	txn2.SiacoinInputs[0].SatisfiedPolicy.Signatures = []types.Signature{pk.SignHash(sigHash)}

	// mine a block so the basis is behind
	tn.MineBlocks(t, types.VoidAddress, 1)

	sces, _, err = c.AddressSiacoinOutputs(addr1, true, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(sces) != 1 {
		t.Fatalf("expected no siacoin elements, got %v", len(sces))
	} else if sces[0].ID != expectedOutputID {
		t.Fatalf("expected siacoin element ID %q, got %q", expectedOutputID, sces[0].ID)
	} else if sces[0].StateElement.LeafIndex == types.UnassignedLeafIndex {
		t.Fatalf("expected siacoin element to have leaf index, got %v", sces[0].StateElement.LeafIndex)
	}

	if _, err := c.TxpoolBroadcast(basis, nil, []types.V2Transaction{txn2}); err != nil {
		t.Fatal(err)
	}

	sces, _, err = c.AddressSiacoinOutputs(addr1, true, 0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(sces) != 0 {
		t.Fatalf("expected no siacoin elements, got %v", len(sces))
	}
}

func TestBroadcastRace(t *testing.T) {
	t.Skip("NDF") // TODO: fix

	log := zap.NewNop()
	tn := newV2TestNode(t, log, types.Siacoins(100000), false, wallet.WithIndexMode(wallet.IndexModeFull))
	c := tn.client

	pk := tn.pk
	sp := types.SpendPolicy{
		Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(pk.PublicKey())),
	}
	addr1 := tn.fundingAddr()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				tn.MineBlocks(t, types.VoidAddress, 1)
			}
		}
	}()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for i := 0; i < 100; i++ {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sces, basis, err := c.AddressSiacoinOutputs(addr1, true, 0, 100)
			if err != nil {
				panic(err)
			}

			burn := types.Siacoins(1)
			rem := sces[0].SiacoinOutput.Value.Sub(burn)
			txn := types.V2Transaction{
				SiacoinInputs: []types.V2SiacoinInput{
					{
						Parent: sces[0].SiacoinElement,
						SatisfiedPolicy: types.SatisfiedPolicy{
							Policy: sp,
						},
					},
				},
				SiacoinOutputs: []types.SiacoinOutput{
					{
						Address: frand.Entropy256(),
						Value:   burn,
					},
					{
						Address: addr1,
						Value:   rem,
					},
				},
			}
			cs, err := c.ConsensusTipState()
			if err != nil {
				t.Fatal(err)
			}
			sigHash := cs.InputSigHash(txn)
			txn.SiacoinInputs[0].SatisfiedPolicy.Signatures = []types.Signature{pk.SignHash(sigHash)}
			if _, err := c.TxpoolBroadcast(basis, nil, []types.V2Transaction{txn}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestTxPoolOverwriteProofs(t *testing.T) {
	log := zaptest.NewLogger(t)
	tn := newV2TestNode(t, log, types.Siacoins(100), false, wallet.WithIndexMode(wallet.IndexModeFull))
	c := tn.client

	senderPrivateKey := tn.pk
	senderPolicy := types.SpendPolicy{Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(senderPrivateKey.PublicKey()))}
	senderAddr := tn.fundingAddr()

	receiverAddr := types.StandardUnlockHash(types.GeneratePrivateKey().PublicKey())

	w, err := c.AddWallet(api.WalletUpdateRequest{
		Name: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}

	wc := c.Wallet(w.ID)
	// add an address
	err = wc.AddAddress(wallet.Address{
		Address:     senderAddr,
		SpendPolicy: &senderPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	tn.MineBlocks(t, types.VoidAddress, 1)

	resp, err := wc.ConstructV2([]types.SiacoinOutput{
		{Value: types.Siacoins(1), Address: receiverAddr},
	}, nil, senderAddr)
	if err != nil {
		t.Fatal(err)
	}

	cs, err := c.ConsensusTipState()
	if err != nil {
		t.Fatal(err)
	}

	// sign the transaction
	sigHash := cs.InputSigHash(resp.Transaction)
	for i := range resp.Transaction.SiacoinInputs {
		resp.Transaction.SiacoinInputs[i].SatisfiedPolicy.Signatures = []types.Signature{senderPrivateKey.SignHash(sigHash)}
	}

	// assert the transaction is valid
	cs, ok := tn.Chain.State(resp.Basis.ID)
	if !ok {
		t.Fatal("failed to get state")
	}
	ms := consensus.NewMidState(cs)
	if err := consensus.ValidateV2Transaction(ms, resp.Transaction); err != nil {
		t.Fatal(err)
	}

	// corrupt the proof
	resp.Transaction.SiacoinInputs[0].Parent.StateElement.MerkleProof[frand.Intn(len(resp.Transaction.SiacoinInputs[0].Parent.StateElement.MerkleProof))] = frand.Entropy256()

	// assert the transaction is invalid
	ms = consensus.NewMidState(cs)
	if err := consensus.ValidateV2Transaction(ms, resp.Transaction); !strings.Contains(err.Error(), "not present in the accumulator") {
		t.Fatalf("expected error to contain %q, got %v", "not present in the accumulator", err)
	}

	if broadcastResp, err := c.TxpoolBroadcast(resp.Basis, nil, []types.V2Transaction{resp.Transaction}); err != nil {
		t.Fatal(err)
	} else if len(broadcastResp.Transactions) != 0 || len(broadcastResp.V2Transactions) != 1 {
		t.Fatalf("expected 1 v1 ID and 0 v2 IDs, got %v and %v", len(broadcastResp.Transactions), len(broadcastResp.V2Transactions))
	} else if broadcastResp.V2Transactions[0].ID() != resp.ID {
		t.Fatalf("expected v2 ID to be %v, got %v", resp.ID, broadcastResp.V2Transactions[0].ID())
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
	tn.MineBlocks(t, types.VoidAddress, 1)

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

func TestTxPoolOverwriteProofsEphemeral(t *testing.T) {
	log := zaptest.NewLogger(t)
	tn := newV2TestNode(t, log, types.Siacoins(100), false, wallet.WithIndexMode(wallet.IndexModeFull))
	c := tn.client

	senderPrivateKey := tn.pk
	senderPolicy := types.SpendPolicy{Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(senderPrivateKey.PublicKey()))}
	senderAddr := tn.fundingAddr()

	receiverAddr := types.StandardUnlockHash(types.GeneratePrivateKey().PublicKey())

	w, err := c.AddWallet(api.WalletUpdateRequest{
		Name: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}

	wc := c.Wallet(w.ID)
	// add an address
	err = wc.AddAddress(wallet.Address{
		Address:     senderAddr,
		SpendPolicy: &senderPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	tn.MineBlocks(t, types.VoidAddress, 1)

	resp, err := wc.ConstructV2([]types.SiacoinOutput{
		{Value: types.Siacoins(1), Address: senderAddr},
	}, nil, senderAddr)
	if err != nil {
		t.Fatal(err)
	}

	cs, err := c.ConsensusTipState()
	if err != nil {
		t.Fatal(err)
	}

	// sign the transaction
	sigHash := cs.InputSigHash(resp.Transaction)
	for i := range resp.Transaction.SiacoinInputs {
		resp.Transaction.SiacoinInputs[i].SatisfiedPolicy.Signatures = []types.Signature{senderPrivateKey.SignHash(sigHash)}
	}

	basis := resp.Basis
	txnset := []types.V2Transaction{resp.Transaction, {
		SiacoinInputs: []types.V2SiacoinInput{
			{
				Parent: resp.Transaction.EphemeralSiacoinOutput(0),
				SatisfiedPolicy: types.SatisfiedPolicy{
					Policy: senderPolicy,
				},
			},
		},
		SiacoinOutputs: []types.SiacoinOutput{
			{Address: receiverAddr, Value: types.Siacoins(1)},
		},
	}}
	sigHash = cs.InputSigHash(txnset[1])
	for i := range txnset[1].SiacoinInputs {
		txnset[1].SiacoinInputs[i].SatisfiedPolicy.Signatures = []types.Signature{senderPrivateKey.SignHash(sigHash)}
	}

	// corrupt the proof
	txnset[0].SiacoinInputs[0].Parent.StateElement.MerkleProof[frand.Intn(len(resp.Transaction.SiacoinInputs[0].Parent.StateElement.MerkleProof))] = frand.Entropy256()

	if broadcastResp, err := c.TxpoolBroadcast(basis, nil, txnset); err != nil {
		t.Fatal(err)
	} else if len(broadcastResp.Transactions) != 0 || len(broadcastResp.V2Transactions) != 2 {
		t.Fatalf("expected 0 v1 ID and 2 v2 IDs, got %v and %v", len(broadcastResp.Transactions), len(broadcastResp.V2Transactions))
	} else if broadcastResp.V2Transactions[0].ID() != txnset[0].ID() {
		t.Fatalf("expected v2 ID to be %v, got %v", txnset[0].ID(), broadcastResp.V2Transactions[0].ID())
	} else if broadcastResp.V2Transactions[1].ID() != txnset[1].ID() {
		t.Fatalf("expected v2 ID to be %v, got %v", txnset[1].ID(), broadcastResp.V2Transactions[1].ID())
	}

	unconfirmed, err := wc.UnconfirmedEvents()
	if err != nil {
		t.Fatal(err)
	} else if len(unconfirmed) != 2 {
		t.Fatalf("expected 2 unconfirmed events, got %v", len(unconfirmed))
	}
	tn.MineBlocks(t, types.VoidAddress, 1)

	confirmed, err := wc.Events(0, 5)
	if err != nil {
		t.Fatal(err)
	} else if len(confirmed) != 3 {
		t.Fatalf("expected 3 confirmed events, got %v", len(confirmed)) // initial gift + setup + sent
	}
}

func TestWalletConfirmations(t *testing.T) {
	log := zaptest.NewLogger(t)
	tn := newV1TestNode(t, log, types.Siacoins(1), true)
	c := tn.client

	giftPrivateKey := tn.pk

	w, err := c.AddWallet(api.WalletUpdateRequest{
		Name: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	wc := c.Wallet(w.ID)

	// create and add an address
	sk2 := types.GeneratePrivateKey()
	addr := types.StandardUnlockHash(sk2.PublicKey())
	err = wc.AddAddress(wallet.Address{
		Address: addr,
		SpendPolicy: &types.SpendPolicy{
			Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(sk2.PublicKey())),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	c.Rescan(0)

	// send gift to wallet
	giftSCOID := tn.genesis.Transactions[0].SiacoinOutputID(0)
	txn := types.Transaction{
		SiacoinInputs: []types.SiacoinInput{{
			ParentID:         giftSCOID,
			UnlockConditions: types.StandardUnlockConditions(giftPrivateKey.PublicKey()),
		}},
		SiacoinOutputs: []types.SiacoinOutput{
			{Address: addr, Value: types.Siacoins(1)},
		},
		SiafundInputs: []types.SiafundInput{{
			ParentID:         tn.genesis.Transactions[0].SiafundOutputID(0),
			UnlockConditions: types.StandardUnlockConditions(giftPrivateKey.PublicKey()),
		}},
		SiafundOutputs: []types.SiafundOutput{
			{Address: addr, Value: tn.genesis.Transactions[0].SiafundOutputs[0].Value},
		},
		Signatures: []types.TransactionSignature{{
			ParentID:      types.Hash256(giftSCOID),
			CoveredFields: types.CoveredFields{WholeTransaction: true},
		}, {
			ParentID:      types.Hash256(tn.genesis.Transactions[0].SiafundOutputID(0)),
			CoveredFields: types.CoveredFields{WholeTransaction: true},
		}},
	}

	cs, err := c.ConsensusTipState()
	if err != nil {
		t.Fatal(err)
	}

	sig := giftPrivateKey.SignHash(cs.WholeSigHash(txn, types.Hash256(giftSCOID), 0, 0, nil))
	txn.Signatures[0].Signature = sig[:]
	sig2 := giftPrivateKey.SignHash(cs.WholeSigHash(txn, types.Hash256(tn.genesis.Transactions[0].SiafundOutputID(0)), 0, 0, nil))
	txn.Signatures[1].Signature = sig2[:]

	// broadcast the transaction to the transaction pool
	if _, err := c.TxpoolBroadcast(cs.Index, []types.Transaction{txn}, nil); err != nil {
		t.Fatal(err)
	}

	// confirm the transaction
	tn.MineBlocks(t, types.VoidAddress, 1)

	assertConfirmations := func(t *testing.T, n uint64) {
		t.Helper()

		outputs, basis, err := wc.SiacoinOutputs(0, 100)
		if err != nil {
			t.Fatal(err)
		} else if len(outputs) != 1 {
			t.Fatal("should have one UTXOs, got", len(outputs))
		} else if basis != tn.Chain.Tip() {
			t.Fatalf("basis should be %v, got %v", tn.Chain.Tip(), basis)
		} else if outputs[0].Confirmations != n {
			t.Fatalf("expected %d confirmation, got %v", n, outputs[0].Confirmations)
		}

		sfe, basis, err := wc.SiafundOutputs(0, 100)
		if err != nil {
			t.Fatal(err)
		} else if len(sfe) != 1 {
			t.Fatal("should have one siafund output, got", len(sfe))
		} else if basis != tn.Chain.Tip() {
			t.Fatalf("basis should be %v, got %v", tn.Chain.Tip(), basis)
		} else if sfe[0].Confirmations != n {
			t.Fatalf("expected %d confirmation, got %v", n, sfe[0].Confirmations)
		}
	}

	assertConfirmations(t, 1)
	tn.MineBlocks(t, types.VoidAddress, 10)
	assertConfirmations(t, 11)
}

func TestTxPoolAllowVoid(t *testing.T) {
	log := zaptest.NewLogger(t)
	tn := newV2TestNode(t, log, types.Siacoins(100), false)
	c := tn.client

	senderPrivateKey := tn.pk
	senderPolicy := types.SpendPolicy{Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(senderPrivateKey.PublicKey()))}
	senderAddr := tn.fundingAddr()

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
	tn.MineBlocks(t, types.VoidAddress, 1)

	sces, basis, err := wc.SiacoinOutputs(0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(sces) != 1 {
		t.Fatalf("expected 1 siacoin output, got %v", len(sces))
	}

	txn := types.V2Transaction{
		SiacoinInputs: []types.V2SiacoinInput{
			{
				Parent: sces[0].SiacoinElement,
				SatisfiedPolicy: types.SatisfiedPolicy{
					Policy: senderPolicy,
				},
			},
		},
		SiacoinOutputs: []types.SiacoinOutput{
			{
				Address: types.VoidAddress,
				Value:   types.Siacoins(50),
			},
			{
				Address: senderAddr,
				Value:   sces[0].SiacoinElement.SiacoinOutput.Value.Sub(types.Siacoins(50)),
			},
		},
	}

	cs, err := c.ConsensusTipState()
	if err != nil {
		t.Fatal(err)
	}
	sigHash := cs.InputSigHash(txn)
	txn.SiacoinInputs[0].SatisfiedPolicy.Signatures = []types.Signature{senderPrivateKey.SignHash(sigHash)}

	// attempt to broadcast without allowing void
	if _, err := c.TxpoolBroadcast(basis, nil, []types.V2Transaction{txn}); err == nil {
		t.Fatal("expected error")
	} else if !strings.Contains(err.Error(), "cannot send to void address") {
		t.Fatalf("expected error to contain %q, got %v", "cannot send to void address", err)
	}

	// broadcast with allowing void
	if _, err := c.TxpoolBroadcast(basis, nil, []types.V2Transaction{txn}, api.WithAllowVoid()); err != nil {
		t.Fatal(err)
	}
}
