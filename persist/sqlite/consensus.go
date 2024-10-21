package sqlite

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/walletd/wallet"
	"go.uber.org/zap"
)

type updateTx struct {
	indexMode wallet.IndexMode

	tx                *txn
	relevantAddresses map[types.Address]bool
}

type addressRef struct {
	ID      int64
	Balance wallet.Balance
}

func (ut *updateTx) SiacoinStateElements() ([]types.StateElement, error) {
	if ut.indexMode == wallet.IndexModeFull {
		panic("SiacoinStateElements called in full index mode")
	}

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
	if ut.indexMode == wallet.IndexModeFull {
		panic("UpdateSiacoinStateElements called in full index mode")
	}

	log := ut.tx.log.Named("UpdateSiacoinStateElements")
	log.Debug("updating siacoin state elements", zap.Int("count", len(elements)))

	const query = `UPDATE siacoin_elements SET merkle_proof=$1, leaf_index=$2 WHERE id=$3 RETURNING id`
	stmt, err := ut.tx.Prepare(query)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, se := range elements {
		var dummy types.Hash256
		err := stmt.QueryRow(encode(se.MerkleProof), se.LeafIndex, encode(se.ID)).Scan(decode(&dummy))
		if err != nil {
			return fmt.Errorf("failed to execute statement: %w", err)
		}
		log.Debug("updated element proof", zap.Stringer("id", se.ID), zap.Uint64("leafIndex", se.LeafIndex))
	}
	return nil
}

func (ut *updateTx) SiafundStateElements() ([]types.StateElement, error) {
	if ut.indexMode == wallet.IndexModeFull {
		panic("SiafundStateElements called in full index mode")
	}

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
	if ut.indexMode == wallet.IndexModeFull {
		panic("UpdateSiafundStateElements called in full index mode")
	}

	const query = `UPDATE siafund_elements SET merkle_proof=$1, leaf_index=$2 WHERE id=$3 RETURNING id`
	stmt, err := ut.tx.Prepare(query)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, se := range elements {
		var dummy types.Hash256
		err := stmt.QueryRow(encode(se.MerkleProof), se.LeafIndex, encode(se.ID)).Scan(decode(&dummy))
		if err != nil {
			return fmt.Errorf("failed to execute statement: %w", err)
		}
	}
	return nil
}

