package wallet

import (
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

		WalletEvents(id WalletID, offset, limit int) ([]Event, error)
		AddWallet(Wallet) (Wallet, error)
		UpdateWallet(Wallet) (Wallet, error)
		DeleteWallet(id WalletID) error
		WalletBalance(id WalletID) (Balance, error)
		WalletSiacoinOutputs(id WalletID, offset, limit int) ([]types.SiacoinElement, error)
		WalletSiafundOutputs(id WalletID, offset, limit int) ([]types.SiafundElement, error)
		WalletAddresses(id WalletID) ([]Address, error)
		Wallets() ([]Wallet, error)

		AddWalletAddress(id WalletID, address Address) error
		RemoveWalletAddress(id WalletID, address types.Address) error

		Annotate(id WalletID, txns []types.Transaction) ([]PoolTransaction, error)

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
func (m *Manager) AddWallet(w Wallet) (Wallet, error) {
	return m.store.AddWallet(w)
}

// UpdateWallet updates the given wallet.
func (m *Manager) UpdateWallet(w Wallet) (Wallet, error) {
	return m.store.UpdateWallet(w)
}

// DeleteWallet deletes the given wallet.
func (m *Manager) DeleteWallet(id WalletID) error {
	return m.store.DeleteWallet(id)
}

// Wallets returns the wallets of the wallet manager.
func (m *Manager) Wallets() ([]Wallet, error) {
	return m.store.Wallets()
}

// AddAddress adds the given address to the given wallet.
func (m *Manager) AddAddress(id WalletID, addr Address) error {
	return m.store.AddWalletAddress(id, addr)
}

// RemoveAddress removes the given address from the given wallet.
func (m *Manager) RemoveAddress(id WalletID, addr types.Address) error {
	return m.store.RemoveWalletAddress(id, addr)
}

// Addresses returns the addresses of the given wallet.
func (m *Manager) Addresses(id WalletID) ([]Address, error) {
	return m.store.WalletAddresses(id)
}

// Events returns the events of the given wallet.
func (m *Manager) Events(id WalletID, offset, limit int) ([]Event, error) {
	return m.store.WalletEvents(id, offset, limit)
}

// UnspentSiacoinOutputs returns a paginated list of unspent siacoin outputs of
// the given wallet and the total number of unspent siacoin outputs.
func (m *Manager) UnspentSiacoinOutputs(id WalletID, offset, limit int) ([]types.SiacoinElement, error) {
	return m.store.WalletSiacoinOutputs(id, offset, limit)
}

// UnspentSiafundOutputs returns the unspent siafund outputs of the given wallet
func (m *Manager) UnspentSiafundOutputs(id WalletID, offset, limit int) ([]types.SiafundElement, error) {
	return m.store.WalletSiafundOutputs(id, offset, limit)
}

// Annotate annotates the given transactions with the wallet they belong to.
func (m *Manager) Annotate(id WalletID, pool []types.Transaction) ([]PoolTransaction, error) {
	return m.store.Annotate(id, pool)
}

// WalletBalance returns the balance of the given wallet.
func (m *Manager) WalletBalance(id WalletID) (Balance, error) {
	return m.store.WalletBalance(id)
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
