package wallet

import (
	"crypto/ed25519"
	"time"

	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

// A SiacoinElement is a SiacoinOutput along with its ID.
type SiacoinElement struct {
	types.SiacoinOutput
	ID             types.SiacoinOutputID
	MaturityHeight types.BlockHeight
}

// A SiafundElement is a SiafundOutput along with its ID.
type SiafundElement struct {
	types.SiafundOutput
	ID types.SiafundOutputID
}

// A FileContractElement is a FileContract along with its ID.
type FileContractElement struct {
	types.FileContract
	ID types.FileContractID
}

type ChainIndex struct {
	ID     types.BlockID
	Height types.BlockHeight
}

// A Transaction is an on-chain transaction relevant to a particular wallet,
// paired with useful metadata.
type Transaction struct {
	Raw       types.Transaction
	Index     ChainIndex
	ID        types.TransactionID
	Inflow    types.Currency
	Outflow   types.Currency
	Timestamp time.Time
}

// A Store stores information needed by a wallet.
type Store interface {
	modules.ConsensusSetSubscriber
	ConsensusChangeID() (modules.ConsensusChangeID, error)

	Transaction(id types.TransactionID) (Transaction, bool, error)
	Transactions(since time.Time, max int) ([]Transaction, error)
	UnspentOutputs() ([]SiacoinElement, error)

	SeedIndex() (uint64, error)
	SetSeedIndex(index uint64) error
	AddAddress(info SeedAddressInfo) error
}

// A Wallet contains a seed for generating addresses and a store for storing
// information relevant to addresses owned by the wallet.
type Wallet struct {
	seed  Seed
	store Store
}

// StandardUnlockConditions returns the standard unlock conditions for a single
// Ed25519 key.
func StandardUnlockConditions(priv ed25519.PublicKey) types.UnlockConditions {
	return types.UnlockConditions{
		PublicKeys: []types.SiaPublicKey{{
			Algorithm: types.SignatureEd25519,
			Key:       priv[32:],
		}},
		SignaturesRequired: 1,
	}
}

// StandardAddress returns the standard address for an Ed25519 key.
func StandardAddress(priv ed25519.PublicKey) types.UnlockHash {
	return StandardUnlockConditions(priv).UnlockHash()
}

// Balance returns the total value of the unspent outputs owned by the wallet.
func (w *Wallet) Balance() (types.Currency, error) {
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

// Address returns an address owned by the wallet.
func (w *Wallet) Address() (types.UnlockHash, error) {
	index, err := w.store.SeedIndex()
	if err != nil {
		return types.UnlockHash{}, err
	}

	info := SeedAddressInfo{
		UnlockConditions: StandardUnlockConditions(w.seed.PublicKey(index).Key),
		KeyIndex:         index,
	}
	w.store.AddAddress(info)
	return info.UnlockConditions.UnlockHash(), nil
}

// UnspentOutputs returns the unspent outputs owned by the wallet.
func (w *Wallet) UnspentOutputs() ([]SiacoinElement, error) {
	return w.store.UnspentOutputs()
}

// Transactions returns transactions relevant to the wallet.
func (w *Wallet) Transactions(since time.Time, max int) ([]Transaction, error) {
	return w.store.Transactions(since, max)
}

// New returns a new Wallet.
func New(seed Seed, store Store) *Wallet {
	return &Wallet{seed, store}
}
