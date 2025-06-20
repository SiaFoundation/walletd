package wallet_test

import (
	"testing"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/v2/internal/testutil"
	"go.sia.tech/walletd/v2/wallet"
	"go.uber.org/zap/zaptest"
	"lukechampine.com/frand"
)

func TestAddressUseTpool(t *testing.T) {
	log := zaptest.NewLogger(t)

	// mine a single payout to the wallet
	pk := types.GeneratePrivateKey()
	uc := types.StandardUnlockConditions(pk.PublicKey())
	addr1 := uc.UnlockHash()

	network, genesisBlock := testutil.V2Network()
	genesisBlock.Transactions[0].SiacoinOutputs = []types.SiacoinOutput{
		{Address: addr1, Value: types.Siacoins(100)},
	}
	cn := testutil.NewConsensusNode(t, network, genesisBlock, log)
	cm := cn.Chain
	db := cn.Store

	wm, err := wallet.NewManager(cm, db, wallet.WithLogger(log.Named("wallet")), wallet.WithIndexMode(wallet.IndexModeFull))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	cn.MineBlocks(t, types.VoidAddress, 1)

	assertSiacoinElement := func(t *testing.T, id types.SiacoinOutputID, value types.Currency, confirmations uint64) {
		t.Helper()

		utxos, _, err := wm.AddressSiacoinOutputs(addr1, true, 0, 1)
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

	airdropID := genesisBlock.Transactions[0].SiacoinOutputID(0)
	assertSiacoinElement(t, airdropID, types.Siacoins(100), 2)

	utxos, basis, err := wm.AddressSiacoinOutputs(addr1, true, 0, 100)
	if err != nil {
		t.Fatal(err)
	}

	cs := cm.TipState()
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

	if _, err := cm.AddV2PoolTransactions(basis, []types.V2Transaction{txn}); err != nil {
		t.Fatal(err)
	}
	wm.SyncPool() // force reindexing of the tpool
	assertSiacoinElement(t, txn.SiacoinOutputID(txn.ID(), 1), types.Siacoins(75), 0)
	cn.MineBlocks(t, types.VoidAddress, 1)
	assertSiacoinElement(t, txn.SiacoinOutputID(txn.ID(), 1), types.Siacoins(75), 1)
}

func TestBatchAddresses(t *testing.T) {
	log := zaptest.NewLogger(t)

	network, genesisBlock := testutil.V2Network()
	cn := testutil.NewConsensusNode(t, network, genesisBlock, log)
	cm := cn.Chain
	db := cn.Store

	wm, err := wallet.NewManager(cm, db, wallet.WithLogger(log.Named("wallet")), wallet.WithIndexMode(wallet.IndexModeFull))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	// mine a bunch of payouts to different addresses
	addresses := make([]types.Address, 100)
	for i := range addresses {
		addresses[i] = types.StandardAddress(types.GeneratePrivateKey().PublicKey())
		cn.MineBlocks(t, addresses[i], 1)
	}

	events, err := wm.BatchAddressEvents(addresses, 0, 1000)
	if err != nil {
		t.Fatal(err)
	} else if len(events) != 100 {
		t.Fatalf("expected 100 events, got %d", len(events))
	}
}

func TestBatchSiacoinOutputs(t *testing.T) {
	log := zaptest.NewLogger(t)

	network, genesisBlock := testutil.V2Network()
	cn := testutil.NewConsensusNode(t, network, genesisBlock, log)
	cm := cn.Chain
	db := cn.Store

	wm, err := wallet.NewManager(cm, db, wallet.WithLogger(log.Named("wallet")), wallet.WithIndexMode(wallet.IndexModeFull))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	// mine a bunch of payouts to different addresses
	addresses := make([]types.Address, 100)
	for i := range addresses {
		addresses[i] = types.StandardAddress(types.GeneratePrivateKey().PublicKey())
		cn.MineBlocks(t, addresses[i], 1)
	}
	cn.MineBlocks(t, types.VoidAddress, int(network.MaturityDelay))

	sces, _, err := wm.BatchAddressSiacoinOutputs(addresses, 0, 1000)
	if err != nil {
		t.Fatal(err)
	} else if len(sces) != 100 {
		t.Fatalf("expected 100 events, got %d", len(sces))
	}
}

func TestBatchSiafundOutputs(t *testing.T) {
	log := zaptest.NewLogger(t)

	giftAddr := types.AnyoneCanSpend().Address()
	network, genesisBlock := testutil.V2Network()
	genesisBlock.Transactions[0].SiafundOutputs = []types.SiafundOutput{
		{Address: giftAddr, Value: 10000},
	}
	cn := testutil.NewConsensusNode(t, network, genesisBlock, log)
	cm := cn.Chain
	db := cn.Store

	wm, err := wallet.NewManager(cm, db, wallet.WithLogger(log.Named("wallet")), wallet.WithIndexMode(wallet.IndexModeFull))
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	cn.WaitForSync(t)

	// distribute the siafund output to multiple addresses
	var addresses []types.Address
	outputID := genesisBlock.Transactions[0].SiafundOutputID(0)
	outputValue := genesisBlock.Transactions[0].SiafundOutputs[0].Value
	for i := range 100 {
		txn := types.V2Transaction{
			SiafundInputs: []types.V2SiafundInput{
				{
					Parent: types.SiafundElement{
						ID: outputID,
					},
					SatisfiedPolicy: types.SatisfiedPolicy{
						Policy: types.AnyoneCanSpend(),
					},
				},
			},
		}

		for range 10 {
			address := types.StandardAddress(types.GeneratePrivateKey().PublicKey())
			addresses = append(addresses, address)
			txn.SiafundOutputs = append(txn.SiafundOutputs, types.SiafundOutput{
				Address: address,
				Value:   1,
			})
			outputValue--
			if outputValue == 0 {
				break
			}
		}

		if outputValue > 0 {
			txn.SiafundOutputs = append(txn.SiafundOutputs, types.SiafundOutput{
				Address: giftAddr,
				Value:   outputValue,
			})
		}
		outputID = txn.SiafundOutputID(txn.ID(), len(txn.SiafundOutputs)-1)
		basis, txns, err := db.OverwriteElementProofs([]types.V2Transaction{txn})
		if err != nil {
			t.Fatalf("failed to update element proofs %d: %s", i, err)
		}
		if _, err := cm.AddV2PoolTransactions(basis, txns); err != nil {
			t.Fatalf("failed to add pool transactions %d: %s", i, err)
		}
		cn.MineBlocks(t, types.VoidAddress, 1)
	}

	sfes, _, err := wm.BatchAddressSiafundOutputs(addresses, 0, 10000)
	if err != nil {
		t.Fatal(err)
	} else if len(sfes) != 1000 {
		t.Fatalf("expected 1000 events, got %d", len(sfes))
	}
}

func BenchmarkBatchAddresses(b *testing.B) {
	log := zaptest.NewLogger(b)

	network, genesisBlock := testutil.V2Network()
	cn := testutil.NewConsensusNode(b, network, genesisBlock, log)
	cm := cn.Chain
	db := cn.Store

	wm, err := wallet.NewManager(cm, db, wallet.WithLogger(log.Named("wallet")), wallet.WithIndexMode(wallet.IndexModeFull))
	if err != nil {
		b.Fatal(err)
	}
	defer wm.Close()

	// mine a bunch of payouts to different addresses
	addresses := make([]types.Address, 10000)
	for i := range addresses {
		addresses[i] = types.StandardAddress(types.GeneratePrivateKey().PublicKey())
		cn.MineBlocks(b, addresses[i], 1)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		slice := addresses[frand.Intn(len(addresses)-1000):][:1000]
		events, err := wm.BatchAddressEvents(slice, 0, 100)
		if err != nil {
			b.Fatal(err)
		} else if len(events) != 100 {
			b.Fatalf("expected 100 events, got %d", len(events))
		}
	}
}
