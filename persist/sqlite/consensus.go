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
	"go.uber.org/zap"
)

type updateTx struct {
	tx *txn

	relevantAddresses map[types.Address]bool
}

func scanStateElement(s scanner) (se types.StateElement, err error) {
	err = s.Scan(decode(&se.ID), &se.LeafIndex, decodeSlice(&se.MerkleProof))
	return
}

func scanSiacoinElement(s scanner) (se types.SiacoinElement, err error) {
	err = s.Scan(decode(&se.ID), decode(&se.SiacoinOutput.Value), decodeSlice(&se.MerkleProof), &se.LeafIndex, &se.MaturityHeight, decode(&se.SiacoinOutput.Address))
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
	return elements, nil
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
	return elements, nil
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

func (ut *updateTx) UpdateBalances(balances []wallet.AddressBalance) error {
	const query = `UPDATE sia_addresses SET siacoin_balance=$1, immature_siacoin_balance=$2, siafund_balance=$3 WHERE sia_address=$4`
	stmt, err := ut.tx.Prepare(query)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, ab := range balances {
		_, err := stmt.Exec(encode(ab.Balance.Siacoins), encode(ab.Balance.ImmatureSiacoins), ab.Balance.Siafunds, encode(ab.Address))
		if err != nil {
			return fmt.Errorf("failed to execute statement: %w", err)
		}
	}
	return nil
}

func (ut *updateTx) MaturedSiacoinElements(index types.ChainIndex) (elements []types.SiacoinElement, err error) {
	const query = `SELECT se.id, se.siacoin_value, se.merkle_proof, se.leaf_index, se.maturity_height, a.sia_address 
FROM siacoin_elements se
INNER JOIN sia_addresses a ON (se.address_id=a.id)
WHERE maturity_height=$1`
	rows, err := ut.tx.Query(query, index.Height)
	if err != nil {
		return nil, fmt.Errorf("failed to query siacoin elements: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		element, err := scanSiacoinElement(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan siacoin element: %w", err)
		}
		elements = append(elements, element)
	}
	return
}

func (ut *updateTx) AddSiacoinElements(elements []types.SiacoinElement) error {
	addrStmt, err := insertAddressStatement(ut.tx)
	if err != nil {
		return fmt.Errorf("failed to prepare address statement: %w", err)
	}
	defer addrStmt.Close()

	inserStmt, err := ut.tx.Prepare(`INSERT INTO siacoin_elements (id, siacoin_value, merkle_proof, leaf_index, maturity_height, address_id) VALUES ($1, $2, $3, $4, $5, $6)`)
	if err != nil {
		return fmt.Errorf("failed to prepare insert statement: %w", err)
	}
	defer inserStmt.Close()

	for _, se := range elements {
		var addressID int64
		err := addrStmt.QueryRow(encode(se.SiacoinOutput.Address), encode(types.ZeroCurrency), 0).Scan(&addressID)
		if err != nil {
			return fmt.Errorf("failed to query address: %w", err)
		}

		_, err = inserStmt.Exec(encode(se.ID), encode(se.SiacoinOutput.Value), encodeSlice(se.MerkleProof), se.LeafIndex, se.MaturityHeight, addressID)
		if err != nil {
			return fmt.Errorf("failed to execute statement: %w", err)
		}
	}
	return nil
}

