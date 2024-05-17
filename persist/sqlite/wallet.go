package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/wallet"
)

func (s *Store) getWalletEventRelevantAddresses(tx *txn, id wallet.ID, eventIDs []int64) (map[int64][]types.Address, error) {
	query := `SELECT ea.event_id, sa.sia_address
FROM event_addresses ea
INNER JOIN sia_addresses sa ON (ea.address_id = sa.id)
WHERE event_id IN (` + queryPlaceHolders(len(eventIDs)) + `) AND address_id IN (SELECT address_id FROM wallet_addresses WHERE wallet_id=?)`

	rows, err := tx.Query(query, append(queryArgs(eventIDs), id)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	relevantAddresses := make(map[int64][]types.Address)
	for rows.Next() {
		var eventID int64
		var address types.Address
		if err := rows.Scan(&eventID, decode(&address)); err != nil {
			return nil, fmt.Errorf("failed to scan relevant address: %w", err)
		}
		relevantAddresses[eventID] = append(relevantAddresses[eventID], address)
	}
	return relevantAddresses, rows.Err()
}

// WalletEvents returns the events relevant to a wallet, sorted by height descending.
func (s *Store) WalletEvents(id wallet.ID, offset, limit int) (events []wallet.Event, err error) {
	err = s.transaction(func(tx *txn) error {
		var dbIDs []int64
		events, dbIDs, err = getWalletEvents(tx, id, offset, limit)
		if err != nil {
			return fmt.Errorf("failed to get wallet events: %w", err)
		}

		eventRelevantAddresses, err := s.getWalletEventRelevantAddresses(tx, id, dbIDs)
		if err != nil {
			return fmt.Errorf("failed to get relevant addresses: %w", err)
		}

		for i := range events {
			events[i].Relevant = eventRelevantAddresses[dbIDs[i]]
		}
		return nil
	})
	return
}

// AddWallet adds a wallet to the database.
func (s *Store) AddWallet(w wallet.Wallet) (wallet.Wallet, error) {
	w.DateCreated = time.Now().Truncate(time.Second)
	w.LastUpdated = time.Now().Truncate(time.Second)

	err := s.transaction(func(tx *txn) error {
		const query = `INSERT INTO wallets (friendly_name, description, date_created, last_updated, extra_data) VALUES ($1, $2, $3, $4, $5) RETURNING id`
		return tx.QueryRow(query, w.Name, w.Description, encode(w.DateCreated), encode(w.LastUpdated), w.Metadata).Scan(&w.ID)
	})
	return w, err
}

// UpdateWallet updates a wallet in the database.
func (s *Store) UpdateWallet(w wallet.Wallet) (wallet.Wallet, error) {
	w.LastUpdated = time.Now()
	err := s.transaction(func(tx *txn) error {
		var dummyID int64
		const query = `UPDATE wallets SET friendly_name=$1, description=$2, last_updated=$3, extra_data=$4 WHERE id=$5 RETURNING id, date_created, last_updated`
		err := tx.QueryRow(query, w.Name, w.Description, encode(w.LastUpdated), w.Metadata, w.ID).Scan(&dummyID, decode(&w.DateCreated), decode(&w.LastUpdated))
		if errors.Is(err, sql.ErrNoRows) {
			return wallet.ErrNotFound
		}
		return err
	})
	return w, err
}

// DeleteWallet deletes a wallet from the database. This does not stop tracking
// addresses that were previously associated with the wallet.
func (s *Store) DeleteWallet(id wallet.ID) error {
	return s.transaction(func(tx *txn) error {
		_, err := tx.Exec(`DELETE FROM wallet_addresses WHERE wallet_id=$1`, id)
		if err != nil {
			return fmt.Errorf("failed to delete wallet addresses: %w", err)
		}

		var dummyID int64
		err = tx.QueryRow(`DELETE FROM wallets WHERE id=$1 RETURNING id`, id).Scan(&dummyID)
		if errors.Is(err, sql.ErrNoRows) {
			return wallet.ErrNotFound
		}
		return err
	})
}

// Wallets returns a map of wallet names to wallet extra data.
func (s *Store) Wallets() (wallets []wallet.Wallet, err error) {
	err = s.transaction(func(tx *txn) error {
		const query = `SELECT id, friendly_name, description, date_created, last_updated, extra_data FROM wallets`

		rows, err := tx.Query(query)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var w wallet.Wallet
			if err := rows.Scan(&w.ID, &w.Name, &w.Description, decode(&w.DateCreated), decode(&w.LastUpdated), (*[]byte)(&w.Metadata)); err != nil {
				return fmt.Errorf("failed to scan wallet: %w", err)
			}
			wallets = append(wallets, w)
		}
		return rows.Err()
	})
	return
}

