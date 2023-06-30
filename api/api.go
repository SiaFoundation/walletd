package api

import (
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/wallet"
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

// WalletBalanceResponse is the response type for /wallets/:name/balance.
type WalletBalanceResponse struct {
	Siacoins types.Currency `json:"siacoins"`
	Siafunds uint64         `json:"siafunds"`
}

// WalletOutputsResponse is the response type for /wallets/:name/outputs.
type WalletOutputsResponse struct {
	SiacoinOutputs []wallet.SiacoinElement `json:"siacoinOutputs"`
	SiafundOutputs []wallet.SiafundElement `json:"siafundOutputs"`
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
