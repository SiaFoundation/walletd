package sqlite

import (
	"fmt"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/wallet"
)

// AddressBalance returns the balance of a single address.
func (s *Store) AddressBalance(address types.Address) (balance wallet.Balance, err error) {
	err = s.transaction(func(tx *txn) error {
		const query = `SELECT siacoin_balance, immature_siacoin_balance, siafund_balance FROM sia_addresses WHERE sia_address=$1`
		return tx.QueryRow(query, encode(address)).Scan(decode(&balance.Siacoins), decode(&balance.ImmatureSiacoins), &balance.Siafunds)
	})
	return
}

// AddressEvents returns the events of a single address.
func (s *Store) AddressEvents(address types.Address, offset, limit int) (events []wallet.Event, err error) {
	err = s.transaction(func(tx *txn) error {
		const query = `SELECT ev.id, ev.event_id, ev.maturity_height, ev.date_created, ev.height, ev.block_id, ev.event_type, ev.event_data
	FROM events ev
	INNER JOIN event_addresses ea ON (ev.id = ea.event_id)
	INNER JOIN sia_addresses sa ON (ea.address_id = sa.id)
	WHERE sa.sia_address = $1
	ORDER BY ev.maturity_height DESC, ev.id DESC
	LIMIT $2 OFFSET $3`

		rows, err := tx.Query(query, encode(address), limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			event, _, err := scanEvent(rows)
			if err != nil {
				return fmt.Errorf("failed to scan event: %w", err)
			}

			events = append(events, event)
		}
		return rows.Err()
	})
	return
}

// AddressSiacoinOutputs returns the unspent siacoin outputs for an address.
func (s *Store) AddressSiacoinOutputs(address types.Address, offset, limit int) (siacoins []types.SiacoinElement, err error) {
	err = s.transaction(func(tx *txn) error {
		const query = `SELECT se.id, se.siacoin_value, se.merkle_proof, se.leaf_index, se.maturity_height, sa.sia_address 
		FROM siacoin_elements se
		INNER JOIN sia_addresses sa ON (se.address_id = sa.id)
		WHERE sa.sia_address=$1
		LIMIT $2 OFFSET $3`

		rows, err := tx.Query(query, encode(address), limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			siacoin, err := scanSiacoinElement(rows)
			if err != nil {
				return fmt.Errorf("failed to scan siacoin element: %w", err)
			}

			siacoins = append(siacoins, siacoin)
		}
		return rows.Err()
	})
	return
}

// AddressSiafundOutputs returns the unspent siafund outputs for an address.
func (s *Store) AddressSiafundOutputs(address types.Address, offset, limit int) (siafunds []types.SiafundElement, err error) {
	err = s.transaction(func(tx *txn) error {
		const query = `SELECT se.id, se.leaf_index, se.merkle_proof, se.siafund_value, se.claim_start, sa.sia_address 
		FROM siafund_elements se
		INNER JOIN sia_addresses sa ON (se.address_id = sa.id)
		WHERE sa.sia_address = $1
		LIMIT $2 OFFSET $3`

		rows, err := tx.Query(query, encode(address), limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			siafund, err := scanSiafundElement(rows)
			if err != nil {
				return fmt.Errorf("failed to scan siafund element: %w", err)
			}
			siafunds = append(siafunds, siafund)
		}
		return rows.Err()
	})
	return
}
