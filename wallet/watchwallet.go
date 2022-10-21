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

var DisabledError = errors.New("This function is disabled in the watch wallet")

// Balance returns the total value of the unspent siacoin and siafund outputs
// owned by the wallet.
func (w *WatchWallet) Balance() (types.Currency, types.Currency, error) {
	scOutputs, err := w.store.UnspentSiacoinOutputs()
	if err != nil {
		return types.Currency{}, types.Currency{}, err
	}

	var sc types.Currency
	for _, out := range scOutputs {
		sc = sc.Add(out.Value)
	}

	sfOutputs, err := w.store.UnspentSiafundOutputs()
	if err != nil {
		return types.Currency{}, types.Currency{}, err
	}

	var sf types.Currency
	for _, out := range sfOutputs {
		sf = sf.Add(out.Value)
	}

	return sc, sf, nil
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

// SignTransaction is disabled in the read only wallet.
func (w *WatchWallet) SignTransaction(txn *types.Transaction, toSign []crypto.Hash) error {
	return DisabledError
}

// FundTransaction is disabled in the read only wallet.
func (w *WatchWallet) FundTransaction(txn *types.Transaction, amountSC types.Currency, amountSF types.Currency) ([]crypto.Hash, func(), error) {
	return nil, nil, DisabledError
}

// DistributeFunds is disabled in the read only wallet.
func (w *WatchWallet) DistributeFunds(outputs int, per, feePerByte types.Currency) (ins []SiacoinElement, fee, change types.Currency, err error) {
	return nil, types.ZeroCurrency, types.ZeroCurrency, DisabledError
}

// NewWatchWallet returns a new WatchWallet.
func NewWatchWallet(store Store) *WatchWallet {
	return &WatchWallet{store}
}
