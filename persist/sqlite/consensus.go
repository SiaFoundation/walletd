package sqlite

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/walletd/wallet"
)

type updateTx struct {
	tx *txn

	relevantAddresses map[types.Address]bool
}

type addressRef struct {
	ID      int64
	Balance wallet.Balance
}

func scanStateElement(s scanner) (se types.StateElement, err error) {
	err = s.Scan(decode(&se.ID), &se.LeafIndex, decodeSlice(&se.MerkleProof))
	return
}

func scanAddress(s scanner) (ab addressRef, err error) {
	err = s.Scan(&ab.ID, decode(&ab.Balance.Siacoins), decode(&ab.Balance.ImmatureSiacoins), &ab.Balance.Siafunds)
	return
}

func (ut *updateTx) SiacoinStateElements() ([]types.StateElement, error) {
	const query = `SELECT id, leaf_index, merkle_proof FROM siacoin_elements`
	rows, err := ut.tx.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query siacoin elements: %w", err)
	}
	defer rows.Close()

	var elements []types.StateElement
	for rows.Next() {
		se, err := scanStateElement(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan state element: %w", err)
		}
		elements = append(elements, se)
	}
	return elements, rows.Err()
}

func (ut *updateTx) UpdateSiacoinStateElements(elements []types.StateElement) error {
	const query = `UPDATE siacoin_elements SET merkle_proof=$1, leaf_index=$2 WHERE id=$3 RETURNING id`
	stmt, err := ut.tx.Prepare(query)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, se := range elements {
		var dummy types.Hash256
		err := stmt.QueryRow(encodeSlice(se.MerkleProof), se.LeafIndex, encode(se.ID)).Scan(decode(&dummy))
		if err != nil {
			return fmt.Errorf("failed to execute statement: %w", err)
		}
	}
	return nil
}

func (ut *updateTx) SiafundStateElements() ([]types.StateElement, error) {
	const query = `SELECT id, leaf_index, merkle_proof FROM siafund_elements`
	rows, err := ut.tx.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query siacoin elements: %w", err)
	}
	defer rows.Close()

	var elements []types.StateElement
	for rows.Next() {
		se, err := scanStateElement(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan state element: %w", err)
		}
		elements = append(elements, se)
	}
	return elements, rows.Err()
}

func (ut *updateTx) UpdateSiafundStateElements(elements []types.StateElement) error {
	const query = `UPDATE siafund_elements SET merkle_proof=$1, leaf_index=$2 WHERE id=$3 RETURNING id`
	stmt, err := ut.tx.Prepare(query)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, se := range elements {
		var dummy types.Hash256
		err := stmt.QueryRow(encodeSlice(se.MerkleProof), se.LeafIndex, encode(se.ID)).Scan(decode(&dummy))
		if err != nil {
			return fmt.Errorf("failed to execute statement: %w", err)
		}
	}
	return nil
}

func (ut *updateTx) AddressRelevant(addr types.Address) (bool, error) {
	if relevant, ok := ut.relevantAddresses[addr]; ok {
		return relevant, nil
	}

	var id int64
	err := ut.tx.QueryRow(`SELECT id FROM sia_addresses WHERE sia_address=$1`, encode(addr)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		ut.relevantAddresses[addr] = false
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("failed to query address: %w", err)
	}
	ut.relevantAddresses[addr] = true
	return ut.relevantAddresses[addr], nil
}

func (ut *updateTx) AddressBalance(addr types.Address) (balance wallet.Balance, err error) {
	err = ut.tx.QueryRow(`SELECT siacoin_balance, immature_siacoin_balance, siafund_balance FROM sia_addresses WHERE sia_address=$1`, encode(addr)).Scan(decode(&balance.Siacoins), decode(&balance.ImmatureSiacoins), &balance.Siafunds)
	return
}

