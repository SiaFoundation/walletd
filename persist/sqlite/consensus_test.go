package sqlite

import (
	"path/filepath"
	"strings"
	"testing"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/testutil"
	"go.sia.tech/walletd/v2/wallet"
)

func mineBlock(state consensus.State, txns []types.Transaction, minerAddr types.Address) types.Block {
	b := types.Block{
		ParentID:     state.Index.ID,
		Timestamp:    types.CurrentTimestamp(),
		Transactions: txns,
		MinerPayouts: []types.SiacoinOutput{{Address: minerAddr, Value: state.BlockReward()}},
	}
	for b.ID().CmpWork(state.PoWTarget()) < 0 {
		b.Nonce += state.NonceFactor()
	}
	return b
}

func syncDB(tb testing.TB, store *Store, cm *chain.Manager) {
	index, err := store.LastCommittedIndex()
	if err != nil {
		tb.Fatalf("failed to get last committed index: %v", err)
	}
	for index != cm.Tip() {
		crus, caus, err := cm.UpdatesSince(index, 1000)
		if err != nil {
			tb.Fatalf("failed to subscribe to chain manager: %v", err)
		} else if err := store.UpdateChainState(crus, caus); err != nil {
			tb.Fatalf("failed to update chain state: %v", err)
		}

		switch {
		case len(caus) > 0:
			index = caus[len(caus)-1].State.Index
		case len(crus) > 0:
			index = crus[len(crus)-1].State.Index
		}
	}
}

