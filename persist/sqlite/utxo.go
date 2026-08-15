package sqlite

import (
	"database/sql"
	"errors"
	"fmt"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/v2/wallet"
)

func getSiacoinElement(tx *txn, id types.SiacoinOutputID, indexMode wallet.IndexMode) (ele types.SiacoinElement, err error) {
	const query = `SELECT se.id, se.siacoin_value, se.merkle_proof, se.leaf_index, se.maturity_height, sa.sia_address 
FROM siacoin_elements se
INNER JOIN sia_addresses sa ON (se.address_id = sa.id)
WHERE se.id=$1 AND spent_index_id IS NULL`

	ele, err = scanSiacoinElement(tx.QueryRow(query, encode(id)))
	if err != nil {
		return types.SiacoinElement{}, err
	}

	// retrieve the merkle proofs for the siacoin element
	if indexMode == wallet.IndexModeFull {
		proof, err := fillElementProofs(tx, []uint64{ele.StateElement.LeafIndex})
		if err != nil {
			return types.SiacoinElement{}, fmt.Errorf("failed to fill element proofs: %w", err)
		} else if len(proof) != 1 {
			panic("expected exactly one proof") // should never happen
		}
		ele.StateElement.MerkleProof = proof[0]
	}
	return
}

func getSiafundElement(tx *txn, id types.SiafundOutputID, indexMode wallet.IndexMode) (ele types.SiafundElement, err error) {
	const query = `SELECT se.id, se.leaf_index, se.merkle_proof, se.siafund_value, se.claim_start, sa.sia_address 
FROM siafund_elements se
INNER JOIN sia_addresses sa ON (se.address_id = sa.id)
WHERE se.id=$1 AND spent_index_id IS NULL`

	ele, err = scanSiafundElement(tx.QueryRow(query, encode(id)))
	if err != nil {
		return types.SiafundElement{}, err
	}

	// retrieve the merkle proofs for the siafund element
	if indexMode == wallet.IndexModeFull {
		proof, err := fillElementProofs(tx, []uint64{ele.StateElement.LeafIndex})
		if err != nil {
			return types.SiafundElement{}, fmt.Errorf("failed to fill element proofs: %w", err)
		} else if len(proof) != 1 {
			panic("expected exactly one proof") // should never happen
		}
		ele.StateElement.MerkleProof = proof[0]
	}
	return
}

// SiacoinElement returns an unspent Siacoin UTXO by its ID.
func (s *Store) SiacoinElement(id types.SiacoinOutputID) (types.SiacoinElement, error) {
	return valuedTransaction(s, func(tx *txn) (types.SiacoinElement, error) {
		ele, err := getSiacoinElement(tx, id, s.indexMode)
		if errors.Is(err, sql.ErrNoRows) {
			return types.SiacoinElement{}, wallet.ErrNotFound
		}
		return ele, err
	})
}

// SiafundElement returns an unspent Siafund UTXO by its ID.
func (s *Store) SiafundElement(id types.SiafundOutputID) (types.SiafundElement, error) {
	return valuedTransaction(s, func(tx *txn) (types.SiafundElement, error) {
		ele, err := getSiafundElement(tx, id, s.indexMode)
		if errors.Is(err, sql.ErrNoRows) {
			return types.SiafundElement{}, wallet.ErrNotFound
		}
		return ele, err
	})
}

// SiacoinElementSpentEvent returns the event that spent a Siacoin UTXO.
func (s *Store) SiacoinElementSpentEvent(id types.SiacoinOutputID) (wallet.Event, bool, error) {
	return valuedTransaction2(s, func(tx *txn) (wallet.Event, bool, error) {
		const query = `SELECT spent_event_id FROM siacoin_elements WHERE id=$1`

		var spentEventID sql.NullInt64
		err := tx.QueryRow(query, encode(id)).Scan(&spentEventID)
		if errors.Is(err, sql.ErrNoRows) {
			return wallet.Event{}, false, wallet.ErrNotFound
		} else if err != nil {
			return wallet.Event{}, false, fmt.Errorf("failed to query spent event ID: %w", err)
		} else if !spentEventID.Valid {
			return wallet.Event{}, false, nil
		}

		events, err := getEventsByID(tx, []int64{spentEventID.Int64})
		if err != nil {
			return wallet.Event{}, false, fmt.Errorf("failed to get events by ID: %w", err)
		} else if len(events) != 1 {
			panic("expected exactly one event") // should never happen
		}
		return events[0], true, nil
	})
}

// SiafundElementSpentEvent returns the event that spent a Siafund UTXO.
func (s *Store) SiafundElementSpentEvent(id types.SiafundOutputID) (wallet.Event, bool, error) {
	return valuedTransaction2(s, func(tx *txn) (wallet.Event, bool, error) {
		const query = `SELECT spent_event_id FROM siafund_elements WHERE id=$1`

		var spentEventID sql.NullInt64
		err := tx.QueryRow(query, encode(id)).Scan(&spentEventID)
		if errors.Is(err, sql.ErrNoRows) {
			return wallet.Event{}, false, wallet.ErrNotFound
		} else if err != nil {
			return wallet.Event{}, false, fmt.Errorf("failed to query spent event ID: %w", err)
		} else if !spentEventID.Valid {
			return wallet.Event{}, false, nil
		}

		events, err := getEventsByID(tx, []int64{spentEventID.Int64})
		if err != nil {
			return wallet.Event{}, false, fmt.Errorf("failed to get events by ID: %w", err)
		} else if len(events) != 1 {
			panic("expected exactly one event") // should never happen
		}
		return events[0], true, nil
	})
}
