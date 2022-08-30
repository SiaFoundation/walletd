package wallet

import (
	"crypto/ed25519"
	"errors"
	"reflect"
	"sync"
	"time"

	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/types"
	"lukechampine.com/frand"
)

// A HotWallet is a wallet that allows funding and signing transactions.
type HotWallet struct {
	mu sync.Mutex

	seed  Seed
	store Store
	used  map[types.SiacoinOutputID]struct{}
}

// Balance returns the total value of the unspent outputs owned by the wallet.
func (w *HotWallet) Balance() (types.Currency, error) {
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
func (w *HotWallet) Address() (types.UnlockHash, error) {
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

// Addresses returns the addresses owned by the wallet.
func (w *HotWallet) Addresses() ([]types.UnlockHash, error) {
	return w.store.Addresses()
}

// UnspentOutputs returns the unspent outputs owned by the wallet.
func (w *HotWallet) UnspentOutputs() ([]SiacoinElement, error) {
	return w.store.UnspentOutputs()
}

// Transaction returns a transaction with the given ID.
func (w *HotWallet) Transaction(id types.TransactionID) (Transaction, error) {
	return w.store.Transaction(id)
}

// Transactions returns transactions relevant to the wallet.
func (w *HotWallet) Transactions(since time.Time, max int) ([]Transaction, error) {
	return w.store.Transactions(since, max)
}

// TransactionsByAddress returns all transactions involving the address.
func (w *HotWallet) TransactionsByAddress(addr types.UnlockHash) ([]Transaction, error) {
	return w.store.TransactionsByAddress(addr)
}

// FundTransaction adds inputs to txn worth at least amount, adding a change
// output if needed. It returns the added input IDs, for use with
// SignTransaction. It also returns a function that will "unclaim" the inputs;
// this function must be called once the transaction has been broadcast or
// discarded.
func (w *HotWallet) FundTransaction(txn *types.Transaction, amount types.Currency) ([]crypto.Hash, func(), error) {
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

	w.mu.Lock()
	for _, o := range unused {
		w.used[o.ID] = struct{}{}
	}
	w.mu.Unlock()

	discard := func() {
		w.mu.Lock()
		for _, o := range unused {
			delete(w.used, o.ID)
		}
		w.mu.Unlock()
	}
	return toSign, discard, nil
}

// SignTransaction signs the specified transaction using keys derived from the
// wallet seed. If toSign is nil, SignTransaction will automatically add
// TransactionSignatures for each input owned by the seed. If toSign is not nil,
// it a list of indices of TransactionSignatures already present in txn;
// SignTransaction will fill in the Signature field of each.
func (w *HotWallet) SignTransaction(txn *types.Transaction, toSign []crypto.Hash) error {
	if len(toSign) == 0 {
		// lazy mode: add standard sigs for every input we own
		for _, input := range txn.SiacoinInputs {
			info, ok, err := w.store.AddressInfo(input.UnlockConditions.UnlockHash())
			if !ok {
				continue
			}
			if err != nil {
				return err
			}
			sk := w.seed.SecretKey(info.KeyIndex)
			txnSig := StandardTransactionSignature(crypto.Hash(input.ParentID))
			AppendTransactionSignature(txn, txnSig, sk)
		}
		return nil
	}

	sigAddr := func(id crypto.Hash) (types.UnlockHash, bool) {
		for _, sci := range txn.SiacoinInputs {
			if crypto.Hash(sci.ParentID) == id {
				return sci.UnlockConditions.UnlockHash(), true
			}
		}
		for _, sfi := range txn.SiafundInputs {
			if crypto.Hash(sfi.ParentID) == id {
				return sfi.UnlockConditions.UnlockHash(), true
			}
		}
		for _, fcr := range txn.FileContractRevisions {
			if crypto.Hash(fcr.ParentID) == id {
				return fcr.UnlockConditions.UnlockHash(), true
			}
		}
		return types.UnlockHash{}, false
	}
	sign := func(i int) error {
		addr, ok := sigAddr(txn.TransactionSignatures[i].ParentID)
		if !ok {
			return errors.New("invalid id")
		}
		info, ok, err := w.store.AddressInfo(addr)
		if !ok {
			return errors.New("can't sign")
		}
		if err != nil {
			return err
		}
		sk := w.seed.SecretKey(info.KeyIndex)
		hash := txn.SigHash(i, types.FoundationHardforkHeight+1)
		txn.TransactionSignatures[i].Signature = ed25519.Sign(sk, hash[:])
		return nil
	}

outer:
	for _, parent := range toSign {
		for sigIndex, sig := range txn.TransactionSignatures {
			if sig.ParentID == parent {
				if err := sign(sigIndex); err != nil {
					return err
				}
				continue outer
			}
		}
		return errors.New("sighash not found in transaction")
	}
	return nil
}

// NewHotWallet returns a new HotWallet.
func NewHotWallet(seed Seed, store Store) *HotWallet {
	return &HotWallet{
		store: store,

		seed: seed,
		used: make(map[types.SiacoinOutputID]struct{}),
	}
}