// AddWalletAddress adds an address to a wallet.
func (s *Store) AddWalletAddress(id wallet.ID, addr wallet.Address) error {
	return s.transaction(func(tx *txn) error {
		if err := walletExists(tx, id); err != nil {
			return err
		}

		addressID, err := insertAddress(tx, addr.Address)
		if err != nil {
			return fmt.Errorf("failed to insert address: %w", err)
		}

		var encodedPolicy any
		if addr.SpendPolicy != nil {
			encodedPolicy = encode(*addr.SpendPolicy)
		}

		_, err = tx.Exec(`INSERT INTO wallet_addresses (wallet_id, address_id, description, spend_policy, extra_data) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (wallet_id, address_id) DO UPDATE set description=EXCLUDED.description, spend_policy=EXCLUDED.spend_policy, extra_data=EXCLUDED.extra_data`, id, addressID, addr.Description, encodedPolicy, addr.Metadata)
		return err
	})
}

// RemoveWalletAddress removes an address from a wallet. This does not stop tracking
// the address.
func (s *Store) RemoveWalletAddress(id wallet.ID, address types.Address) error {
	return s.transaction(func(tx *txn) error {
		const query = `DELETE FROM wallet_addresses WHERE wallet_id=$1 AND address_id=(SELECT id FROM sia_addresses WHERE sia_address=$2) RETURNING address_id`
		var dummyID int64
		err := tx.QueryRow(query, id, encode(address)).Scan(&dummyID)
		if errors.Is(err, sql.ErrNoRows) {
			return wallet.ErrNotFound
		}
		return err
	})
}

// WalletAddresses returns a slice of addresses registered to the wallet.
func (s *Store) WalletAddresses(id wallet.ID) (addresses []wallet.Address, err error) {
	err = s.transaction(func(tx *txn) error {
		if err := walletExists(tx, id); err != nil {
			return err
		}

		const query = `SELECT sa.sia_address, wa.description, wa.spend_policy, wa.extra_data
FROM wallet_addresses wa
INNER JOIN sia_addresses sa ON (sa.id = wa.address_id)
WHERE wa.wallet_id=$1`

		rows, err := tx.Query(query, id)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var address wallet.Address
			var decodedPolicy any
			if err := rows.Scan(decode(&address.Address), &address.Description, &decodedPolicy, (*[]byte)(&address.Metadata)); err != nil {
				return fmt.Errorf("failed to scan address: %w", err)
			}

			if decodedPolicy != nil {
				switch v := decodedPolicy.(type) {
				case []byte:
					dec := types.NewBufDecoder(v)
					address.SpendPolicy = new(types.SpendPolicy)
					address.SpendPolicy.DecodeFrom(dec)
					if err := dec.Err(); err != nil {
						return fmt.Errorf("failed to decode spend policy: %w", err)
					}
				default:
					return fmt.Errorf("unexpected spend policy type: %T", decodedPolicy)
				}
			}

			addresses = append(addresses, address)
		}
		return rows.Err()
	})
	return
}

// WalletSiacoinOutputs returns the unspent siacoin outputs for a wallet.
func (s *Store) WalletSiacoinOutputs(id wallet.ID, offset, limit int) (siacoins []types.SiacoinElement, err error) {
	err = s.transaction(func(tx *txn) error {
		if err := walletExists(tx, id); err != nil {
			return err
		}

		const query = `SELECT se.id, se.siacoin_value, se.merkle_proof, se.leaf_index, se.maturity_height, sa.sia_address
		FROM siacoin_elements se
		INNER JOIN sia_addresses sa ON (se.address_id = sa.id)
		WHERE se.spent_index_id IS NULL AND se.address_id IN (SELECT address_id FROM wallet_addresses WHERE wallet_id=$1)
		LIMIT $2 OFFSET $3`

		rows, err := tx.Query(query, id, limit, offset)
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

		if err := rows.Err(); err != nil {
			return err
		}

		// retrieve the merkle proofs for the siacoin elements
		if s.indexMode == wallet.IndexModeFull {
			indices := make([]uint64, len(siacoins))
			for i, se := range siacoins {
				indices[i] = se.LeafIndex
			}
			proofs, err := fillElementProofs(tx, indices)
			if err != nil {
				return fmt.Errorf("failed to fill element proofs: %w", err)
			}
			for i, proof := range proofs {
				siacoins[i].MerkleProof = proof
			}
		}
		return nil
	})
	return
}

