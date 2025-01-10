package api

import (
	"fmt"
	"sync"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/jape"
	"go.sia.tech/walletd/wallet"
)

// A Client provides methods for interacting with a walletd API server.
type Client struct {
	c jape.Client

	mu sync.Mutex // protects n
	n  *consensus.Network
}

func (c *Client) getNetwork() (*consensus.Network, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.n == nil {
		var err error
		c.n, err = c.ConsensusNetwork()
		if err != nil {
			return nil, err
		}
	}
	return c.n, nil
}

// BaseURL returns the URL of the walletd server.
func (c *Client) BaseURL() string {
	return c.c.BaseURL
}

// State returns information about the current state of the walletd daemon.
func (c *Client) State() (resp StateResponse, err error) {
	err = c.c.GET("/state", &resp)
	return
}

// TxpoolBroadcast broadcasts a set of transaction to the network.
func (c *Client) TxpoolBroadcast(basis types.ChainIndex, txns []types.Transaction, v2txns []types.V2Transaction) (err error) {
	err = c.c.POST("/txpool/broadcast", TxpoolBroadcastRequest{
		Basis:          basis,
		Transactions:   txns,
		V2Transactions: v2txns,
	}, nil)
	return
}

// TxpoolTransactions returns all transactions in the transaction pool.
func (c *Client) TxpoolTransactions() (basis types.ChainIndex, txns []types.Transaction, v2txns []types.V2Transaction, err error) {
	var resp TxpoolTransactionsResponse
	err = c.c.GET("/txpool/transactions", &resp)
	return resp.Basis, resp.Transactions, resp.V2Transactions, err
}

// TxpoolParents returns the parents of a transaction that are currently in the
// transaction pool.
func (c *Client) TxpoolParents(txn types.Transaction) (resp []types.Transaction, err error) {
	err = c.c.POST("/txpool/parents", txn, &resp)
	return
}

// TxpoolFee returns the recommended fee (per weight unit) to ensure a high
// probability of inclusion in the next block.
func (c *Client) TxpoolFee() (resp types.Currency, err error) {
	err = c.c.GET("/txpool/fee", &resp)
	return
}

// ConsensusNetwork returns the node's network metadata.
func (c *Client) ConsensusNetwork() (resp *consensus.Network, err error) {
	resp = new(consensus.Network)
	err = c.c.GET("/consensus/network", resp)
	return
}

// ConsensusIndex returns the consensus index at the specified height.
func (c *Client) ConsensusIndex(height uint64) (resp types.ChainIndex, err error) {
	err = c.c.GET(fmt.Sprintf("/consensus/index/%d", height), &resp)
	return
}

// ConsensusUpdates returns at most n consensus updates that have occurred since
// the specified index
func (c *Client) ConsensusUpdates(index types.ChainIndex, limit int) ([]chain.RevertUpdate, []chain.ApplyUpdate, error) {
	// index.String() is a short-hand representation. We need the full text
	indexBuf, err := index.MarshalText()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal index: %w", err)
	}

	var resp ConsensusUpdatesResponse
	if err := c.c.GET(fmt.Sprintf("/consensus/updates/%s?limit=%d", indexBuf, limit), &resp); err != nil {
		return nil, nil, err
	}

	network, err := c.getNetwork()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get network metadata: %w", err)
	}

	reverted := make([]chain.RevertUpdate, 0, len(resp.Reverted))
	for _, u := range resp.Reverted {
		revert := chain.RevertUpdate{
			RevertUpdate: u.Update,
			State:        u.State,
			Block:        u.Block,
		}
		revert.State.Network = network
		reverted = append(reverted, revert)
	}

	applied := make([]chain.ApplyUpdate, 0, len(resp.Applied))
	for _, u := range resp.Applied {
		apply := chain.ApplyUpdate{
			ApplyUpdate: u.Update,
			State:       u.State,
			Block:       u.Block,
		}
		apply.State.Network = network
		applied = append(applied, apply)
	}
	return reverted, applied, nil
}

// ConsensusTipState returns the current tip state.
func (c *Client) ConsensusTipState() (resp consensus.State, err error) {
	if err = c.c.GET("/consensus/tipstate", &resp); err != nil {
		return
	}
	resp.Network, err = c.getNetwork()
	return
}

// ConsensusTip returns the current tip index.
func (c *Client) ConsensusTip() (resp types.ChainIndex, err error) {
	err = c.c.GET("/consensus/tip", &resp)
	return
}

// SyncerPeers returns the current peers of the syncer.
func (c *Client) SyncerPeers() (resp []GatewayPeer, err error) {
	err = c.c.GET("/syncer/peers", &resp)
	return
}

