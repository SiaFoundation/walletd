package wallet

import (
	"crypto/ed25519"
	"errors"
	"reflect"
	"sort"
	"sync"
	"time"

	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/types"
	"lukechampine.com/frand"
)

// CoinSelection selects the coin selection algorithm used by the wallet.
type CoinSelection int

const (
	Random CoinSelection = iota
	Bitcoin
	SingleRandomDraw
)

func (c CoinSelection) String() string {
	switch c {
	case Random:
		return "Random"
	case Bitcoin:
		return "Bitcoin"
	case SingleRandomDraw:
		return "SingleRandomDraw"
	}
	return "Unknown"
}

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
func (w *HotWallet) Balance() (types.Currency, types.Currency, error) {
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

// BalanceSiafund returns the total value of the unspent siafund outputs owned
// by the wallet.
func (w *HotWallet) BalanceSiafund() (types.Currency, error) {
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

// AddressInfo returns seed information associated with the specified address.
func (w *HotWallet) AddressInfo(addr types.UnlockHash) (SeedAddressInfo, error) {
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
func (w *HotWallet) TransactionsByAddress(addr types.UnlockHash) ([]Transaction, error) {
	return w.store.TransactionsByAddress(addr)
}

// FundTransactionSiacoin adds siacoin inputs to txn worth at least amount,
// adding a change output if needed. It returns the added input IDs, for use
// with SignTransaction. It also returns a function that will "unclaim" the
// inputs; this function must be called once the transaction has been broadcast
// or discarded.
func (w *HotWallet) FundTransaction(txn *types.Transaction, amountSC types.Currency, amountSF types.Currency, feePerByte types.Currency, coinSelection CoinSelection) ([]crypto.Hash, func(), error) {
	if amountSC.IsZero() && amountSF.IsZero() {
		return nil, func() {}, nil
	}

	exactTargetSC := func(outputs []SiacoinElement) int {
		for i, o := range outputs {
			if o.Value.Cmp(amountSC) == 0 {
				return i
			}
		}
		return -1
	}

	smallestGreaterTargetSC := func(outputs []SiacoinElement) int {
		index := -1
		for i, o := range outputs {
			if index == -1 && o.Value.Cmp(amountSC) == 1 {
				// o.Value > target
				index = i
			} else if index != -1 && o.Value.Cmp(amountSC) == 1 && o.Value.Cmp(outputs[index].Value) == -1 {
				// o.Value > target && o.Value < current min greater than target
				index = i
			}
		}
		return index
	}

	estimateFee := func(n int) types.Currency {
		const bytesPerOutput = 64 // approximate; depends on currency size
		outputFees := feePerByte.Mul64(bytesPerOutput).Mul64(uint64(n))
		feePerInput := feePerByte.Mul64(BytesPerInput)
		return feePerInput.Mul64(uint64(n)).Add(outputFees)
	}

	outputsSC, err := w.store.UnspentSiacoinOutputs()
	if err != nil {
		return nil, nil, err
	}

	var unusedSC []SiacoinElement
	for _, o := range outputsSC {
		if _, ok := w.usedSC[o.ID]; !ok {
			unusedSC = append(unusedSC, o)
		}
	}

	var outputSumSC types.Currency
	if coinSelection == Random {
		// choose outputs randomly
		frand.Shuffle(len(unusedSC), reflect.Swapper(unusedSC))

		// keep adding outputs until we have enough
		for i, o := range unusedSC {
			if outputSumSC = outputSumSC.Add(o.Value); outputSumSC.Cmp(amountSC) >= 0 {
				unusedSC = unusedSC[:i+1]
				break
			}
		}
	} else if coinSelection == Bitcoin {
		// utxos smaller than target amount
		var smallerUnusedSC []SiacoinElement
		for _, o := range unusedSC {
			if o.Value.Cmp(amountSC) == -1 {
				smallerUnusedSC = append(smallerUnusedSC, o)
			}
		}

		if exactIndex := exactTargetSC(unusedSC); exactIndex != -1 {
			// if any UTXO matches the target, use that utxo
			exactElem := unusedSC[exactIndex]
			unusedSC = []SiacoinElement{exactElem}
		} else if SumOutputs(smallerUnusedSC).Cmp(amountSC) == 0 {
			// if sum of all UTXOs smaller than target == target, use all UTXOs smaller than target
			unusedSC = smallerUnusedSC
		} else if smallestGreaterIndex := smallestGreaterTargetSC(unusedSC); smallestGreaterIndex != -1 {
			// use smallest UTXO that is larger than target
			smallestGreaterElem := unusedSC[smallestGreaterIndex]
			unusedSC = []SiacoinElement{smallestGreaterElem}
		} else {
			sort.Slice(unusedSC, func(i, j int) bool {
				return unusedSC[i].Value.Cmp(unusedSC[j].Value) == -1
			})

			best := SumOutputs(smallerUnusedSC)
			included := make([]bool, len(unusedSC))
			includedBest := make([]bool, len(unusedSC))
			for i := 0; i < 1000 && best.Cmp(amountSC) != 0; i++ {
				for j := 0; j < len(included); j++ {
					included[j] = false
				}

				var reachedTarget bool
				var total types.Currency
				for j := 0; j < 2 && !reachedTarget; j++ {
					for k := 0; k < len(unusedSC); k++ {
						if (j == 0 && ((frand.Uint64n(1) % 2) == 1)) || (j != 0 && !included[k]) {
							total = total.Add(unusedSC[i].Value)
							included[k] = true
							if cmp := total.Cmp(amountSC); cmp == 0 || cmp == 1 {
								reachedTarget = true
								if total.Cmp(best) == -1 {
									best = total
									for i := 0; i < len(included); i++ {
										includedBest[i] = included[i]
									}
								}
								total = total.Sub(unusedSC[i].Value)
								included[i] = false
							}
						}
					}
				}
			}

			// if the stochastic approximation didn't find a solution, just keep
			// adding outputs till we have enough
			if best.Cmp(amountSC) == -1 {
				// choose outputs randomly
				frand.Shuffle(len(unusedSC), reflect.Swapper(unusedSC))

				// keep adding outputs until we have enough
				for i, o := range unusedSC {
					if outputSumSC = outputSumSC.Add(o.Value); outputSumSC.Cmp(amountSC) >= 0 {
						unusedSC = unusedSC[:i+1]
						break
					}
				}
			} else {
				// if we found a solution
				var newUnusedSC []SiacoinElement
				for i := 0; i < len(includedBest); i++ {
					if includedBest[i] {
						newUnusedSC = append(newUnusedSC, unusedSC[i])
					}
				}
				unusedSC = newUnusedSC
			}
		}
		outputSumSC = SumOutputs(unusedSC)
	} else if coinSelection == SingleRandomDraw {
		// choose outputs randomly
		frand.Shuffle(len(unusedSC), reflect.Swapper(unusedSC))

		mc := amountSC
		// keep adding outputs until we have enough
		for i, o := range unusedSC {
			if outputSumSC = outputSumSC.Add(o.Value); outputSumSC.Cmp(amountSC.Add(mc).Add(estimateFee(i))) >= 0 {
				unusedSC = unusedSC[:i+1]
				break
			}
		}
	}

	var toSignSC []crypto.Hash
	for _, o := range unusedSC {
		info, err := w.store.AddressInfo(o.UnlockHash)
		if err != nil {
			return nil, nil, err
		}
		txn.SiacoinInputs = append(txn.SiacoinInputs, types.SiacoinInput{
			ParentID:         o.ID,
			UnlockConditions: info.UnlockConditions,
		})
		txn.TransactionSignatures = append(txn.TransactionSignatures, StandardTransactionSignature(crypto.Hash(o.ID)))
		toSignSC = append(toSignSC, crypto.Hash(o.ID))
	}
	// add change output, if needed
	if outputSumSC.Cmp(amountSC) > 0 {
		if change := outputSumSC.Sub(amountSC); !change.IsZero() {
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
	for _, o := range outputsSF {
		if _, ok := w.usedSF[o.ID]; !ok {
			unusedSF = append(unusedSF, o)
		}
	}

	// choose outputs randomly
	frand.Shuffle(len(unusedSF), reflect.Swapper(unusedSF))

	// keep adding outputs until we have enough
	var outputSumSF types.Currency
	for i, o := range unusedSF {
		if outputSumSF = outputSumSF.Add(o.Value); outputSumSF.Cmp(amountSF) >= 0 {
			unusedSF = unusedSF[:i+1]
			break
		}
	}

	var toSignSF []crypto.Hash
	for _, o := range unusedSF {
		info, err := w.store.AddressInfo(o.UnlockHash)
		if err != nil {
			return nil, nil, err
		}
		txn.SiafundInputs = append(txn.SiafundInputs, types.SiafundInput{
			ParentID:         o.ID,
			UnlockConditions: info.UnlockConditions,
		})
		txn.TransactionSignatures = append(txn.TransactionSignatures, StandardTransactionSignature(crypto.Hash(o.ID)))
		toSignSF = append(toSignSF, crypto.Hash(o.ID))
	}
	// add change output, if needed
	if change := outputSumSF.Sub(amountSF); !change.IsZero() {
		index, err := w.store.SeedIndex()
		if err != nil {
			return nil, nil, err
		}
		info := SeedAddressInfo{
			UnlockConditions: StandardUnlockConditions(w.seed.PublicKey(index).Key),
			KeyIndex:         index,
		}
		w.store.AddAddress(info)
		txn.SiafundOutputs = append(txn.SiafundOutputs, types.SiafundOutput{
			UnlockHash: info.UnlockConditions.UnlockHash(),
			Value:      change,
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
		if !amountSF.IsZero() {
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
// TransactionSignatures for each input owned by the seed. If toSign is not nil,
// it a list of indices of TransactionSignatures already present in txn;
// SignTransaction will fill in the Signature field of each.
func (w *HotWallet) SignTransaction(txn *types.Transaction, toSign []crypto.Hash) error {
	if len(toSign) == 0 {
		// lazy mode: add standard sigs for every input we own
		for _, input := range txn.SiacoinInputs {
			info, err := w.store.AddressInfo(input.UnlockConditions.UnlockHash())
			if err != nil {
				continue
			}
			sk := w.seed.SecretKey(info.KeyIndex)
			txnSig := StandardTransactionSignature(crypto.Hash(input.ParentID))
			AppendTransactionSignature(txn, txnSig, sk)
		}
		for _, input := range txn.SiafundInputs {
			info, err := w.store.AddressInfo(input.UnlockConditions.UnlockHash())
			if err != nil {
				continue
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
		info, err := w.store.AddressInfo(addr)
		if err != nil {
			return errors.New("can't sign")
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
func NewHotWallet(store Store, seed Seed) *HotWallet {
	return &HotWallet{
		store: store,

		seed:   seed,
		usedSC: make(map[types.SiacoinOutputID]struct{}),
		usedSF: make(map[types.SiafundOutputID]struct{}),
	}
}
