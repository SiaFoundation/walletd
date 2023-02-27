package wallet

import (
	"crypto/ed25519"
	"errors"
	"reflect"
	"sync"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/core/wallet"
	"lukechampine.com/frand"
)

// A HotWallet is a wallet that allows funding and signing transactions.
type HotWallet struct {
	mu sync.Mutex

	seed   Seed
	store  Store
	usedSC map[types.SiacoinOutputID]struct{}
	usedSF map[types.SiafundOutputID]struct{}
}

// Balance returns the total value of the unspent siacoin and siafund outputs
// owned by the wallet.
func (w *HotWallet) Balance() (types.Currency, uint64, error) {
	scOutputs, err := w.store.UnspentSiacoinOutputs()
	if err != nil {
		return types.Currency{}, 0, err
	}

	var sc types.Currency
	for _, out := range scOutputs {
		sc = sc.Add(out.Value)
	}

	sfOutputs, err := w.store.UnspentSiafundOutputs()
	if err != nil {
		return types.Currency{}, 0, err
	}

	var sf uint64
	for _, out := range sfOutputs {
		sf += out.Value
	}

	return sc, sf, nil
}

// BalanceSiafund returns the total value of the unspent siafund outputs owned
// by the wallet.
func (w *HotWallet) BalanceSiafund() (uint64, error) {
	outputs, err := w.store.UnspentSiafundOutputs()
	if err != nil {
		return 0, err
	}

	var sum uint64
	for _, out := range outputs {
		sum += out.Value
	}
	return sum, nil
}

// Address returns an address owned by the wallet.
func (w *HotWallet) Address() (types.Address, error) {
	index, err := w.store.SeedIndex()
	if err != nil {
		return types.Address{}, err
	}

	info := SeedAddressInfo{
		UnlockConditions: wallet.StandardUnlockConditions(w.seed.PublicKey(index)),
		KeyIndex:         index,
	}
	w.store.AddAddress(info)
	return info.UnlockConditions.UnlockHash(), nil
}

// Addresses returns the addresses owned by the wallet.
func (w *HotWallet) Addresses() ([]types.Address, error) {
	return w.store.Addresses()
}

// AddressInfo returns seed information associated with the specified address.
func (w *HotWallet) AddressInfo(addr types.Address) (SeedAddressInfo, error) {
	return w.store.AddressInfo(addr)
}

// UnspentSiacoinOutputs returns the unspent siacoin outputs owned by the wallet.
func (w *HotWallet) UnspentSiacoinOutputs() ([]SiacoinElement, error) {
	return w.store.UnspentSiacoinOutputs()
}

// UnspentSiafundOutputs returns the unspent siafund outputs owned by the wallet.
func (w *HotWallet) UnspentSiafundOutputs() ([]SiafundElement, error) {
	return w.store.UnspentSiafundOutputs()
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
func (w *HotWallet) TransactionsByAddress(addr types.Address) ([]Transaction, error) {
	return w.store.TransactionsByAddress(addr)
}

// FundTransactionSiacoin adds siacoin inputs to txn worth at least amount,
// adding a change output if needed. It returns the added input IDs, for use
// with SignTransaction. It also returns a function that will "unclaim" the
// inputs; this function must be called once the transaction has been broadcast
// or discarded.
func (w *HotWallet) FundTransaction(txn *types.Transaction, amountSC types.Currency, amountSF uint64) ([]types.Hash256, func(), error) {
	if amountSC.IsZero() && amountSF == 0 {
		return nil, func() {}, nil
	}

	outputsSC, err := w.store.UnspentSiacoinOutputs()
	if err != nil {
		return nil, nil, err
	}

	// See discussion on https://github.com/SiaFoundation/walletd/pull/8 for
	// why this algorithm was chosen.
	var unusedSC []SiacoinElement
	for _, out := range outputsSC {
		if _, ok := w.usedSC[out.ID]; !ok {
			unusedSC = append(unusedSC, out)
		}
	}

	var balanceSC types.Currency
	for _, o := range unusedSC {
		balanceSC = balanceSC.Add(o.Value)
	}

	// choose outputs randomly
	frand.Shuffle(len(unusedSC), reflect.Swapper(unusedSC))

	// keep adding outputs until we have enough
	var outputSumSC types.Currency
	for i, o := range unusedSC {
		if outputSumSC = outputSumSC.Add(o.Value); outputSumSC.Cmp(amountSC) >= 0 {
			unusedSC = unusedSC[:i+1]
			break
		}
	}

	var toSignSC []types.Hash256
	for _, o := range unusedSC {
		info, err := w.store.AddressInfo(o.Address)
		if err != nil {
			return nil, nil, err
		}
		txn.SiacoinInputs = append(txn.SiacoinInputs, types.SiacoinInput{
			ParentID:         o.ID,
			UnlockConditions: info.UnlockConditions,
		})
		txn.Signatures = append(txn.Signatures, StandardTransactionSignature(types.Hash256(o.ID)))
		toSignSC = append(toSignSC, types.Hash256(o.ID))
	}
	// add change output, if needed
	if change := outputSumSC.Sub(amountSC); !change.IsZero() {
		index, err := w.store.SeedIndex()
		if err != nil {
			return nil, nil, err
		}
		info := SeedAddressInfo{
			UnlockConditions: wallet.StandardUnlockConditions(w.seed.PublicKey(index)),
			KeyIndex:         index,
		}
		w.store.AddAddress(info)
		txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.SiacoinOutput{
			Address: info.UnlockConditions.UnlockHash(),
			Value:   change,
		})
	}

	w.mu.Lock()
	for _, o := range unusedSC {
		w.usedSC[o.ID] = struct{}{}
	}
	w.mu.Unlock()

	outputsSF, err := w.store.UnspentSiafundOutputs()
	if err != nil {
		return nil, nil, err
	}

	var unusedSF []SiafundElement
	for _, out := range outputsSF {
		if _, ok := w.usedSF[out.ID]; !ok {
			unusedSF = append(unusedSF, out)
		}
	}

	var balanceSF uint64
	for _, o := range unusedSF {
		balanceSF += o.Value
	}

	// choose outputs randomly
	frand.Shuffle(len(unusedSF), reflect.Swapper(unusedSF))

	// keep adding outputs until we have enough
	var outputSumSF uint64
	for i, o := range unusedSF {
		if outputSumSF = outputSumSF + o.Value; outputSumSF >= amountSF {
			unusedSF = unusedSF[:i+1]
			break
		}
	}

	var toSignSF []types.Hash256
	for _, o := range unusedSF {
		info, err := w.store.AddressInfo(o.Address)
		if err != nil {
			return nil, nil, err
		}
		txn.SiafundInputs = append(txn.SiafundInputs, types.SiafundInput{
			ParentID:         o.ID,
			UnlockConditions: info.UnlockConditions,
		})
		txn.Signatures = append(txn.Signatures, StandardTransactionSignature(types.Hash256(o.ID)))
		toSignSF = append(toSignSF, types.Hash256(o.ID))
	}
	// add change output, if needed
	if change := outputSumSF - amountSF; outputSumSF > amountSF {
		index, err := w.store.SeedIndex()
		if err != nil {
			return nil, nil, err
		}
		info := SeedAddressInfo{
			UnlockConditions: wallet.StandardUnlockConditions(w.seed.PublicKey(index)),
			KeyIndex:         index,
		}
		w.store.AddAddress(info)
		txn.SiafundOutputs = append(txn.SiafundOutputs, types.SiafundOutput{
			Address: info.UnlockConditions.UnlockHash(),
			Value:   change,
		})
	}

	w.mu.Lock()
	for _, o := range unusedSF {
		w.usedSF[o.ID] = struct{}{}
	}
	w.mu.Unlock()

	discard := func() {
		w.mu.Lock()
		if !amountSC.IsZero() {
			for _, o := range unusedSC {
				delete(w.usedSC, o.ID)
			}
		}
		if amountSF != 0 {
			for _, o := range unusedSF {
				delete(w.usedSF, o.ID)
			}
		}
		w.mu.Unlock()
	}

	toSign := append(toSignSC, toSignSF...)
	return toSign, discard, nil
}