// WalletSiafundOutputs returns the unspent siafund outputs for a wallet.
func (s *Store) WalletSiafundOutputs(id wallet.ID, offset, limit int) (siafunds []types.SiafundElement, err error) {
	err = s.transaction(func(tx *txn) error {
		if err := walletExists(tx, id); err != nil {
			return err
		}

		const query = `SELECT se.id, se.leaf_index, se.merkle_proof, se.siafund_value, se.claim_start, sa.sia_address 
		FROM siafund_elements se
		INNER JOIN sia_addresses sa ON (se.address_id = sa.id)
		WHERE se.spent_index_id IS NULL AND se.address_id IN (SELECT address_id FROM wallet_addresses WHERE wallet_id=$1)
		LIMIT $2 OFFSET $3`

		rows, err := tx.Query(query, id, limit, offset)
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
		if err := rows.Err(); err != nil {
			return err
		}

		// retrieve the merkle proofs for the siacoin elements
		if s.indexMode == wallet.IndexModeFull {
			indices := make([]uint64, len(siafunds))
			for i, se := range siafunds {
				indices[i] = se.LeafIndex
			}
			proofs, err := fillElementProofs(tx, indices)
			if err != nil {
				return fmt.Errorf("failed to fill element proofs: %w", err)
			}
			for i, proof := range proofs {
				siafunds[i].MerkleProof = proof
			}
		}
		return nil
	})
	return
}

// WalletBalance returns the total balance of a wallet.
func (s *Store) WalletBalance(id wallet.ID) (balance wallet.Balance, err error) {
	err = s.transaction(func(tx *txn) error {
		if err := walletExists(tx, id); err != nil {
			return err
		}

		const query = `SELECT siacoin_balance, immature_siacoin_balance, siafund_balance FROM sia_addresses sa
		INNER JOIN wallet_addresses wa ON (sa.id = wa.address_id)
		WHERE wa.wallet_id=$1`

		rows, err := tx.Query(query, id)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var addressSC types.Currency
			var addressISC types.Currency
			var addressSF uint64

			if err := rows.Scan(decode(&addressSC), decode(&addressISC), &addressSF); err != nil {
				return fmt.Errorf("failed to scan address balance: %w", err)
			}
			balance.Siacoins = balance.Siacoins.Add(addressSC)
			balance.ImmatureSiacoins = balance.ImmatureSiacoins.Add(addressISC)
			balance.Siafunds += addressSF
		}
		return rows.Err()
	})
	return
}

// Annotate annotates a list of transactions using the wallet's addresses.
func (s *Store) Annotate(id wallet.ID, txns []types.Transaction) (annotated []wallet.PoolTransaction, err error) {
	err = s.transaction(func(tx *txn) error {
		if err := walletExists(tx, id); err != nil {
			return err
		}

		const query = `SELECT sa.id FROM sia_addresses sa
INNER JOIN wallet_addresses wa ON (sa.id = wa.address_id)
WHERE wa.wallet_id=$1 AND sa.sia_address=$2 LIMIT 1`
		stmt, err := tx.Prepare(query)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer stmt.Close()

		// note: this would be more performant for small wallets to load all
		// addresses into memory. However, for larger wallets (> 10K addresses),
		// this is time consuming. Instead, the database is queried for each
		// address. Monitor performance and consider changing this in the
		// future. From a memory perspective, it would be fine to lazy load all
		// addresses into memory.
		ownsAddress := func(address types.Address) bool {
			var dbID int64
			err := stmt.QueryRow(id, encode(address)).Scan(&dbID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				panic(err) // database error
			}
			return err == nil
		}

		for _, txn := range txns {
			ptxn := wallet.Annotate(txn, ownsAddress)
			if ptxn.Type != "unrelated" {
				annotated = append(annotated, ptxn)
			}
		}
		return nil
	})
	return
}

func scanSiacoinElement(s scanner) (se types.SiacoinElement, err error) {
	err = s.Scan(decode(&se.ID), decode(&se.SiacoinOutput.Value), decodeSlice(&se.MerkleProof), &se.LeafIndex, &se.MaturityHeight, decode(&se.SiacoinOutput.Address))
	return
}

func scanSiafundElement(s scanner) (se types.SiafundElement, err error) {
	err = s.Scan(decode(&se.ID), &se.LeafIndex, decodeSlice(&se.MerkleProof), &se.SiafundOutput.Value, decode(&se.ClaimStart), decode(&se.SiafundOutput.Address))
	return
}

