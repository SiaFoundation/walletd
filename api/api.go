package api

import (
	"time"

	"go.sia.tech/core/types"
)

// A GatewayPeer is a currently-connected peer.
type GatewayPeer struct {
	Addr    string `json:"addr"`
	Inbound bool   `json:"inbound"`
	Version string `json:"version"`

	FirstSeen      time.Time     `json:"firstSeen"`
	ConnectedSince time.Time     `json:"connectedSince"`
	SyncedBlocks   uint64        `json:"syncedBlocks"`
	SyncDuration   time.Duration `json:"syncDuration"`
}

// TxpoolBroadcastRequest is the request type for /txpool/broadcast.
type TxpoolBroadcastRequest struct {
	Transactions   []types.Transaction   `json:"transactions"`
	V2Transactions []types.V2Transaction `json:"v2transactions"`
}

// TxpoolTransactionsResponse is the response type for /txpool/transactions.
type TxpoolTransactionsResponse struct {
	Transactions   []types.Transaction   `json:"transactions"`
	V2Transactions []types.V2Transaction `json:"v2transactions"`
}

// WalletBalanceResponse is the response type for /wallets/:name/balance.
type WalletBalanceResponse struct {
	Siacoins         types.Currency `json:"siacoins"`
	ImmatureSiacoins types.Currency `json:"immatureSiacoins"`
	Siafunds         uint64         `json:"siafunds"`
}

// WalletOutputsResponse is the response type for /wallets/:name/outputs.
type WalletOutputsResponse struct {
	SiacoinOutputs []types.SiacoinElement `json:"siacoinOutputs"`
	SiafundOutputs []types.SiafundElement `json:"siafundOutputs"`
}

// WalletReserveRequest is the request type for /wallets/:name/reserve.
type WalletReserveRequest struct {
	SiacoinOutputs []types.SiacoinOutputID `json:"siacoinOutputs"`
	SiafundOutputs []types.SiafundOutputID `json:"siafundOutputs"`
	Duration       time.Duration           `json:"duration"`
}

// WalletReleaseRequest is the request type for /wallets/:name/release.
type WalletReleaseRequest struct {
	SiacoinOutputs []types.SiacoinOutputID `json:"siacoinOutputs"`
	SiafundOutputs []types.SiafundOutputID `json:"siafundOutputs"`
}

// WalletFundRequest is the request type for /wallets/:name/fund.
type WalletFundRequest struct {
	Transaction   types.Transaction `json:"transaction"`
	Amount        types.Currency    `json:"amount"`
	ChangeAddress types.Address     `json:"changeAddress"`
}

// WalletFundSFRequest is the request type for /wallets/:name/fundsf.
type WalletFundSFRequest struct {
	Transaction   types.Transaction `json:"transaction"`
	Amount        uint64            `json:"amount"`
	ChangeAddress types.Address     `json:"changeAddress"`
	ClaimAddress  types.Address     `json:"claimAddress"`
}

// WalletFundResponse is the response type for /wallets/:name/fund.
type WalletFundResponse struct {
	Transaction types.Transaction   `json:"transaction"`
	ToSign      []types.Hash256     `json:"toSign"`
	DependsOn   []types.Transaction `json:"dependsOn"`
}

// SeedSignRequest requests that a transaction be signed using the keys derived
// from the given indices.
type SeedSignRequest struct {
	Transaction types.Transaction `json:"transaction"`
	Keys        []uint64          `json:"keys"`
}
