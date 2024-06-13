package sqlite

import (
	"database/sql"
	"encoding/json"
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

func scanEvent(s scanner) (ev wallet.Event, eventID int64, err error) {
	var eventBuf []byte

	err = s.Scan(&eventID, decode(&ev.ID), &ev.MaturityHeight, decode(&ev.Timestamp), &ev.Index.Height, decode(&ev.Index.ID), &ev.Type, &eventBuf)
	if err != nil {
		return
	}

	switch ev.Type {
	case wallet.EventTypeV1Transaction:
		var tx wallet.EventV1Transaction
		if err = json.Unmarshal(eventBuf, &tx); err != nil {
			return wallet.Event{}, 0, fmt.Errorf("failed to unmarshal transaction event: %w", err)
		}
		ev.Data = tx
	case wallet.EventTypeV2Transaction:
		var tx wallet.EventV2Transaction
		if err = json.Unmarshal(eventBuf, &tx); err != nil {
			return wallet.Event{}, 0, fmt.Errorf("failed to unmarshal transaction event: %w", err)
		}
		ev.Data = tx
	case wallet.EventTypeV1ContractResolution:
		var r wallet.EventV1ContractResolution
		if err = json.Unmarshal(eventBuf, &r); err != nil {
			return wallet.Event{}, 0, fmt.Errorf("failed to unmarshal missed file contract event: %w", err)
		}
		ev.Data = r
	case wallet.EventTypeV2ContractResolution:
		var r wallet.EventV2ContractResolution
		if err = json.Unmarshal(eventBuf, &r); err != nil {
			return wallet.Event{}, 0, fmt.Errorf("failed to unmarshal file contract event: %w", err)
		}
		ev.Data = r
	case wallet.EventTypeSiafundClaim, wallet.EventTypeMinerPayout, wallet.EventTypeFoundationSubsidy:
		var p wallet.EventPayout
		if err = json.Unmarshal(eventBuf, &p); err != nil {
			return wallet.Event{}, 0, fmt.Errorf("failed to unmarshal event %q (%q): %w", ev.ID, ev.Type, err)
		}
		ev.Data = p
	default:
		return wallet.Event{}, 0, fmt.Errorf("unknown event type: %q", ev.Type)
	}
	return
}
