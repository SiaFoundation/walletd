package wallet

import (
	"crypto/ed25519"
	"errors"
	"reflect"
	"time"

	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
	"lukechampine.com/frand"
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
	AddressInfo(addr types.UnlockHash) (SeedAddressInfo, bool, error)
	AddAddress(info SeedAddressInfo) error
}

// A Wallet contains a seed for generating addresses and a store for storing
// information relevant to addresses owned by the wallet.
type Wallet struct {
	store Store

	seed Seed
	used map[types.SiacoinOutputID]struct{}
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

// StandardTransactionSignature is the most common form of TransactionSignature.
// It covers the entire transaction and references the first (typically the
// only) public key.
func StandardTransactionSignature(id crypto.Hash) types.TransactionSignature {
	return types.TransactionSignature{
		ParentID:       id,
		CoveredFields:  types.FullCoveredFields,
		PublicKeyIndex: 0,
	}
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

// FundTransaction adds inputs to txn worth at least amount, adding a change
// output if needed. It returns the added input IDs, for use with
// SignTransaction. It also returns a function that will "unclaim" the inputs;
// this function must be called once the transaction has been broadcast or
// discarded.
func (w *Wallet) FundTransaction(txn *types.Transaction, amount types.Currency) ([]crypto.Hash, func(), error) {
	if amount.IsZero() {
		return nil, func() {}, nil
	}

	outputs, err := w.store.UnspentOutputs()
	if err != nil {
		return nil, nil, err
	}

	var unused []SiacoinElement
	for _, out := range outputs {
		if _, ok := w.used[out.ID]; !ok {
			unused = append(unused, out)
		}
	}

	var balance types.Currency
	for _, o := range unused {
		balance = balance.Add(o.Value)
	}

	// choose outputs randomly
	frand.Shuffle(len(unused), reflect.Swapper(unused))

	// keep adding outputs until we have enough
	var outputSum types.Currency
	for i, o := range unused {
		if outputSum = outputSum.Add(o.Value); outputSum.Cmp(amount) >= 0 {
			unused = unused[:i+1]
			break
		}
	}

	var toSign []crypto.Hash
	for _, o := range unused {
		info, ok, err := w.store.AddressInfo(o.UnlockHash)
		if err != nil {
			return nil, nil, err
		} else if !ok {
			return nil, nil, errors.New("missing unlock conditions for address")
		}
		txn.SiacoinInputs = append(txn.SiacoinInputs, types.SiacoinInput{
			ParentID:         o.ID,
			UnlockConditions: info.UnlockConditions,
		})
		txn.TransactionSignatures = append(txn.TransactionSignatures, StandardTransactionSignature(crypto.Hash(o.ID)))
		toSign = append(toSign, crypto.Hash(o.ID))
	}
	// add change output, if needed
	if change := outputSum.Sub(amount); !change.IsZero() {
		index, err := w.store.SeedIndex()
		if err != nil {
			return nil, nil, err
		}
		info := SeedAddressInfo{
			UnlockConditions: StandardUnlockConditions(w.seed.PublicKey(index).Key),
			KeyIndex:         index,
		}
		w.store.AddAddress(info)
		txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.SiacoinOutput{
			UnlockHash: info.UnlockConditions.UnlockHash(),
			Value:      change,
		})
	}

	for _, o := range unused {
		w.used[o.ID] = struct{}{}
	}
	discard := func() {
		for _, o := range unused {
			delete(w.used, o.ID)
		}
	}
	return toSign, discard, nil
}

// New returns a new Wallet.
func New(seed Seed, store Store) *Wallet {
	return &Wallet{
		store: store,

		seed: seed,
		used: make(map[types.SiacoinOutputID]struct{}),
	}
}
