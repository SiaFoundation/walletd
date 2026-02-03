package wallet_test

import (
	"testing"

	proto2 "go.sia.tech/core/rhp/v2"
	proto4 "go.sia.tech/core/rhp/v4"
	"go.sia.tech/core/types"
	ctestutil "go.sia.tech/coreutils/testutil"
	"go.sia.tech/walletd/v2/internal/testutil"
	"go.sia.tech/walletd/v2/wallet"
	"lukechampine.com/frand"
)

func TestDecorateBlock(t *testing.T) {
	testOrigin := func(t *testing.T, tn *testNode, pk types.PrivateKey, uc types.UnlockConditions, expected wallet.SiacoinOrigin) {
		t.Helper()
		cm, db := tn.Chain, tn.Store
		addr := uc.UnlockHash()
		utxos, _, err := tn.manager.AddressSiacoinOutputs(addr, false, 0, 100)
		if err != nil {
			t.Fatal(err)
		} else if len(utxos) != 1 {
			t.Fatal("expected exactly one utxo")
		}

		txn := types.Transaction{
			SiacoinInputs: []types.SiacoinInput{
				{ParentID: utxos[0].ID, UnlockConditions: uc},
			},
			SiacoinOutputs: []types.SiacoinOutput{
				{Address: types.VoidAddress, Value: utxos[0].SiacoinOutput.Value},
			},
			Signatures: []types.TransactionSignature{
				{
					ParentID:      types.Hash256(utxos[0].ID),
					CoveredFields: types.CoveredFields{WholeTransaction: true},
				},
			},
		}
		sigHash := cm.TipState().WholeSigHash(txn, txn.Signatures[0].ParentID, 0, 0, nil)
		sig := pk.SignHash(sigHash)
		txn.Signatures[0].Signature = sig[:]

		if _, err := cm.AddPoolTransactions([]types.Transaction{txn}); err != nil {
			t.Fatal(err)
		}
		ctestutil.MineBlocks(t, cm, types.VoidAddress, 1)
		waitForBlock(t, cm, db)

		// check the last block contains the decorated input
		block, ok := cm.Block(cm.Tip().ID)
		if !ok {
			t.Fatal("could not retrieve block")
		}
		decorated, err := db.DecorateConsensusBlock(block)
		if err != nil {
			t.Fatal(err)
		} else if len(decorated.Transactions) != 1 {
			t.Fatalf("expected 1 transaction, got %d", len(decorated.Transactions))
		} else if len(decorated.Transactions[0].SiacoinInputs) != 1 {
			t.Fatalf("expected 1 siacoin input, got %d", len(decorated.Transactions[0].SiacoinInputs))
		} else if decorated.Transactions[0].SiacoinInputs[0].Origin != expected {
			t.Fatalf("expected origin %v, got %v", expected, decorated.Transactions[0].SiacoinInputs[0].Origin)
		}
	}

	testV2Origin := func(t *testing.T, tn *testNode, pk types.PrivateKey, sp types.SpendPolicy, expected wallet.SiacoinOrigin) {
		t.Helper()

		cm, db := tn.Chain, tn.Store
		addr := sp.Address()
		utxos, tip, err := tn.manager.AddressSiacoinOutputs(addr, false, 0, 100)
		if err != nil {
			t.Fatal(err)
		} else if len(utxos) != 1 {
			t.Fatal("expected exactly one utxo")
		}

		txn := types.V2Transaction{
			SiacoinInputs: []types.V2SiacoinInput{
				{
					Parent: utxos[0].SiacoinElement,
					SatisfiedPolicy: types.SatisfiedPolicy{
						Policy: sp,
					},
				},
			},
			SiacoinOutputs: []types.SiacoinOutput{
				{Address: types.VoidAddress, Value: utxos[0].SiacoinOutput.Value},
			},
		}
		txn.SiacoinInputs[0].SatisfiedPolicy.Signatures = []types.Signature{pk.SignHash(cm.TipState().InputSigHash(txn))}

		if _, err := cm.AddV2PoolTransactions(tip, []types.V2Transaction{txn}); err != nil {
			t.Fatal(err)
		}
		ctestutil.MineBlocks(t, cm, types.VoidAddress, 1)
		waitForBlock(t, cm, db)

		// check the last block contains the decorated input
		block, ok := cm.Block(cm.Tip().ID)
		if !ok {
			t.Fatal("could not retrieve block")
		}

		decorated, err := db.DecorateConsensusBlock(block)
		if err != nil {
			t.Fatal(err)
		} else if len(decorated.V2.Transactions) != 2 {
			// one "coinbase" txn + the test txn
			t.Fatalf("expected 2 transactions, got %d", len(decorated.V2.Transactions))
		} else if len(decorated.V2.Transactions[1].SiacoinInputs) != 1 {
			t.Fatalf("expected 1 siacoin input, got %d", len(decorated.V2.Transactions[0].SiacoinInputs))
		} else if decorated.V2.Transactions[1].SiacoinInputs[0].Origin != expected {
			t.Fatalf("expected origin %v, got %v", expected, decorated.V2.Transactions[1].SiacoinInputs[0].Origin)
		}
	}

	t.Run("transaction", func(t *testing.T) {
		pk := types.GeneratePrivateKey()
		uc := types.StandardUnlockConditions(pk.PublicKey())
		addr := uc.UnlockHash()

		network, genesisBlock := testutil.V1Network()
		genesisBlock.Transactions[0].SiacoinOutputs = []types.SiacoinOutput{
			{Address: types.VoidAddress, Value: types.Siacoins(1)},
			{Address: types.VoidAddress, Value: types.Siacoins(2)},
			{Address: addr, Value: types.Siacoins(100)}, // gift output is index 2
		}
		giftTxnID := genesisBlock.Transactions[0].ID()

		tn := newTestNode(t, network, genesisBlock, wallet.WithIndexMode(wallet.IndexModeFull))
		cm, db := tn.Chain, tn.Store

		ctestutil.MineBlocks(t, cm, types.VoidAddress, 1)
		waitForBlock(t, cm, db)

		testOrigin(t, tn, pk, uc, wallet.SiacoinOrigin{
			Source: wallet.ElementSourceTransaction,
			ID:     types.Hash256(giftTxnID),
			Index:  2,
		})
	})

	t.Run("v2 transaction", func(t *testing.T) {
		pk := types.GeneratePrivateKey()
		sp := types.PolicyPublicKey(pk.PublicKey())
		addr := sp.Address()

		network, genesisBlock := testutil.V2Network()
		genesisBlock.Transactions[0].SiacoinOutputs = []types.SiacoinOutput{
			{Address: types.VoidAddress, Value: types.Siacoins(1)},
			{Address: types.VoidAddress, Value: types.Siacoins(2)},
			{Address: addr, Value: types.Siacoins(100)}, // gift output is index 2
		}
		giftTxnID := genesisBlock.Transactions[0].ID()
		tn := newTestNode(t, network, genesisBlock, wallet.WithIndexMode(wallet.IndexModeFull))
		cm, db := tn.Chain, tn.Store

		ctestutil.MineBlocks(t, cm, types.VoidAddress, 1)
		waitForBlock(t, cm, db)

		testV2Origin(t, tn, pk, sp, wallet.SiacoinOrigin{
			Source: wallet.ElementSourceTransaction,
			ID:     types.Hash256(giftTxnID),
			Index:  2,
		})
	})

	t.Run("miner", func(t *testing.T) {
		// Create a UTXO from a miner payout
		pk := types.GeneratePrivateKey()
		sp := types.PolicyPublicKey(pk.PublicKey())
		addr := sp.Address()

		network, genesisBlock := testutil.V2Network()
		tn := newTestNode(t, network, genesisBlock, wallet.WithIndexMode(wallet.IndexModeFull))
		cm, db := tn.Chain, tn.Store

		ctestutil.MineBlocks(t, cm, addr, 1)
		waitForBlock(t, cm, db)

		minerBlock := cm.Tip()

		// mine until it matures
		ctestutil.MineBlocks(t, cm, types.VoidAddress, int(network.MaturityDelay))
		waitForBlock(t, cm, db)

		testV2Origin(t, tn, pk, sp, wallet.SiacoinOrigin{
			Source: wallet.ElementSourceMiner,
			ID:     types.Hash256(minerBlock.ID),
			Index:  0,
		})
	})

	t.Run("siafund", func(t *testing.T) {
		pk := types.GeneratePrivateKey()
		uc := types.StandardUnlockConditions(pk.PublicKey())
		addr := uc.UnlockHash()

		network, genesisBlock := testutil.V1Network()
		genesisBlock.Transactions[0].SiacoinOutputs = []types.SiacoinOutput{
			{Address: addr, Value: types.Siacoins(1000)},
		}
		genesisBlock.Transactions[0].SiafundOutputs[0].Address = addr
		tn := newTestNode(t, network, genesisBlock, wallet.WithIndexMode(wallet.IndexModeFull))
		cm, db := tn.Chain, tn.Store

		ctestutil.MineBlocks(t, cm, types.VoidAddress, 1)
		waitForBlock(t, cm, db)

		var sector [proto4.SectorSize]byte
		frand.Read(sector[:])
		roots := []types.Hash256{proto4.SectorRoot(&sector)}

		payout := types.Siacoins(500)
		fc := types.FileContract{
			UnlockHash:     addr,
			Filesize:       proto4.SectorSize,
			FileMerkleRoot: proto2.MetaRoot(roots),
			Payout:         taxAdjustedPayout(payout),
			WindowStart:    cm.Tip().Height + 10,
			WindowEnd:      cm.Tip().Height + 20,
			ValidProofOutputs: []types.SiacoinOutput{
				{Address: types.VoidAddress, Value: payout},
			},
			MissedProofOutputs: []types.SiacoinOutput{
				{Address: types.VoidAddress, Value: payout},
			},
		}

		fcTxn := types.Transaction{
			FileContracts: []types.FileContract{fc},
		}

		utxos, _, err := tn.manager.AddressSiacoinOutputs(addr, false, 0, 100)
		if err != nil {
			t.Fatal(err)
		} else if len(utxos) == 0 {
			t.Fatal("expected at least one utxo")
		}

		fcTxn.SiacoinInputs = []types.SiacoinInput{
			{ParentID: utxos[0].ID, UnlockConditions: uc},
		}
		change := utxos[0].SiacoinOutput.Value.Sub(fc.Payout)
		fcTxn.SiacoinOutputs = []types.SiacoinOutput{
			{Address: types.VoidAddress, Value: change}, // burn the rest for easy testing
		}
		fcTxn.Signatures = []types.TransactionSignature{
			{
				ParentID:      types.Hash256(utxos[0].ID),
				CoveredFields: types.CoveredFields{WholeTransaction: true},
			},
		}

		sigHash := cm.TipState().WholeSigHash(fcTxn, fcTxn.Signatures[0].ParentID, 0, 0, nil)
		sig := pk.SignHash(sigHash)
		fcTxn.Signatures[0].Signature = sig[:]

		// confirm the contract
		if _, err := cm.AddPoolTransactions([]types.Transaction{fcTxn}); err != nil {
			t.Fatal(err)
		}
		ctestutil.MineBlocks(t, cm, types.VoidAddress, 1)
		waitForBlock(t, cm, db)

		// claim the siafund tax revenue
		sfUtxos, _, err := tn.manager.AddressSiafundOutputs(addr, false, 0, 100)
		if err != nil {
			t.Fatal(err)
		} else if len(sfUtxos) == 0 {
			t.Fatal("expected at least one siafund utxo")
		}

		claimTxn := types.Transaction{
			SiafundInputs: []types.SiafundInput{
				{
					ParentID:         sfUtxos[0].ID,
					UnlockConditions: uc,
					ClaimAddress:     addr,
				},
			},
			SiafundOutputs: []types.SiafundOutput{
				{Address: addr, Value: sfUtxos[0].SiafundOutput.Value},
			},
			Signatures: []types.TransactionSignature{
				{
					ParentID:      types.Hash256(sfUtxos[0].ID),
					CoveredFields: types.CoveredFields{WholeTransaction: true},
				},
			},
		}
		sigHash = cm.TipState().WholeSigHash(claimTxn, claimTxn.Signatures[0].ParentID, 0, 0, nil)
		sig = pk.SignHash(sigHash)
		claimTxn.Signatures[0].Signature = sig[:]

		if _, err := cm.AddPoolTransactions([]types.Transaction{claimTxn}); err != nil {
			t.Fatal(err)
		}
		ctestutil.MineBlocks(t, cm, types.VoidAddress, int(network.MaturityDelay+1))
		waitForBlock(t, cm, db)

		testOrigin(t, tn, pk, uc, wallet.SiacoinOrigin{
			Source: wallet.ElementSourceSiafund,
			ID:     types.Hash256(claimTxn.ID()),
			Index:  0,
		})
	})

	t.Run("v2 siafund", func(t *testing.T) {
		pk := types.GeneratePrivateKey()
		sp := types.PolicyPublicKey(pk.PublicKey())
		addr := sp.Address()

		network, genesis := ctestutil.V2Network()
		genesis.Transactions[0].SiacoinOutputs = []types.SiacoinOutput{
			{Address: addr, Value: types.Siacoins(1000)},
		}
		genesis.Transactions[0].SiafundOutputs[0].Address = addr
		tn := newTestNode(t, network, genesis, wallet.WithIndexMode(wallet.IndexModeFull))
		cm, db := tn.Chain, tn.Store

		ctestutil.MineBlocks(t, cm, types.VoidAddress, 1)
		waitForBlock(t, cm, db)

		renterKey, hostKey := types.GeneratePrivateKey(), types.GeneratePrivateKey()

		cs := cm.TipState()

		// generate tax revenue by creating and funding a file contract
		fc := types.V2FileContract{
			HostOutput: types.SiacoinOutput{
				Address: types.VoidAddress,
				Value:   types.Siacoins(250),
			},
			RenterOutput: types.SiacoinOutput{
				Address: types.VoidAddress,
				Value:   types.Siacoins(250),
			},
			RenterPublicKey:  renterKey.PublicKey(),
			HostPublicKey:    hostKey.PublicKey(),
			ProofHeight:      cs.Index.Height + 10,
			ExpirationHeight: cs.Index.Height + 20,
		}
		fc.RenterSignature = renterKey.SignHash(cs.ContractSigHash(fc))
		fc.HostSignature = hostKey.SignHash(cs.ContractSigHash(fc))

		utxos, basis, err := tn.manager.AddressSiacoinOutputs(addr, false, 0, 100)
		if err != nil {
			t.Fatal(err)
		} else if len(utxos) == 0 {
			t.Fatal("expected at least one utxo")
		}

		fundAmount := types.Siacoins(500).Add(cs.V2FileContractTax(fc))
		fcTxn := types.V2Transaction{
			SiacoinInputs: []types.V2SiacoinInput{
				{
					Parent: utxos[0].SiacoinElement,
					SatisfiedPolicy: types.SatisfiedPolicy{
						Policy: sp,
					},
				},
			},
			SiacoinOutputs: []types.SiacoinOutput{
				{Address: types.VoidAddress, Value: utxos[0].SiacoinOutput.Value.Sub(fundAmount)}, // burn the rest for easy testing
			},
			FileContracts: []types.V2FileContract{fc},
		}
		fcTxn.SiacoinInputs[0].SatisfiedPolicy.Signatures = []types.Signature{pk.SignHash(cm.TipState().InputSigHash(fcTxn))}

		if _, err := cm.AddV2PoolTransactions(basis, []types.V2Transaction{fcTxn}); err != nil {
			t.Fatal(err)
		}
		ctestutil.MineBlocks(t, cm, types.VoidAddress, 1)
		waitForBlock(t, cm, db)

		// claim the siafunds
		sfUtxos, basis, err := tn.manager.AddressSiafundOutputs(addr, false, 0, 100)
		if err != nil {
			t.Fatal(err)
		} else if len(sfUtxos) == 0 {
			t.Fatal("expected at least one siafund utxo")
		}
		sfClaimTxn := types.V2Transaction{
			SiafundInputs: []types.V2SiafundInput{
				{
					Parent: sfUtxos[0].SiafundElement,
					SatisfiedPolicy: types.SatisfiedPolicy{
						Policy: sp,
					},
					ClaimAddress: addr,
				},
			},
			SiafundOutputs: []types.SiafundOutput{
				{Address: addr, Value: sfUtxos[0].SiafundOutput.Value},
			},
		}
		sfClaimTxn.SiafundInputs[0].SatisfiedPolicy.Signatures = []types.Signature{pk.SignHash(cm.TipState().InputSigHash(sfClaimTxn))}

		// mine until the claim matures
		if _, err := cm.AddV2PoolTransactions(basis, []types.V2Transaction{sfClaimTxn}); err != nil {
			t.Fatal(err)
		}
		ctestutil.MineBlocks(t, cm, types.VoidAddress, int(network.MaturityDelay)+1)
		waitForBlock(t, cm, db)

		testV2Origin(t, tn, pk, sp, wallet.SiacoinOrigin{
			Source: wallet.ElementSourceSiafund,
			ID:     types.Hash256(sfClaimTxn.ID()),
			Index:  0,
		})
	})

	t.Run("valid contract", func(t *testing.T) {
		pk := types.GeneratePrivateKey()
		uc := types.StandardUnlockConditions(pk.PublicKey())
		addr := uc.UnlockHash()

		network, genesisBlock := testutil.V1Network()
		genesisBlock.Transactions[0].SiacoinOutputs = []types.SiacoinOutput{
			{Address: types.VoidAddress, Value: types.Siacoins(1)},
			{Address: types.VoidAddress, Value: types.Siacoins(2)},
			{Address: addr, Value: types.Siacoins(150)}, // gift output is index 2
		}
		tn := newTestNode(t, network, genesisBlock, wallet.WithIndexMode(wallet.IndexModeFull))
		cm, db := tn.Chain, tn.Store

		ctestutil.MineBlocks(t, cm, types.VoidAddress, 1)
		waitForBlock(t, cm, db)

		var sector [proto4.SectorSize]byte
		frand.Read(sector[:])
		roots := []types.Hash256{proto4.SectorRoot(&sector)}

		cs := cm.TipState()
		fc := types.FileContract{
			UnlockHash:     addr,
			Filesize:       proto4.SectorSize,
			FileMerkleRoot: proto2.MetaRoot(roots),
			Payout:         taxAdjustedPayout(types.Siacoins(3)),
			WindowStart:    cm.Tip().Height + 10,
			WindowEnd:      cm.Tip().Height + 20,
			ValidProofOutputs: []types.SiacoinOutput{
				{Address: types.VoidAddress, Value: types.Siacoins(1)},
				{Address: addr, Value: types.Siacoins(2)}, // origin index 1
			},
			MissedProofOutputs: []types.SiacoinOutput{
				{Address: types.VoidAddress, Value: types.Siacoins(3)},
			},
		}

		fcTxn := types.Transaction{
			FileContracts: []types.FileContract{fc},
		}

		utxos, _, err := tn.manager.AddressSiacoinOutputs(addr, false, 0, 100)
		if err != nil {
			t.Fatal(err)
		} else if len(utxos) == 0 {
			t.Fatal("expected at least one utxo")
		}

		fcTxn.SiacoinInputs = []types.SiacoinInput{
			{ParentID: utxos[0].ID, UnlockConditions: uc},
		}
		change := utxos[0].SiacoinOutput.Value.Sub(fc.Payout)
		fcTxn.SiacoinOutputs = []types.SiacoinOutput{
			{Address: types.VoidAddress, Value: change}, // burn the rest for easy testing
		}
		fcTxn.Signatures = []types.TransactionSignature{
			{
				ParentID:      types.Hash256(utxos[0].ID),
				CoveredFields: types.CoveredFields{WholeTransaction: true},
			},
		}

		sigHash := cm.TipState().WholeSigHash(fcTxn, fcTxn.Signatures[0].ParentID, 0, 0, nil)
		sig := pk.SignHash(sigHash)
		fcTxn.Signatures[0].Signature = sig[:]

		if _, err := cm.AddPoolTransactions([]types.Transaction{fcTxn}); err != nil {
			t.Fatal(err)
		}
		ctestutil.MineBlocks(t, cm, types.VoidAddress, 1)
		waitForBlock(t, cm, db)

		fcID := fcTxn.FileContractID(0)

		// mine until the proof window
		ctestutil.MineBlocks(t, cm, types.VoidAddress, int(fc.WindowStart-cm.Tip().Height-1))
		waitForBlock(t, cm, db)

		// submit a valid proof
		index := cs.StorageProofLeafIndex(fc.Filesize, cm.Tip().ID, fcID)
		sectorIndex := index / proto4.LeavesPerSector
		leafIndex := index % proto4.LeavesPerSector
		leafProof := proto2.ConvertProofOrdering(proto2.BuildProof(&sector, leafIndex, leafIndex+1, nil), leafIndex)
		sectorProof := proto2.ConvertProofOrdering(proto2.BuildSectorRangeProof(roots, sectorIndex, sectorIndex+1), sectorIndex)
		proofTxn := types.Transaction{
			StorageProofs: []types.StorageProof{{
				ParentID: fcID,
				Leaf:     [64]byte(sector[leafIndex*proto4.LeafSize:][:proto4.LeafSize]),
				Proof:    append(leafProof, sectorProof...),
			}},
		}
		if _, err := cm.AddPoolTransactions([]types.Transaction{proofTxn}); err != nil {
			t.Fatal(err)
		}
		ctestutil.MineBlocks(t, cm, types.VoidAddress, 1)
		waitForBlock(t, cm, db)

		// mine until the payout matures
		ctestutil.MineBlocks(t, cm, types.VoidAddress, int(network.MaturityDelay))
		waitForBlock(t, cm, db)

		testOrigin(t, tn, pk, uc, wallet.SiacoinOrigin{
			Source: wallet.ElementSourceContract,
			ID:     types.Hash256(fcID),
			Index:  1,
		})
	})

	t.Run("missed contract", func(t *testing.T) {
		pk := types.GeneratePrivateKey()
		uc := types.StandardUnlockConditions(pk.PublicKey())
		addr := uc.UnlockHash()

		network, genesisBlock := testutil.V1Network()
		genesisBlock.Transactions[0].SiacoinOutputs = []types.SiacoinOutput{
			{Address: types.VoidAddress, Value: types.Siacoins(1)},
			{Address: types.VoidAddress, Value: types.Siacoins(2)},
			{Address: addr, Value: types.Siacoins(150)}, // gift output is index 2
		}
		tn := newTestNode(t, network, genesisBlock, wallet.WithIndexMode(wallet.IndexModeFull))
		cm, db := tn.Chain, tn.Store

		ctestutil.MineBlocks(t, cm, types.VoidAddress, 1)
		waitForBlock(t, cm, db)

		var sector [proto4.SectorSize]byte
		frand.Read(sector[:])
		roots := []types.Hash256{proto4.SectorRoot(&sector)}

		fc := types.FileContract{
			UnlockHash:     addr,
			Filesize:       proto4.SectorSize,
			FileMerkleRoot: proto2.MetaRoot(roots),
			Payout:         taxAdjustedPayout(types.Siacoins(3)),
			WindowStart:    cm.Tip().Height + 10,
			WindowEnd:      cm.Tip().Height + 20,
			ValidProofOutputs: []types.SiacoinOutput{
				{Address: types.VoidAddress, Value: types.Siacoins(3)},
			},
			MissedProofOutputs: []types.SiacoinOutput{
				{Address: types.VoidAddress, Value: types.Siacoins(1)},
				{Address: addr, Value: types.Siacoins(2)}, // origin index 1
			},
		}

		fcTxn := types.Transaction{
			FileContracts: []types.FileContract{fc},
		}

		utxos, _, err := tn.manager.AddressSiacoinOutputs(addr, false, 0, 100)
		if err != nil {
			t.Fatal(err)
		} else if len(utxos) == 0 {
			t.Fatal("expected at least one utxo")
		}

		fcTxn.SiacoinInputs = []types.SiacoinInput{
			{ParentID: utxos[0].ID, UnlockConditions: uc},
		}
		change := utxos[0].SiacoinOutput.Value.Sub(fc.Payout)
		fcTxn.SiacoinOutputs = []types.SiacoinOutput{
			{Address: types.VoidAddress, Value: change}, // burn the rest for easy testing
		}
		fcTxn.Signatures = []types.TransactionSignature{
			{
				ParentID:      types.Hash256(utxos[0].ID),
				CoveredFields: types.CoveredFields{WholeTransaction: true},
			},
		}

		sigHash := cm.TipState().WholeSigHash(fcTxn, fcTxn.Signatures[0].ParentID, 0, 0, nil)
		sig := pk.SignHash(sigHash)
		fcTxn.Signatures[0].Signature = sig[:]

		if _, err := cm.AddPoolTransactions([]types.Transaction{fcTxn}); err != nil {
			t.Fatal(err)
		}
		ctestutil.MineBlocks(t, cm, types.VoidAddress, 1)
		waitForBlock(t, cm, db)

		fcID := fcTxn.FileContractID(0)

		// mine until contract expires and output matures
		ctestutil.MineBlocks(t, cm, types.VoidAddress, int(fc.WindowEnd-cm.Tip().Height+network.MaturityDelay+1))
		waitForBlock(t, cm, db)

		testOrigin(t, tn, pk, uc, wallet.SiacoinOrigin{
			Source: wallet.ElementSourceContract,
			ID:     types.Hash256(fcID),
			Index:  1,
		})
	})

	t.Run("v2 contract", func(t *testing.T) {
		pk := types.GeneratePrivateKey()
		sp := types.PolicyPublicKey(pk.PublicKey())
		addr := sp.Address()

		network, genesis := ctestutil.V2Network()
		genesis.Transactions[0].SiacoinOutputs = []types.SiacoinOutput{
			{Address: addr, Value: types.Siacoins(1000)},
		}
		genesis.Transactions[0].SiafundOutputs[0].Address = addr
		tn := newTestNode(t, network, genesis, wallet.WithIndexMode(wallet.IndexModeFull))
		cm, db := tn.Chain, tn.Store

		ctestutil.MineBlocks(t, cm, types.VoidAddress, 1)
		waitForBlock(t, cm, db)

		renterKey, hostKey := types.GeneratePrivateKey(), types.GeneratePrivateKey()

		cs := cm.TipState()

		// generate tax revenue by creating and funding a file contract
		fc := types.V2FileContract{
			HostOutput: types.SiacoinOutput{
				Address: types.VoidAddress,
				Value:   types.Siacoins(250),
			},
			RenterOutput: types.SiacoinOutput{
				Address: addr, // renter output is created regardless
				Value:   types.Siacoins(250),
			},
			RenterPublicKey:  renterKey.PublicKey(),
			HostPublicKey:    hostKey.PublicKey(),
			ProofHeight:      cs.Index.Height + 10,
			ExpirationHeight: cs.Index.Height + 20,
		}
		fc.RenterSignature = renterKey.SignHash(cs.ContractSigHash(fc))
		fc.HostSignature = hostKey.SignHash(cs.ContractSigHash(fc))

		utxos, basis, err := tn.manager.AddressSiacoinOutputs(addr, false, 0, 100)
		if err != nil {
			t.Fatal(err)
		} else if len(utxos) == 0 {
			t.Fatal("expected at least one utxo")
		}

		fundAmount := types.Siacoins(500).Add(cs.V2FileContractTax(fc))
		fcTxn := types.V2Transaction{
			SiacoinInputs: []types.V2SiacoinInput{
				{
					Parent: utxos[0].SiacoinElement,
					SatisfiedPolicy: types.SatisfiedPolicy{
						Policy: sp,
					},
				},
			},
			SiacoinOutputs: []types.SiacoinOutput{
				{Address: types.VoidAddress, Value: utxos[0].SiacoinOutput.Value.Sub(fundAmount)}, // burn the rest for easy testing
			},
			FileContracts: []types.V2FileContract{fc},
		}
		fcTxn.SiacoinInputs[0].SatisfiedPolicy.Signatures = []types.Signature{pk.SignHash(cm.TipState().InputSigHash(fcTxn))}

		if _, err := cm.AddV2PoolTransactions(basis, []types.V2Transaction{fcTxn}); err != nil {
			t.Fatal(err)
		}
		// mine until the contract expires
		ctestutil.MineBlocks(t, cm, types.VoidAddress, int(fc.ExpirationHeight-cm.Tip().Height+1))
		waitForBlock(t, cm, db)

		// keep track of the file contract element
		var fce *types.V2FileContractElement
		_, applied, err := cm.UpdatesSince(types.ChainIndex{}, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, cau := range applied {
			for _, diff := range cau.V2FileContractElementDiffs() {
				if diff.Created {
					fce = &diff.V2FileContractElement
				}
			}
			if fce != nil {
				cau.UpdateElementProof(&fce.StateElement)
			}
		}
		if fce == nil {
			t.Fatal("could not find file contract element")
		}

		// resolve the contract to get the payout utxo
		resolveTxn := types.V2Transaction{
			FileContractResolutions: []types.V2FileContractResolution{
				{
					Parent:     *fce,
					Resolution: &types.V2FileContractExpiration{},
				},
			},
		}
		if _, err := cm.AddV2PoolTransactions(cm.Tip(), []types.V2Transaction{resolveTxn}); err != nil {
			t.Fatal(err)
		}
		// mine until the payout matures
		ctestutil.MineBlocks(t, cm, types.VoidAddress, int(network.MaturityDelay)+1)
		waitForBlock(t, cm, db)

		testV2Origin(t, tn, pk, sp, wallet.SiacoinOrigin{
			Source: wallet.ElementSourceContract,
			ID:     types.Hash256(fce.ID),
			Index:  1,
		})
	})

	t.Run("foundation", func(t *testing.T) {
		pk := types.GeneratePrivateKey()
		sp := types.PolicyPublicKey(pk.PublicKey())
		addr := sp.Address()

		network, genesisBlock := testutil.V2Network()
		network.HardforkFoundation.PrimaryAddress = addr
		tn := newTestNode(t, network, genesisBlock, wallet.WithIndexMode(wallet.IndexModeFull))
		cm, db := tn.Chain, tn.Store

		// mine until the first subsidy
		ctestutil.MineBlocks(t, cm, types.VoidAddress, int(network.HardforkFoundation.Height))
		waitForBlock(t, cm, db)

		foundatonSubsidyID := cm.Tip().ID

		// mine until the first foundation subsidy matures
		ctestutil.MineBlocks(t, cm, types.VoidAddress, int(network.MaturityDelay)+1)
		waitForBlock(t, cm, db)

		testV2Origin(t, tn, pk, sp, wallet.SiacoinOrigin{
			Source: wallet.ElementSourceFoundationSubsidy,
			ID:     types.Hash256(foundatonSubsidyID),
			Index:  0,
		})
	})
}