// SyncerConnect adds the address as a peer of the syncer.
func (c *Client) SyncerConnect(addr string) (err error) {
	err = c.c.POST("/syncer/connect", addr, nil)
	return
}

// SyncerBroadcastBlock broadcasts a block to all peers.
func (c *Client) SyncerBroadcastBlock(b types.Block) (err error) {
	err = c.c.POST("/syncer/broadcast/block", b, nil)
	return
}

// Wallets returns the set of tracked wallets.
func (c *Client) Wallets() (ws []wallet.Wallet, err error) {
	err = c.c.GET("/wallets", &ws)
	return
}

// AddWallet adds a wallet to the set of tracked wallets.
func (c *Client) AddWallet(uw WalletUpdateRequest) (w wallet.Wallet, err error) {
	err = c.c.POST("/wallets", uw, &w)
	return
}

// UpdateWallet updates a wallet.
func (c *Client) UpdateWallet(id wallet.ID, uw WalletUpdateRequest) (w wallet.Wallet, err error) {
	err = c.c.POST(fmt.Sprintf("/wallets/%v", id), uw, &w)
	return
}

// RemoveWallet deletes a wallet. If the wallet is currently subscribed, it will
// be unsubscribed.
func (c *Client) RemoveWallet(id wallet.ID) (err error) {
	err = c.c.DELETE(fmt.Sprintf("/wallets/%v", id))
	return
}

// Wallet returns a client for interacting with the specified wallet.
func (c *Client) Wallet(id wallet.ID) *WalletClient {
	return &WalletClient{c: c.c, id: id}
}

// ScanStatus returns the current state of wallet scanning.
func (c *Client) ScanStatus() (resp RescanResponse, err error) {
	err = c.c.GET("/rescan", &resp)
	return
}

// Rescan rescans the blockchain starting from the specified height.
func (c *Client) Rescan(height uint64) (err error) {
	err = c.c.POST("/rescan", height, nil)
	return
}

// AddressBalance returns the balance of a single address.
func (c *Client) AddressBalance(addr types.Address) (resp BalanceResponse, err error) {
	err = c.c.GET(fmt.Sprintf("/addresses/%v/balance", addr), &resp)
	return
}

// AddressEvents returns the events of a single address.
func (c *Client) AddressEvents(addr types.Address, offset, limit int) (resp []wallet.Event, err error) {
	err = c.c.GET(fmt.Sprintf("/addresses/%v/events?offset=%d&limit=%d", addr, offset, limit), &resp)
	return
}

// AddressUnconfirmedEvents returns the unconfirmed events for a single address.
func (c *Client) AddressUnconfirmedEvents(addr types.Address) (resp []wallet.Event, err error) {
	err = c.c.GET(fmt.Sprintf("/addresses/%v/events/unconfirmed", addr), &resp)
	return
}

// AddressSiacoinOutputs returns the unspent siacoin outputs for an address.
func (c *Client) AddressSiacoinOutputs(addr types.Address, offset, limit int) (resp []types.SiacoinElement, err error) {
	err = c.c.GET(fmt.Sprintf("/addresses/%v/outputs/siacoin?offset=%d&limit=%d", addr, offset, limit), &resp)
	return
}

// AddressSiafundOutputs returns the unspent siafund outputs for an address.
func (c *Client) AddressSiafundOutputs(addr types.Address, offset, limit int) (resp []types.SiafundElement, err error) {
	err = c.c.GET(fmt.Sprintf("/addresses/%v/outputs/siafund?offset=%d&limit=%d", addr, offset, limit), &resp)
	return
}

// Event returns the event with the specified ID.
func (c *Client) Event(id types.Hash256) (resp wallet.Event, err error) {
	err = c.c.GET(fmt.Sprintf("/events/%v", id), &resp)
	return
}

// A WalletClient provides methods for interacting with a particular wallet on a
// walletd API server.
type WalletClient struct {
	c  jape.Client
	id wallet.ID
}

// AddAddress adds the specified address and associated metadata to the
// wallet.
func (c *WalletClient) AddAddress(a wallet.Address) (err error) {
	err = c.c.PUT(fmt.Sprintf("/wallets/%v/addresses", c.id), a)
	return
}

// RemoveAddress removes the specified address from the wallet.
func (c *WalletClient) RemoveAddress(addr types.Address) (err error) {
	err = c.c.DELETE(fmt.Sprintf("/wallets/%v/addresses/%v", c.id, addr))
	return
}

// Addresses the addresses controlled by the wallet.
func (c *WalletClient) Addresses() (resp []wallet.Address, err error) {
	err = c.c.GET(fmt.Sprintf("/wallets/%v/addresses", c.id), &resp)
	return
}

