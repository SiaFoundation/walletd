package wallet_test

import (
	"testing"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/v2/internal/testutil"
	"go.sia.tech/walletd/v2/wallet"
	"go.uber.org/zap/zaptest"
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
