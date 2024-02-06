package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/walletd/wallet"
	"go.uber.org/zap"
)

const updateProofBatchSize = 1000

type chainUpdate interface {
	UpdateElementProof(*types.StateElement)
	ForEachTreeNode(func(row, col uint64, h types.Hash256))
	ForEachSiacoinElement(func(types.SiacoinElement, bool))
	ForEachSiafundElement(func(types.SiafundElement, bool))
}

func insertChainIndex(tx *txn, index types.ChainIndex) (id int64, err error) {
	err = tx.QueryRow(`INSERT INTO chain_indices (height, block_id) VALUES ($1, $2) ON CONFLICT (block_id) DO UPDATE SET height=EXCLUDED.height RETURNING id`, index.Height, encode(index.ID)).Scan(&id)
	return
}

func applyEvents(tx *txn, events []wallet.Event) error {
	stmt, err := tx.Prepare(`INSERT INTO events (date_created, index_id, event_type, event_data) VALUES ($1, $2, $3, $4) RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	addRelevantAddrStmt, err := tx.Prepare(`INSERT INTO event_addresses (event_id, address_id, block_height) VALUES ($1, $2, $3)`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer addRelevantAddrStmt.Close()

	for _, event := range events {
		id, err := insertChainIndex(tx, event.Index)
		if err != nil {
			return fmt.Errorf("failed to create chain index: %w", err)
		}

		buf, err := json.Marshal(event.Val)
		if err != nil {
			return fmt.Errorf("failed to marshal event: %w", err)
		}

		var eventID int64
		err = stmt.QueryRow(encode(event.Timestamp), id, event.Val.EventType(), buf).Scan(&eventID)
		if err != nil {
			return fmt.Errorf("failed to execute statement: %w", err)
		}

		for _, addr := range event.Relevant {
			addressID, err := insertAddress(tx, addr)
			if err != nil {
				return fmt.Errorf("failed to insert address: %w", err)
			} else if _, err := addRelevantAddrStmt.Exec(eventID, addressID, event.Index.Height); err != nil {
				return fmt.Errorf("failed to add relevant address: %w", err)
			}
		}
	}
	return nil
}

func applySiacoinElements(tx *txn, index types.ChainIndex, cu chainUpdate, relevantAddress func(types.Address) bool, log *zap.Logger) error {
	addrStatement, err := tx.Prepare(`INSERT INTO sia_addresses (sia_address, siacoin_balance, immature_siacoin_balance, siafund_balance) VALUES ($1, $2, $2, 0) 
ON CONFLICT (sia_address) DO UPDATE SET sia_address=EXCLUDED.sia_address 
RETURNING id, siacoin_balance, immature_siacoin_balance`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer addrStatement.Close()

	updateBalanceStmt, err := tx.Prepare(`UPDATE sia_addresses SET siacoin_balance=$1, immature_siacoin_balance=$2 WHERE id=$3`)
	if err != nil {
		return fmt.Errorf("failed to prepare update statement: %w", err)
	}
	defer updateBalanceStmt.Close()

	addStmt, err := tx.Prepare(`INSERT INTO siacoin_elements (id, address_id, siacoin_value, merkle_proof, leaf_index, maturity_height) VALUES ($1, $2, $3, $4, $5, $6)`)
	if err != nil {
		return fmt.Errorf("failed to prepare insert statement: %w", err)
	}
	defer addStmt.Close()

	spendStmt, err := tx.Prepare(`DELETE FROM siacoin_elements WHERE id=$1 RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer spendStmt.Close()

	// using ForEachSiacoinElement creates an interesting problem. The
	// ForEachSiacoinElement function is only called once for each element. So
	// if a siacoin element is spent and created in the same block, the element
	// will not exist in the database.
	//
	// This creates a problem with balance tracking since it subtracts the
	// element value from the balance. However, since the element value was
	// never added to the balance in the first place, the balance will be
	// incorrect. The solution is to check if the UTXO is in the database before
	// decrementing the balance.
	//
	// This is an important implementation detail since the store must assume
	// the chain manager is correct and can't check the integrity of the database
	// without reimplementing some of the consensus logic.
	cu.ForEachSiacoinElement(func(se types.SiacoinElement, spent bool) {
		// sticky error
		if err != nil {
			return
		} else if !relevantAddress(se.SiacoinOutput.Address) {
			return
		}

		// query the address database ID and balance
		var addressID int64
		var balance, immatureBalance types.Currency
		err = addrStatement.QueryRow(encode(se.SiacoinOutput.Address), encode(types.ZeroCurrency)).Scan(&addressID, decode(&balance), decode(&immatureBalance))
		if err != nil {
			err = fmt.Errorf("failed to query address %q: %w", se.SiacoinOutput.Address, err)
			return
		}

		if spent {
			var dummy types.Hash256
			err = spendStmt.QueryRow(encode(se.ID)).Scan(decode(&dummy))
			if errors.Is(err, sql.ErrNoRows) {
				// spent output not found, most likely an ephemeral output. ignore
				err = nil
				return
			} else if err != nil {
				err = fmt.Errorf("failed to delete output %q: %w", se.ID, err)
				return
			}

			if se.MaturityHeight > index.Height {
				immatureBalance = immatureBalance.Sub(se.SiacoinOutput.Value)
			} else {
				balance = balance.Sub(se.SiacoinOutput.Value)
			}

			_, err = updateBalanceStmt.Exec(encode(balance), encode(immatureBalance), addressID)
			if err != nil {
				err = fmt.Errorf("failed to update address %q balance: %w", se.SiacoinOutput.Address, err)
				return
			}

			log.Debug("removed utxo", zap.Stringer("address", se.SiacoinOutput.Address), zap.Stringer("outputID", se.ID), zap.String("value", se.SiacoinOutput.Value.ExactString()), zap.Int64("addressID", addressID))
		} else {
			// insert the created utxo
			_, err = addStmt.Exec(encode(se.ID), addressID, encode(se.SiacoinOutput.Value), encodeSlice(se.MerkleProof), se.LeafIndex, se.MaturityHeight)
			if err != nil {
				err = fmt.Errorf("failed to insert output %q: %w", se.ID, err)
				return
			}

			if se.MaturityHeight > index.Height {
				immatureBalance = immatureBalance.Add(se.SiacoinOutput.Value)
				log.Debug("adding immature balance")
			} else {
				balance = balance.Add(se.SiacoinOutput.Value)
				log.Debug("adding balance")
			}

			// update the balance
			_, err = updateBalanceStmt.Exec(encode(balance), encode(immatureBalance), addressID)
			if err != nil {
				err = fmt.Errorf("failed to update address %q balance: %w", se.SiacoinOutput.Address, err)
				return
			}
			log.Debug("added utxo", zap.Uint64("maturityHeight", se.MaturityHeight), zap.Stringer("address", se.SiacoinOutput.Address), zap.Stringer("outputID", se.ID), zap.String("value", se.SiacoinOutput.Value.ExactString()), zap.Int64("addressID", addressID))
		}
	})
	return err
}

func applySiafundElements(tx *txn, cu chainUpdate, relevantAddress func(types.Address) bool, log *zap.Logger) error {
	// create the address if it doesn't exist
	addrStatement, err := tx.Prepare(`INSERT INTO sia_addresses (sia_address, siacoin_balance, immature_siacoin_balance, siafund_balance) VALUES ($1, $2, $2, 0)
ON CONFLICT (sia_address) DO UPDATE SET sia_address=EXCLUDED.sia_address 
RETURNING id, siafund_balance`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer addrStatement.Close()

	updateBalanceStmt, err := tx.Prepare(`UPDATE sia_addresses SET siafund_balance=$1 WHERE id=$2`)
	if err != nil {
		return fmt.Errorf("failed to prepare update statement: %w", err)
	}
	defer updateBalanceStmt.Close()

	addStmt, err := tx.Prepare(`INSERT INTO siafund_elements (id, address_id, claim_start, merkle_proof, leaf_index, siafund_value) VALUES ($1, $2, $3, $4, $5, $6)`)
	if err != nil {
		return fmt.Errorf("failed to prepare insert statement: %w", err)
	}
	defer addStmt.Close()

	spendStmt, err := tx.Prepare(`DELETE FROM siacoin_elements WHERE id=$1 RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer spendStmt.Close()

	cu.ForEachSiafundElement(func(se types.SiafundElement, spent bool) {
		// sticky error
		if err != nil {
			return
		} else if !relevantAddress(se.SiafundOutput.Address) {
			return
		}

		// query the address database ID and balance
		var addressID int64
		var balance uint64
		// get the address ID
		err = addrStatement.QueryRow(encode(se.SiafundOutput.Address), encode(types.ZeroCurrency)).Scan(&addressID, &balance)
		if err != nil {
			err = fmt.Errorf("failed to query address %q: %w", se.SiafundOutput.Address, err)
			return
		}

		// update the balance
		if spent {
			var dummy types.Hash256
			err = spendStmt.QueryRow(encode(se.ID)).Scan(decode(&dummy))
			if errors.Is(err, sql.ErrNoRows) {
				// spent output not found, most likely an ephemeral output.
				// ignore
				err = nil
				return
			} else if err != nil {
				err = fmt.Errorf("failed to delete output %q: %w", se.ID, err)
				return
			}

			// update the balance only if the utxo was successfully deleted
			if se.SiafundOutput.Value > balance {
				log.Panic("balance is negative", zap.Stringer("address", se.SiafundOutput.Address), zap.Uint64("balance", balance), zap.Stringer("outputID", se.ID), zap.Uint64("value", se.SiafundOutput.Value))
			}

			balance -= se.SiafundOutput.Value
			_, err = updateBalanceStmt.Exec(encode(balance), addressID)
			if err != nil {
				err = fmt.Errorf("failed to update address %q balance: %w", se.SiafundOutput.Address, err)
				return
			}
		} else {
			balance += se.SiafundOutput.Value
			// update the balance
			_, err = updateBalanceStmt.Exec(balance, addressID)
			if err != nil {
				err = fmt.Errorf("failed to update address %q balance: %w", se.SiafundOutput.Address, err)
				return
			}

			// insert the created utxo
			_, err = addStmt.Exec(encode(se.ID), addressID, encode(se.ClaimStart), encodeSlice(se.MerkleProof), se.LeafIndex, se.SiafundOutput.Value)
			if err != nil {
				err = fmt.Errorf("failed to insert output %q: %w", se.ID, err)
				return
			}
		}
	})
	return err
}

func updateLastIndexedTip(tx *txn, tip types.ChainIndex) error {
	_, err := tx.Exec(`UPDATE global_settings SET last_indexed_tip=$1`, encode(tip))
	return err
}

func getStateElementBatch(s *stmt, offset, limit int) ([]types.StateElement, error) {
	rows, err := s.Query(limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to query siacoin elements: %w", err)
	}
	defer rows.Close()

	var updated []types.StateElement
	for rows.Next() {
		var se types.StateElement
		err := rows.Scan(decode(&se.ID), decodeSlice(&se.MerkleProof), &se.LeafIndex)
		if err != nil {
			return nil, fmt.Errorf("failed to scan state element: %w", err)
		}
		updated = append(updated, se)
	}
	return updated, nil
}

func updateStateElement(s *stmt, se types.StateElement) error {
	res, err := s.Exec(encodeSlice(se.MerkleProof), se.LeafIndex, encode(se.ID))
	if err != nil {
		return fmt.Errorf("failed to update siacoin element %q: %w", se.ID, err)
	} else if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	} else if n != 1 {
		return fmt.Errorf("expected 1 row to be affected, got %d", n)
	}
	return nil
}

// how slow is this going to be 😬?
func updateElementProofs(tx *txn, table string, cu chainUpdate) error {
	stmt, err := tx.Prepare(`SELECT id, merkle_proof, leaf_index FROM ` + table + ` LIMIT $1 OFFSET $2`)
	if err != nil {
		return fmt.Errorf("failed to prepare batch statement: %w", err)
	}
	defer stmt.Close()

	updateStmt, err := tx.Prepare(`UPDATE ` + table + ` SET merkle_proof=$1, leaf_index=$2 WHERE id=$3 RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare update statement: %w", err)
	}
	defer updateStmt.Close()

	for offset := 0; ; offset += updateProofBatchSize {
		elements, err := getStateElementBatch(stmt, offset, updateProofBatchSize)
		if err != nil {
			return fmt.Errorf("failed to get state element batch: %w", err)
		} else if len(elements) == 0 {
			break
		}

		for _, se := range elements {
			cu.UpdateElementProof(&se)
			if err := updateStateElement(updateStmt, se); err != nil {
				return fmt.Errorf("failed to update state element: %w", err)
			}
		}
	}
	return nil
}

func getMaturedValue(tx *txn, index types.ChainIndex) (matured map[int64]types.Currency, err error) {
	rows, err := tx.Query(`SELECT address_id, siacoin_value FROM siacoin_elements WHERE maturity_height=$1`, index.Height)
	if err != nil {
		return nil, fmt.Errorf("failed to query siacoin elements: %w", err)
	}
	defer rows.Close()

	matured = make(map[int64]types.Currency)
	for rows.Next() {
		var addressID int64
		var value types.Currency
		err := rows.Scan(&addressID, decode(&value))
		if err != nil {
			return nil, fmt.Errorf("failed to scan matured balance: %w", err)
		}
		matured[addressID] = matured[addressID].Add(value)
	}
	return
}

func updateImmatureBalance(tx *txn, index types.ChainIndex, revert bool) error {
	balanceStmt, err := tx.Prepare(`SELECT siacoin_balance, immature_siacoin_balance FROM sia_addresses WHERE id=$1`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer balanceStmt.Close()

	updateStmt, err := tx.Prepare(`UPDATE sia_addresses SET siacoin_balance=$1, immature_siacoin_balance=$2 WHERE id=$3`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer updateStmt.Close()

	delta, err := getMaturedValue(tx, index)
	if err != nil {
		return fmt.Errorf("failed to get matured utxos: %w", err)
	}

	for addressID, value := range delta {
		var balance, immatureBalance types.Currency
		err := balanceStmt.QueryRow(addressID).Scan(decode(&balance), decode(&immatureBalance))
		if err != nil {
			return fmt.Errorf("failed to query address %d: %w", addressID, err)
		}

		if revert {
			balance = balance.Sub(value)
			immatureBalance = immatureBalance.Add(value)
		} else {
			balance = balance.Add(value)
			immatureBalance = immatureBalance.Sub(value)
		}

		_, err = updateStmt.Exec(encode(balance), encode(immatureBalance), addressID)
		if err != nil {
			return fmt.Errorf("failed to update address %d: %w", addressID, err)
		}
	}
	return nil
}

// applyChainUpdates applies the given chain updates to the database.
func applyChainUpdates(tx *txn, updates []*chain.ApplyUpdate, log *zap.Logger) error {
	stmt, err := tx.Prepare(`SELECT id FROM sia_addresses WHERE sia_address=$1 LIMIT 1`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	// note: this would be more performant for small wallets to load all
	// addresses into memory. However, for larger wallets (> 10K addresses),
	// this is time consuming. Instead, the database is queried for each
	// address. Monitor performance and consider changing this in the
	// future. From a memory perspective, it would be fine to lazy load all
	// addresses into memory.
	relevantAddress := func(address types.Address) bool {
		var dbID int64
		err := stmt.QueryRow(encode(address)).Scan(&dbID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			panic(err) // database error
		}
		return err == nil
	}

	for _, update := range updates {
		// mature the immature balance first
		if err := updateImmatureBalance(tx, update.State.Index, false); err != nil {
			return fmt.Errorf("failed to update immature balance: %w", err)
		}
		// apply new events
		events := wallet.AppliedEvents(update.State, update.Block, update, relevantAddress)
		if err := applyEvents(tx, events); err != nil {
			return fmt.Errorf("failed to apply events: %w", err)
		}

		// apply new elements
		if err := applySiacoinElements(tx, update.State.Index, update, relevantAddress, log.Named("siacoins")); err != nil {
			return fmt.Errorf("failed to apply siacoin elements: %w", err)
		} else if err := applySiafundElements(tx, update, relevantAddress, log.Named("siafunds")); err != nil {
			return fmt.Errorf("failed to apply siafund elements: %w", err)
		}

		// update proofs
		if err := updateElementProofs(tx, "siacoin_elements", update); err != nil {
			return fmt.Errorf("failed to update siacoin element proofs: %w", err)
		} else if err := updateElementProofs(tx, "siafund_elements", update); err != nil {
			return fmt.Errorf("failed to update siafund element proofs: %w", err)
		}
	}

	lastTip := updates[len(updates)-1].State.Index
	if err := updateLastIndexedTip(tx, lastTip); err != nil {
		return fmt.Errorf("failed to update last indexed tip: %w", err)
	}
	return nil
}

// ProcessChainApplyUpdate implements chain.Subscriber
func (s *Store) ProcessChainApplyUpdate(cau *chain.ApplyUpdate, mayCommit bool) error {
	s.updates = append(s.updates, cau)

	if mayCommit {
		return s.transaction(func(tx *txn) error {
			if err := applyChainUpdates(tx, s.updates, s.log.Named("apply")); err != nil {
				return err
			}
			s.updates = nil
			return nil
		})
	}
	return nil
}

// ProcessChainRevertUpdate implements chain.Subscriber
func (s *Store) ProcessChainRevertUpdate(cru *chain.RevertUpdate) error {
	log := s.log.Named("revert")

	// update hasn't been committed yet
	if len(s.updates) > 0 && s.updates[len(s.updates)-1].Block.ID() == cru.Block.ID() {
		s.updates = s.updates[:len(s.updates)-1]
		return nil
	}

	// update has been committed, revert it
	return s.transaction(func(tx *txn) error {
		stmt, err := tx.Prepare(`SELECT id FROM sia_addresses WHERE sia_address=$1 LIMIT 1`)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer stmt.Close()

		// note: this would be more performant for small wallets to load all
		// addresses into memory. However, for larger wallets (> 10K addresses),
		// this is time consuming. Instead, the database is queried for each
		// address. Monitor performance and consider changing this in the
		// future. From a memory perspective, it would be fine to lazy load all
		// addresses into memory.
		relevantAddress := func(address types.Address) bool {
			var dbID int64
			err := stmt.QueryRow(encode(address)).Scan(&dbID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				panic(err) // database error
			}
			return err == nil
		}

		if err := applySiacoinElements(tx, cru.State.Index, cru, relevantAddress, log.Named("siacoins")); err != nil {
			return fmt.Errorf("failed to apply siacoin elements: %w", err)
		} else if err := applySiafundElements(tx, cru, relevantAddress, log.Named("siafunds")); err != nil {
			return fmt.Errorf("failed to apply siafund elements: %w", err)
		}

		// revert events
		_, err = tx.Exec(`DELETE FROM chain_indices WHERE block_id=$1`, cru.Block.ID())
		if err != nil {
			return fmt.Errorf("failed to delete chain index: %w", err)
		}

		// revert immature balance
		if err := updateImmatureBalance(tx, cru.State.Index, true); err != nil {
			return fmt.Errorf("failed to update immature balance: %w", err)
		}

		// update proofs
		if err := updateElementProofs(tx, "siacoin_elements", cru); err != nil {
			return fmt.Errorf("failed to update siacoin element proofs: %w", err)
		} else if err := updateElementProofs(tx, "siafund_elements", cru); err != nil {
			return fmt.Errorf("failed to update siafund element proofs: %w", err)
		}
		return nil
	})
}

// LastCommittedIndex returns the last chain index that was committed.
func (s *Store) LastCommittedIndex() (index types.ChainIndex, err error) {
	err = s.db.QueryRow(`SELECT last_indexed_tip FROM global_settings`).Scan(decode(&index))
	return
}
