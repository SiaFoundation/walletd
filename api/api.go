package api

import (
	"time"

	"go.sia.tech/siad/crypto"

	"go.sia.tech/core/types"
)

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
	ToSign      []crypto.Hash     `json:"toSign"`
}

// WalletSiacoinsResponse is the response to /wallet/siacoins.
type WalletSiacoinsResponse struct {
	Transactions   []types.Transaction `json:"transactions"`
	TransactionIDs []crypto.Hash       `json:"transactionids"`
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
	ToSign      []crypto.Hash       `json:"toSign"`
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