func (ut *updateTx) RemoveSiacoinElements(elements []types.SiacoinOutputID) error {
	stmt, err := ut.tx.Prepare(`DELETE FROM siacoin_elements WHERE id=$1 RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, id := range elements {
		var dummy types.Hash256
		err := stmt.QueryRow(encode(id)).Scan(decode(&dummy))
		if err != nil {
			return fmt.Errorf("failed to delete element %q: %w", id, err)
		}
	}
	return nil
}

func (ut *updateTx) AddSiafundElements(elements []types.SiafundElement) error {
	addrStmt, err := insertAddressStatement(ut.tx)
	if err != nil {
		return fmt.Errorf("failed to prepare address statement: %w", err)
	}
	defer addrStmt.Close()

	inserStmt, err := ut.tx.Prepare(`INSERT INTO siafund_elements (id, siafund_value, merkle_proof, leaf_index, claim_start, address_id) VALUES ($1, $2, $3, $4, $5, $6)`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer inserStmt.Close()

	for _, se := range elements {
		var addressID int64
		err := addrStmt.QueryRow(encode(se.SiafundOutput.Address), encode(types.ZeroCurrency), 0).Scan(&addressID)
		if err != nil {
			return fmt.Errorf("failed to query address: %w", err)
		}

		_, err = inserStmt.Exec(encode(se.ID), se.SiafundOutput.Value, encodeSlice(se.MerkleProof), se.LeafIndex, encode(se.ClaimStart), addressID)
		if err != nil {
			return fmt.Errorf("failed to execute statement: %w", err)
		}
	}
	return nil
}

func (ut *updateTx) RemoveSiafundElements(elements []types.SiafundOutputID) error {
	stmt, err := ut.tx.Prepare(`DELETE FROM siacoin_elements WHERE id=$1 RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, id := range elements {
		var dummy types.Hash256
		err := stmt.QueryRow(encode(id)).Scan(decode(&dummy))
		if err != nil {
			return fmt.Errorf("failed to delete element %q: %w", id, err)
		}
	}
	return nil
}

func (ut *updateTx) AddEvents(events []wallet.Event) error {
	indexStmt, err := ut.tx.Prepare(`INSERT INTO chain_indices (height, block_id) VALUES ($1, $2) ON CONFLICT (block_id) DO UPDATE SET height=EXCLUDED.height RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare index statement: %w", err)
	}
	defer indexStmt.Close()

	eventStmt, err := ut.tx.Prepare(`INSERT INTO events (event_id, maturity_height, date_created, index_id, event_type, event_data) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`)
	if err != nil {
		return fmt.Errorf("failed to prepare event statement: %w", err)
	}
	defer eventStmt.Close()

	addrStmt, err := insertAddressStatement(ut.tx)
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
		err = eventStmt.QueryRow(encode(event.ID), event.MaturityHeight, encode(event.Timestamp), chainIndexID, event.Data.EventType(), buf.String()).Scan(&eventID)
		if err != nil {
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

// RevertEvents reverts the events that were added in the given block.
func (ut *updateTx) RevertEvents(blockID types.BlockID) error {
	var id int64
	err := ut.tx.QueryRow(`DELETE FROM chain_indices WHERE block_id=$1 RETURNING id`, encode(blockID)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

// ProcessChainApplyUpdate implements chain.Subscriber
func (s *Store) ProcessChainApplyUpdate(cau *chain.ApplyUpdate, mayCommit bool) error {
	s.updates = append(s.updates, cau)
	log := s.log.Named("ProcessChainApplyUpdate").With(zap.Stringer("index", cau.State.Index))
	log.Debug("received update")
	if mayCommit {
		log.Debug("committing updates", zap.Int("n", len(s.updates)))
		return s.transaction(func(tx *txn) error {
			utx := &updateTx{
				tx:                tx,
				relevantAddresses: make(map[types.Address]bool),
			}

			if err := wallet.ApplyChainUpdates(utx, s.updates); err != nil {
				return fmt.Errorf("failed to apply updates: %w", err)
			} else if err := setLastCommittedIndex(tx, cau.State.Index); err != nil {
				return fmt.Errorf("failed to set last committed index: %w", err)
			}
			s.updates = nil
			return nil
		})
	}

	return nil
}

// ProcessChainRevertUpdate implements chain.Subscriber
func (s *Store) ProcessChainRevertUpdate(cru *chain.RevertUpdate) error {
	log := s.log.Named("ProcessChainRevertUpdate").With(zap.Stringer("index", cru.State.Index))

	// update hasn't been committed yet
	if len(s.updates) > 0 && s.updates[len(s.updates)-1].Block.ID() == cru.Block.ID() {
		log.Debug("removed uncommitted update")
		s.updates = s.updates[:len(s.updates)-1]
		return nil
	}

	log.Debug("reverting update")
	// update has been committed, revert it
	return s.transaction(func(tx *txn) error {
		utx := &updateTx{
			tx:                tx,
			relevantAddresses: make(map[types.Address]bool),
		}

		if err := wallet.RevertChainUpdate(utx, cru); err != nil {
			return fmt.Errorf("failed to revert update: %w", err)
		} else if err := setLastCommittedIndex(tx, cru.State.Index); err != nil {
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
	return tx.Prepare(`INSERT INTO sia_addresses (sia_address, siacoin_balance, immature_siacoin_balance, siafund_balance) VALUES ($1, $2, $2, $3) ON CONFLICT (sia_address) DO UPDATE SET sia_address=EXCLUDED.sia_address RETURNING id`)
}
