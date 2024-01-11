package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/wallet"
)

func insertAddress(tx txn, addr types.Address) (id int64, err error) {
	const query = `INSERT INTO sia_addresses (sia_address, siacoin_balance, siafund_balance) 
VALUES ($1, $2, 0) ON CONFLICT (sia_address) DO UPDATE SET sia_address=EXCLUDED.sia_address 
RETURNING id`

	err = tx.QueryRow(query, encode(addr), (*sqlCurrency)(&types.ZeroCurrency)).Scan(&id)
	return
}

// WalletEvents returns the events relevant to a wallet, sorted by height descending.
func (s *Store) WalletEvents(walletID string, offset, limit int) (events []wallet.Event, err error) {
	err = s.transaction(func(tx txn) error {
		const query = `SELECT ev.id, ev.date_created, ci.height, ci.block_id, ev.event_type, ev.event_data
FROM events ev
INNER JOIN chain_indices ci ON (ev.index_id = ci.id)
WHERE ev.id IN (SELECT event_id FROM event_addresses WHERE address_id IN (SELECT address_id FROM wallet_addresses WHERE wallet_id=$1))
ORDER BY ci.height DESC, ev.id ASC
LIMIT $2 OFFSET $3`

		rows, err := tx.Query(query, walletID, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var eventID int64
			var event wallet.Event
			var eventType string
			var eventBuf []byte

			err := rows.Scan(&eventID, (*sqlTime)(&event.Timestamp), &event.Index.Height, decode(&event.Index.ID), &eventType, &eventBuf)
			if err != nil {
				return fmt.Errorf("failed to scan event: %w", err)
			}

			switch eventType {
			case wallet.EventTypeTransaction:
				var tx wallet.EventTransaction
				if err = json.Unmarshal(eventBuf, &tx); err != nil {
					return fmt.Errorf("failed to unmarshal transaction event: %w", err)
				}
				event.Val = &tx
			case wallet.EventTypeMissedFileContract:
				var m wallet.EventMissedFileContract
				if err = json.Unmarshal(eventBuf, &m); err != nil {
					return fmt.Errorf("failed to unmarshal missed file contract event: %w", err)
				}
				event.Val = &m
			case wallet.EventTypeMinerPayout:
				var m wallet.EventMinerPayout
				if err = json.Unmarshal(eventBuf, &m); err != nil {
					return fmt.Errorf("failed to unmarshal payout event: %w", err)
				}
				event.Val = &m
			default:
				return fmt.Errorf("unknown event type: %s", eventType)
			}

			// event.Relevant = relevantAddresses[eventID]
			events = append(events, event)
		}
		return nil
	})
	return
}

// AddWallet adds a wallet to the database.
func (s *Store) AddWallet(name string, info json.RawMessage) error {
	return s.transaction(func(tx txn) error {
		const query = `INSERT INTO wallets (id, extra_data) VALUES ($1, $2)`

		_, err := tx.Exec(query, name, info)
		if err != nil {
			return fmt.Errorf("failed to insert wallet: %w", err)
		}
		return nil
	})
}

// DeleteWallet deletes a wallet from the database. This does not stop tracking
// addresses that were previously associated with the wallet.
func (s *Store) DeleteWallet(name string) error {
	return s.transaction(func(tx txn) error {
		_, err := tx.Exec(`DELETE FROM wallets WHERE id=$1`, name)
		return err
	})
}

// Wallets returns a map of wallet names to wallet extra data.
func (s *Store) Wallets() (map[string]json.RawMessage, error) {
	wallets := make(map[string]json.RawMessage)
	err := s.transaction(func(tx txn) error {
		const query = `SELECT id, extra_data FROM wallets`

		rows, err := tx.Query(query)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var friendlyName string
			var extraData json.RawMessage
			if err := rows.Scan(&friendlyName, &extraData); err != nil {
				return fmt.Errorf("failed to scan wallet: %w", err)
			}
			wallets[friendlyName] = extraData
		}
		return nil
	})
	return wallets, err
}

// AddAddress adds an address to a wallet.
func (s *Store) AddAddress(walletID string, address types.Address, info json.RawMessage) error {
	return s.transaction(func(tx txn) error {
		addressID, err := insertAddress(tx, address)
		if err != nil {
			return fmt.Errorf("failed to insert address: %w", err)
		}
		_, err = tx.Exec(`INSERT INTO wallet_addresses (wallet_id, extra_data, address_id) VALUES ($1, $2, $3)`, walletID, info, addressID)
		return err
	})
}

// RemoveAddress removes an address from a wallet. This does not stop tracking
// the address.
func (s *Store) RemoveAddress(walletID string, address types.Address) error {
	return s.transaction(func(tx txn) error {
		const query = `DELETE FROM wallet_addresses WHERE wallet_id=$1 AND address_id=(SELECT id FROM sia_addresses WHERE sia_address=$2)`
		_, err := tx.Exec(query, walletID, encode(address))
		return err
	})
}