func TestPruneSiacoins(t *testing.T) {
	db := newTestStore(t, WithRetainSpentElements(20))

	bdb, err := coreutils.OpenBoltChainDB(filepath.Join(t.TempDir(), "consensus.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()

	// mine a single payout to the wallet
	pk := types.GeneratePrivateKey()
	addr := types.StandardUnlockHash(pk.PublicKey())

	network, genesisBlock := testutil.Network()
	store, genesisState, err := chain.NewDBStore(bdb, network, genesisBlock, nil)
	if err != nil {
		t.Fatal(err)
	}

	cm := chain.NewManager(store, genesisState)

	// create a wallet
	w, err := db.AddWallet(wallet.Wallet{Name: "test"})
	if err != nil {
		t.Fatal(err)
	} else if err := db.AddWalletAddresses(w.ID, wallet.Address{Address: addr}); err != nil {
		t.Fatal(err)
	}

	// mine a block to the wallet
	expectedPayout := cm.TipState().BlockReward()
	maturityHeight := cm.TipState().MaturityHeight()
	if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, addr)}); err != nil {
		t.Fatal(err)
	}
	syncDB(t, db, cm)

	assertBalance := func(siacoin, immature types.Currency) {
		t.Helper()

		b, err := db.WalletBalance(w.ID)
		if err != nil {
			t.Fatalf("failed to get wallet balance: %v", err)
		} else if !b.ImmatureSiacoins.Equals(immature) {
			t.Fatalf("expected immature siacoin balance %v, got %v", immature, b.ImmatureSiacoins)
		} else if !b.Siacoins.Equals(siacoin) {
			t.Fatalf("expected siacoin balance %v, got %v", siacoin, b.Siacoins)
		}
	}

	assertUTXOs := func(spent int, unspent int) {
		t.Helper()

		var n int
		err := db.db.QueryRow(`SELECT COUNT(*) FROM siacoin_elements WHERE spent_index_id IS NOT NULL`).Scan(&n)
		if err != nil {
			t.Fatalf("failed to count spent siacoin elements: %v", err)
		} else if n != spent {
			t.Fatalf("expected %v spent siacoin elements, got %v", spent, n)
		}

		err = db.db.QueryRow(`SELECT COUNT(*) FROM siacoin_elements WHERE spent_index_id IS NULL`).Scan(&n)
		if err != nil {
			t.Fatalf("failed to count unspent siacoin elements: %v", err)
		} else if n != unspent {
			t.Fatalf("expected %v unspent siacoin elements, got %v", unspent, n)
		}
	}

	assertBalance(types.ZeroCurrency, expectedPayout)
	assertUTXOs(0, 1)

	// mine until the payout matures
	for range maturityHeight {
		if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, types.VoidAddress)}); err != nil {
			t.Fatal(err)
		}
	}
	syncDB(t, db, cm)
	assertBalance(expectedPayout, types.ZeroCurrency)
	assertUTXOs(0, 1)

	// spend the utxo
	utxos, _, err := db.WalletSiacoinOutputs(w.ID, 0, 100)
	if err != nil {
		t.Fatalf("failed to get wallet siacoin outputs: %v", err)
	}

	txn := types.Transaction{
		SiacoinInputs: []types.SiacoinInput{{
			ParentID:         types.SiacoinOutputID(utxos[0].ID),
			UnlockConditions: types.StandardUnlockConditions(pk.PublicKey()),
		}},
		SiacoinOutputs: []types.SiacoinOutput{
			{Value: utxos[0].SiacoinOutput.Value, Address: types.VoidAddress},
		},
	}

	sigHash := cm.TipState().WholeSigHash(txn, types.Hash256(utxos[0].ID), 0, 0, nil)
	sig := pk.SignHash(sigHash)
	txn.Signatures = append(txn.Signatures, types.TransactionSignature{
		ParentID:       types.Hash256(utxos[0].ID),
		CoveredFields:  types.CoveredFields{WholeTransaction: true},
		PublicKeyIndex: 0,
		Timelock:       0,
		Signature:      sig[:],
	})

	// mine a block with the transaction
	if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), []types.Transaction{txn}, types.VoidAddress)}); err != nil {
		t.Fatal(err)
	}
	syncDB(t, db, cm)

	// the utxo should now have 0 balance and 1 spent element
	assertBalance(types.ZeroCurrency, types.ZeroCurrency)
	assertUTXOs(1, 0)

	// mine until the element is pruned
	for i := uint64(0); i < db.spentElementRetentionBlocks-1; i++ {
		if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, types.VoidAddress)}); err != nil {
			t.Fatal(err)
		}
		syncDB(t, db, cm)
		assertUTXOs(1, 0) // check that the element is not pruned early
	}

	// trigger the pruning
	if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, types.VoidAddress)}); err != nil {
		t.Fatal(err)
	}
	syncDB(t, db, cm)
	assertUTXOs(0, 0)
}

