package wallet

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	"go.uber.org/zap"
)

type (
	// A ChainManager manages the consensus state
	ChainManager interface {
		AddSubscriber(chain.Subscriber, types.ChainIndex) error
		RemoveSubscriber(chain.Subscriber)

		BestIndex(height uint64) (types.ChainIndex, bool)
	}

	// A Store is a persistent store of wallet data.
	Store interface {
		chain.Subscriber

		WalletEvents(name string, offset, limit int) ([]Event, error)
		AddWallet(name string, info json.RawMessage) error
		DeleteWallet(name string) error
		Wallets() (map[string]json.RawMessage, error)

		AddAddress(walletID string, address types.Address, info json.RawMessage) error
		RemoveAddress(walletID string, address types.Address) error
		Addresses(walletID string) (map[types.Address]json.RawMessage, error)
		UnspentSiacoinOutputs(walletID string) ([]types.SiacoinElement, error)
		UnspentSiafundOutputs(walletID string) ([]types.SiafundElement, error)
		Annotate(walletID string, txns []types.Transaction) ([]PoolTransaction, error)
		WalletBalance(walletID string) (sc, immature types.Currency, sf uint64, err error)

		AddressBalance(address types.Address) (sc types.Currency, sf uint64, err error)

		LastCommittedIndex() (types.ChainIndex, error)
	}

	// A Manager manages wallets.
	Manager struct {
		chain ChainManager
		store Store
		log   *zap.Logger

		mu   sync.Mutex
		used map[types.Hash256]bool
	}
)

// AddWallet adds the given wallet.
func (m *Manager) AddWallet(name string, info json.RawMessage) error {
	return m.store.AddWallet(name, info)
}

// DeleteWallet deletes the given wallet.
func (m *Manager) DeleteWallet(name string) error {
	return m.store.DeleteWallet(name)
}

// Wallets returns the wallets of the wallet manager.
func (m *Manager) Wallets() (map[string]json.RawMessage, error) {
	return m.store.Wallets()
}

// AddAddress adds the given address to the given wallet.
func (m *Manager) AddAddress(name string, addr types.Address, info json.RawMessage) error {
	return m.store.AddAddress(name, addr, info)
}

// RemoveAddress removes the given address from the given wallet.
func (m *Manager) RemoveAddress(name string, addr types.Address) error {
	return m.store.RemoveAddress(name, addr)
}

// Addresses returns the addresses of the given wallet.
func (m *Manager) Addresses(name string) (map[types.Address]json.RawMessage, error) {
	return m.store.Addresses(name)
}

// Events returns the events of the given wallet.
func (m *Manager) Events(name string, offset, limit int) ([]Event, error) {
	return m.store.WalletEvents(name, offset, limit)
}

// UnspentSiacoinOutputs returns the unspent siacoin outputs of the given wallet
func (m *Manager) UnspentSiacoinOutputs(name string) ([]types.SiacoinElement, error) {
	return m.store.UnspentSiacoinOutputs(name)
}

// UnspentSiafundOutputs returns the unspent siafund outputs of the given wallet
func (m *Manager) UnspentSiafundOutputs(name string) ([]types.SiafundElement, error) {
	return m.store.UnspentSiafundOutputs(name)
}

// Annotate annotates the given transactions with the wallet they belong to.
func (m *Manager) Annotate(name string, pool []types.Transaction) ([]PoolTransaction, error) {
	return m.store.Annotate(name, pool)
}

// WalletBalance returns the balance of the given wallet.
func (m *Manager) WalletBalance(walletID string) (sc, immature types.Currency, sf uint64, err error) {
	return m.store.WalletBalance(walletID)
}

// AddressBalance returns the balance of the given address.
func (m *Manager) AddressBalance(address types.Address) (sc types.Currency, sf uint64, err error) {
	return m.store.AddressBalance(address)
}

// Reserve reserves the given ids for the given duration.
func (m *Manager) Reserve(ids []types.Hash256, duration time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// check if any of the ids are already reserved
	for _, id := range ids {
		if m.used[id] {
			return fmt.Errorf("output %q already reserved", id)
		}
	}

	// reserve the ids
	for _, id := range ids {
		m.used[id] = true
	}

	// sleep for the duration and then unreserve the ids
	time.AfterFunc(duration, func() {
		m.mu.Lock()
		defer m.mu.Unlock()

		for _, id := range ids {
			delete(m.used, id)
		}
	})
	return nil
}

// Subscribe resubscribes the indexer starting at the given height.
func (m *Manager) Subscribe(startHeight uint64) error {
	var index types.ChainIndex
	if startHeight > 0 {
		var ok bool
		index, ok = m.chain.BestIndex(startHeight - 1)
		if !ok {
			return errors.New("invalid height")
		}
	}
	m.chain.RemoveSubscriber(m.store)
	return m.chain.AddSubscriber(m.store, index)
}

// NewManager creates a new wallet manager.
func NewManager(cm ChainManager, store Store, log *zap.Logger) (*Manager, error) {
	m := &Manager{
		chain: cm,
		store: store,
		log:   log,
	}

	lastTip, err := store.LastCommittedIndex()
	if err != nil {
		return nil, fmt.Errorf("failed to get last committed index: %w", err)
	} else if err := cm.AddSubscriber(store, lastTip); err != nil {
		return nil, fmt.Errorf("failed to subscribe to chain manager: %w", err)
	}
	return m, nil
}
