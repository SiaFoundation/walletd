package wallet

import (
	"errors"
	"time"

	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/types"
)

// A WatchWallet is a wallet that allows monitoring addresses but not signing
// or funding transactions.
type WatchWallet struct {
	store Store
}

// BalanceSiacoin returns the total value of the unspent siacoin outputs owned
// by the wallet.
func (w *WatchWallet) BalanceSiacoin() (types.Currency, error) {
	outputs, err := w.store.UnspentSiacoinOutputs()
	if err != nil {
		return types.Currency{}, err
	}

	var sum types.Currency
	for _, out := range outputs {
		sum = sum.Add(out.Value)
	}
	return sum, nil
}

// BalanceSiafund returns the total value of the unspent siafund outputs owned
// by the wallet.
func (w *WatchWallet) BalanceSiafund() (types.Currency, error) {
	outputs, err := w.store.UnspentSiafundOutputs()
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

// UnspentSiacoinOutputs returns the unspent outputs owned by the wallet.
func (w *WatchWallet) UnspentSiacoinOutputs() ([]SiacoinElement, error) {
	return w.store.UnspentSiacoinOutputs()
}

// UnspentSiafundOutputs returns the unspent siafund outputs owned by the wallet.
func (w *WatchWallet) UnspentSiafundOutputs() ([]SiafundElement, error) {
	return w.store.UnspentSiafundOutputs()
}

// Transaction returns a transaction with the given ID.
func (w *WatchWallet) Transaction(id types.TransactionID) (Transaction, error) {
	return w.store.Transaction(id)
}

// TransactionsByAddress returns all transactions involving the address.
func (w *WatchWallet) TransactionsByAddress(addr types.UnlockHash) ([]Transaction, error) {
	return w.store.TransactionsByAddress(addr)
}

// Transactions returns transactions relevant to the wallet.
func (w *WatchWallet) Transactions(since time.Time, max int) ([]Transaction, error) {
	return w.store.Transactions(since, max)
}

func (w *WatchWallet) SignTransaction(txn *types.Transaction, toSign []crypto.Hash) error {
	return nil
}

func (w *WatchWallet) FundTransactionSiacoin(txn *types.Transaction, amount types.Currency) ([]crypto.Hash, func(), error) {
	return nil, nil, nil
}

func (w *WatchWallet) FundTransactionSiafund(txn *types.Transaction, amount types.Currency) ([]crypto.Hash, func(), error) {
	return nil, nil, nil
}

// NewWatchWallet returns a new WatchWallet.
func NewWatchWallet(store Store) *WatchWallet {
	return &WatchWallet{store}
}
