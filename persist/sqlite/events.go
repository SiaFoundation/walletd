package sqlite

import (
	"database/sql"
	"errors"
	"fmt"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/wallet"
)

// Events returns the events with the given event IDs. If an event is not found,
// it is skipped.
func (s *Store) Events(eventIDs []types.Hash256) (events []wallet.Event, err error) {
	err = s.transaction(func(tx *txn) error {
		// sqlite doesn't have easy support for IN clauses, use a statement since
		// the number of event IDs is likely to be small instead of dynamically
		// building the query
		const query = `SELECT ev.id, ev.event_id, ev.maturity_height, ev.date_created, ci.height, ci.block_id, ev.event_type, ev.event_data
	FROM events ev
	INNER JOIN event_addresses ea ON (ev.id = ea.event_id)
	INNER JOIN sia_addresses sa ON (ea.address_id = sa.id)
	INNER JOIN chain_indices ci ON (ev.chain_index_id = ci.id)
	WHERE ev.event_id = $1`

		stmt, err := tx.Prepare(query)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer stmt.Close()

		events = make([]wallet.Event, 0, len(eventIDs))
		for _, id := range eventIDs {
			event, _, err := scanEvent(stmt.QueryRow(encode(id)))
			if errors.Is(err, sql.ErrNoRows) {
				continue
			} else if err != nil {
				return fmt.Errorf("failed to query transaction %q: %w", id, err)
			}
			events = append(events, event)
		}
		return nil
	})
	return
}
