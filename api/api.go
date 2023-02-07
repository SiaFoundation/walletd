package api

import (
	"bytes"
	"fmt"
	"time"

	"gitlab.com/NebulousLabs/encoding"

	"go.sia.tech/core/types"
)

func coreConvertToSiad(from types.EncoderTo, to interface{}) {
	var buf bytes.Buffer
	e := types.NewEncoder(&buf)
	from.EncodeTo(e)
	e.Flush()
	if err := encoding.Unmarshal(buf.Bytes(), to); err != nil {
		panic(fmt.Sprintf("type conversion failed (%T->%T): %v", from, to, err))
	}
}

func siadConvertToCore(from interface{}, to types.DecoderFrom) {
	d := types.NewBufDecoder(encoding.Marshal(from))
	to.DecodeFrom(d)
	if err := d.Err(); err != nil {
		panic(fmt.Sprintf("type conversion failed (%T->%T): %v", from, to, err))
	}
}

// for encoding/decoding time.Time values in API params
type paramTime time.Time

func (t paramTime) String() string                { return (time.Time)(t).Format(time.RFC3339) }
func (t *paramTime) UnmarshalText(b []byte) error { return (*time.Time)(t).UnmarshalText(b) }

type ChainIndex struct {
	Height uint64        `json:"height"`
	ID     types.BlockID `json:"id"`
}

type ConsensusState struct {
	Index ChainIndex `json:"index"`
}

// WalletBalanceResponse is the response to /wallet/balance.
type WalletBalanceResponse struct {
	Siacoins types.Currency `json:"siacoins"`
	Siafunds uint64         `json:"siafunds"`
}

// WalletSignRequest requests that a transaction be signed.
type WalletSignRequest struct {
	Transaction types.Transaction `json:"transaction"`
	ToSign      []types.Hash256   `json:"toSign"`
}

// WalletSiacoinsResponse is the response to /wallet/siacoins.
type WalletSiacoinsResponse struct {
	Transactions   []types.Transaction `json:"transactions"`
	TransactionIDs []types.Hash256     `json:"transactionids"`
}

// WalletFundRequest is the request type for /wallet/fund.
type WalletFundRequest struct {
	Transaction types.Transaction `json:"transaction"`
	Siacoins    types.Currency    `json:"siacoins"`
	Siafunds    uint64            `json:"siafunds"`
}

// WalletFundResponse is the response to /wallet/fund.
type WalletFundResponse struct {
	Transaction types.Transaction   `json:"transaction"`
	ToSign      []types.Hash256     `json:"toSign"`
	DependsOn   []types.Transaction `json:"dependsOn"`
}

// WalletSendRequest is the request type for /wallet/send.
type WalletSendRequest struct {
	Type        string
	Amount      types.Currency
	Destination types.Address
}

// WalletSendResponse is the response to /wallet/send.
type WalletSendResponse struct {
	ID          types.TransactionID
	Transaction types.Transaction
}

// WalletSplitRequest is the request type for /wallet/split
type WalletSplitRequest struct {
	Outputs int            `json:"outputs"`
	Amount  types.Currency `json:"amount"`
}
