package api_test

import (
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/syncer"
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

func runServer(cm api.ChainManager, s api.Syncer, wm api.WalletManager) (*api.Client, func()) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		panic(err)
	}
	go func() {
		srv := api.NewServer(cm, s, wm)
		http.Serve(l, jape.BasicAuth("password")(srv))
	}()
	c := api.NewClient("http://"+l.Addr().String(), "password")
	return c, func() { l.Close() }
}

func TestWallet(t *testing.T) {
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
	wm, err := wallet.NewManager(cm, ws, log.Named("wallet"))
	if err != nil {
		t.Fatal(err)
	}

	sav := wallet.NewSeedAddressVault(wallet.NewSeed(), 0, 20)
	c, shutdown := runServer(cm, nil, wm)
	defer shutdown()
	if err := c.AddWallet("primary", nil); err != nil {
		t.Fatal(err)
	}
	wc := c.Wallet("primary")
	if err := c.Resubscribe(0); err != nil {
		t.Fatal(err)
	}

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
	addr, info := sav.NewAddress("primary")
	if err := wc.AddAddress(addr, info); err != nil {
		t.Fatal(err)
	}

	// should have an address now
	addresses, err = wc.Addresses()
	if err != nil {
		t.Fatal(err)
	} else if _, ok := addresses[addr]; !ok || len(addresses) != 1 {
		t.Fatal("bad address list", addresses)
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

	// get new balance
	balance, err = wc.Balance()
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.Equals(types.Siacoins(1)) {
		t.Error("balance should be 1 SC, got", balance.Siacoins)
	} else if !balance.ImmatureSiacoins.IsZero() {
		t.Error("immature balance should be 0 SC, got", balance.ImmatureSiacoins)
	}

	// transaction should appear in history
	events, err = wc.Events(0, 100)
	if err != nil {
		t.Fatal(err)
	} else if len(events) == 0 {
		t.Error("transaction should appear in history")
	}

	outputs, _, err := wc.Outputs()
	if err != nil {
		t.Fatal(err)
	} else if len(outputs) != 2 {
		t.Error("should have two UTXOs, got", len(outputs))
	}

	// mine a block to add an immature balance
	cs = cm.TipState()
	b = types.Block{
		ParentID:     cs.Index.ID,
		Timestamp:    types.CurrentTimestamp(),
		MinerPayouts: []types.SiacoinOutput{{Address: addr, Value: cs.BlockReward()}},
	}
	for b.ID().CmpWork(cs.ChildTarget) < 0 {
		b.Nonce += cs.NonceFactor()
	}
	if err := cm.AddBlocks([]types.Block{b}); err != nil {
		t.Fatal(err)
	}

	// get new balance
	balance, err = wc.Balance()
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.Equals(types.Siacoins(1)) {
		t.Error("balance should be 1 SC, got", balance.Siacoins)
	} else if !balance.ImmatureSiacoins.Equals(b.MinerPayouts[0].Value) {
		t.Errorf("immature balance should be %d SC, got %d SC", b.MinerPayouts[0].Value, balance.ImmatureSiacoins)
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

	// get new balance
	balance, err = wc.Balance()
	if err != nil {
		t.Fatal(err)
	} else if !balance.Siacoins.Equals(expectedBalance) {
		t.Errorf("balance should be %d, got %d", expectedBalance, balance.Siacoins)
	} else if !balance.ImmatureSiacoins.IsZero() {
		t.Error("immature balance should be 0 SC, got", balance.ImmatureSiacoins)
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
	wm, err := wallet.NewManager(cm, ws, log.Named("wallet"))
	if err != nil {
		t.Fatal(err)
	}
	c, shutdown := runServer(cm, nil, wm)
	defer shutdown()
	if err := c.AddWallet("primary", nil); err != nil {
		t.Fatal(err)
	}
	primary := c.Wallet("primary")
	if err := primary.AddAddress(primaryAddress, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.AddWallet("secondary", nil); err != nil {
		t.Fatal(err)
	}
	secondary := c.Wallet("secondary")
	if err := secondary.AddAddress(secondaryAddress, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Resubscribe(0); err != nil {
		t.Fatal(err)
	}

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
		key := primaryPrivateKey
		dest := secondaryAddress
		pbal, sbal := types.ZeroCurrency, types.ZeroCurrency
		sces, _, err := primary.Outputs()
		if err != nil {
			t.Fatal(err)
		}
		if len(sces) == 0 {
			sces, _, err = secondary.Outputs()
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
				ParentID:      sce.ID,
				CoveredFields: types.CoveredFields{WholeTransaction: true},
			}},
		}
		sig := key.SignHash(cm.TipState().WholeSigHash(txn, sce.ID, 0, 0, nil))
		txn.Signatures[0].Signature = sig[:]
		if err := addBlock([]types.Transaction{txn}, nil); err != nil {
			return err
		}
		checkBalances(pbal, sbal)
		return nil
	}
	sendV2 := func() error {
		t.Helper()

		// which wallet is sending?
		key := primaryPrivateKey
		dest := secondaryAddress
		pbal, sbal := types.ZeroCurrency, types.ZeroCurrency
		sces, _, err := primary.Outputs()
		if err != nil {
			t.Fatal(err)
		}
		if len(sces) == 0 {
			sces, _, err = secondary.Outputs()
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
	wm1, err := wallet.NewManager(cm1, store1, log1.Named("wallet"))
	if err != nil {
		t.Fatal(err)
	}
	l1, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer l1.Close()
	s1 := syncer.New(l1, cm1, store1, gateway.Header{
		GenesisID:  genesisBlock.ID(),
		UniqueID:   gateway.GenerateUniqueID(),
		NetAddress: l1.Addr().String(),
	})
	go s1.Run()
	c1, shutdown := runServer(cm1, s1, wm1)
	defer shutdown()
	if err := c1.AddWallet("primary", nil); err != nil {
		t.Fatal(err)
	}
	primary := c1.Wallet("primary")
	if err := primary.AddAddress(primaryAddress, nil); err != nil {
		t.Fatal(err)
	}
	if err := c1.Resubscribe(0); err != nil {
		t.Fatal(err)
	}

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
	wm2, err := wallet.NewManager(cm2, store2, log2.Named("wallet"))
	if err != nil {
		t.Fatal(err)
	}
	l2, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	s2 := syncer.New(l2, cm2, store2, gateway.Header{
		GenesisID:  genesisBlock.ID(),
		UniqueID:   gateway.GenerateUniqueID(),
		NetAddress: l2.Addr().String(),
	}, syncer.WithLogger(zaptest.NewLogger(t)))
	go s2.Run()
	c2, shutdown2 := runServer(cm2, s2, wm2)
	defer shutdown2()
	if err := c2.AddWallet("secondary", nil); err != nil {
		t.Fatal(err)
	}
	secondary := c2.Wallet("secondary")
	if err := secondary.AddAddress(secondaryAddress, nil); err != nil {
		t.Fatal(err)
	}
	if err := c2.Resubscribe(0); err != nil {
		t.Fatal(err)
	}

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

		txns, v2txns, err := c.TxpoolTransactions()
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
		sces, _, err := primary.Outputs()
		if err != nil {
			t.Fatal(err)
		}
		if len(sces) == 0 {
			c = c2
			key = secondaryPrivateKey
			dest = primaryAddress
			sces, _, err = secondary.Outputs()
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
				ParentID:      sce.ID,
				CoveredFields: types.CoveredFields{WholeTransaction: true},
			}},
		}
		cs, err := c.ConsensusTipState()
		if err != nil {
			return err
		}
		sig := key.SignHash(cs.WholeSigHash(txn, sce.ID, 0, 0, nil))
		txn.Signatures[0].Signature = sig[:]
		if err := c.TxpoolBroadcast([]types.Transaction{txn}, nil); err != nil {
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
		sces, _, err := primary.Outputs()
		if err != nil {
			t.Fatal(err)
		}
		if len(sces) == 0 {
			c = c2
			key = secondaryPrivateKey
			dest = primaryAddress
			sces, _, err = secondary.Outputs()
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
		if err := c.TxpoolBroadcast(nil, []types.V2Transaction{txn}); err != nil {
			return err
		} else if err := addBlock(); err != nil {
			return err
		}
		checkBalances(pbal, sbal)
		return nil
	}

	// connect the syncers
	if _, err := s1.Connect(s2.Addr()); err != nil {
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
	// v1 transactions should no longer work
	if err := sendV1(); err == nil {
		t.Fatal("expected v1 txn to be rejected")
	}
	// use a v2 transaction instead
	if err := sendV2(); err != nil {
		t.Fatal(err)
	}
}
