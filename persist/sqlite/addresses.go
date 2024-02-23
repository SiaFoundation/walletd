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
func (s *Store) AddressEvents(address types.Address, limit, offset int) (events []wallet.Event, err error) {
	err = s.transaction(func(tx *txn) error {
		const query = `SELECT ev.id, ev.event_id, ev.maturity_height, ev.date_created, ci.height, ci.block_id, ev.event_type, ev.event_data
	FROM events ev
	INNER JOIN chain_indices ci ON (ev.index_id = ci.id)
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
		if err = rows.Err(); err != nil {
			return err
		}
		return nil
	})
	return
}