func TestPruneSiafunds(t *testing.T) {
	db := newTestStore(t)

	bdb, err := coreutils.OpenBoltChainDB(filepath.Join(t.TempDir(), "consensus.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()

	// mine a single payout to the wallet
	pk := types.GeneratePrivateKey()
	addr := types.StandardUnlockHash(pk.PublicKey())

	network, genesisBlock := testutil.Network()
	// send the siafund airdrop to the wallet
	genesisBlock.Transactions[0].SiafundOutputs[0].Address = addr
	store, genesisState, err := chain.NewDBStore(bdb, network, genesisBlock, nil)
	if err != nil {
		t.Fatal(err)
	}

	cm := chain.NewManager(store, genesisState)

	// create a wallet
	w, err := db.AddWallet(wallet.Wallet{Name: "test"})
	if err != nil {
		t.Fatal(err)
	} else if err := db.AddWalletAddresses(w.ID, wallet.Address{Address: addr}); err != nil {
		t.Fatal(err)
	}

	syncDB(t, db, cm)

	assertBalance := func(siafunds uint64) {
		t.Helper()

		b, err := db.WalletBalance(w.ID)
		if err != nil {
			t.Fatalf("failed to get wallet balance: %v", err)
		} else if b.Siafunds != siafunds {
			t.Fatalf("expected siafund balance %v, got %v", siafunds, b.ImmatureSiacoins)
		}
	}

	assertUTXOs := func(spent int, unspent int) {
		t.Helper()

		var n int
		err := db.db.QueryRow(`SELECT COUNT(*) FROM siafund_elements WHERE spent_index_id IS NOT NULL`).Scan(&n)
		if err != nil {
			t.Fatalf("failed to count spent siacoin elements: %v", err)
		} else if n != spent {
			t.Fatalf("expected %v spent siacoin elements, got %v", spent, n)
		}

		err = db.db.QueryRow(`SELECT COUNT(*) FROM siafund_elements WHERE spent_index_id IS NULL`).Scan(&n)
		if err != nil {
			t.Fatalf("failed to count unspent siacoin elements: %v", err)
		} else if n != unspent {
			t.Fatalf("expected %v unspent siacoin elements, got %v", unspent, n)
		}
	}

	assertBalance(cm.TipState().SiafundCount())
	assertUTXOs(0, 1)

	// spend the utxo
	utxos, _, err := db.WalletSiafundOutputs(w.ID, 0, 100)
	if err != nil {
		t.Fatalf("failed to get wallet siacoin outputs: %v", err)
	}

	txn := types.Transaction{
		SiafundInputs: []types.SiafundInput{{
			ParentID:         types.SiafundOutputID(utxos[0].ID),
			UnlockConditions: types.StandardUnlockConditions(pk.PublicKey()),
		}},
		SiafundOutputs: []types.SiafundOutput{
			{Value: utxos[0].SiafundOutput.Value, Address: types.VoidAddress},
		},
	}

	sigHash := cm.TipState().WholeSigHash(txn, types.Hash256(utxos[0].ID), 0, 0, nil)
	sig := pk.SignHash(sigHash)
	txn.Signatures = append(txn.Signatures, types.TransactionSignature{
		ParentID:       types.Hash256(utxos[0].ID),
		CoveredFields:  types.CoveredFields{WholeTransaction: true},
		PublicKeyIndex: 0,
		Timelock:       0,
		Signature:      sig[:],
	})

	// mine a block with the transaction
	if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), []types.Transaction{txn}, types.VoidAddress)}); err != nil {
		t.Fatal(err)
	}
	syncDB(t, db, cm)

	// the utxo should now have 0 balance and 1 spent element
	assertBalance(0)
	assertUTXOs(1, 0)

	// mine until the element is pruned
	for i := uint64(0); i < db.spentElementRetentionBlocks-1; i++ {
		if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, types.VoidAddress)}); err != nil {
			t.Fatal(err)
		}
		syncDB(t, db, cm) // check that the element is not pruned early
		assertUTXOs(1, 0)
	}

	// the spent element should now be pruned
	if err := cm.AddBlocks([]types.Block{mineBlock(cm.TipState(), nil, types.VoidAddress)}); err != nil {
		t.Fatal(err)
	}
	syncDB(t, db, cm)
	assertUTXOs(0, 0)
}

