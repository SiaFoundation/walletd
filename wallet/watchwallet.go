package wallet

import (
	"errors"
	"time"

	"go.sia.tech/siad/types"
)

// A WatchWallet is a wallet that allows monitoring addresses but not signing
// or funding transactions.
type WatchWallet struct {
	store Store
}

// Balance returns the total value of the unspent outputs owned by the wallet.
func (w *WatchWallet) Balance() (types.Currency, error) {
	outputs, err := w.store.UnspentOutputs()
	if err != nil {
		return types.Currency{}, err
	}

	var sum types.Currency
	for _, out := range outputs {
		sum = sum.Add(out.Value)
	}
	return sum, nil
}

func (w *WatchWallet) AddAddress(uc types.UnlockConditions) error {
	index, err := w.store.SeedIndex()
	if err != nil {
		return nil
	}
	return w.store.AddAddress(SeedAddressInfo{
		UnlockConditions: uc,
		KeyIndex:         index,
	})
}

// Address returns an address owned by the wallet.
func (w *WatchWallet) Address() (types.UnlockHash, error) {
	addresses, err := w.store.Addresses()
	if err != nil {
		return types.UnlockHash{}, err
	} else if len(addresses) == 0 {
		return types.UnlockHash{}, errors.New("no monitored addresses")
	}
	return addresses[0], nil
}

// Addresses returns the addresses owned by the wallet.
func (w *WatchWallet) Addresses() ([]types.UnlockHash, error) {
	return w.store.Addresses()
}

// UnspentOutputs returns the unspent outputs owned by the wallet.
func (w *WatchWallet) UnspentOutputs() ([]SiacoinElement, error) {
	return w.store.UnspentOutputs()
}

// Transactions returns transactions relevant to the wallet.
func (w *WatchWallet) Transactions(since time.Time, max int) ([]Transaction, error) {
	return w.store.Transactions(since, max)
}

// NewWatchWallet returns a new WatchWallet.
func NewWatchWallet(store Store) *WatchWallet {
	return &WatchWallet{store}
}