func insertAddress(tx *txn, addr types.Address) (id int64, err error) {
	const query = `INSERT INTO sia_addresses (sia_address, siacoin_balance, immature_siacoin_balance, siafund_balance) 
VALUES ($1, $2, $3, 0) ON CONFLICT (sia_address) DO UPDATE SET sia_address=EXCLUDED.sia_address 
RETURNING id`

	err = tx.QueryRow(query, encode(addr), encode(types.ZeroCurrency), encode(types.ZeroCurrency)).Scan(&id)
	return
}

func fillElementProofs(tx *txn, indices []uint64) (proofs [][]types.Hash256, _ error) {
	if len(indices) == 0 {
		return nil, nil
	}

	var numLeaves uint64
	if err := tx.QueryRow(`SELECT element_num_leaves FROM global_settings LIMIT 1`).Scan(&numLeaves); err != nil {
		return nil, fmt.Errorf("failed to query state tree leaves: %w", err)
	}

	stmt, err := tx.Prepare(`SELECT value FROM state_tree WHERE row=? AND column=?`)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	data := make(map[uint64]map[uint64]types.Hash256)
	for _, leafIndex := range indices {
		proof := make([]types.Hash256, bits.Len64(leafIndex^numLeaves)-1)
		for j := range proof {
			row, col := uint64(j), (leafIndex>>j)^1

			// check if the hash is already in the cache
			if h, ok := data[row][col]; ok {
				proof[j] = h
				continue
			}

			// query the hash from the database
			if err := stmt.QueryRow(row, col).Scan(decode(&proof[j])); err != nil {
				return nil, fmt.Errorf("failed to query state element (%d,%d): %w", row, col, err)
			}

			// cache the hash
			if _, ok := data[row]; !ok {
				data[row] = make(map[uint64]types.Hash256)
			}
			data[row][col] = proof[j]
		}
		proofs = append(proofs, proof)
	}
	return
}

func scanEvent(s scanner) (ev wallet.Event, eventID int64, err error) {
	var eventBuf []byte

	err = s.Scan(&eventID, decode(&ev.ID), &ev.MaturityHeight, decode(&ev.Timestamp), &ev.Index.Height, decode(&ev.Index.ID), &ev.Type, &eventBuf)
	if err != nil {
		return
	}

	switch ev.Type {
	case wallet.EventTypeTransaction:
		var tx wallet.EventTransaction
		if err = json.Unmarshal(eventBuf, &tx); err != nil {
			return wallet.Event{}, 0, fmt.Errorf("failed to unmarshal transaction event: %w", err)
		}
		ev.Data = &tx
	case wallet.EventTypeContractPayout:
		var m wallet.EventContractPayout
		if err = json.Unmarshal(eventBuf, &m); err != nil {
			return wallet.Event{}, 0, fmt.Errorf("failed to unmarshal missed file contract event: %w", err)
		}
		ev.Data = &m
	case wallet.EventTypeMinerPayout:
		var m wallet.EventMinerPayout
		if err = json.Unmarshal(eventBuf, &m); err != nil {
			return wallet.Event{}, 0, fmt.Errorf("failed to unmarshal payout event: %w", err)
		}
		ev.Data = &m
	case wallet.EventTypeFoundationSubsidy:
		var m wallet.EventFoundationSubsidy
		if err = json.Unmarshal(eventBuf, &m); err != nil {
			return wallet.Event{}, 0, fmt.Errorf("failed to unmarshal foundation subsidy event: %w", err)
		}
		ev.Data = &m
	default:
		return wallet.Event{}, 0, fmt.Errorf("unknown event type: %q", ev.Type)
	}
	return
}

func getWalletEvents(tx *txn, id wallet.ID, offset, limit int) (events []wallet.Event, eventIDs []int64, err error) {
	const query = `SELECT ev.id, ev.event_id, ev.maturity_height, ev.date_created, ci.height, ci.block_id, ev.event_type, ev.event_data
	FROM events ev
	INNER JOIN chain_indices ci ON (ev.chain_index_id = ci.id)
	WHERE ev.id IN (SELECT event_id FROM event_addresses WHERE address_id IN (SELECT address_id FROM wallet_addresses WHERE wallet_id=$1))
	ORDER BY ev.maturity_height DESC, ev.id DESC
	LIMIT $2 OFFSET $3`

	rows, err := tx.Query(query, id, limit, offset)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	for rows.Next() {
		event, eventID, err := scanEvent(rows)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to scan event: %w", err)
		}

		events = append(events, event)
		eventIDs = append(eventIDs, eventID)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return
}

func walletExists(tx *txn, id wallet.ID) error {
	const query = `SELECT 1 FROM wallets WHERE id=$1`
	var dummy int
	err := tx.QueryRow(query, id).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return wallet.ErrNotFound
	}
	return err
}
