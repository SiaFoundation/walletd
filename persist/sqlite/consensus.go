package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/walletd/wallet"
)

const updateProofBatchSize = 1000

type proofUpdater interface {
	UpdateElementProof(*types.StateElement)
}

func insertChainIndex(tx txn, index types.ChainIndex) (id int64, err error) {
	err = tx.QueryRow(`INSERT INTO chain_indices (height, block_id) VALUES ($1, $2) ON CONFLICT (block_id) DO UPDATE SET height=EXCLUDED.height RETURNING id`, index.Height, encode(index.ID)).Scan(&id)
	return
}

func applyEvents(tx txn, events []wallet.Event) error {
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
		err = stmt.QueryRow(sqlTime(event.Timestamp), id, event.Val.EventType(), buf).Scan(&eventID)
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
			log.Println("added relevant address", eventID, addr)
		}
	}
	return nil
}

func deleteSiacoinOutputs(tx txn, spent []types.SiacoinElement) error {
	addrStmt, err := tx.Prepare(`SELECT id, siacoin_balance FROM sia_addresses WHERE sia_address=$1 LIMIT 1`)
	if err != nil {
		return fmt.Errorf("failed to prepare lookup statement: %w", err)
	}
	defer addrStmt.Close()

	updateBalanceStmt, err := tx.Prepare(`UPDATE sia_addresses SET siacoin_balance=$1 WHERE id=$2`)
	if err != nil {
		return fmt.Errorf("failed to prepare update statement: %w", err)
	}
	defer updateBalanceStmt.Close()

	deleteStmt, err := tx.Prepare(`DELETE FROM siacoin_elements WHERE id=$1 RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer deleteStmt.Close()

	for _, se := range spent {
		// query the address database ID and balance
		var addressID int64
		var balance types.Currency
		err := addrStmt.QueryRow(encode(se.SiacoinOutput.Address)).Scan(&addressID, (*sqlCurrency)(&balance))
		if err != nil {
			return fmt.Errorf("failed to lookup address %q: %w", se.SiacoinOutput.Address, err)
		}

		// update the balance
		balance = balance.Sub(se.SiacoinOutput.Value)
		_, err = updateBalanceStmt.Exec((*sqlCurrency)(&balance), addressID)
		if err != nil {
			return fmt.Errorf("failed to update address %q balance: %w", se.SiacoinOutput.Address, err)
		}

		var dummy types.Hash256
		err = deleteStmt.QueryRow(encode(se.ID)).Scan(decode(&dummy, 32))
		if err != nil {
			return fmt.Errorf("failed to delete output %q: %w", se.ID, err)
		}
	}
	return nil
}

func applySiacoinOutputs(tx txn, added map[types.Hash256]types.SiacoinElement) error {
	addrStmt, err := tx.Prepare(`SELECT id, siacoin_balance FROM sia_addresses WHERE sia_address=$1 LIMIT 1`)
	if err != nil {
		return fmt.Errorf("failed to prepare lookup statement: %w", err)
	}
	defer addrStmt.Close()

	updateBalanceStmt, err := tx.Prepare(`UPDATE sia_addresses SET siacoin_balance=$1 WHERE id=$2`)
	if err != nil {
		return fmt.Errorf("failed to prepare update statement: %w", err)
	}
	defer updateBalanceStmt.Close()

	addStmt, err := tx.Prepare(`INSERT INTO siacoin_elements (id, address_id, siacoin_value, merkle_proof, leaf_index, maturity_height) VALUES ($1, $2, $3, $4, $5, $6)`)
	if err != nil {
		return fmt.Errorf("failed to prepare insert statement: %w", err)
	}
	defer addStmt.Close()

	for _, se := range added {
		// query the address database ID and balance
		var addressID int64
		var balance types.Currency
		err := addrStmt.QueryRow(encode(se.SiacoinOutput.Address)).Scan(&addressID, (*sqlCurrency)(&balance))
		if err != nil {
			return fmt.Errorf("failed to lookup address %q: %w", se.SiacoinOutput.Address, err)
		}

		// update the balance
		balance = balance.Add(se.SiacoinOutput.Value)
		_, err = updateBalanceStmt.Exec((*sqlCurrency)(&balance), addressID)
		if err != nil {
			return fmt.Errorf("failed to update address %q balance: %w", se.SiacoinOutput.Address, err)
		}

		// insert the created utxo
		_, err = addStmt.Exec(encode(se.ID), addressID, sqlCurrency(se.SiacoinOutput.Value), encodeSlice(se.MerkleProof), se.MaturityHeight, se.LeafIndex)
		if err != nil {
			return fmt.Errorf("failed to insert output %q: %w", se.ID, err)
		}
	}
	return nil
}

func deleteSiafundOutputs(tx txn, spent []types.SiafundElement) error {
	addrStmt, err := tx.Prepare(`SELECT id, siafund_balance FROM sia_addresses WHERE sia_address=$1 LIMIT 1`)
	if err != nil {
		return fmt.Errorf("failed to prepare lookup statement: %w", err)
	}
	defer addrStmt.Close()

	updateBalanceStmt, err := tx.Prepare(`UPDATE sia_addresses SET siafund_balance=$1 WHERE id=$2`)
	if err != nil {
		return fmt.Errorf("failed to prepare update statement: %w", err)
	}
	defer updateBalanceStmt.Close()

	spendStmt, err := tx.Prepare(`DELETE FROM siafund_elements WHERE id=$1 RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer spendStmt.Close()

	for _, se := range spent {
		// query the address database ID and balance
		var addressID int64
		var balance uint64
		err := addrStmt.QueryRow(encode(se.SiafundOutput.Address)).Scan(&addressID, balance)
		if err != nil {
			return fmt.Errorf("failed to lookup address %q: %w", se.SiafundOutput.Address, err)
		}

		// update the balance
		if balance < se.SiafundOutput.Value {
			panic("siafund balance is negative") // developer error
		}
		balance -= se.SiafundOutput.Value
		_, err = updateBalanceStmt.Exec(balance, addressID)
		if err != nil {
			return fmt.Errorf("failed to update address %q balance: %w", se.SiafundOutput.Address, err)
		}

		var dummy types.Hash256
		err = spendStmt.QueryRow(encode(se.ID)).Scan(decode(&dummy, 32))
		if err != nil {
			return fmt.Errorf("failed to delete output %q: %w", se.ID, err)
		}
	}
	return nil
}

func applySiafundOutputs(tx txn, added map[types.Hash256]types.SiafundElement) error {
	addrStmt, err := tx.Prepare(`SELECT id, siafund_balance FROM sia_addresses WHERE sia_address=$1 LIMIT 1`)
	if err != nil {
		return fmt.Errorf("failed to prepare lookup statement: %w", err)
	}
	defer addrStmt.Close()

	updateBalanceStmt, err := tx.Prepare(`UPDATE sia_addresses SET siafund_balance=$1 WHERE id=$2`)
	if err != nil {
		return fmt.Errorf("failed to prepare update statement: %w", err)
	}
	defer updateBalanceStmt.Close()

	addStmt, err := tx.Prepare(`INSERT INTO siafund_elements (id, address_id, claim_start, siafund_value, merkle_proof, leaf_index) VALUES ($1, $2, $3, $4, $5, $6)`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer addStmt.Close()

	for _, se := range added {
		// query the address database ID and balance
		var addressID int64
		var balance uint64
		err := addrStmt.QueryRow(encode(se.SiafundOutput.Address)).Scan(&addressID, balance)
		if err != nil {
			return fmt.Errorf("failed to lookup address %q: %w", se.SiafundOutput.Address, err)
		}

		// update the balance
		if balance < se.SiafundOutput.Value {
			panic("siafund balance is negative") // developer error
		}
		balance -= se.SiafundOutput.Value
		_, err = updateBalanceStmt.Exec(balance, addressID)
		if err != nil {
			return fmt.Errorf("failed to update address %q balance: %w", se.SiafundOutput.Address, err)
		}

		_, err = addStmt.Exec(encode(se.ID), addressID, sqlCurrency(se.ClaimStart), se.SiafundOutput.Value, encodeSlice(se.MerkleProof), se.LeafIndex)
		if err != nil {
			return fmt.Errorf("failed to insert output %q: %w", se.ID, err)
		}
	}
	return nil
}

func updateLastIndexedTip(tx txn, tip types.ChainIndex) error {
	_, err := tx.Exec(`UPDATE global_settings SET last_indexed_tip=$1`, encode(tip))
	return err
}

// how slow is this going to be 😬?
//
// todo: determine if it's feasible for exchange mode to keep everything in
// memory.
func updateElementProofs(tx txn, table string, updater proofUpdater) error {
	stmt, err := tx.Prepare(`SELECT id, merkle_proof, leaf_index FROM ` + table + ` LIMIT $1 OFFSET $2`)
	if err != nil {
		return fmt.Errorf("failed to prepare batch statement: %w", err)
	}
	defer stmt.Close()

	updateStmt, err := tx.Prepare(`UPDATE ` + table + ` SET merkle_proof=$1, leaf_index=$2 WHERE id=$3`)
	if err != nil {
		return fmt.Errorf("failed to prepare update statement: %w", err)
	}
	defer updateStmt.Close()

	var updated []types.StateElement
	for offset := 0; ; offset += updateProofBatchSize {
		updated = updated[:0]

		more, err := func(n int) (bool, error) {
			rows, err := stmt.Query(updateProofBatchSize, n)
			if err != nil {
				return false, fmt.Errorf("failed to query siacoin elements: %w", err)
			}
			defer rows.Close()

			var more bool
			for rows.Next() {
				// if we get here, there may be more rows to process
				more = true

				var se types.StateElement
				err := rows.Scan(decode(&se.ID, 32), decodeSlice(&se.MerkleProof, 32*1000), &se.LeafIndex)
				if err != nil {
					return false, fmt.Errorf("failed to scan state element: %w", err)
				}
				updater.UpdateElementProof(&se)
				updated = append(updated, se)
			}
			return more, nil
		}(offset)
		if err != nil {
			return err
		}

		for _, se := range updated {
			_, err := updateStmt.Exec(encodeSlice(se.MerkleProof), se.LeafIndex, encode(se.ID))
			if err != nil {
				return fmt.Errorf("failed to update siacoin element %q: %w", se.ID, err)
			}
		}

		if !more {
			break
		}
	}

	return nil
}

func applyChainUpdates(tx txn, updates []*chain.ApplyUpdate) error {
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
	ownsAddress := func(address types.Address) bool {
		var dbID int64
		err := stmt.QueryRow(encode(address)).Scan(&dbID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			panic(err) // database error
		}
		return err == nil
	}

	for _, update := range updates {
		events := wallet.AppliedEvents(update.State, update.Block, update, ownsAddress)
		if err := applyEvents(tx, events); err != nil {
			return fmt.Errorf("failed to apply events: %w", err)
		}

		var spentSiacoinOutputs []types.SiacoinElement
		newSiacoinOutputs := make(map[types.Hash256]types.SiacoinElement)
		update.ForEachSiacoinElement(func(se types.SiacoinElement, spent bool) {
			if !ownsAddress(se.SiacoinOutput.Address) {
				return
			}

			if spent {
				spentSiacoinOutputs = append(spentSiacoinOutputs, se)
				delete(newSiacoinOutputs, se.ID)
			} else {
				newSiacoinOutputs[se.ID] = se
			}
		})

		if err := deleteSiacoinOutputs(tx, spentSiacoinOutputs); err != nil {
			return fmt.Errorf("failed to delete siacoin outputs: %w", err)
		} else if err := applySiacoinOutputs(tx, newSiacoinOutputs); err != nil {
			return fmt.Errorf("failed to apply siacoin outputs: %w", err)
		}

		var spentSiafundOutputs []types.SiafundElement
		newSiafundOutputs := make(map[types.Hash256]types.SiafundElement)
		update.ForEachSiafundElement(func(sf types.SiafundElement, spent bool) {
			if !ownsAddress(sf.SiafundOutput.Address) {
				return
			}

			if spent {
				spentSiafundOutputs = append(spentSiafundOutputs, sf)
				delete(newSiafundOutputs, sf.ID)
			} else {
				newSiafundOutputs[sf.ID] = sf
			}
		})

		if err := deleteSiafundOutputs(tx, spentSiafundOutputs); err != nil {
			return fmt.Errorf("failed to delete siafund outputs: %w", err)
		} else if err := applySiafundOutputs(tx, newSiafundOutputs); err != nil {
			return fmt.Errorf("failed to apply siafund outputs: %w", err)
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

func (s *Store) ProcessChainApplyUpdate(cau *chain.ApplyUpdate, mayCommit bool) error {
	s.updates = append(s.updates, cau)

	if mayCommit {
		return s.transaction(func(tx txn) error {
			if err := applyChainUpdates(tx, s.updates); err != nil {
				return err
			}
			s.updates = nil
			return nil
		})
	}
	return nil
}

func (s *Store) ProcessChainRevertUpdate(cru *chain.RevertUpdate) error {
	// update hasn't been committed yet
	if len(s.updates) > 0 && s.updates[len(s.updates)-1].Block.ID() == cru.Block.ID() {
		s.updates = s.updates[:len(s.updates)-1]
		return nil
	}

	// update has been committed, revert it
	return s.transaction(func(tx txn) error {
		stmt, err := tx.Prepare(`SELECT sia_address FROM sia_addresses WHERE sia_address=$1 LIMIT 1`)
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
		ownsAddress := func(address types.Address) bool {
			var dbID int64
			err := stmt.QueryRow(encode(address)).Scan(&dbID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				panic(err) // database error
			}
			return err == nil
		}

		var spentSiacoinOutputs []types.SiacoinElement
		var spentSiafundOutputs []types.SiafundElement
		addedSiacoinOutputs := make(map[types.Hash256]types.SiacoinElement)
		addedSiafundOutputs := make(map[types.Hash256]types.SiafundElement)

		cru.ForEachSiacoinElement(func(se types.SiacoinElement, spent bool) {
			if !ownsAddress(se.SiacoinOutput.Address) {
				return
			}

			if !spent {
				spentSiacoinOutputs = append(spentSiacoinOutputs, se)
			} else {
				addedSiacoinOutputs[se.ID] = se
			}
		})

		cru.ForEachSiafundElement(func(se types.SiafundElement, spent bool) {
			if !ownsAddress(se.SiafundOutput.Address) {
				return
			}

			if !spent {
				spentSiafundOutputs = append(spentSiafundOutputs, se)
			} else {
				addedSiafundOutputs[se.ID] = se
			}
		})

		// revert siacoin outputs
		if err := deleteSiacoinOutputs(tx, spentSiacoinOutputs); err != nil {
			return fmt.Errorf("failed to delete siacoin outputs: %w", err)
		} else if err := applySiacoinOutputs(tx, addedSiacoinOutputs); err != nil {
			return fmt.Errorf("failed to apply siacoin outputs: %w", err)
		}

		// revert siafund outputs
		if err := deleteSiafundOutputs(tx, spentSiafundOutputs); err != nil {
			return fmt.Errorf("failed to delete siafund outputs: %w", err)
		} else if err := applySiafundOutputs(tx, addedSiafundOutputs); err != nil {
			return fmt.Errorf("failed to apply siafund outputs: %w", err)
		}

		// revert events
		_, err = tx.Exec(`DELETE FROM chain_indices WHERE block_id=$1`, cru.Block.ID())
		if err != nil {
			return fmt.Errorf("failed to delete chain index: %w", err)
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
	err = s.db.QueryRow(`SELECT last_indexed_tip FROM global_settings`).Scan(decode(&index, 40))
	return
}
