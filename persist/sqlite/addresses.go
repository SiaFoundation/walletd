package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/v2/wallet"
)

// CheckAddresses returns true if any of the addresses have been seen on the
// blockchain. This is a quick way to scan wallets for lookaheads.
//
// If the index mode is not full, this function will only return true if
// an address is registered with a wallet.
func (s *Store) CheckAddresses(addresses []types.Address) (known bool, err error) {
	err = s.transaction(func(tx *txn) error {
		stmt, err := tx.Prepare(`SELECT true FROM sia_addresses WHERE sia_address=$1`)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer stmt.Close()

		for _, addr := range addresses {
			if err := stmt.QueryRow(encode(addr)).Scan(&known); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				return fmt.Errorf("failed to query address: %w", err)
			}
			if known {
				return nil
			}
		}
		return nil
	})
	return
}

// AddressBalance returns the aggregate balance of the addresses.
func (s *Store) AddressBalance(address ...types.Address) (balance wallet.Balance, err error) {
	if len(address) == 0 {
		return wallet.Balance{}, nil // no addresses, no balance
	}
	err = s.transaction(func(tx *txn) error {
		const query = `SELECT siacoin_balance, immature_siacoin_balance, siafund_balance FROM sia_addresses WHERE sia_address=$1`
		stmt, err := tx.Prepare(query)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer stmt.Close()

		for _, addr := range address {
			var siacoins, immatureSiacoins types.Currency
			var siafunds uint64

			if err := stmt.QueryRow(encode(addr)).Scan(decode(&siacoins), decode(&immatureSiacoins), &siafunds); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("failed to query address %q: %w", addr, err)
			}
			balance.Siacoins = balance.Siacoins.Add(siacoins)
			balance.ImmatureSiacoins = balance.ImmatureSiacoins.Add(immatureSiacoins)
			balance.Siafunds += siafunds
		}
		return nil
	})
	return
}

// BatchAddressEvents returns the events for a batch of addresses.
func (s *Store) BatchAddressEvents(addresses []types.Address, offset, limit int) (events []wallet.Event, err error) {
	if len(addresses) == 0 {
		return nil, nil // no addresses, no events
	}
	err = s.transaction(func(tx *txn) error {
		dbIDs, err := s.getAddressesEvents(tx, addresses, offset, limit)
		if err != nil {
			return fmt.Errorf("failed to get events for addresses: %w", err)
		}
		if len(dbIDs) == 0 {
			return nil // no events found
		}

		events, err = getEventsByID(tx, dbIDs)
		if err != nil {
			return fmt.Errorf("failed to get events by ID: %w", err)
		}

		addressMap := make(map[types.Address]bool)
		for _, addr := range addresses {
			addressMap[addr] = true
		}
		for i := range events {
			seen := make(map[types.Address]bool)
			switch ev := events[i].Data.(type) {
			case wallet.EventV1Transaction:
				for _, sci := range ev.Transaction.SiacoinInputs {
					addr := sci.UnlockConditions.UnlockHash()
					if addressMap[addr] && !seen[addr] {
						seen[addr] = true
						events[i].Relevant = append(events[i].Relevant, addr)
					}
				}
				for _, sco := range ev.Transaction.SiacoinOutputs {
					if addressMap[sco.Address] && !seen[sco.Address] {
						seen[sco.Address] = true
						events[i].Relevant = append(events[i].Relevant, sco.Address)
					}
				}
				for _, sfi := range ev.Transaction.SiafundInputs {
					addr := sfi.UnlockConditions.UnlockHash()
					if addressMap[addr] && !seen[addr] {
						seen[addr] = true
						events[i].Relevant = append(events[i].Relevant, addr)
					}
				}
				for _, sfo := range ev.Transaction.SiafundOutputs {
					if addressMap[sfo.Address] && !seen[sfo.Address] {
						seen[sfo.Address] = true
						events[i].Relevant = append(events[i].Relevant, sfo.Address)
					}
				}
			case wallet.EventV2Transaction:
				for _, sci := range ev.SiacoinInputs {
					if addressMap[sci.Parent.SiacoinOutput.Address] && !seen[sci.Parent.SiacoinOutput.Address] {
						seen[sci.Parent.SiacoinOutput.Address] = true
						events[i].Relevant = append(events[i].Relevant, sci.Parent.SiacoinOutput.Address)
					}
				}
				for _, sco := range ev.SiacoinOutputs {
					if addressMap[sco.Address] && !seen[sco.Address] {
						seen[sco.Address] = true
						events[i].Relevant = append(events[i].Relevant, sco.Address)
					}
				}
				for _, sfi := range ev.SiafundInputs {
					if addressMap[sfi.Parent.SiafundOutput.Address] && !seen[sfi.Parent.SiafundOutput.Address] {
						seen[sfi.Parent.SiafundOutput.Address] = true
						events[i].Relevant = append(events[i].Relevant, sfi.Parent.SiafundOutput.Address)
					}
				}
				for _, sfo := range ev.SiafundOutputs {
					if addressMap[sfo.Address] && !seen[sfo.Address] {
						seen[sfo.Address] = true
						events[i].Relevant = append(events[i].Relevant, sfo.Address)
					}
				}
			case wallet.EventPayout:
				events[i].Relevant = append(events[i].Relevant, ev.SiacoinElement.SiacoinOutput.Address)
			}
		}
		return nil
	})
	return
}

