package wallet

import (
	"sort"

	"go.sia.tech/core/types"
)

// BytesPerInput is the encoded size of a SiacoinInput and corresponding
// TransactionSignature, assuming standard UnlockConditions.
const BytesPerInput = 241

// SumOutputs returns the total value of the supplied outputs.
func SumOutputs(outputs []SiacoinElement) (sum types.Currency) {
	for _, o := range outputs {
		sum = sum.Add(o.Value)
	}
	return
}

// DistributeFunds is a helper function for distributing the value in a set of
// inputs among n outputs, each containing per siacoins. It returns the minimal
// set of inputs that will fund such a transaction, along with the resulting fee
// and change. Inputs with value equal to per are ignored. If the inputs are not
// sufficient to fund n outputs, DistributeFunds returns nil.
func DistributeFunds(inputs []SiacoinElement, n int, per, feePerByte types.Currency) (ins []SiacoinElement, fee, change types.Currency) {
	// sort
	ins = append([]SiacoinElement(nil), inputs...)
	sort.Slice(ins, func(i, j int) bool {
		return ins[i].Value.Cmp(ins[j].Value) > 0
	})
	// filter
	filtered := ins[:0]
	for _, in := range ins {
		if !in.Value.Equals(per) {
			filtered = append(filtered, in)
		}
	}
	ins = filtered

	const bytesPerOutput = 64 // approximate; depends on currency size
	outputFees := feePerByte.Mul64(bytesPerOutput).Mul64(uint64(n))
	feePerInput := feePerByte.Mul64(BytesPerInput)

	// search for minimal set
	want := per.Mul64(uint64(n))
	i := sort.Search(len(ins)+1, func(i int) bool {
		fee = feePerInput.Mul64(uint64(i)).Add(outputFees)
		return SumOutputs(ins[:i]).Cmp(want.Add(fee)) >= 0
	})
	if i == len(ins)+1 {
		// insufficient funds
		return nil, types.ZeroCurrency, types.ZeroCurrency
	}
	fee = feePerInput.Mul64(uint64(i)).Add(outputFees)
	change = SumOutputs(ins[:i]).Sub(want.Add(fee))
	return ins[:i], fee, change
}