func (ut *updateTx) ApplyMatureSiacoinBalance(index types.ChainIndex) error {
	const query = `SELECT se.address_id, se.siacoin_value
FROM siacoin_elements se
WHERE maturity_height=$1`
	rows, err := ut.tx.Query(query, index.Height)
	if err != nil {
		return fmt.Errorf("failed to query siacoin elements: %w", err)
	}
	defer rows.Close()

	balanceDelta := make(map[int64]types.Currency)
	for rows.Next() {
		var addressID int64
		var value types.Currency

		if err := rows.Scan(&addressID, decode(&value)); err != nil {
			return fmt.Errorf("failed to scan siacoin balance: %w", err)
		}
		balanceDelta[addressID] = balanceDelta[addressID].Add(value)
	}

	getAddressBalanceStmt, err := ut.tx.Prepare(`SELECT siacoin_balance, immature_siacoin_balance FROM sia_addresses WHERE id=$1`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer getAddressBalanceStmt.Close()

	updateAddressBalanceStmt, err := ut.tx.Prepare(`UPDATE sia_addresses SET siacoin_balance=$1, immature_siacoin_balance=$2 WHERE id=$3`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer updateAddressBalanceStmt.Close()

	for addressID, delta := range balanceDelta {
		var balance, immatureBalance types.Currency
		err := getAddressBalanceStmt.QueryRow(addressID).Scan(decode(&balance), decode(&immatureBalance))
		if err != nil {
			return fmt.Errorf("failed to get address balance: %w", err)
		}

		balance = balance.Add(delta)
		immatureBalance = immatureBalance.Sub(delta)

		res, err := updateAddressBalanceStmt.Exec(encode(balance), encode(immatureBalance), addressID)
		if err != nil {
			return fmt.Errorf("failed to update address balance: %w", err)
		} else if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("failed to get rows affected: %w", err)
		} else if n != 1 {
			return fmt.Errorf("expected 1 row affected, got %v", n)
		}
	}
	return nil
}

func (ut *updateTx) RevertMatureSiacoinBalance(index types.ChainIndex) error {
	const query = `SELECT se.address_id, se.siacoin_value
	FROM siacoin_elements se
	WHERE maturity_height=$1`
	rows, err := ut.tx.Query(query, index.Height)
	if err != nil {
		return fmt.Errorf("failed to query siacoin elements: %w", err)
	}
	defer rows.Close()

	balanceDelta := make(map[int64]types.Currency)
	for rows.Next() {
		var addressID int64
		var value types.Currency

		if err := rows.Scan(&addressID, decode(&value)); err != nil {
			return fmt.Errorf("failed to scan siacoin balance: %w", err)
		}
		balanceDelta[addressID] = balanceDelta[addressID].Add(value)
	}

	getAddressBalanceStmt, err := ut.tx.Prepare(`SELECT siacoin_balance, immature_siacoin_balance FROM sia_addresses WHERE id=$1`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer getAddressBalanceStmt.Close()

	updateAddressBalanceStmt, err := ut.tx.Prepare(`UPDATE sia_addresses SET siacoin_balance=$1, immature_siacoin_balance=$2 WHERE id=$3`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer updateAddressBalanceStmt.Close()

	for addressID, delta := range balanceDelta {
		var balance, immatureBalance types.Currency
		err := getAddressBalanceStmt.QueryRow(addressID).Scan(decode(&balance), decode(&immatureBalance))
		if err != nil {
			return fmt.Errorf("failed to get address balance: %w", err)
		}

		balance = balance.Sub(delta)
		immatureBalance = immatureBalance.Add(delta)

		res, err := updateAddressBalanceStmt.Exec(encode(balance), encode(immatureBalance), addressID)
		if err != nil {
			return fmt.Errorf("failed to update address balance: %w", err)
		} else if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("failed to get rows affected: %w", err)
		} else if n != 1 {
			return fmt.Errorf("expected 1 row affected, got %v", n)
		}
	}
	return nil
}

func (ut *updateTx) AddSiacoinElements(elements []types.SiacoinElement, index types.ChainIndex) error {
	if len(elements) == 0 {
		return nil
	}

	addrStmt, err := insertAddressStatement(ut.tx)
	if err != nil {
		return fmt.Errorf("failed to prepare address statement: %w", err)
	}
	defer addrStmt.Close()

	// ignore elements already in the database.
	insertStmt, err := ut.tx.Prepare(`INSERT INTO siacoin_elements (id, siacoin_value, merkle_proof, leaf_index, maturity_height, address_id) VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (id) DO NOTHING RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare insert statement: %w", err)
	}
	defer insertStmt.Close()

	balanceChanges := make(map[int64]wallet.Balance)
	for _, se := range elements {
		addrRef, err := scanAddress(addrStmt.QueryRow(encode(se.SiacoinOutput.Address), encode(types.ZeroCurrency), 0))
		if err != nil {
			return fmt.Errorf("failed to query address: %w", err)
		} else if _, ok := balanceChanges[addrRef.ID]; !ok {
			balanceChanges[addrRef.ID] = addrRef.Balance
		}

		var dummyID types.Hash256
		err = insertStmt.QueryRow(encode(se.ID), encode(se.SiacoinOutput.Value), encodeSlice(se.MerkleProof), se.LeafIndex, se.MaturityHeight, addrRef.ID).Scan(decode(&dummyID))
		if errors.Is(err, sql.ErrNoRows) {
			continue // skip if the element already exists
		} else if err != nil {
			return fmt.Errorf("failed to execute statement: %w", err)
		}

		// update the balance if the element does not exist
		balance := balanceChanges[addrRef.ID]
		if se.MaturityHeight <= index.Height {
			balance.Siacoins = balance.Siacoins.Add(se.SiacoinOutput.Value)
		} else {
			balance.ImmatureSiacoins = balance.ImmatureSiacoins.Add(se.SiacoinOutput.Value)
		}
		balanceChanges[addrRef.ID] = balance
	}

	if len(balanceChanges) == 0 {
		return nil
	}

	updateAddressBalanceStmt, err := ut.tx.Prepare(`UPDATE sia_addresses SET siacoin_balance=$1, immature_siacoin_balance=$2 WHERE id=$3`)
	if err != nil {
		return fmt.Errorf("failed to prepare update balance statement: %w", err)
	}
	defer updateAddressBalanceStmt.Close()

	for addrID, balance := range balanceChanges {
		res, err := updateAddressBalanceStmt.Exec(encode(balance.Siacoins), encode(balance.ImmatureSiacoins), addrID)
		if err != nil {
			return fmt.Errorf("failed to update balance: %w", err)
		} else if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("failed to get rows affected: %w", err)
		} else if n != 1 {
			return fmt.Errorf("expected 1 row affected, got %v", n)
		}
	}
	return nil
}

func (ut *updateTx) RemoveSiacoinElements(elements []types.SiacoinElement, index types.ChainIndex) error {
	if len(elements) == 0 {
		return nil
	}

	addrStmt, err := insertAddressStatement(ut.tx)
	if err != nil {
		return fmt.Errorf("failed to prepare address statement: %w", err)
	}
	defer addrStmt.Close()

	stmt, err := ut.tx.Prepare(`DELETE FROM siacoin_elements WHERE id=$1 RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	balanceChanges := make(map[int64]wallet.Balance)
	for _, se := range elements {
		addrRef, err := scanAddress(addrStmt.QueryRow(encode(se.SiacoinOutput.Address), encode(types.ZeroCurrency), 0))
		if err != nil {
			return fmt.Errorf("failed to query address: %w", err)
		} else if _, ok := balanceChanges[addrRef.ID]; !ok {
			balanceChanges[addrRef.ID] = addrRef.Balance
		}

		var dummy types.Hash256
		err = stmt.QueryRow(encode(se.ID)).Scan(decode(&dummy))
		if err != nil {
			return fmt.Errorf("failed to delete element %q: %w", se.ID, err)
		}

		balance := balanceChanges[addrRef.ID]
		if se.MaturityHeight < index.Height {
			balance.Siacoins = balance.Siacoins.Sub(se.SiacoinOutput.Value)
		} else {
			balance.ImmatureSiacoins = balance.ImmatureSiacoins.Sub(se.SiacoinOutput.Value)
		}
		balanceChanges[addrRef.ID] = balance
	}

	if len(balanceChanges) == 0 {
		return nil
	}

	updateAddressBalanceStmt, err := ut.tx.Prepare(`UPDATE sia_addresses SET siacoin_balance=$1, immature_siacoin_balance=$2 WHERE id=$3`)
	if err != nil {
		return fmt.Errorf("failed to prepare update balance statement: %w", err)
	}
	defer updateAddressBalanceStmt.Close()

	for addrID, balance := range balanceChanges {
		res, err := updateAddressBalanceStmt.Exec(encode(balance.Siacoins), encode(balance.ImmatureSiacoins), addrID)
		if err != nil {
			return fmt.Errorf("failed to update balance: %w", err)
		} else if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("failed to get rows affected: %w", err)
		} else if n != 1 {
			return fmt.Errorf("expected 1 row affected, got %v", n)
		}
	}
	return nil
}

func (ut *updateTx) AddSiafundElements(elements []types.SiafundElement, index types.ChainIndex) error {
	if len(elements) == 0 {
		return nil
	}

	addrStmt, err := insertAddressStatement(ut.tx)
	if err != nil {
		return fmt.Errorf("failed to prepare address statement: %w", err)
	}
	defer addrStmt.Close()

	insertStmt, err := ut.tx.Prepare(`INSERT INTO siafund_elements (id, siafund_value, merkle_proof, leaf_index, claim_start, address_id) VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (id) DO NOTHING RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer insertStmt.Close()

	balanceChanges := make(map[int64]uint64)
	for _, se := range elements {
		addrRef, err := scanAddress(addrStmt.QueryRow(encode(se.SiafundOutput.Address), encode(types.ZeroCurrency), 0))
		if err != nil {
			return fmt.Errorf("failed to query address: %w", err)
		} else if _, ok := balanceChanges[addrRef.ID]; !ok {
			balanceChanges[addrRef.ID] = addrRef.Balance.Siafunds
		}

		var dummy types.Hash256
		err = insertStmt.QueryRow(encode(se.ID), se.SiafundOutput.Value, encodeSlice(se.MerkleProof), se.LeafIndex, encode(se.ClaimStart), addrRef.ID).Scan(decode(&dummy))
		if errors.Is(err, sql.ErrNoRows) {
			continue // skip if the element already exists
		} else if err != nil {
			return fmt.Errorf("failed to execute statement: %w", err)
		}
		balanceChanges[addrRef.ID] += se.SiafundOutput.Value
	}

	if len(balanceChanges) == 0 {
		return nil
	}

	updateAddressBalanceStmt, err := ut.tx.Prepare(`UPDATE sia_addresses SET siafund_balance=$1 WHERE id=$2`)
	if err != nil {
		return fmt.Errorf("failed to prepare update balance statement: %w", err)
	}
	defer updateAddressBalanceStmt.Close()

	for addrID, balance := range balanceChanges {
		res, err := updateAddressBalanceStmt.Exec(balance, addrID)
		if err != nil {
			return fmt.Errorf("failed to update balance: %w", err)
		} else if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("failed to get rows affected: %w", err)
		} else if n != 1 {
			return fmt.Errorf("expected 1 row affected, got %v", n)
		}
	}
	return nil
}

func (ut *updateTx) RemoveSiafundElements(elements []types.SiafundElement, index types.ChainIndex) error {
	addrStmt, err := insertAddressStatement(ut.tx)
	if err != nil {
		return fmt.Errorf("failed to prepare address statement: %w", err)
	}
	defer addrStmt.Close()

	stmt, err := ut.tx.Prepare(`DELETE FROM siacoin_elements WHERE id=$1 RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	balanceChanges := make(map[int64]uint64)
	for _, se := range elements {
		addrRef, err := scanAddress(addrStmt.QueryRow(encode(se.SiafundOutput.Address), encode(types.ZeroCurrency), 0))
		if err != nil {
			return fmt.Errorf("failed to query address: %w", err)
		} else if _, ok := balanceChanges[addrRef.ID]; !ok {
			balanceChanges[addrRef.ID] = addrRef.Balance.Siafunds
		}

		var dummy types.Hash256
		err = stmt.QueryRow(encode(se.ID)).Scan(decode(&dummy))
		if err != nil {
			return fmt.Errorf("failed to delete element %q: %w", se.ID, err)
		}

		if balanceChanges[addrRef.ID] < se.SiafundOutput.Value {
			panic("siafund balance cannot be negative")
		}
		balanceChanges[addrRef.ID] -= se.SiafundOutput.Value
	}

	if len(balanceChanges) == 0 {
		return nil
	}

	updateAddressBalanceStmt, err := ut.tx.Prepare(`UPDATE sia_addresses SET siafund_balance=$1 WHERE id=$2`)
	if err != nil {
		return fmt.Errorf("failed to prepare update balance statement: %w", err)
	}
	defer updateAddressBalanceStmt.Close()

	for addrID, balance := range balanceChanges {
		res, err := updateAddressBalanceStmt.Exec(balance, addrID)
		if err != nil {
			return fmt.Errorf("failed to update balance: %w", err)
		} else if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("failed to get rows affected: %w", err)
		} else if n != 1 {
			return fmt.Errorf("expected 1 row affected, got %v", n)
		}
	}
	return nil
}

func (ut *updateTx) AddEvents(events []wallet.Event) error {
	if len(events) == 0 {
		return nil
	}

	indexStmt, err := insertIndexStmt(ut.tx)
	if err != nil {
		return fmt.Errorf("failed to prepare index statement: %w", err)
	}
	defer indexStmt.Close()

	insertEventStmt, err := ut.tx.Prepare(`INSERT INTO events (event_id, maturity_height, date_created, index_id, event_type, event_data) VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (event_id) DO NOTHING RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare event statement: %w", err)
	}
	defer insertEventStmt.Close()

	addrStmt, err := ut.tx.Prepare(`INSERT INTO sia_addresses (sia_address, siacoin_balance, immature_siacoin_balance, siafund_balance) VALUES ($1, $2, $3, 0) ON CONFLICT (sia_address) DO UPDATE SET sia_address=EXCLUDED.sia_address RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare address statement: %w", err)
	}
	defer addrStmt.Close()

	relevantAddrStmt, err := ut.tx.Prepare(`INSERT INTO event_addresses (event_id, address_id) VALUES ($1, $2) ON CONFLICT (event_id, address_id) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("failed to prepare relevant address statement: %w", err)
	}
	defer addrStmt.Close()

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, event := range events {
		var chainIndexID int64
		err := indexStmt.QueryRow(event.Index.Height, encode(event.Index.ID)).Scan(&chainIndexID)
		if err != nil {
			return fmt.Errorf("failed to execute statement: %w", err)
		}

		buf.Reset()
		if err := enc.Encode(event.Data); err != nil {
			return fmt.Errorf("failed to encode event: %w", err)
		}

		var eventID int64
		err = insertEventStmt.QueryRow(encode(event.ID), event.MaturityHeight, encode(event.Timestamp), chainIndexID, event.Data.EventType(), buf.String()).Scan(&eventID)
		if errors.Is(err, sql.ErrNoRows) {
			continue // skip if the event already exists
		} else if err != nil {
			return fmt.Errorf("failed to add event: %w", err)
		}

		used := make(map[types.Address]bool)
		for _, addr := range event.Relevant {
			if used[addr] {
				continue
			}

			var addressID int64
			err = addrStmt.QueryRow(encode(addr), encode(types.ZeroCurrency), 0).Scan(&addressID)
			if err != nil {
				return fmt.Errorf("failed to get address: %w", err)
			}

			_, err = relevantAddrStmt.Exec(eventID, addressID)
			if err != nil {
				return fmt.Errorf("failed to add relevant address: %w", err)
			}

			used[addr] = true
		}
	}
	return nil
}

// RevertEvents reverts any events that were added by the index
func (ut *updateTx) RevertEvents(index types.ChainIndex) error {
	var id int64
	err := ut.tx.QueryRow(`DELETE FROM chain_indices WHERE block_id=$1 AND height=$2 RETURNING id`, encode(index.ID), index.Height).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

// ProcessChainApplyUpdate implements chain.Subscriber
func (s *Store) UpdateChainState(reverted []chain.RevertUpdate, applied []chain.ApplyUpdate) error {
	return s.transaction(func(tx *txn) error {
		utx := &updateTx{
			tx:                tx,
			relevantAddresses: make(map[types.Address]bool),
		}

		if err := wallet.UpdateChainState(utx, reverted, applied); err != nil {
			return fmt.Errorf("failed to update chain state: %w", err)
		} else if err := setLastCommittedIndex(tx, applied[len(applied)-1].State.Index); err != nil {
			return fmt.Errorf("failed to set last committed index: %w", err)
		}
		return nil
	})
}

// LastCommittedIndex returns the last chain index that was committed.
func (s *Store) LastCommittedIndex() (index types.ChainIndex, err error) {
	err = s.db.QueryRow(`SELECT last_indexed_tip FROM global_settings`).Scan(decode(&index))
	return
}

func setLastCommittedIndex(tx *txn, index types.ChainIndex) error {
	_, err := tx.Exec(`UPDATE global_settings SET last_indexed_tip=$1`, encode(index))
	return err
}

func insertAddressStatement(tx *txn) (*stmt, error) {
	return tx.Prepare(`INSERT INTO sia_addresses (sia_address, siacoin_balance, immature_siacoin_balance, siafund_balance) VALUES ($1, $2, $2, $3) ON CONFLICT (sia_address) DO UPDATE SET sia_address=EXCLUDED.sia_address RETURNING id, siacoin_balance, immature_siacoin_balance, siafund_balance`)
}

func insertIndexStmt(tx *txn) (*stmt, error) {
	return tx.Prepare(`INSERT INTO chain_indices (height, block_id) VALUES ($1, $2) ON CONFLICT (block_id) DO UPDATE SET height=EXCLUDED.height RETURNING id`)
}
