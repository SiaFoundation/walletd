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
		Tip() types.ChainIndex
		BestIndex(height uint64) (types.ChainIndex, bool)

		OnReorg(fn func(types.ChainIndex)) (cancel func())
		UpdatesSince(index types.ChainIndex, max int) (rus []chain.RevertUpdate, aus []chain.ApplyUpdate, err error)
	}

	// A Store is a persistent store of wallet data.
	Store interface {
		UpdateChainState(reverted []chain.RevertUpdate, applied []chain.ApplyUpdate) error

		WalletEvents(walletID ID, offset, limit int) ([]Event, error)
		AddWallet(Wallet) (Wallet, error)
		UpdateWallet(Wallet) (Wallet, error)
		DeleteWallet(walletID ID) error
		WalletBalance(walletID ID) (Balance, error)
		WalletSiacoinOutputs(walletID ID, offset, limit int) ([]types.SiacoinElement, error)
		WalletSiafundOutputs(walletID ID, offset, limit int) ([]types.SiafundElement, error)
		WalletAddresses(walletID ID) ([]Address, error)
		Wallets() ([]Wallet, error)

		AddWalletAddress(walletID ID, address Address) error
		RemoveWalletAddress(walletID ID, address types.Address) error

		Annotate(walletID ID, txns []types.Transaction) ([]PoolTransaction, error)

		AddressBalance(address types.Address) (balance Balance, err error)
		AddressEvents(address types.Address, offset, limit int) (events []Event, err error)
		AddressSiacoinOutputs(address types.Address, offset, limit int) (siacoins []types.SiacoinElement, err error)
		AddressSiafundOutputs(address types.Address, offset, limit int) (siafunds []types.SiafundElement, err error)

		LastCommittedIndex() (types.ChainIndex, error)
	}

	// A Manager manages wallets.
	Manager struct {
		chain ChainManager
		store Store
		log   *zap.Logger

		mu          sync.Mutex
		used        map[types.Hash256]bool
		unsubscribe func()
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
func (m *Manager) DeleteWallet(walletID ID) error {
	return m.store.DeleteWallet(walletID)
}

// Wallets returns the wallets of the wallet manager.
func (m *Manager) Wallets() ([]Wallet, error) {
	return m.store.Wallets()
}

// AddAddress adds the given address to the given wallet.
func (m *Manager) AddAddress(walletID ID, addr Address) error {
	return m.store.AddWalletAddress(walletID, addr)
}

// RemoveAddress removes the given address from the given wallet.
func (m *Manager) RemoveAddress(walletID ID, addr types.Address) error {
	return m.store.RemoveWalletAddress(walletID, addr)
}

// Addresses returns the addresses of the given wallet.
func (m *Manager) Addresses(walletID ID) ([]Address, error) {
	return m.store.WalletAddresses(walletID)
}

// Events returns the events of the given wallet.
func (m *Manager) Events(walletID ID, offset, limit int) ([]Event, error) {
	return m.store.WalletEvents(walletID, offset, limit)
}

// UnspentSiacoinOutputs returns a paginated list of unspent siacoin outputs of
// the given wallet and the total number of unspent siacoin outputs.
func (m *Manager) UnspentSiacoinOutputs(walletID ID, offset, limit int) ([]types.SiacoinElement, error) {
	return m.store.WalletSiacoinOutputs(walletID, offset, limit)
}

// UnspentSiafundOutputs returns the unspent siafund outputs of the given wallet
func (m *Manager) UnspentSiafundOutputs(walletID ID, offset, limit int) ([]types.SiafundElement, error) {
	return m.store.WalletSiafundOutputs(walletID, offset, limit)
}

// Annotate annotates the given transactions with the wallet they belong to.
func (m *Manager) Annotate(walletID ID, pool []types.Transaction) ([]PoolTransaction, error) {
	return m.store.Annotate(walletID, pool)
}

// WalletBalance returns the balance of the given wallet.
func (m *Manager) WalletBalance(walletID ID) (Balance, error) {
	return m.store.WalletBalance(walletID)
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
	m.mu.Lock()
	defer m.mu.Unlock()
	// TODO: is this right? won't it result in duplicate state?
	return syncStore(m.store, m.chain, index)
}

func syncStore(store Store, cm ChainManager, index types.ChainIndex) error {
	for index != cm.Tip() {
		crus, caus, err := cm.UpdatesSince(index, 1000)
		if err != nil {
			return fmt.Errorf("failed to subscribe to chain manager: %w", err)
		} else if err := store.UpdateChainState(crus, caus); err != nil {
			return fmt.Errorf("failed to update chain state: %w", err)
		}
		index = caus[len(caus)-1].State.Index
	}
	return nil
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
	}
	if err := syncStore(store, cm, lastTip); err != nil {
		return nil, fmt.Errorf("failed to subscribe to chain manager: %w", err)
	}

	reorgChan := make(chan types.ChainIndex, 1)
	go func() {
		for range reorgChan {
			m.mu.Lock()
			lastTip, err := store.LastCommittedIndex()
			if err != nil {
				log.Error("failed to get last committed index", zap.Error(err))
			} else if err := syncStore(store, cm, lastTip); err != nil {
				log.Error("failed to sync store", zap.Error(err))
			}
			m.mu.Unlock()
		}
	}()
	m.unsubscribe = cm.OnReorg(func(index types.ChainIndex) {
		select {
		case reorgChan <- index:
		default:
		}
	})
	return m, nil
}