// Addresses returns a map of addresses to their extra data for a wallet.
func (s *Store) Addresses(walletID string) (map[types.Address]json.RawMessage, error) {
	addresses := make(map[types.Address]json.RawMessage)
	err := s.transaction(func(tx txn) error {
		const query = `SELECT sa.sia_address, wa.extra_data 
FROM wallet_addresses wa
INNER JOIN sia_addresses sa ON (sa.id = wa.address_id)
WHERE wa.wallet_id=$1`

		rows, err := tx.Query(query, walletID)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var address types.Address
			var extraData json.RawMessage
			if err := rows.Scan(decode(&address), &extraData); err != nil {
				return fmt.Errorf("failed to scan address: %w", err)
			}
			addresses[address] = extraData
		}
		return nil
	})
	return addresses, err
}

// UnspentSiacoinOutputs returns the unspent siacoin outputs for a wallet.
func (s *Store) UnspentSiacoinOutputs(walletID string) (siacoins []types.SiacoinElement, err error) {
	err = s.transaction(func(tx txn) error {
		const query = `SELECT se.id, se.leaf_index, se.merkle_proof, se.siacoin_value, sa.sia_address, se.maturity_height 
		FROM siacoin_elements se
		INNER JOIN sia_addresses sa ON (se.address_id = sa.id)
		WHERE se.address_id IN (SELECT address_id FROM wallet_addresses WHERE wallet_id=$1)`

		rows, err := tx.Query(query, walletID)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var siacoin types.SiacoinElement
			err := rows.Scan(decode(&siacoin.ID), &siacoin.LeafIndex, decodeSlice[types.Hash256](&siacoin.MerkleProof), (*sqlCurrency)(&siacoin.SiacoinOutput.Value), decode(&siacoin.SiacoinOutput.Address), &siacoin.MaturityHeight)
			if err != nil {
				return fmt.Errorf("failed to scan siacoin element: %w", err)
			}

			siacoins = append(siacoins, siacoin)
		}
		return nil
	})
	return
}

// UnspentSiafundOutputs returns the unspent siafund outputs for a wallet.
func (s *Store) UnspentSiafundOutputs(walletID string) (siafunds []types.SiafundElement, err error) {
	err = s.transaction(func(tx txn) error {
		const query = `SELECT se.id, se.leaf_index, se.merkle_proof, se.siafund_value, se.claim_start, sa.sia_address 
		FROM siafund_elements se
		INNER JOIN sia_addresses sa ON (se.address_id = sa.id)
		WHERE se.address_id IN (SELECT address_id FROM wallet_addresses WHERE wallet_id=$1)`

		rows, err := tx.Query(query, walletID)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var siafund types.SiafundElement
			err := rows.Scan(decode(&siafund.ID), &siafund.LeafIndex, decodeSlice(&siafund.MerkleProof), &siafund.SiafundOutput.Value, (*sqlCurrency)(&siafund.ClaimStart), decode(&siafund.SiafundOutput.Address))
			if err != nil {
				return fmt.Errorf("failed to scan siacoin element: %w", err)
			}
			siafunds = append(siafunds, siafund)
		}
		return nil
	})
	return
}

// WalletBalance returns the total balance of a wallet.
func (s *Store) WalletBalance(walletID string) (sc types.Currency, sf uint64, err error) {
	err = s.transaction(func(tx txn) error {
		const query = `SELECT siacoin_balance, siafund_balance FROM sia_addresses sa
		INNER JOIN wallet_addresses wa ON (sa.id = wa.address_id)
		WHERE wa.wallet_id=$1`

		rows, err := tx.Query(query, walletID)
		if err != nil {
			return err
		}

		for rows.Next() {
			var siacoin types.Currency
			var siafund uint64

			if err := rows.Scan((*sqlCurrency)(&siacoin), &siafund); err != nil {
				return fmt.Errorf("failed to scan address balance: %w", err)
			}
			sc = sc.Add(siacoin)
			sf += siafund
		}
		return nil
	})
	return
}

// AddressBalance returns the balance of a single address.
func (s *Store) AddressBalance(address types.Address) (sc types.Currency, sf uint64, err error) {
	err = s.transaction(func(tx txn) error {
		const query = `SELECT siacoin_balance, siafund_balance FROM address_balance WHERE sia_address=$1`
		return tx.QueryRow(query, encode(address)).Scan((*sqlCurrency)(&sc), &sf)
	})
	return
}

// Annotate annotates a list of transactions using the wallet's addresses.
func (s *Store) Annotate(walletID string, txns []types.Transaction) (annotated []wallet.PoolTransaction, err error) {
	err = s.transaction(func(tx txn) error {
		stmt, err := tx.Prepare(`SELECT sia_address FROM wallet_addresses WHERE wallet_id=$1 AND sia_address=$2 LIMIT 1`)
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
			err := stmt.QueryRow(walletID, encode(address)).Scan(dbID)
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