func TestDecorateConsensusBlock(t *testing.T) {
	db := newTestStore(t)
	addr := types.VoidAddress

	t.Run("NullOriginFields", func(t *testing.T) {
		// Manually insert a siacoin element without setting origin fields
		// This simulates an older database or an element that doesn't have origin tracking
		outputID := types.SiacoinOutputID{1, 2, 3}
		value := types.Siacoins(100)

		err := db.transaction(func(tx *txn) error {
			// First insert a chain index
			var indexID int64
			err := tx.QueryRow(`INSERT INTO chain_indices (block_id, height) VALUES ($1, $2) RETURNING id`,
				encode(types.BlockID{}), 0).Scan(&indexID)
			if err != nil {
				return err
			}

			// Insert an address
			var addressID int64
			err = tx.QueryRow(`INSERT INTO sia_addresses (sia_address, siacoin_balance, immature_siacoin_balance, siafund_balance)
				VALUES ($1, $2, $3, $4) RETURNING id`,
				encode(addr), encode(types.ZeroCurrency), encode(types.ZeroCurrency), 0).Scan(&addressID)
			if err != nil {
				return err
			}

			// Insert a siacoin element with NULL origin_transaction_id and origin_transaction_index.
			// Hacky to use queries directly, but tests backwards compatibility with existing databases.
			_, err = tx.Exec(`INSERT INTO siacoin_elements
				(id, siacoin_value, merkle_proof, leaf_index, maturity_height, address_id, matured, chain_index_id, origin_source, origin_transaction_id, origin_transaction_index)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULL, NULL)`,
				encode(outputID), encode(value), encode([]types.Hash256{}), 0, 0, addressID, true, indexID, "miner_payout")
			return err
		})
		if err != nil {
			t.Fatal(err)
		}

		// Create a block with a transaction that spends the element
		pk := types.GeneratePrivateKey()
		block := types.Block{
			Transactions: []types.Transaction{{
				SiacoinInputs: []types.SiacoinInput{{
					ParentID:         outputID,
					UnlockConditions: types.StandardUnlockConditions(pk.PublicKey()),
				}},
			}},
		}

		// Call DecorateConsensusBlock - this should not error even though origin fields are NULL
		decorated, err := db.DecorateConsensusBlock(block)
		if err != nil {
			t.Fatalf("DecorateConsensusBlock failed with NULL origin fields: %v", err)
		}

		// Verify the transaction was decorated
		if len(decorated.Transactions) != 1 {
			t.Fatalf("expected 1 transaction, got %d", len(decorated.Transactions))
		}

		// Verify the siacoin input was decorated
		if len(decorated.Transactions[0].SiacoinInputs) != 1 {
			t.Fatalf("expected 1 siacoin input, got %d", len(decorated.Transactions[0].SiacoinInputs))
		}

		// Verify the origin is empty (zero value) when origin fields are NULL
		origin := decorated.Transactions[0].SiacoinInputs[0].Origin
		if origin.Source != "" || origin.ID != (types.Hash256{}) || origin.Index != 0 {
			t.Errorf("expected empty origin, got Source=%q, ID=%v, Index=%d", origin.Source, origin.ID, origin.Index)
		}
	})

	t.Run("CompleteOriginFields", func(t *testing.T) {
		// Test that elements with complete origin fields decorate correctly
		outputID := types.SiacoinOutputID{4, 5, 6}
		value := types.Siacoins(200)
		originTxnID := types.TransactionID{7, 8, 9}
		originIndex := uint64(2)

		err := db.transaction(func(tx *txn) error {
			// Insert a chain index for the second element
			var indexID int64
			err := tx.QueryRow(`INSERT INTO chain_indices (block_id, height) VALUES ($1, $2) RETURNING id`,
				encode(types.BlockID{1}), 1).Scan(&indexID)
			if err != nil {
				return err
			}

			// Reuse the same address
			var addressID int64
			err = tx.QueryRow(`SELECT id FROM sia_addresses WHERE sia_address=$1`, encode(addr)).Scan(&addressID)
			if err != nil {
				return err
			}

			// Insert a siacoin element with ALL origin fields properly set
			// This simulates what UpdateChainState would do for a transaction output
			_, err = tx.Exec(`INSERT INTO siacoin_elements
				(id, siacoin_value, merkle_proof, leaf_index, maturity_height, address_id, matured, chain_index_id, origin_source, origin_transaction_id, origin_transaction_index)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
				encode(outputID), encode(value), encode([]types.Hash256{}), 1, 0, addressID, true, indexID, "transaction", encode(originTxnID), originIndex)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}

		// Create a block that spends the element with complete origin
		pk := types.GeneratePrivateKey()
		block := types.Block{
			Transactions: []types.Transaction{{
				SiacoinInputs: []types.SiacoinInput{{
					ParentID:         outputID,
					UnlockConditions: types.StandardUnlockConditions(pk.PublicKey()),
				}},
			}},
		}

		// Decorate the block - should properly retrieve origin information
		decorated, err := db.DecorateConsensusBlock(block)
		if err != nil {
			t.Fatalf("DecorateConsensusBlock failed with complete origin fields: %v", err)
		}

		// Verify the origin is properly populated
		if len(decorated.Transactions) != 1 || len(decorated.Transactions[0].SiacoinInputs) != 1 {
			t.Fatal("unexpected transaction structure")
		}

		origin := decorated.Transactions[0].SiacoinInputs[0].Origin
		if origin.Source != "transaction" {
			t.Errorf("expected origin source 'transaction', got %q", origin.Source)
		}
		if origin.ID != types.Hash256(originTxnID) {
			t.Errorf("expected origin ID %v, got %v", types.Hash256(originTxnID), origin.ID)
		}
		if origin.Index != originIndex {
			t.Errorf("expected origin index %d, got %d", originIndex, origin.Index)
		}
	})

	t.Run("V2NullOriginFields", func(t *testing.T) {
		// Test V2 transactions with NULL origin fields
		outputID := types.SiacoinOutputID{10, 11, 12}
		value := types.Siacoins(300)

		err := db.transaction(func(tx *txn) error {
			var indexID int64
			err := tx.QueryRow(`INSERT INTO chain_indices (block_id, height) VALUES ($1, $2) RETURNING id`,
				encode(types.BlockID{2}), 2).Scan(&indexID)
			if err != nil {
				return err
			}

			var addressID int64
			err = tx.QueryRow(`SELECT id FROM sia_addresses WHERE sia_address=$1`, encode(addr)).Scan(&addressID)
			if err != nil {
				return err
			}

			_, err = tx.Exec(`INSERT INTO siacoin_elements
				(id, siacoin_value, merkle_proof, leaf_index, maturity_height, address_id, matured, chain_index_id, origin_source, origin_transaction_id, origin_transaction_index)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULL, NULL)`,
				encode(outputID), encode(value), encode([]types.Hash256{}), 2, 0, addressID, true, indexID, "miner_payout")
			return err
		})
		if err != nil {
			t.Fatal(err)
		}

		// Create a V2 block with a transaction that spends the element
		block := types.Block{
			V2: &types.V2BlockData{
				Height:     1,
				Commitment: types.Hash256{},
				Transactions: []types.V2Transaction{{
					SiacoinInputs: []types.V2SiacoinInput{{
						Parent: types.SiacoinElement{
							ID: outputID,
							SiacoinOutput: types.SiacoinOutput{
								Address: addr,
								Value:   value,
							},
						},
						SatisfiedPolicy: types.SatisfiedPolicy{},
					}},
				}},
			},
		}

		decorated, err := db.DecorateConsensusBlock(block)
		if err != nil {
			t.Fatalf("DecorateConsensusBlock failed with NULL origin fields on V2: %v", err)
		}

		// Verify V2 transaction was decorated
		if decorated.V2 == nil || len(decorated.V2.Transactions) != 1 {
			t.Fatalf("expected 1 V2 transaction, got %d", len(decorated.V2.Transactions))
		}

		if len(decorated.V2.Transactions[0].SiacoinInputs) != 1 {
			t.Fatalf("expected 1 siacoin input, got %d", len(decorated.V2.Transactions[0].SiacoinInputs))
		}

		// Verify the origin is empty for V2 transactions with NULL fields
		origin := decorated.V2.Transactions[0].SiacoinInputs[0].Origin
		if origin.Source != "" || origin.ID != (types.Hash256{}) || origin.Index != 0 {
			t.Errorf("expected empty origin, got Source=%q, ID=%v, Index=%d", origin.Source, origin.ID, origin.Index)
		}
	})

	t.Run("V2CompleteOriginFields", func(t *testing.T) {
		// Test V2 transactions with complete origin fields
		outputID := types.SiacoinOutputID{13, 14, 15}
		value := types.Siacoins(400)
		originTxnID := types.TransactionID{16, 17, 18}
		originIndex := uint64(3)

		err := db.transaction(func(tx *txn) error {
			var indexID int64
			err := tx.QueryRow(`INSERT INTO chain_indices (block_id, height) VALUES ($1, $2) RETURNING id`,
				encode(types.BlockID{3}), 3).Scan(&indexID)
			if err != nil {
				return err
			}

			var addressID int64
			err = tx.QueryRow(`SELECT id FROM sia_addresses WHERE sia_address=$1`, encode(addr)).Scan(&addressID)
			if err != nil {
				return err
			}

			_, err = tx.Exec(`INSERT INTO siacoin_elements
				(id, siacoin_value, merkle_proof, leaf_index, maturity_height, address_id, matured, chain_index_id, origin_source, origin_transaction_id, origin_transaction_index)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
				encode(outputID), encode(value), encode([]types.Hash256{}), 3, 0, addressID, true, indexID, "transaction", encode(originTxnID), originIndex)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}

		// Create a V2 block with a transaction that spends the element
		block := types.Block{
			V2: &types.V2BlockData{
				Height:     2,
				Commitment: types.Hash256{},
				Transactions: []types.V2Transaction{{
					SiacoinInputs: []types.V2SiacoinInput{{
						Parent: types.SiacoinElement{
							ID: outputID,
							SiacoinOutput: types.SiacoinOutput{
								Address: addr,
								Value:   value,
							},
						},
						SatisfiedPolicy: types.SatisfiedPolicy{},
					}},
				}},
			},
		}

		decorated, err := db.DecorateConsensusBlock(block)
		if err != nil {
			t.Fatalf("DecorateConsensusBlock failed with complete origin fields on V2: %v", err)
		}

		// Verify V2 transaction was decorated
		if decorated.V2 == nil || len(decorated.V2.Transactions) != 1 {
			t.Fatalf("expected 1 V2 transaction, got %d", len(decorated.V2.Transactions))
		}

		if len(decorated.V2.Transactions[0].SiacoinInputs) != 1 {
			t.Fatalf("expected 1 siacoin input, got %d", len(decorated.V2.Transactions[0].SiacoinInputs))
		}

		// Verify the origin is properly populated for V2 transactions
		origin := decorated.V2.Transactions[0].SiacoinInputs[0].Origin
		if origin.Source != "transaction" {
			t.Errorf("expected origin source 'transaction', got %q", origin.Source)
		}
		if origin.ID != types.Hash256(originTxnID) {
			t.Errorf("expected origin ID %v, got %v", types.Hash256(originTxnID), origin.ID)
		}
		if origin.Index != originIndex {
			t.Errorf("expected origin index %d, got %d", originIndex, origin.Index)
		}
	})

	t.Run("MissingElement", func(t *testing.T) {
		// Test error handling when element doesn't exist in database
		nonExistentID := types.SiacoinOutputID{99, 99, 99}

		pk := types.GeneratePrivateKey()
		block := types.Block{
			Transactions: []types.Transaction{{
				SiacoinInputs: []types.SiacoinInput{{
					ParentID:         nonExistentID,
					UnlockConditions: types.StandardUnlockConditions(pk.PublicKey()),
				}},
			}},
		}

		// Should return an error when the element doesn't exist
		_, err := db.DecorateConsensusBlock(block)
		if err == nil {
			t.Fatal("expected error when decorating block with missing element, got nil")
		}

		// Verify it's the expected error about the missing element
		expectedErrMsg := "failed to query siacoin input source"
		if !strings.Contains(err.Error(), expectedErrMsg) {
			t.Errorf("expected error containing %q, got %q", expectedErrMsg, err.Error())
		}
	})
}
