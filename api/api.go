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
	Address string `json:"address"`
	Inbound bool   `json:"inbound"`
	Version string `json:"version"`

	FirstSeen      time.Time     `json:"firstSeen,omitempty"`
	ConnectedSince time.Time     `json:"connectedSince,omitempty"`
	SyncedBlocks   uint64        `json:"syncedBlocks,omitempty"`
	SyncDuration   time.Duration `json:"syncDuration,omitempty"`
}

// TxpoolBroadcastRequest is the request type for /txpool/broadcast.
type TxpoolBroadcastRequest struct {
	Basis          types.ChainIndex      `json:"basis"`
	Transactions   []types.Transaction   `json:"transactions"`
	V2Transactions []types.V2Transaction `json:"v2transactions"`
}

// TxpoolTransactionsResponse is the response type for /txpool/transactions.
type TxpoolTransactionsResponse struct {
	Basis          types.ChainIndex      `json:"basis"`
	Transactions   []types.Transaction   `json:"transactions"`
	V2Transactions []types.V2Transaction `json:"v2transactions"`
}

// TxpoolUpdateV2TransactionsRequest is the request type for /txpool/transactions/v2/basis.
type TxpoolUpdateV2TransactionsRequest struct {
	Basis        types.ChainIndex      `json:"basis"`
	Target       types.ChainIndex      `json:"target"`
	Transactions []types.V2Transaction `json:"transactions"`
}

// TxpoolUpdateV2TransactionsResponse is the response type for /txpool/transactions/v2/basis.
type TxpoolUpdateV2TransactionsResponse struct {
	Basis        types.ChainIndex      `json:"basis"`
	Transactions []types.V2Transaction `json:"transactions"`
}

// BalanceResponse is the response type for /wallets/:id/balance.
type BalanceResponse wallet.Balance

// WalletReserveRequest is the request type for /wallets/:id/reserve.
type WalletReserveRequest struct {
	SiacoinOutputs []types.SiacoinOutputID `json:"siacoinOutputs"`
	SiafundOutputs []types.SiafundOutputID `json:"siafundOutputs"`
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
	Basis       types.ChainIndex    `json:"basis"`
	Transaction types.Transaction   `json:"transaction"`
	ToSign      []types.Hash256     `json:"toSign"`
	DependsOn   []types.Transaction `json:"dependsOn"`
}

// WalletConstructRequest is the request type for /wallets/:id/construct.
type WalletConstructRequest struct {
	Siacoins      []types.SiacoinOutput `json:"siacoins"`
	Siafunds      []types.SiafundOutput `json:"siafunds"`
	ChangeAddress types.Address         `json:"changeAddress"`
}

// SignaturePayload is a signature that is required to finalize a transaction.
type SignaturePayload struct {
	PublicKey types.PublicKey `json:"publicKey"`
	SigHash   types.Hash256   `json:"sigHash"`
}

// WalletConstructResponse is the response type for /wallets/:id/construct/transaction.
type WalletConstructResponse struct {
	Basis        types.ChainIndex    `json:"basis"`
	ID           types.TransactionID `json:"id"`
	Transaction  types.Transaction   `json:"transaction"`
	EstimatedFee types.Currency      `json:"estimatedFee"`
}

// WalletConstructV2Response is the response type for /wallets/:id/construct/v2/transaction.
type WalletConstructV2Response struct {
	Basis        types.ChainIndex    `json:"basis"`
	ID           types.TransactionID `json:"id"`
	Transaction  types.V2Transaction `json:"transaction"`
	EstimatedFee types.Currency      `json:"estimatedFee"`
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

// SiacoinElementsResponse is the response type for any endpoint that returns
// siacoin UTXOs
type SiacoinElementsResponse struct {
	Basis   types.ChainIndex       `json:"basis"`
	Outputs []types.SiacoinElement `json:"outputs"`
}

// SiafundElementsResponse is the response type for any endpoint that returns
// siafund UTXOs
type SiafundElementsResponse struct {
	Basis   types.ChainIndex       `json:"basis"`
	Outputs []types.SiafundElement `json:"outputs"`
}

// ElementSpentResponse is the response type for /outputs/siacoin/:id/spent and
// /outputs/siafund/:id/spent.
type ElementSpentResponse struct {
	Spent bool          `json:"spent"`
	Event *wallet.Event `json:"event,omitempty"`
}

type MiningGetBlockTemplateRequest struct {
	PayoutAddress types.Address `json:"payoutAddress,omitempty"`
	LongPollID    string        `json:"longpollid,omitempty"`
}

type MiningGetBlockTemplateResponse struct {
	Transactions []MiningGetBlockTemplateResponseTxn `json:"transactions"`
	MinerPayout  []MiningGetBlockTemplateResponseTxn `json:"minerpayout"`
	PreviousHash string                              `json:"previousblockhash"`

	// Optional long polling from BIP 0022.
	LongPollID string `json:"longpollid"`

	// Basic pool extension from BIP 0023.
	Target string `json:"target"`
	Height uint32 `json:"height"`

	// Mutations from BIP 0023.
	Timestamp int32 `json:"curtime"`

	// Block proposal from BIP 0023.
	Version uint32 `json:"version"`
	Bits    string `json:"bits"`
}

type MiningGetBlockTemplateResponseTxn struct {
	Data    string  `json:"data"`
	Hash    string  `json:"hash"`
	TxID    string  `json:"txid"`
	Depends []int64 `json:"depends"`
	Fee     int64   `json:"fee"`
	SigOps  int64   `json:"sigops"`
	TxType  string  `json:"txtype"`
}

type MiningSubmitBlockRequest struct {
	Params []string `json:"params"`
}
