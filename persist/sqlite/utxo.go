package sqlite

import (
	"database/sql"
	"errors"
	"fmt"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/wallet"
)

// SiacoinElement returns an unspent Siacoin UTXO by its ID.
func (s *Store) SiacoinElement(id types.SiacoinOutputID) (ele types.SiacoinElement, err error) {
	err = s.transaction(func(tx *txn) error {
		const query = `SELECT se.id, se.siacoin_value, se.merkle_proof, se.leaf_index, se.maturity_height, sa.sia_address 
FROM siacoin_elements se
INNER JOIN sia_addresses sa ON (se.address_id = sa.id)
WHERE se.id=$1 AND spent_index_id IS NULL`

		ele, err = scanSiacoinElement(tx.QueryRow(query, encode(id)))
		if err != nil {
			return err
		}

		// retrieve the merkle proofs for the siacoin element
		if s.indexMode == wallet.IndexModeFull {
			proof, err := fillElementProofs(tx, []uint64{ele.StateElement.LeafIndex})
			if err != nil {
				return fmt.Errorf("failed to fill element proofs: %w", err)
			} else if len(proof) != 1 {
				panic("expected exactly one proof") // should never happen
			}
			ele.StateElement.MerkleProof = proof[0]
		}
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		err = wallet.ErrNotFound
	}
	return
}

// SiafundElement returns an unspent Siafund UTXO by its ID.
func (s *Store) SiafundElement(id types.SiafundOutputID) (ele types.SiafundElement, err error) {
	err = s.transaction(func(tx *txn) error {
		const query = `SELECT se.id, se.leaf_index, se.merkle_proof, se.siafund_value, se.claim_start, sa.sia_address 
FROM siafund_elements se
INNER JOIN sia_addresses sa ON (se.address_id = sa.id)
WHERE se.id=$1 AND spent_index_id IS NULL`

		ele, err = scanSiafundElement(tx.QueryRow(query, encode(id)))
		if err != nil {
			return err
		}

		// retrieve the merkle proofs for the siafund element
		if s.indexMode == wallet.IndexModeFull {
			proof, err := fillElementProofs(tx, []uint64{ele.StateElement.LeafIndex})
			if err != nil {
				return fmt.Errorf("failed to fill element proofs: %w", err)
			} else if len(proof) != 1 {
				panic("expected exactly one proof") // should never happen
			}
			ele.StateElement.MerkleProof = proof[0]
		}
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		err = wallet.ErrNotFound
	}
	return
}
