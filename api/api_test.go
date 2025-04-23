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
		Handler:      api.NewServer(cn.Chain, cn.Syncer, wm, api.WithDebug(), api.WithLogger(log)),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}
	tb.Cleanup(func() { server.Close() })

	go server.Serve(l)
	return api.NewClient("http://"+l.Addr().String(), "password")
}

func TestWalletAdd(t *testing.T) {
	log := zaptest.NewLogger(t)

	n, genesisBlock := testutil.V1Network()
	giftPrivateKey := types.GeneratePrivateKey()
	giftAddress := types.StandardUnlockHash(giftPrivateKey.PublicKey())
	genesisBlock.Transactions[0].SiacoinOutputs[0] = types.SiacoinOutput{
		Value:   types.Siacoins(1),
		Address: giftAddress,
	}
	cn := testutil.NewConsensusNode(t, n, genesisBlock, log)
	c := startWalletServer(t, cn, log)

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
	n, genesisBlock := testutil.V1Network()
	giftPrivateKey := types.GeneratePrivateKey()
	giftAddress := types.StandardUnlockHash(giftPrivateKey.PublicKey())
	genesisBlock.Transactions[0].SiacoinOutputs[0] = types.SiacoinOutput{
		Value:   types.Siacoins(1),
		Address: giftAddress,
	}
	cn := testutil.NewConsensusNode(t, n, genesisBlock, log)
	c := startWalletServer(t, cn, log)

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
	cn.WaitForSync(t)

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
	giftSCOID := genesisBlock.Transactions[0].SiacoinOutputID(0)
	txn := types.Transaction{
		SiacoinInputs: []types.SiacoinInput{{
			ParentID:         giftSCOID,
			UnlockConditions: types.StandardUnlockConditions(giftPrivateKey.PublicKey()),
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

	sig := giftPrivateKey.SignHash(cs.WholeSigHash(txn, types.Hash256(giftSCOID), 0, 0, nil))
	txn.Signatures[0].Signature = sig[:]

	// broadcast the transaction to the transaction pool
	if err := c.TxpoolBroadcast(cs.Index, []types.Transaction{txn}, nil); err != nil {
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
	cn.MineBlocks(t, types.VoidAddress, 1)

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
	} else if basis != cn.Chain.Tip() {
		t.Fatalf("basis should be %v, got %v", cn.Chain.Tip(), basis)
	}

	// mine a block to add an immature balance
	expectedPayout := cn.Chain.TipState().BlockReward()
	cn.MineBlocks(t, addr, 1)

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
	cn.MineBlocks(t, types.VoidAddress, int(n.MaturityDelay))

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

	n, genesisBlock := testutil.V1Network()
	giftPrivateKey := types.GeneratePrivateKey()
	giftAddress := types.StandardUnlockHash(giftPrivateKey.PublicKey())
	genesisBlock.Transactions[0].SiacoinOutputs[0] = types.SiacoinOutput{
		Value:   types.Siacoins(1),
		Address: giftAddress,
	}

	cn := testutil.NewConsensusNode(t, n, genesisBlock, log)
	c := startWalletServer(t, cn, log)

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
	giftSCOID := genesisBlock.Transactions[0].SiacoinOutputID(0)
	txn := types.Transaction{
		SiacoinInputs: []types.SiacoinInput{{
			ParentID:         giftSCOID,
			UnlockConditions: types.StandardUnlockConditions(giftPrivateKey.PublicKey()),
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

	sig := giftPrivateKey.SignHash(cs.WholeSigHash(txn, types.Hash256(giftSCOID), 0, 0, nil))
	txn.Signatures[0].Signature = sig[:]

	// broadcast the transaction to the transaction pool
	if err := c.TxpoolBroadcast(cs.Index, []types.Transaction{txn}, nil); err != nil {
		t.Fatal(err)
	}
	cn.MineBlocks(t, types.VoidAddress, 1)

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
	} else if basis != cn.Chain.Tip() {
		t.Fatalf("basis should be %v, got %v", cn.Chain.Tip(), basis)
	}

	// mine a block to add an immature balance
	expectedPayout := cn.Chain.TipState().BlockReward()
	cn.MineBlocks(t, addr, 1)

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
	cn.MineBlocks(t, types.VoidAddress, int(n.MaturityDelay))

	// get new balance
	balance, err = c.AddressBalance(addr)
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.Equals(expectedBalance) {
		t.Fatalf("balance should be %d, got %d", expectedBalance, balance.Siacoins)
	} else if !balance.ImmatureSiacoins.IsZero() {
		t.Fatal("immature balance should be 0 SC, got", balance.ImmatureSiacoins)
	}
}

func TestConsensus(t *testing.T) {
	log := zaptest.NewLogger(t)

	n, genesisBlock := testutil.V2Network()
	giftPrivateKey := types.GeneratePrivateKey()
	giftAddress := types.StandardUnlockHash(giftPrivateKey.PublicKey())
	genesisBlock.Transactions[0].SiacoinOutputs[0] = types.SiacoinOutput{
		Value:   types.Siacoins(1),
		Address: giftAddress,
	}

	cn := testutil.NewConsensusNode(t, n, genesisBlock, log)
	c := startWalletServer(t, cn, log)

	// mine a block
	minedBlock, ok := coreutils.MineBlock(cn.Chain, types.Address{}, time.Minute)
	if !ok {
		t.Fatal("no block found")
	} else if err := cn.Chain.AddBlocks([]types.Block{minedBlock}); err != nil {
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
	} else if b.ID() != minedBlock.ID() {
		t.Fatal("mismatch")
	}
}

func TestConsensusUpdates(t *testing.T) {
	log := zaptest.NewLogger(t)

	n, genesisBlock := testutil.V1Network()
	giftPrivateKey := types.GeneratePrivateKey()
	giftAddress := types.StandardUnlockHash(giftPrivateKey.PublicKey())
	genesisBlock.Transactions[0].SiacoinOutputs[0] = types.SiacoinOutput{
		Value:   types.Siacoins(1),
		Address: giftAddress,
	}

	cn := testutil.NewConsensusNode(t, n, genesisBlock, log)
	c := startWalletServer(t, cn, log)
	cn.MineBlocks(t, types.VoidAddress, 10)

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
		expected, ok := cn.Chain.BestIndex(uint64(i))
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

	n, genesisBlock := testutil.V1Network()
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

	cn := testutil.NewConsensusNode(t, n, genesisBlock, log)
	c := startWalletServer(t, cn, log)

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
	cn.MineBlocks(t, types.VoidAddress, 1)

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
	cn.MineBlocks(t, types.VoidAddress, 1)

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

	n, genesisBlock := testutil.V1Network()
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

	cn := testutil.NewConsensusNode(t, n, genesisBlock, log)
	c := startWalletServer(t, cn, log)

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
	cn.MineBlocks(t, types.VoidAddress, 1)

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
	cn.MineBlocks(t, types.VoidAddress, 1)

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

	cm := testutil.NewConsensusNode(t, n, genesisBlock, log)
	c := startWalletServer(t, cm, log)

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
	cm.MineBlocks(t, types.VoidAddress, 1)

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
	cm.MineBlocks(t, types.VoidAddress, 1)

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

	cn := testutil.NewConsensusNode(t, n, genesisBlock, log)
	c := startWalletServer(t, cn, log)

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
	cn.MineBlocks(t, types.VoidAddress, 1)

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
	cn.MineBlocks(t, types.VoidAddress, 1)

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

	cn := testutil.NewConsensusNode(t, n, genesisBlock, log)
	c := startWalletServer(t, cn, log, wallet.WithIndexMode(wallet.IndexModeFull))

	// trigger initial scan
	cn.MineBlocks(t, types.VoidAddress, 1)

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

	if err := c.TxpoolBroadcast(basis, nil, []types.V2Transaction{txn}); err != nil {
		t.Fatal(err)
	}
	cn.MineBlocks(t, types.VoidAddress, 1)

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
	cn.MineBlocks(t, types.VoidAddress, 144)

	_, err = c.SpentSiacoinElement(sce[0].ID)
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected error to contain %q, got %q", "not found", err)
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

	if err := c.TxpoolBroadcast(basis, nil, []types.V2Transaction{txn}); err != nil {
		t.Fatal(err)
	}
	cn.MineBlocks(t, types.VoidAddress, 1)

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
	cn.MineBlocks(t, types.VoidAddress, 144)

	_, err = c.SpentSiafundElement(sfe[0].ID)
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected error to contain %q, got %q", "not found", err)
	}
}

func TestDebugMine(t *testing.T) {
	log := zaptest.NewLogger(t)
	n, genesisBlock := testutil.V1Network()

	cn := testutil.NewConsensusNode(t, n, genesisBlock, log)
	c := startWalletServer(t, cn, log)

	jc := jape.Client{
		BaseURL:  c.BaseURL(),
		Password: "password",
	}

	err := jc.POST(context.Background(), "/debug/mine", api.DebugMineRequest{
		Blocks:  5,
		Address: types.VoidAddress,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cn.WaitForSync(t)

	tip, err := c.ConsensusTip()
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
		Handler:      api.NewServer(cn.Chain, cn.Syncer, wm, api.WithDebug(), api.WithLogger(zaptest.NewLogger(t)), api.WithBasicAuth("test")),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}
	defer server.Close()
	go server.Serve(httpListener)

	replaceHandler := func(apiOpts ...api.ServerOption) {
		server.Handler = api.NewServer(cn.Chain, cn.Syncer, wm, apiOpts...)
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
	} else if err.Error() == "unauthorized" {
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
	n, genesisBlock := testutil.V1Network()

	cn := testutil.NewConsensusNode(t, n, genesisBlock, log)
	c := startWalletServer(t, cn, log)

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

	cn := testutil.NewConsensusNode(t, n, genesisBlock, log)
	c := startWalletServer(t, cn, log)

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
	cn.MineBlocks(t, addr, 5+int(n.MaturityDelay))

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
	cn.MineBlocks(t, types.VoidAddress, 1)

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

	if err := c.TxpoolBroadcast(basis, nil, txnset); err != nil {
		t.Fatal(err)
	}
	cn.MineBlocks(t, types.VoidAddress, 1)
}

func TestAddressTPool(t *testing.T) {
	log := zaptest.NewLogger(t)

	pk := types.GeneratePrivateKey()
	uc := types.StandardUnlockConditions(pk.PublicKey())
	addr1 := types.StandardUnlockHash(pk.PublicKey())

	n, genesisBlock := testutil.V2Network()
	genesisBlock.Transactions[0].SiacoinOutputs[0] = types.SiacoinOutput{
		Value:   types.Siacoins(100),
		Address: addr1,
	}

	cn := testutil.NewConsensusNode(t, n, genesisBlock, log)
	c := startWalletServer(t, cn, log, wallet.WithIndexMode(wallet.IndexModeFull))

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

	cn.MineBlocks(t, types.VoidAddress, 1)

	airdropID := genesisBlock.Transactions[0].SiacoinOutputID(0)
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
				Address: types.VoidAddress,
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

	if err := c.TxpoolBroadcast(basis, nil, []types.V2Transaction{txn}); err != nil {
		t.Fatal(err)
	}

	assertSiacoinElement(t, txn.SiacoinOutputID(txn.ID(), 1), types.Siacoins(75), 0)
	cn.MineBlocks(t, types.VoidAddress, 1)
	assertSiacoinElement(t, txn.SiacoinOutputID(txn.ID(), 1), types.Siacoins(75), 1)
}

func TestEphemeralTransactions(t *testing.T) {
	log := zaptest.NewLogger(t)
	pk := types.GeneratePrivateKey()
	sp := types.SpendPolicy{
		Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(pk.PublicKey())),
	}
	addr1 := sp.Address()

	n, genesisBlock := testutil.V2Network()
	genesisBlock.Transactions[0].SiacoinOutputs[0] = types.SiacoinOutput{
		Value:   types.Siacoins(100),
		Address: addr1,
	}

	cn := testutil.NewConsensusNode(t, n, genesisBlock, log)
	c := startWalletServer(t, cn, log, wallet.WithIndexMode(wallet.IndexModeFull))

	cn.MineBlocks(t, types.VoidAddress, 1)

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
				Address: types.VoidAddress,
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

	if err := c.TxpoolBroadcast(basis, nil, []types.V2Transaction{txn}); err != nil {
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
				Address: types.VoidAddress,
				Value:   sces[0].SiacoinOutput.Value,
			},
		},
	}
	sigHash = cs.InputSigHash(txn2)
	txn2.SiacoinInputs[0].SatisfiedPolicy.Signatures = []types.Signature{pk.SignHash(sigHash)}

	// mine a block so the basis is behind
	cn.MineBlocks(t, types.VoidAddress, 1)

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

	if err := c.TxpoolBroadcast(basis, nil, []types.V2Transaction{txn2}); err != nil {
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
	log := zap.NewNop()
	pk := types.GeneratePrivateKey()
	sp := types.SpendPolicy{
		Type: types.PolicyTypeUnlockConditions(types.StandardUnlockConditions(pk.PublicKey())),
	}
	addr1 := sp.Address()

	n, genesisBlock := testutil.V2Network()
	genesisBlock.Transactions[0].SiacoinOutputs[0] = types.SiacoinOutput{
		Value:   types.Siacoins(100000),
		Address: addr1,
	}

	cn := testutil.NewConsensusNode(t, n, genesisBlock, log)
	c := startWalletServer(t, cn, log, wallet.WithIndexMode(wallet.IndexModeFull))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				cn.MineBlocks(t, types.VoidAddress, 1)
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
						Address: types.VoidAddress,
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
			t.Log("broadcasting", txn.ID(), cs.Index)
			if err := c.TxpoolBroadcast(basis, nil, []types.V2Transaction{txn}); err != nil {
				t.Fatal(err)
			}
		}
	}
}