// SignTransaction signs the specified transaction using keys derived from the
// wallet seed. If toSign is nil, SignTransaction will automatically add
// Signatures for each input owned by the seed. If toSign is not nil,
// it a list of indices of Signatures already present in txn;
// SignTransaction will fill in the Signature field of each.
func (w *HotWallet) SignTransaction(txn *types.Transaction, toSign []types.Hash256) error {
	if len(toSign) == 0 {
		// lazy mode: add standard sigs for every input we own
		for _, input := range txn.SiacoinInputs {
			info, err := w.store.AddressInfo(input.UnlockConditions.UnlockHash())
			if err != nil {
				continue
			}
			sk := w.seed.PrivateKey(info.KeyIndex)
			txnSig := StandardTransactionSignature(types.Hash256(input.ParentID))
			AppendTransactionSignature(txn, txnSig, sk)
		}
		for _, input := range txn.SiafundInputs {
			info, err := w.store.AddressInfo(input.UnlockConditions.UnlockHash())
			if err != nil {
				continue
			}
			sk := w.seed.PrivateKey(info.KeyIndex)
			txnSig := StandardTransactionSignature(types.Hash256(input.ParentID))
			AppendTransactionSignature(txn, txnSig, sk)
		}
		return nil
	}

	sigAddr := func(id types.Hash256) (types.Address, bool) {
		for _, sci := range txn.SiacoinInputs {
			if types.Hash256(sci.ParentID) == id {
				return sci.UnlockConditions.UnlockHash(), true
			}
		}
		for _, sfi := range txn.SiafundInputs {
			if types.Hash256(sfi.ParentID) == id {
				return sfi.UnlockConditions.UnlockHash(), true
			}
		}
		for _, fcr := range txn.FileContractRevisions {
			if types.Hash256(fcr.ParentID) == id {
				return fcr.UnlockConditions.UnlockHash(), true
			}
		}
		return types.Address{}, false
	}
	sign := func(i int) error {
		addr, ok := sigAddr(txn.Signatures[i].ParentID)
		if !ok {
			return errors.New("invalid id")
		}
		info, err := w.store.AddressInfo(addr)
		if err != nil {
			return errors.New("can't sign")
		}
		sk := w.seed.PrivateKey(info.KeyIndex)
		hash := txn.ID()
		txn.Signatures[i].Signature = ed25519.Sign(ed25519.PrivateKey(sk), hash[:])
		return nil
	}

outer:
	for _, parent := range toSign {
		for sigIndex, sig := range txn.Signatures {
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
func NewHotWallet(store Store, seed Seed) *HotWallet {
	return &HotWallet{
		store: store,

		seed:   seed,
		usedSC: make(map[types.SiacoinOutputID]struct{}),
		usedSF: make(map[types.SiafundOutputID]struct{}),
	}
}