func (ut *updateTx) UpdateStateTree(changes []wallet.TreeNodeUpdate) error {
	if ut.indexMode != wallet.IndexModeFull {
		panic("UpdateStateTree called in personal index mode")
	}

	stmt, err := ut.tx.Prepare(`INSERT INTO state_tree (row, column, value) VALUES ($1, $2, $3) ON CONFLICT (row, column) DO UPDATE SET value=EXCLUDED.value`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, change := range changes {
		_, err := stmt.Exec(change.Row, change.Column, encode(change.Hash))
		if err != nil {
			return fmt.Errorf("failed to execute statement: %w", err)
		}
	}
	return nil
}

func (ut *updateTx) AddressRelevant(addr types.Address) (bool, error) {
	if ut.indexMode == wallet.IndexModeFull {
		return true, nil
	}

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

func (ut *updateTx) ApplyIndex(index types.ChainIndex, state wallet.AppliedState) error {
	tx := ut.tx
	log := tx.log.Named("ApplyIndex").With(zap.Stringer("blockID", index.ID), zap.Uint64("height", index.Height))

	if err := revertOrphans(tx, index, log.Named("revertOrphans")); err != nil {
		return fmt.Errorf("failed to revert orphans: %w", err)
	}

	if err := applyMatureSiacoinBalance(tx, index, log.Named("applyMatureSiacoinBalance")); err != nil {
		return fmt.Errorf("failed to apply mature siacoin balance: %w", err)
	}

	var indexID int64
	if err := tx.QueryRow(`INSERT INTO chain_indices (block_id, height) VALUES ($1, $2) ON CONFLICT (block_id) DO UPDATE SET height=height RETURNING id`, encode(index.ID), index.Height).Scan(&indexID); err != nil {
		return fmt.Errorf("failed to insert chain index: %w", err)
	}

	if err := spendSiacoinElements(tx, state.SpentSiacoinElements, indexID); err != nil {
		return fmt.Errorf("failed to spend siacoin elements: %w", err)
	} else if err := addSiacoinElements(tx, state.CreatedSiacoinElements, indexID, ut.indexMode, log.Named("addSiacoinElements")); err != nil {
		return fmt.Errorf("failed to add siacoin elements: %w", err)
	}

	if err := spendSiafundElements(tx, state.SpentSiafundElements, indexID); err != nil {
		return fmt.Errorf("failed to spend siafund elements: %w", err)
	} else if err := addSiafundElements(tx, state.CreatedSiafundElements, indexID, ut.indexMode, log.Named("addSiafundElements")); err != nil {
		return fmt.Errorf("failed to add siafund elements: %w", err)
	}

	if err := addEvents(tx, state.Events, indexID); err != nil {
		return fmt.Errorf("failed to add events: %w", err)
	}
	return nil
}

func (ut *updateTx) RevertIndex(index types.ChainIndex, state wallet.RevertedState) error {
	tx := ut.tx

	if err := revertSpentSiacoinElements(tx, state.UnspentSiacoinElements); err != nil {
		return fmt.Errorf("failed to revert spent siacoin elements: %w", err)
	} else if err := removeSiacoinElements(tx, state.DeletedSiacoinElements); err != nil {
		return fmt.Errorf("failed to remove siacoin elements: %w", err)
	}

	if err := revertSpentSiafundElements(tx, state.UnspentSiafundElements); err != nil {
		return fmt.Errorf("failed to revert spent siafund elements: %w", err)
	} else if err := removeSiafundElements(tx, state.DeletedSiafundElements); err != nil {
		return fmt.Errorf("failed to remove siafund elements: %w", err)
	}

	if err := revertEvents(tx, index); err != nil {
		return fmt.Errorf("failed to revert events: %w", err)
	} else if err := revertMatureSiacoinBalance(tx, index); err != nil {
		return fmt.Errorf("failed to revert mature siacoin balance: %w", err)
	}
	return nil
}

// UpdateChainState implements chain.Subscriber
func (s *Store) UpdateChainState(reverted []chain.RevertUpdate, applied []chain.ApplyUpdate) error {
	if len(applied) == 0 && len(reverted) == 0 {
		return nil
	}

	log := s.log.Named("UpdateChainState").With(zap.Int("revertedUpdates", len(reverted)), zap.Int("appliedUpdates", len(applied)))
	return s.transaction(func(tx *txn) error {
		utx := &updateTx{
			indexMode: s.indexMode,

			tx:                tx,
			relevantAddresses: make(map[types.Address]bool),
		}

		if err := wallet.UpdateChainState(utx, reverted, applied, s.indexMode, log); err != nil {
			return err
		}

		var state consensus.State
		switch {
		case len(applied) > 0:
			state = applied[len(applied)-1].State
		case len(reverted) > 0:
			state = reverted[len(reverted)-1].State
		}

		if err := setGlobalState(tx, state.Index, state.Elements.NumLeaves); err != nil {
			return fmt.Errorf("failed to set last committed index: %w", err)
		}

		// skip pruning if there are no applied updates
		if len(applied) == 0 {
			return nil
		}

		if state.Index.Height > spentElementRetentionBlocks {
			pruneHeight := state.Index.Height - spentElementRetentionBlocks

			siacoins, err := pruneSpentSiacoinElements(tx, pruneHeight)
			if err != nil {
				return fmt.Errorf("failed to cleanup siacoin elements: %w", err)
			}

			siafunds, err := pruneSpentSiafundElements(tx, pruneHeight)
			if err != nil {
				return fmt.Errorf("failed to cleanup siafund elements: %w", err)
			}
			log.Debug("pruned elements", zap.Int64("siacoins", siacoins), zap.Int64("siafunds", siafunds), zap.Uint64("pruneHeight", pruneHeight))
		}
		return nil
	})
}

// LastCommittedIndex returns the last chain index that was committed.
func (s *Store) LastCommittedIndex() (index types.ChainIndex, err error) {
	err = s.db.QueryRow(`SELECT last_indexed_height, last_indexed_id FROM global_settings`).Scan(&index.Height, decode(&index.ID))
	return
}

// ResetLastIndex resets the last indexed tip to trigger a full rescan.
func (s *Store) ResetLastIndex() error {
	_, err := s.db.Exec(`UPDATE global_settings SET last_indexed_height=0, last_indexed_id=$1`, encode(types.BlockID{}))
	return err
}

// IndexMode returns the current index mode.
func (s *Store) IndexMode() (wallet.IndexMode, error) {
	var mode wallet.IndexMode
	err := s.db.QueryRow(`SELECT index_mode FROM global_settings`).Scan(&mode)
	return mode, err
}

// SetIndexMode sets the index mode. If the index mode is already set, this
// function will return an error.
func (s *Store) SetIndexMode(mode wallet.IndexMode) error {
	return s.transaction(func(tx *txn) error {
		_, err := tx.Exec(`UPDATE global_settings SET index_mode=$1 WHERE index_mode IS NULL`, mode)
		if err != nil {
			return fmt.Errorf("failed to set index mode: %w", err)
		}

		// check that the index mode was set
		var existingMode wallet.IndexMode
		err = tx.QueryRow(`SELECT index_mode FROM global_settings`).Scan(&existingMode)
		if err != nil {
			return fmt.Errorf("failed to query index mode: %w", err)
		} else if existingMode != mode {
			return fmt.Errorf("cannot change index mode from %v to %v", existingMode, mode)
		}
		s.indexMode = mode // this is a bit annoying
		return nil
	})
}

func scanStateElement(s scanner) (se types.StateElement, err error) {
	err = s.Scan(decode(&se.ID), &se.LeafIndex, decode(&se.MerkleProof))
	return
}

func scanAddress(s scanner) (ab addressRef, err error) {
	err = s.Scan(&ab.ID, decode(&ab.Balance.Siacoins), decode(&ab.Balance.ImmatureSiacoins), &ab.Balance.Siafunds)
	return
}

func applyMatureSiacoinBalance(tx *txn, index types.ChainIndex, log *zap.Logger) error {
	log = log.With(zap.Uint64("maturityHeight", index.Height))
	const query = `SELECT id, address_id, siacoin_value
FROM siacoin_elements
WHERE maturity_height=$1 AND matured=false AND spent_index_id IS NULL`
	rows, err := tx.Query(query, index.Height)
	if err != nil {
		return fmt.Errorf("failed to query siacoin elements: %w", err)
	}
	defer rows.Close()

	var matured []types.SiacoinOutputID
	balanceDelta := make(map[int64]types.Currency)
	for rows.Next() {
		var outputID types.SiacoinOutputID
		var addressID int64
		var value types.Currency

		if err := rows.Scan(decode(&outputID), &addressID, decode(&value)); err != nil {
			return fmt.Errorf("failed to scan siacoin balance: %w", err)
		}
		balanceDelta[addressID] = balanceDelta[addressID].Add(value)
		matured = append(matured, outputID)
		log.Debug("matured siacoin output", zap.Stringer("outputID", outputID), zap.Int64("addressID", addressID), zap.Stringer("value", value))
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to scan siacoin elements: %w", err)
	}

	updateMaturedStmt, err := tx.Prepare(`UPDATE siacoin_elements SET matured=true WHERE id=$1`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer updateMaturedStmt.Close()

	getAddressBalanceStmt, err := tx.Prepare(`SELECT siacoin_balance, immature_siacoin_balance FROM sia_addresses WHERE id=$1`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer getAddressBalanceStmt.Close()

	updateAddressBalanceStmt, err := tx.Prepare(`UPDATE sia_addresses SET siacoin_balance=$1, immature_siacoin_balance=$2 WHERE id=$3`)
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

	for _, id := range matured {
		res, err := updateMaturedStmt.Exec(encode(id))
		if err != nil {
			return fmt.Errorf("failed to update matured: %w", err)
		} else if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("failed to get rows affected: %w", err)
		} else if n != 1 {
			return fmt.Errorf("expected 1 row affected, got %v", n)
		}
	}
	return nil
}

func revertMatureSiacoinBalance(tx *txn, index types.ChainIndex) error {
	const query = `SELECT se.id, se.address_id, se.siacoin_value
	FROM siacoin_elements se
	WHERE maturity_height=$1 AND matured=true AND spent_index_id IS NULL`
	rows, err := tx.Query(query, index.Height)
	if err != nil {
		return fmt.Errorf("failed to query siacoin elements: %w", err)
	}
	defer rows.Close()

	var matured []types.SiacoinOutputID
	balanceDelta := make(map[int64]types.Currency)
	for rows.Next() {
		var outputID types.SiacoinOutputID
		var addressID int64
		var value types.Currency

		if err := rows.Scan(decode(&outputID), &addressID, decode(&value)); err != nil {
			return fmt.Errorf("failed to scan siacoin balance: %w", err)
		}
		balanceDelta[addressID] = balanceDelta[addressID].Add(value)
		matured = append(matured, outputID)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to scan siacoin elements: %w", err)
	}

	updateMaturedStmt, err := tx.Prepare(`UPDATE siacoin_elements SET matured=false WHERE id=$1`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer updateMaturedStmt.Close()

	getAddressBalanceStmt, err := tx.Prepare(`SELECT siacoin_balance, immature_siacoin_balance FROM sia_addresses WHERE id=$1`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer getAddressBalanceStmt.Close()

	updateAddressBalanceStmt, err := tx.Prepare(`UPDATE sia_addresses SET siacoin_balance=$1, immature_siacoin_balance=$2 WHERE id=$3`)
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

	for _, id := range matured {
		res, err := updateMaturedStmt.Exec(encode(id))
		if err != nil {
			return fmt.Errorf("failed to update matured: %w", err)
		} else if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("failed to get rows affected: %w", err)
		} else if n != 1 {
			return fmt.Errorf("expected 1 row affected, got %v", n)
		}
	}
	return nil
}

func addSiacoinElements(tx *txn, elements []types.SiacoinElement, indexID int64, indexMode wallet.IndexMode, log *zap.Logger) error {
	if len(elements) == 0 {
		return nil
	}

	addressRefStmt, done, err := addressRefStmt(tx)
	if err != nil {
		return fmt.Errorf("failed to prepare address statement: %w", err)
	}
	defer done()

	existsStmt, err := tx.Prepare(`SELECT EXISTS(SELECT 1 FROM siacoin_elements WHERE id=$1)`)
	if err != nil {
		return fmt.Errorf("failed to prepare exists statement: %w", err)
	}
	defer existsStmt.Close()

	// ignore elements already in the database.
	insertStmt, err := tx.Prepare(`INSERT INTO siacoin_elements (id, siacoin_value, merkle_proof, leaf_index, maturity_height, address_id, matured, chain_index_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (id) DO UPDATE SET leaf_index=EXCLUDED.leaf_index, merkle_proof=EXCLUDED.merkle_proof`)
	if err != nil {
		return fmt.Errorf("failed to prepare insert statement: %w", err)
	}
	defer insertStmt.Close()

	balanceChanges := make(map[int64]wallet.Balance)
	for _, se := range elements {
		addrRef, err := addressRefStmt(se.SiacoinOutput.Address)
		if err != nil {
			return fmt.Errorf("failed to query address: %w", err)
		} else if _, ok := balanceChanges[addrRef.ID]; !ok {
			balanceChanges[addrRef.ID] = addrRef.Balance
		}

		var exists bool
		err = existsStmt.QueryRow(encode(se.ID)).Scan(&exists)
		if err != nil {
			return fmt.Errorf("failed to check if siacoin element exists: %w", err)
		}

		// in full index mode, Merkle proofs are stored in the state tree table
		// rather than per element.
		if indexMode == wallet.IndexModeFull {
			se.MerkleProof = nil
		}

		_, err = insertStmt.Exec(encode(se.ID), encode(se.SiacoinOutput.Value), encode(se.MerkleProof), se.LeafIndex, se.MaturityHeight, addrRef.ID, se.MaturityHeight == 0, indexID)
		if err != nil {
			return fmt.Errorf("failed to execute statement: %w", err)
		}
		// skip balance update if the element already exists
		if exists {
			log.Debug("updated siacoin element", zap.Stringer("id", se.ID), zap.Stringer("address", se.SiacoinOutput.Address), zap.Stringer("value", se.SiacoinOutput.Value))
			continue
		}

		balance := balanceChanges[addrRef.ID]
		if se.MaturityHeight == 0 {
			balance.Siacoins = balance.Siacoins.Add(se.SiacoinOutput.Value)
			log.Debug("added siacoin output", zap.Stringer("id", se.ID), zap.Stringer("address", se.SiacoinOutput.Address), zap.Stringer("value", se.SiacoinOutput.Value))
		} else {
			balance.ImmatureSiacoins = balance.ImmatureSiacoins.Add(se.SiacoinOutput.Value)
			log.Debug("added immature siacoin output", zap.Stringer("id", se.ID), zap.Stringer("address", se.SiacoinOutput.Address), zap.Stringer("value", se.SiacoinOutput.Value), zap.Uint64("maturityHeight", se.MaturityHeight))
		}
		balanceChanges[addrRef.ID] = balance
	}

	if len(balanceChanges) == 0 {
		return nil
	}

	updateAddressBalanceStmt, err := tx.Prepare(`UPDATE sia_addresses SET siacoin_balance=$1, immature_siacoin_balance=$2 WHERE id=$3`)
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

func removeSiacoinElements(tx *txn, elements []types.SiacoinElement) error {
	if len(elements) == 0 {
		return nil
	}

	addressRefStmt, done, err := addressRefStmt(tx)
	if err != nil {
		return fmt.Errorf("failed to prepare address statement: %w", err)
	}
	defer done()

	stmt, err := tx.Prepare(`DELETE FROM siacoin_elements WHERE id=$1 RETURNING id, matured`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	balanceChanges := make(map[int64]wallet.Balance)
	for _, se := range elements {
		addrRef, err := addressRefStmt(se.SiacoinOutput.Address)
		if err != nil {
			return fmt.Errorf("failed to query address: %w", err)
		} else if _, ok := balanceChanges[addrRef.ID]; !ok {
			balanceChanges[addrRef.ID] = addrRef.Balance
		}

		var dummy types.Hash256
		var matured bool
		err = stmt.QueryRow(encode(se.ID)).Scan(decode(&dummy), &matured)
		if err != nil {
			return fmt.Errorf("failed to delete element %q: %w", se.ID, err)
		}

		balance := balanceChanges[addrRef.ID]
		if matured {
			balance.Siacoins = balance.Siacoins.Sub(se.SiacoinOutput.Value)
		} else {
			balance.ImmatureSiacoins = balance.ImmatureSiacoins.Sub(se.SiacoinOutput.Value)
		}
		balanceChanges[addrRef.ID] = balance
	}

	if len(balanceChanges) == 0 {
		return nil
	}

	updateAddressBalanceStmt, err := tx.Prepare(`UPDATE sia_addresses SET siacoin_balance=$1, immature_siacoin_balance=$2 WHERE id=$3`)
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

func revertSpentSiacoinElements(tx *txn, elements []types.SiacoinElement) error {
	if len(elements) == 0 {
		return nil
	}

	addressRefStmt, done, err := addressRefStmt(tx)
	if err != nil {
		return fmt.Errorf("failed to prepare address statement: %w", err)
	}
	defer done()

	stmt, err := tx.Prepare(`UPDATE siacoin_elements SET spent_index_id=NULL WHERE id=$1 AND spent_index_id IS NOT NULL RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	balanceChanges := make(map[int64]wallet.Balance)
	for _, se := range elements {
		addrRef, err := addressRefStmt(se.SiacoinOutput.Address)
		if err != nil {
			return fmt.Errorf("failed to query address: %w", err)
		} else if _, ok := balanceChanges[addrRef.ID]; !ok {
			balanceChanges[addrRef.ID] = addrRef.Balance
		}

		var dummy types.Hash256
		if err := stmt.QueryRow(encode(se.ID)).Scan(decode(&dummy)); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		} else if errors.Is(err, sql.ErrNoRows) {
			continue // skip if the element does not exist
		}

		balance := balanceChanges[addrRef.ID]
		balance.Siacoins = balance.Siacoins.Add(se.SiacoinOutput.Value)
		balanceChanges[addrRef.ID] = balance
	}

	if len(balanceChanges) == 0 {
		return nil
	}

	updateAddressBalanceStmt, err := tx.Prepare(`UPDATE sia_addresses SET siacoin_balance=$1 WHERE id=$2`)
	if err != nil {
		return fmt.Errorf("failed to prepare update balance statement: %w", err)
	}
	defer updateAddressBalanceStmt.Close()

	for addrID, balance := range balanceChanges {
		res, err := updateAddressBalanceStmt.Exec(encode(balance.Siacoins), addrID)
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

func spendSiacoinElements(tx *txn, elements []types.SiacoinElement, indexID int64) error {
	if len(elements) == 0 {
		return nil
	}

	addressRefStmt, done, err := addressRefStmt(tx)
	if err != nil {
		return fmt.Errorf("failed to prepare address statement: %w", err)
	}
	defer done()

	stmt, err := tx.Prepare(`UPDATE siacoin_elements SET spent_index_id=$1 WHERE id=$2 AND spent_index_id IS NULL RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	balanceChanges := make(map[int64]wallet.Balance)
	for _, se := range elements {
		addrRef, err := addressRefStmt(se.SiacoinOutput.Address)
		if err != nil {
			return fmt.Errorf("failed to query address: %w", err)
		} else if _, ok := balanceChanges[addrRef.ID]; !ok {
			balanceChanges[addrRef.ID] = addrRef.Balance
		}

		var dummy types.Hash256
		if err := stmt.QueryRow(indexID, encode(se.ID)).Scan(decode(&dummy)); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		} else if errors.Is(err, sql.ErrNoRows) {
			continue // skip if the element does not exist
		}

		balance := balanceChanges[addrRef.ID]
		balance.Siacoins = balance.Siacoins.Sub(se.SiacoinOutput.Value)
		balanceChanges[addrRef.ID] = balance
	}

	if len(balanceChanges) == 0 {
		return nil
	}

	updateAddressBalanceStmt, err := tx.Prepare(`UPDATE sia_addresses SET siacoin_balance=$1 WHERE id=$2`)
	if err != nil {
		return fmt.Errorf("failed to prepare update balance statement: %w", err)
	}
	defer updateAddressBalanceStmt.Close()

	for addrID, balance := range balanceChanges {
		res, err := updateAddressBalanceStmt.Exec(encode(balance.Siacoins), addrID)
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

func addSiafundElements(tx *txn, elements []types.SiafundElement, indexID int64, indexMode wallet.IndexMode, log *zap.Logger) error {
	if len(elements) == 0 {
		return nil
	}

	addressRefStmt, done, err := addressRefStmt(tx)
	if err != nil {
		return fmt.Errorf("failed to prepare address statement: %w", err)
	}
	defer done()

	existsStmt, err := tx.Prepare(`SELECT EXISTS(SELECT 1 FROM siafund_elements WHERE id=$1)`)
	if err != nil {
		return fmt.Errorf("failed to prepare exists statement: %w", err)
	}
	defer existsStmt.Close()

	insertStmt, err := tx.Prepare(`INSERT INTO siafund_elements (id, siafund_value, merkle_proof, leaf_index, claim_start, address_id, chain_index_id) VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (id) DO UPDATE SET leaf_index=EXCLUDED.leaf_index, merkle_proof=EXCLUDED.merkle_proof`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer insertStmt.Close()

	balanceChanges := make(map[int64]uint64)
	for _, se := range elements {
		addrRef, err := addressRefStmt(se.SiafundOutput.Address)
		if err != nil {
			return fmt.Errorf("failed to query address: %w", err)
		} else if _, ok := balanceChanges[addrRef.ID]; !ok {
			balanceChanges[addrRef.ID] = addrRef.Balance.Siafunds
		}

		var exists bool
		if err := existsStmt.QueryRow(encode(se.ID)).Scan(&exists); err != nil {
			return fmt.Errorf("failed to check if siafund element exists: %w", err)
		}

		// in full index mode, Merkle proofs are stored in the state tree table
		// rather than per element.
		if indexMode == wallet.IndexModeFull {
			se.MerkleProof = nil
		}

		_, err = insertStmt.Exec(encode(se.ID), se.SiafundOutput.Value, encode(se.MerkleProof), se.LeafIndex, encode(se.ClaimStart), addrRef.ID, indexID)
		if err != nil {
			return fmt.Errorf("failed to execute statement: %w", err)
		} else if exists {
			// skip balance update if the element already exists
			log.Debug("updated siafund element", zap.Stringer("id", se.ID), zap.Stringer("address", se.SiafundOutput.Address), zap.Uint64("value", se.SiafundOutput.Value))
			continue
		}
		balanceChanges[addrRef.ID] += se.SiafundOutput.Value
	}

	if len(balanceChanges) == 0 {
		return nil
	}

	updateAddressBalanceStmt, err := tx.Prepare(`UPDATE sia_addresses SET siafund_balance=$1 WHERE id=$2`)
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

func removeSiafundElements(tx *txn, elements []types.SiafundElement) error {
	if len(elements) == 0 {
		return nil
	}

	addressRefStmt, done, err := addressRefStmt(tx)
	if err != nil {
		return fmt.Errorf("failed to prepare address statement: %w", err)
	}
	defer done()

	stmt, err := tx.Prepare(`DELETE FROM siafund_elements WHERE id=$1 RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	balanceChanges := make(map[int64]uint64)
	for _, se := range elements {
		addrRef, err := addressRefStmt(se.SiafundOutput.Address)
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

	updateAddressBalanceStmt, err := tx.Prepare(`UPDATE sia_addresses SET siafund_balance=$1 WHERE id=$2`)
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

func spendSiafundElements(tx *txn, elements []types.SiafundElement, indexID int64) error {
	if len(elements) == 0 {
		return nil
	}

	addressRefStmt, done, err := addressRefStmt(tx)
	if err != nil {
		return fmt.Errorf("failed to prepare address statement: %w", err)
	}
	defer done()

	stmt, err := tx.Prepare(`UPDATE siafund_elements SET spent_index_id=$1 WHERE id=$2 AND spent_index_id IS NULL RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	balanceChanges := make(map[int64]wallet.Balance)
	for _, se := range elements {
		addrRef, err := addressRefStmt(se.SiafundOutput.Address)
		if err != nil {
			return fmt.Errorf("failed to query address: %w", err)
		} else if _, ok := balanceChanges[addrRef.ID]; !ok {
			balanceChanges[addrRef.ID] = addrRef.Balance
		}

		var dummy types.Hash256
		if err := stmt.QueryRow(indexID, encode(se.ID)).Scan(decode(&dummy)); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		} else if errors.Is(err, sql.ErrNoRows) {
			continue // skip if the element does not exist
		}

		balance := balanceChanges[addrRef.ID]
		if balance.Siafunds < se.SiafundOutput.Value {
			panic("siafund balance cannot be negative")
		}
		balance.Siafunds -= se.SiafundOutput.Value

		balanceChanges[addrRef.ID] = balance
	}

	if len(balanceChanges) == 0 {
		return nil
	}

	updateAddressBalanceStmt, err := tx.Prepare(`UPDATE sia_addresses SET siafund_balance=$1 WHERE id=$3`)
	if err != nil {
		return fmt.Errorf("failed to prepare update balance statement: %w", err)
	}
	defer updateAddressBalanceStmt.Close()

	for addrID, balance := range balanceChanges {
		res, err := updateAddressBalanceStmt.Exec(balance.Siafunds, addrID)
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

func revertSpentSiafundElements(tx *txn, elements []types.SiafundElement) error {
	if len(elements) == 0 {
		return nil
	}

	addressRefStmt, done, err := addressRefStmt(tx)
	if err != nil {
		return fmt.Errorf("failed to prepare address statement: %w", err)
	}
	defer done()

	stmt, err := tx.Prepare(`UPDATE siafund_elements SET spent_index_id=NULL WHERE id=$1 AND spent_index_id IS NOT NULL RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	balanceChanges := make(map[int64]wallet.Balance)
	for _, se := range elements {
		addrRef, err := addressRefStmt(se.SiafundOutput.Address)
		if err != nil {
			return fmt.Errorf("failed to query address: %w", err)
		} else if _, ok := balanceChanges[addrRef.ID]; !ok {
			balanceChanges[addrRef.ID] = addrRef.Balance
		}

		var dummy types.Hash256
		if err := stmt.QueryRow(encode(se.ID)).Scan(decode(&dummy)); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		} else if errors.Is(err, sql.ErrNoRows) {
			continue // skip if the element does not exist
		}

		balance := balanceChanges[addrRef.ID]
		balance.Siafunds += se.SiafundOutput.Value
		balanceChanges[addrRef.ID] = balance
	}

	if len(balanceChanges) == 0 {
		return nil
	}

	updateAddressBalanceStmt, err := tx.Prepare(`UPDATE sia_addresses SET siafund_balance=$1 WHERE id=$3`)
	if err != nil {
		return fmt.Errorf("failed to prepare update balance statement: %w", err)
	}
	defer updateAddressBalanceStmt.Close()

	for addrID, balance := range balanceChanges {
		res, err := updateAddressBalanceStmt.Exec(balance.Siafunds, addrID)
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

func addEvents(tx *txn, events []wallet.Event, indexID int64) error {
	if len(events) == 0 {
		return nil
	}

	insertEventStmt, err := tx.Prepare(`INSERT INTO events (event_id, maturity_height, date_created, event_type, event_data, chain_index_id) VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (event_id) DO NOTHING RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare event statement: %w", err)
	}
	defer insertEventStmt.Close()

	addrStmt, err := tx.Prepare(`INSERT INTO sia_addresses (sia_address, siacoin_balance, immature_siacoin_balance, siafund_balance) VALUES ($1, $2, $2, 0) ON CONFLICT (sia_address) DO UPDATE SET sia_address=EXCLUDED.sia_address RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare address statement: %w", err)
	}
	defer addrStmt.Close()

	relevantAddrStmt, err := tx.Prepare(`INSERT INTO event_addresses (event_id, address_id) VALUES ($1, $2) ON CONFLICT (event_id, address_id) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("failed to prepare relevant address statement: %w", err)
	}
	defer relevantAddrStmt.Close()

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, event := range events {
		buf.Reset()
		if err := enc.Encode(event.Data); err != nil {
			return fmt.Errorf("failed to encode event: %w", err)
		}

		var eventID int64
		err = insertEventStmt.QueryRow(encode(event.ID), event.MaturityHeight, encode(event.Timestamp), event.Type, buf.String(), indexID).Scan(&eventID)
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
			err = addrStmt.QueryRow(encode(addr), encode(types.ZeroCurrency)).Scan(&addressID)
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
func revertEvents(tx *txn, index types.ChainIndex) error {
	const query = `DELETE FROM events WHERE chain_index_id IN (SELECT id FROM chain_indices WHERE block_id=$1 AND height=$2)`
	_, err := tx.Exec(query, encode(index.ID), index.Height)
	return err
}

func revertSpentOrphanedSiacoinElements(tx *txn, index types.ChainIndex, log *zap.Logger) (map[int64]wallet.Balance, error) {
	rows, err := tx.Query(`UPDATE siacoin_elements SET spent_index_id=NULL WHERE id IN (SELECT se.id FROM siacoin_elements se
INNER JOIN chain_indices ci ON (ci.id=se.spent_index_id)
WHERE ci.height=$1 AND ci.block_id<>$2)
RETURNING address_id, siacoin_value`, index.Height, encode(index.ID))
	if err != nil {
		return nil, fmt.Errorf("failed to query siacoin elements: %w", err)
	}
	defer rows.Close()

	balances := make(map[int64]wallet.Balance)
	for rows.Next() {
		var addrID int64
		var value types.Currency

		if err := rows.Scan(&addrID, decode(&value)); err != nil {
			return nil, fmt.Errorf("failed to scan siacoin element: %w", err)
		}

		balance := balances[addrID]
		balance.Siacoins = balance.Siacoins.Add(value)
		balances[addrID] = balance
		log.Debug("reverting spent orphaned siacoin element", zap.Stringer("value", value))
	}
	return balances, rows.Err()
}

func deleteOrphanedSiacoinElements(tx *txn, index types.ChainIndex, log *zap.Logger) (map[int64]wallet.Balance, error) {
	rows, err := tx.Query(`DELETE FROM siacoin_elements WHERE id IN (SELECT se.id FROM siacoin_elements se
INNER JOIN chain_indices ci ON (ci.id=se.chain_index_id)
WHERE ci.height=$1 AND ci.block_id<>$2)
RETURNING id, address_id, siacoin_value, matured, spent_index_id IS NOT NULL`, index.Height, encode(index.ID))
	if err != nil {
		return nil, fmt.Errorf("failed to query siacoin elements: %w", err)
	}
	defer rows.Close()

	balances := make(map[int64]wallet.Balance)
	for rows.Next() {
		var outputID types.SiacoinOutputID
		var addrID int64
		var value types.Currency
		var matured bool
		var spent bool

		if err := rows.Scan(decode(&outputID), &addrID, decode(&value), &matured, &spent); err != nil {
			return nil, fmt.Errorf("failed to scan siacoin element: %w", err)
		}

		balance := balances[addrID]
		if !matured {
			balance.ImmatureSiacoins = balance.ImmatureSiacoins.Add(value)
		} else if !spent {
			balance.Siacoins = balance.Siacoins.Add(value)
		}
		balances[addrID] = balance
		log.Debug("deleting orphaned siacoin element", zap.Stringer("id", outputID), zap.Stringer("value", value), zap.Bool("matured", matured), zap.Bool("spent", spent))
	}
	return balances, rows.Err()
}

func revertSpentOrphanedSiafundElements(tx *txn, index types.ChainIndex, log *zap.Logger) (map[int64]uint64, error) {
	rows, err := tx.Query(`UPDATE siafund_elements SET spent_index_id=NULL WHERE id IN (SELECT se.id FROM siafund_elements se
INNER JOIN chain_indices ci ON (ci.id=se.spent_index_id)
WHERE ci.height=$1 AND ci.block_id<>$2)
RETURNING id, address_id, siafund_value`, index.Height, encode(index.ID))
	if err != nil {
		return nil, fmt.Errorf("failed to query siafund elements: %w", err)
	}
	defer rows.Close()

	balances := make(map[int64]uint64)
	for rows.Next() {
		var outputID types.SiafundOutputID
		var addrID int64
		var value uint64

		if err := rows.Scan(decode(&outputID), &addrID, value); err != nil {
			return nil, fmt.Errorf("failed to scan siafund element: %w", err)
		}

		balance := balances[addrID]
		balance += value
		balances[addrID] = balance
		log.Debug("reverting spent orphaned siafund element", zap.Stringer("id", outputID), zap.Uint64("value", value))
	}
	return balances, rows.Err()
}

func deleteOrphanedSiafundElements(tx *txn, index types.ChainIndex, log *zap.Logger) (map[int64]uint64, error) {
	rows, err := tx.Query(`DELETE FROM siafund_elements WHERE id IN (SELECT se.id FROM siafund_elements se
INNER JOIN chain_indices ci ON (ci.id=se.chain_index_id)
WHERE ci.height=$1 AND ci.block_id<>$2) 
RETURNING id, address_id, siafund_value, spent_index_id IS NOT NULL`, index.Height, encode(index.ID))
	if err != nil {
		return nil, fmt.Errorf("failed to query siafund elements: %w", err)
	}
	defer rows.Close()

	balances := make(map[int64]uint64)
	for rows.Next() {
		var outputID types.SiafundOutputID
		var addrID int64
		var value uint64
		var spent bool

		if err := rows.Scan(decode(&outputID), &addrID, &value, &spent); err != nil {
			return nil, fmt.Errorf("failed to scan siafund element: %w", err)
		}
		balances[addrID] += value
		log.Debug("deleting orphaned siafund element", zap.Stringer("id", outputID), zap.Uint64("value", value), zap.Bool("spent", spent))
	}
	return balances, rows.Err()
}

func deleteOrphanedEvents(tx *txn, index types.ChainIndex) error {
	_, err := tx.Exec(`DELETE FROM events WHERE id IN (SELECT ev.id FROM events ev
INNER JOIN chain_indices ci ON (ev.chain_index_id=ci.id)
WHERE ci.height=$1 AND ci.block_id<>$2);`, index.Height, encode(index.ID))
	return err
}

// revertOrphans reverts any chain indices that were orphaned by the given index
func revertOrphans(tx *txn, index types.ChainIndex, log *zap.Logger) error {
	// fetch orphaned siacoin balances
	deletedSiacoins, err := deleteOrphanedSiacoinElements(tx, index, log.Named("deleteOrphanedSiacoinElements"))
	if err != nil {
		return fmt.Errorf("failed to get orphaned siacoin elements: %w", err)
	}

	// fetch orphaned siafund balances
	deletedSiafunds, err := deleteOrphanedSiafundElements(tx, index, log.Named("deleteOrphanedSiafundElements"))
	if err != nil {
		return fmt.Errorf("failed to get orphaned siafund elements: %w", err)
	}

	unspentSiacoins, err := revertSpentOrphanedSiacoinElements(tx, index, log.Named("revertSpentOrphanedSiacoinElements"))
	if err != nil {
		return fmt.Errorf("failed to revert spent orphaned siacoin elements: %w", err)
	}

	unspentSiafunds, err := revertSpentOrphanedSiafundElements(tx, index, log.Named("revertSpentOrphanedSiafundElements"))
	if err != nil {
		return fmt.Errorf("failed to revert spent orphaned siafund elements: %w", err)
	}

	// get the addrIDs of all affected addresses
	addrIDs := make(map[int64]bool)
	for id := range deletedSiacoins {
		addrIDs[id] = true
	}
	for id := range deletedSiafunds {
		addrIDs[id] = true
	}
	for id := range unspentSiacoins {
		addrIDs[id] = true
	}
	for id := range unspentSiafunds {
		addrIDs[id] = true
	}

	getBalanceStmt, err := tx.Prepare(`SELECT siacoin_balance, immature_siacoin_balance, siafund_balance FROM sia_addresses WHERE id=$1`)
	if err != nil {
		return fmt.Errorf("failed to prepare balance statement: %w", err)
	}
	defer getBalanceStmt.Close()

	updateBalanceStmt, err := tx.Prepare(`UPDATE sia_addresses SET siacoin_balance=$1, immature_siacoin_balance=$2, siafund_balance=$3 WHERE id=$4`)
	if err != nil {
		return fmt.Errorf("failed to prepare update statement: %w", err)
	}
	defer updateBalanceStmt.Close()

	for addrID := range addrIDs {
		var existing wallet.Balance
		err := getBalanceStmt.QueryRow(addrID).Scan(decode(&existing.Siacoins), decode(&existing.ImmatureSiacoins), &existing.Siafunds)
		if err != nil {
			return fmt.Errorf("failed to get balance: %w", err)
		}

		existing.Siacoins = existing.Siacoins.Sub(deletedSiacoins[addrID].Siacoins)
		existing.ImmatureSiacoins = existing.ImmatureSiacoins.Sub(deletedSiacoins[addrID].ImmatureSiacoins)
		if existing.Siafunds < deletedSiafunds[addrID] {
			panic("siafund balance cannot be negative")
		}
		existing.Siafunds -= deletedSiafunds[addrID]

		existing.Siacoins = existing.Siacoins.Add(unspentSiacoins[addrID].Siacoins)
		existing.Siafunds += unspentSiafunds[addrID]

		res, err := updateBalanceStmt.Exec(encode(existing.Siacoins), encode(existing.ImmatureSiacoins), existing.Siafunds, addrID)
		if err != nil {
			return fmt.Errorf("failed to update balance: %w", err)
		} else if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("failed to get rows affected: %w", err)
		} else if n != 1 {
			return fmt.Errorf("expected 1 row affected, got %v", n)
		}
	}

	if err := deleteOrphanedEvents(tx, index); err != nil {
		return fmt.Errorf("failed to delete orphaned events: %w", err)
	}

	_, err = tx.Exec(`DELETE FROM chain_indices WHERE height=$1 AND block_id<>$2`, index.Height, encode(index.ID))
	return err
}

func pruneSpentSiacoinElements(tx *txn, height uint64) (removed int64, err error) {
	const query = `DELETE FROM siacoin_elements WHERE spent_index_id IN (SELECT id FROM chain_indices WHERE height <= $1)`
	res, err := tx.Exec(query, height)
	if err != nil {
		return 0, fmt.Errorf("failed to query siacoin elements: %w", err)
	}
	return res.RowsAffected()
}

func pruneSpentSiafundElements(tx *txn, height uint64) (removed int64, err error) {
	const query = `DELETE FROM siafund_elements WHERE spent_index_id IN (SELECT id FROM chain_indices WHERE height <= $1)`
	res, err := tx.Exec(query, height)
	if err != nil {
		return 0, fmt.Errorf("failed to query siacoin elements: %w", err)
	}
	return res.RowsAffected()
}

func setGlobalState(tx *txn, index types.ChainIndex, numLeaves uint64) error {
	_, err := tx.Exec(`UPDATE global_settings SET last_indexed_height=$1, last_indexed_id=$2, element_num_leaves=$3`, index.Height, encode(index.ID), numLeaves)
	return err
}

func addressRefStmt(tx *txn) (func(types.Address) (addressRef, error), func() error, error) {
	stmt, err := tx.Prepare(`INSERT INTO sia_addresses (sia_address, siacoin_balance, immature_siacoin_balance, siafund_balance) VALUES ($1, $2, $3, $4) ON CONFLICT (sia_address) DO UPDATE SET sia_address=EXCLUDED.sia_address RETURNING id, siacoin_balance, immature_siacoin_balance, siafund_balance`)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to prepare address statement: %w", err)
	}
	// the on conflict is effectively a no-op, but enables us to return the id of the existing address
	return func(addr types.Address) (addressRef, error) {
		ref, err := scanAddress(stmt.QueryRow(encode(addr), encode(types.ZeroCurrency), encode(types.ZeroCurrency), 0))
		if err != nil {
			return addressRef{}, fmt.Errorf("failed to get address %q: %w", addr, err)
		}
		return ref, nil
	}, stmt.Close, nil
}