// Balance returns the current wallet balance.
func (c *WalletClient) Balance() (resp BalanceResponse, err error) {
	err = c.c.GET(fmt.Sprintf("/wallets/%v/balance", c.id), &resp)
	return
}

// Events returns all events relevant to the wallet.
func (c *WalletClient) Events(offset, limit int) (resp []wallet.Event, err error) {
	err = c.c.GET(fmt.Sprintf("/wallets/%v/events?offset=%d&limit=%d", c.id, offset, limit), &resp)
	return
}

// UnconfirmedEvents returns all unconfirmed events relevant to the wallet.
func (c *WalletClient) UnconfirmedEvents() (resp []wallet.Event, err error) {
	err = c.c.GET(fmt.Sprintf("/wallets/%v/events/unconfirmed", c.id), &resp)
	return
}

// SiacoinOutputs returns the set of unspent outputs controlled by the wallet.
func (c *WalletClient) SiacoinOutputs(offset, limit int) (sc []types.SiacoinElement, err error) {
	err = c.c.GET(fmt.Sprintf("/wallets/%v/outputs/siacoin?offset=%d&limit=%d", c.id, offset, limit), &sc)
	return
}

// SiafundOutputs returns the set of unspent outputs controlled by the wallet.
func (c *WalletClient) SiafundOutputs(offset, limit int) (sf []types.SiafundElement, err error) {
	err = c.c.GET(fmt.Sprintf("/wallets/%v/outputs/siafund?offset=%d&limit=%d", c.id, offset, limit), &sf)
	return
}

// Reserve reserves a set outputs for use in a transaction.
func (c *WalletClient) Reserve(sc []types.SiacoinOutputID, sf []types.SiafundOutputID, duration time.Duration) (err error) {
	err = c.c.POST(fmt.Sprintf("/wallets/%v/reserve", c.id), WalletReserveRequest{
		SiacoinOutputs: sc,
		SiafundOutputs: sf,
	}, nil)
	return
}

// Release releases a set of previously-reserved outputs.
func (c *WalletClient) Release(sc []types.SiacoinOutputID, sf []types.SiafundOutputID) (err error) {
	err = c.c.POST(fmt.Sprintf("/wallets/%v/release", c.id), WalletReleaseRequest{
		SiacoinOutputs: sc,
		SiafundOutputs: sf,
	}, nil)
	return
}

// Fund funds a siacoin transaction.
func (c *WalletClient) Fund(txn types.Transaction, amount types.Currency, changeAddr types.Address) (resp WalletFundResponse, err error) {
	err = c.c.POST(fmt.Sprintf("/wallets/%v/fund", c.id), WalletFundRequest{
		Transaction:   txn,
		Amount:        amount,
		ChangeAddress: changeAddr,
	}, &resp)
	return
}

// FundSF funds a siafund transaction.
func (c *WalletClient) FundSF(txn types.Transaction, amount uint64, changeAddr, claimAddr types.Address) (resp WalletFundResponse, err error) {
	err = c.c.POST(fmt.Sprintf("/wallets/%v/fundsf", c.id), WalletFundSFRequest{
		Transaction:   txn,
		Amount:        amount,
		ChangeAddress: changeAddr,
		ClaimAddress:  claimAddr,
	}, &resp)
	return
}

// Construct constructs a transaction sending the specified Siacoins or Siafunds to the recipients. The transaction is returned
// along with its ID and calculated miner fee. The transaction will need to be signed before broadcasting.
func (c *WalletClient) Construct(siacoins []types.SiacoinOutput, siafunds []types.SiafundOutput, change types.Address) (resp WalletConstructResponse, err error) {
	err = c.c.POST(fmt.Sprintf("/wallets/%v/construct/transaction", c.id), WalletConstructRequest{
		Siacoins:      siacoins,
		Siafunds:      siafunds,
		ChangeAddress: change,
	}, &resp)
	return
}

// ConstructV2 constructs a V2 transaction sending the specified Siacoins or Siafunds to the recipients. The transaction is returned
// along with its ID and calculated miner fee. The transaction will need to be signed before broadcasting.
func (c *WalletClient) ConstructV2(siacoins []types.SiacoinOutput, siafunds []types.SiafundOutput, change types.Address) (resp WalletConstructV2Response, err error) {
	err = c.c.POST(fmt.Sprintf("/wallets/%v/construct/v2/transaction", c.id), WalletConstructRequest{
		Siacoins:      siacoins,
		Siafunds:      siafunds,
		ChangeAddress: change,
	}, &resp)
	return
}

// NewClient returns a client that communicates with a walletd server listening
// on the specified address.
func NewClient(addr, password string) *Client {
	return &Client{c: jape.Client{
		BaseURL:  addr,
		Password: password,
	}}
}
