package api

import (
	"encoding/json"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"go.sia.tech/walletd/wallet"
)

// A StateResponse returns information about the current state of the walletd
// daemon.
type StateResponse struct {
	Version   string           `json:"version"`
	Commit    string           `json:"commit"`
	OS        string           `json:"os"`
	BuildTime time.Time        `json:"buildTime"`
	StartTime time.Time        `json:"startTime"`
	IndexMode wallet.IndexMode `json:"indexMode"`
}

// A GatewayPeer is a currently-connected peer.
type GatewayPeer struct {
	Addr    string `json:"addr"`
	Inbound bool   `json:"inbound"`
	Version string `json:"version"`

	FirstSeen      time.Time     `json:"firstSeen,omitempty"`
	ConnectedSince time.Time     `json:"connectedSince,omitempty"`
	SyncedBlocks   uint64        `json:"syncedBlocks,omitempty"`
	SyncDuration   time.Duration `json:"syncDuration,omitempty"`
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

// BalanceResponse is the response type for /wallets/:id/balance.
type BalanceResponse wallet.Balance

// WalletReserveRequest is the request type for /wallets/:id/reserve.
type WalletReserveRequest struct {
	SiacoinOutputs []types.SiacoinOutputID `json:"siacoinOutputs"`
	SiafundOutputs []types.SiafundOutputID `json:"siafundOutputs"`
	Duration       time.Duration           `json:"duration"`
}

// A WalletUpdateRequest is a request to update a wallet
type WalletUpdateRequest struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Metadata    json.RawMessage `json:"metadata"`
}

// WalletReleaseRequest is the request type for /wallets/:id/release.
type WalletReleaseRequest struct {
	SiacoinOutputs []types.SiacoinOutputID `json:"siacoinOutputs"`
	SiafundOutputs []types.SiafundOutputID `json:"siafundOutputs"`
}

// WalletFundRequest is the request type for /wallets/:id/fund.
type WalletFundRequest struct {
	Transaction   types.Transaction `json:"transaction"`
	Amount        types.Currency    `json:"amount"`
	ChangeAddress types.Address     `json:"changeAddress"`
}

// WalletFundSFRequest is the request type for /wallets/:id/fundsf.
type WalletFundSFRequest struct {
	Transaction   types.Transaction `json:"transaction"`
	Amount        uint64            `json:"amount"`
	ChangeAddress types.Address     `json:"changeAddress"`
	ClaimAddress  types.Address     `json:"claimAddress"`
}

// WalletFundResponse is the response type for /wallets/:id/fund.
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

// RescanResponse contains information about the state of a chain rescan.
type RescanResponse struct {
	StartIndex types.ChainIndex `json:"startIndex"`
	Index      types.ChainIndex `json:"index"`
	StartTime  time.Time        `json:"startTime"`
	Error      *string          `json:"error,omitempty"`
}

// An ApplyUpdate is a consensus update that was applied to the best chain.
type ApplyUpdate struct {
	Update consensus.ApplyUpdate `json:"update"`
	State  consensus.State       `json:"state"`
	Block  types.Block           `json:"block"`
}

// A RevertUpdate is a consensus update that was reverted from the best chain.
type RevertUpdate struct {
	Update consensus.RevertUpdate `json:"update"`
	State  consensus.State        `json:"state"`
	Block  types.Block            `json:"block"`
}

// ConsensusUpdatesResponse is the response type for /consensus/updates/:index.
type ConsensusUpdatesResponse struct {
	Applied  []ApplyUpdate  `json:"applied"`
	Reverted []RevertUpdate `json:"reverted"`
}

// DebugMineRequest is the request type for /debug/mine.
type DebugMineRequest struct {
	Blocks  int           `json:"blocks"`
	Address types.Address `json:"address"`
}
