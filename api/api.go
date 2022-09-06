package api

import (
	"go.sia.tech/siad/crypto"

	"go.sia.tech/siad/types"
)

type ChainIndex struct {
	Height uint64        `json:"height"`
	ID     types.BlockID `json:"id"`
}

type ConsensusState struct {
	Index ChainIndex `json:"index"`
}

// A SyncerPeerResponse is a unique peer that is being used by the syncer.
type SyncerPeerResponse struct {
	NetAddress string `json:"netAddress"`
}

// A SyncerConnectRequest requests that the syncer connect to a peer.
type SyncerConnectRequest struct {
	NetAddress string `json:"netAddress"`
}

// WalletBalanceResponse is the response to /wallet/balance.
type WalletBalanceResponse struct {
	Siacoins types.Currency `json:"siacoins"`
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
	Amount      types.Currency    `json:"amount"`
}

// WalletFundResponse is the response type for /wallet/fund.
type WalletFundResponse struct {
	Transaction types.Transaction   `json:"transaction"`
	ToSign      []types.OutputID    `json:"toSign"`
	DependsOn   []types.Transaction `json:"dependsOn"`
}
