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
		const query = `
WITH last_chain_index AS (
	SELECT last_indexed_height+1 AS height FROM global_settings LIMIT 1
)
SELECT 
	ev.id, 
	ev.event_id, 
	ev.maturity_height, 
	ev.date_created, 
	ci.height, 
	ci.block_id, 
	CASE 
		WHEN last_chain_index.height < ci.height THEN 0
		ELSE last_chain_index.height - ci.height
	END AS confirmations,
	ev.event_type, 
	ev.event_data
FROM events ev
INNER JOIN event_addresses ea ON (ev.id = ea.event_id)
INNER JOIN sia_addresses sa ON (ea.address_id = sa.id)
INNER JOIN chain_indices ci ON (ev.chain_index_id = ci.id)
CROSS JOIN last_chain_index
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

func decodeEventData[T wallet.EventPayout |
	wallet.EventV1Transaction |
	wallet.EventV2Transaction |
	wallet.EventV1ContractResolution |
	wallet.EventV2ContractResolution, TP interface {
	*T
	types.DecoderFrom
}](dec *types.Decoder) T {
	v := new(T)
	TP(v).DecodeFrom(dec)
	return *v
}

func scanEvent(s scanner) (ev wallet.Event, eventID int64, err error) {
	var eventBuf []byte
	err = s.Scan(&eventID, decode(&ev.ID), &ev.MaturityHeight, decode(&ev.Timestamp), &ev.Index.Height, decode(&ev.Index.ID), &ev.Confirmations, &ev.Type, &eventBuf)
	if err != nil {
		return
	}

	dec := types.NewBufDecoder(eventBuf)
	switch ev.Type {
	case wallet.EventTypeV1Transaction:
		ev.Data = decodeEventData[wallet.EventV1Transaction](dec)
	case wallet.EventTypeV2Transaction:
		ev.Data = decodeEventData[wallet.EventV2Transaction](dec)
	case wallet.EventTypeV1ContractResolution:
		ev.Data = decodeEventData[wallet.EventV1ContractResolution](dec)
	case wallet.EventTypeV2ContractResolution:
		ev.Data = decodeEventData[wallet.EventV2ContractResolution](dec)
	case wallet.EventTypeSiafundClaim, wallet.EventTypeMinerPayout, wallet.EventTypeFoundationSubsidy:
		ev.Data = decodeEventData[wallet.EventPayout](dec)
	default:
		return wallet.Event{}, 0, fmt.Errorf("unknown event type: %q", ev.Type)
	}
	if err := dec.Err(); err != nil {
		return wallet.Event{}, 0, fmt.Errorf("failed to decode event data: %w", err)
	}

	return
}