// BatchAddressSiacoinOutputs returns the unspent siacoin outputs for an address.
func (s *Store) BatchAddressSiacoinOutputs(addresses []types.Address, offset, limit int) (siacoins []wallet.UnspentSiacoinElement, basis types.ChainIndex, err error) {
	err = s.transaction(func(tx *txn) error {
		basis, err = getScanBasis(tx)
		if err != nil {
			return fmt.Errorf("failed to get basis: %w", err)
		}

		query := `SELECT se.id, se.siacoin_value, se.merkle_proof, se.leaf_index, se.maturity_height, sa.sia_address, ci.height 
		FROM siacoin_elements se
		INNER JOIN chain_indices ci ON (se.chain_index_id = ci.id)
		INNER JOIN sia_addresses sa ON (se.address_id = sa.id)
		WHERE sa.sia_address IN (` + queryPlaceHolders(len(addresses)) + `) AND se.maturity_height <= ? AND se.spent_index_id IS NULL
		LIMIT ? OFFSET ?`

		rows, err := tx.Query(query, append(encodeSlice(addresses), basis.Height, limit, offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			siacoin, err := scanUnspentSiacoinElement(rows, basis.Height)
			if err != nil {
				return fmt.Errorf("failed to scan siacoin element: %w", err)
			}

			siacoins = append(siacoins, siacoin)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		// retrieve the merkle proofs for the siacoin elements
		if s.indexMode == wallet.IndexModeFull {
			indices := make([]uint64, len(siacoins))
			for i, se := range siacoins {
				indices[i] = se.StateElement.LeafIndex
			}
			proofs, err := fillElementProofs(tx, indices)
			if err != nil {
				return fmt.Errorf("failed to fill element proofs: %w", err)
			}
			for i, proof := range proofs {
				siacoins[i].StateElement.MerkleProof = proof
			}
		}
		return nil
	})
	return
}

// BatchAddressSiafundOutputs returns the unspent siafund outputs for an address.
func (s *Store) BatchAddressSiafundOutputs(addresses []types.Address, offset, limit int) (siafunds []wallet.UnspentSiafundElement, basis types.ChainIndex, err error) {
	err = s.transaction(func(tx *txn) error {
		basis, err = getScanBasis(tx)
		if err != nil {
			return fmt.Errorf("failed to get basis: %w", err)
		}

		query := `SELECT se.id, se.leaf_index, se.merkle_proof, se.siafund_value, se.claim_start, sa.sia_address, ci.height
		FROM siafund_elements se
		INNER JOIN chain_indices ci ON (se.chain_index_id = ci.id)
		INNER JOIN sia_addresses sa ON (se.address_id = sa.id)
		WHERE sa.sia_address IN(` + queryPlaceHolders(len(addresses)) + `) AND se.spent_index_id IS NULL
		ORDER BY se.id DESC
		LIMIT ? OFFSET ?`

		rows, err := tx.Query(query, append(encodeSlice(addresses), limit, offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			siafund, err := scanUnspentSiafundElement(rows, basis.Height)
			if err != nil {
				return fmt.Errorf("failed to scan siafund element: %w", err)
			}
			siafunds = append(siafunds, siafund)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		// retrieve the merkle proofs for the siafund elements
		if s.indexMode == wallet.IndexModeFull {
			indices := make([]uint64, len(siafunds))
			for i, se := range siafunds {
				indices[i] = se.StateElement.LeafIndex
			}
			proofs, err := fillElementProofs(tx, indices)
			if err != nil {
				return fmt.Errorf("failed to fill element proofs: %w", err)
			}
			for i, proof := range proofs {
				siafunds[i].StateElement.MerkleProof = proof
			}
		}
		return nil
	})
	return
}

// AddressEvents returns the events of a single address.
func (s *Store) AddressEvents(address types.Address, offset, limit int) (events []wallet.Event, err error) {
	err = s.transaction(func(tx *txn) error {
		dbIDs, err := getAddressEvents(tx, address, offset, limit)
		if err != nil {
			return err
		}

		events, err = getEventsByID(tx, dbIDs)
		if err != nil {
			return fmt.Errorf("failed to get events by ID: %w", err)
		}

		for i := range events {
			events[i].Relevant = []types.Address{address}
		}
		return nil
	})
	return
}

// AddressSiacoinOutputs returns the unspent siacoin outputs for an address.
func (s *Store) AddressSiacoinOutputs(address types.Address, tpoolSpent []types.SiacoinOutputID, offset, limit int) (siacoins []wallet.UnspentSiacoinElement, basis types.ChainIndex, err error) {
	err = s.transaction(func(tx *txn) error {
		basis, err = getScanBasis(tx)
		if err != nil {
			return fmt.Errorf("failed to get basis: %w", err)
		}

		query := `SELECT se.id, se.siacoin_value, se.merkle_proof, se.leaf_index, se.maturity_height, sa.sia_address, ci.height 
		FROM siacoin_elements se
		INNER JOIN chain_indices ci ON (se.chain_index_id = ci.id)
		INNER JOIN sia_addresses sa ON (se.address_id = sa.id)
		WHERE sa.sia_address = ? AND se.maturity_height <= ? AND se.spent_index_id IS NULL`

		params := []any{encode(address), basis.Height}
		if len(tpoolSpent) > 0 {
			query += ` AND se.ID NOT IN (` + queryPlaceHolders(len(tpoolSpent)) + `)`
			params = append(params, encodeSlice(tpoolSpent)...)
		}

		query += ` ORDER BY se.maturity_height DESC, se.id DESC
		LIMIT ? OFFSET ?`

		params = append(params, limit, offset)

		rows, err := tx.Query(query, params...)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			siacoin, err := scanUnspentSiacoinElement(rows, basis.Height)
			if err != nil {
				return fmt.Errorf("failed to scan siacoin element: %w", err)
			}

			siacoins = append(siacoins, siacoin)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		// retrieve the merkle proofs for the siacoin elements
		if s.indexMode == wallet.IndexModeFull {
			indices := make([]uint64, len(siacoins))
			for i, se := range siacoins {
				indices[i] = se.StateElement.LeafIndex
			}
			proofs, err := fillElementProofs(tx, indices)
			if err != nil {
				return fmt.Errorf("failed to fill element proofs: %w", err)
			}
			for i, proof := range proofs {
				siacoins[i].StateElement.MerkleProof = proof
			}
		}
		return nil
	})
	return
}

// AddressSiafundOutputs returns the unspent siafund outputs for an address.
func (s *Store) AddressSiafundOutputs(address types.Address, tpoolSpent []types.SiafundOutputID, offset, limit int) (siafunds []wallet.UnspentSiafundElement, basis types.ChainIndex, err error) {
	err = s.transaction(func(tx *txn) error {
		basis, err = getScanBasis(tx)
		if err != nil {
			return fmt.Errorf("failed to get basis: %w", err)
		}

		query := `SELECT se.id, se.leaf_index, se.merkle_proof, se.siafund_value, se.claim_start, sa.sia_address, ci.height
		FROM siafund_elements se
		INNER JOIN chain_indices ci ON (se.chain_index_id = ci.id)
		INNER JOIN sia_addresses sa ON (se.address_id = sa.id)
		WHERE sa.sia_address=? AND se.spent_index_id IS NULL`

		params := []any{encode(address)}

		if len(tpoolSpent) > 0 {
			query += ` AND se.id NOT IN (` + queryPlaceHolders(len(tpoolSpent)) + `)`
			params = append(params, encodeSlice(tpoolSpent)...)
		}

		query += ` ORDER BY se.id DESC
		LIMIT ? OFFSET ?`

		params = append(params, limit, offset)

		rows, err := tx.Query(query, params...)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			siafund, err := scanUnspentSiafundElement(rows, basis.Height)
			if err != nil {
				return fmt.Errorf("failed to scan siafund element: %w", err)
			}
			siafunds = append(siafunds, siafund)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		// retrieve the merkle proofs for the siafund elements
		if s.indexMode == wallet.IndexModeFull {
			indices := make([]uint64, len(siafunds))
			for i, se := range siafunds {
				indices[i] = se.StateElement.LeafIndex
			}
			proofs, err := fillElementProofs(tx, indices)
			if err != nil {
				return fmt.Errorf("failed to fill element proofs: %w", err)
			}
			for i, proof := range proofs {
				siafunds[i].StateElement.MerkleProof = proof
			}
		}
		return nil
	})
	return
}

// AnnotateV1Events annotates a list of unconfirmed transactions with
// relevant addresses and siacoin/siafund elements.
func (s *Store) AnnotateV1Events(index types.ChainIndex, timestamp time.Time, v1 []types.Transaction) (annotated []wallet.Event, err error) {
	err = s.transaction(func(tx *txn) error {
		siacoinElementStmt, err := tx.Prepare(`SELECT se.id, se.siacoin_value, se.merkle_proof, se.leaf_index, se.maturity_height, sa.sia_address
		FROM siacoin_elements se
		INNER JOIN sia_addresses sa ON (se.address_id = sa.id)
		WHERE se.id=$1`)
		if err != nil {
			return fmt.Errorf("failed to prepare siacoin statement: %w", err)
		}
		defer siacoinElementStmt.Close()

		siacoinElementCache := make(map[types.SiacoinOutputID]types.SiacoinElement)
		fetchSiacoinElement := func(id types.SiacoinOutputID) (types.SiacoinElement, error) {
			if se, ok := siacoinElementCache[id]; ok {
				return se, nil
			}

			se, err := scanSiacoinElement(siacoinElementStmt.QueryRow(encode(id)))
			if err != nil {
				return types.SiacoinElement{}, fmt.Errorf("failed to fetch siacoin element: %w", err)
			}
			siacoinElementCache[id] = se
			return se, nil
		}

		siafundElementStmt, err := tx.Prepare(`SELECT se.id, se.leaf_index, se.merkle_proof, se.siafund_value, se.claim_start, sa.sia_address
		FROM siafund_elements se
		INNER JOIN sia_addresses sa ON (se.address_id = sa.id)
		WHERE se.id=$1`)
		if err != nil {
			return fmt.Errorf("failed to prepare siafund statement: %w", err)
		}
		defer siafundElementStmt.Close()

		siafundElementCache := make(map[types.SiafundOutputID]types.SiafundElement)
		fetchSiafundElement := func(id types.SiafundOutputID) (types.SiafundElement, error) {
			if se, ok := siafundElementCache[id]; ok {
				return se, nil
			}

			se, err := scanSiafundElement(siafundElementStmt.QueryRow(encode(id)))
			if err != nil {
				return types.SiafundElement{}, fmt.Errorf("failed to fetch siafund element: %w", err)
			}
			siafundElementCache[id] = se
			return se, nil
		}

		addEvent := func(id types.Hash256, data wallet.EventData) {
			annotated = append(annotated, wallet.Event{
				ID:             id,
				Index:          index,
				Timestamp:      timestamp,
				MaturityHeight: index.Height,
				Type:           wallet.EventTypeV1Transaction,
				Data:           data,
			})
		}

		for _, txn := range v1 {
			var relevant bool
			ev := wallet.EventV1Transaction{
				Transaction: txn,
			}

			for _, input := range txn.SiacoinInputs {
				// fetch the siacoin element
				sce, err := fetchSiacoinElement(input.ParentID)
				if errors.Is(err, sql.ErrNoRows) {
					continue // ignore elements that are not found
				} else if err != nil {
					return fmt.Errorf("failed to fetch siacoin element %q: %w", input.ParentID, err)
				}
				ev.SpentSiacoinElements = append(ev.SpentSiacoinElements, sce)
				relevant = true
			}

			for i, output := range txn.SiacoinOutputs {
				sce := types.SiacoinElement{
					ID: txn.SiacoinOutputID(i),
					StateElement: types.StateElement{
						LeafIndex: types.UnassignedLeafIndex,
					},
					SiacoinOutput: output,
				}
				siacoinElementCache[sce.ID] = sce
				relevant = true
			}

			for _, input := range txn.SiafundInputs {
				// fetch the siafund element
				sfe, err := fetchSiafundElement(input.ParentID)
				if errors.Is(err, sql.ErrNoRows) {
					continue // ignore elements that are not found
				} else if err != nil {
					return fmt.Errorf("failed to fetch siafund element %q: %w", input.ParentID, err)
				}
				ev.SpentSiafundElements = append(ev.SpentSiafundElements, sfe)
				relevant = true
			}

			for i, output := range txn.SiafundOutputs {
				sfe := types.SiafundElement{
					ID: txn.SiafundOutputID(i),
					StateElement: types.StateElement{
						LeafIndex: types.UnassignedLeafIndex,
					},
					SiafundOutput: output,
				}
				siafundElementCache[sfe.ID] = sfe
				relevant = true
			}

			if !relevant {
				continue
			}

			addEvent(types.Hash256(txn.ID()), ev)
		}
		return nil
	})
	return
}

func getAddressEvents(tx *txn, address types.Address, offset, limit int) (eventIDs []int64, err error) {
	const query = `SELECT DISTINCT ea.event_id
FROM event_addresses ea
INNER JOIN sia_addresses sa ON ea.address_id = sa.id
WHERE sa.sia_address = $1
ORDER BY ea.event_maturity_height DESC, ea.event_id DESC
LIMIT $2 OFFSET $3;`

	rows, err := tx.Query(query, encode(address), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		eventIDs = append(eventIDs, id)
	}
	return eventIDs, rows.Err()
}

func (s *Store) getAddressesEvents(tx *txn, addresses []types.Address, offset, limit int) (eventIDs []int64, err error) {
	if len(addresses) == 0 {
		return nil, nil // no addresses, no events
	}

	query := `SELECT DISTINCT ea.event_id
FROM event_addresses ea
INNER JOIN sia_addresses sa ON ea.address_id = sa.id
WHERE sa.sia_address IN (` + queryPlaceHolders(len(addresses)) + `)
ORDER BY ea.event_maturity_height DESC, ea.event_id DESC
LIMIT ? OFFSET ?;`

	params := make([]any, 0, len(addresses)+2)
	for _, addr := range addresses {
		params = append(params, encode(addr))
	}
	params = append(params, limit, offset)
	rows, err := tx.Query(query, params...)
	if err != nil {
		return nil, fmt.Errorf("failed to query address events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan event ID: %w", err)
		}
		eventIDs = append(eventIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating over rows: %w", err)
	}
	return eventIDs, nil
}
