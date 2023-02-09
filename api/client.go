package api

import (
	"fmt"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/jape"
	"go.sia.tech/walletd/wallet"
)

// A Client provides methods for interacting with a walletd API server.
type Client struct {
	c jape.Client
}

// TxpoolBroadcast broadcasts a set of transaction to the network.
func (c *Client) TxpoolBroadcast(txns []types.Transaction) (err error) {
	err = c.c.POST("/txpool/broadcast", txns, nil)
	return
}

// TxpoolTransactions returns all transactions in the transaction pool.
func (c *Client) TxpoolTransactions() (resp []types.Transaction, err error) {
	err = c.c.GET("/txpool/transactions", &resp)
	return
}

// ConsensusTip returns the current tip index.
func (c *Client) ConsensusTip() (resp ChainIndex, err error) {
	err = c.c.GET("/consensus/tip", &resp)
	return
}

// SyncerPeers returns the current peers of the syncer.
func (c *Client) SyncerPeers() (resp []string, err error) {
	err = c.c.GET("/syncer/peers", &resp)
	return
}

// SyncerConnect adds the address as a peer of the syncer.
func (c *Client) SyncerConnect(addr string) (err error) {
	err = c.c.POST("/syncer/connect", addr, nil)
	return
}

// WalletBalance returns the current wallet balance.
func (c *Client) WalletBalance() (resp WalletBalanceResponse, err error) {
	err = c.c.GET("/wallet/balance", &resp)
	return
}

// WalletAddress returns an address controlled by the wallet.
func (c *Client) WalletAddress() (resp types.Address, err error) {
	err = c.c.GET("/wallet/address", &resp)
	return
}

// WalletAddresses the addresses controlled by the wallet.
func (c *Client) WalletAddresses() (resp []types.Address, err error) {
	err = c.c.GET("/wallet/addresses", &resp)
	return
}

// WalletOutputs returns the set of unspent outputs controlled by the wallet.
func (c *Client) WalletOutputs() (resp []wallet.SiacoinElement, err error) {
	err = c.c.GET("/wallet/outputs", &resp)
	return
}

// WalletTransaction returns the transaction with the given ID.
func (c *Client) WalletTransaction(id types.TransactionID) (resp wallet.Transaction, err error) {
	err = c.c.GET(fmt.Sprintf("/wallet/transaction/%s", id), &resp)
	return
}

// WalletTransactions returns all transactions relevant to the wallet.
func (c *Client) WalletTransactions(since time.Time, max int) (resp []wallet.Transaction, err error) {
	err = c.c.GET(fmt.Sprintf("/wallet/transactions?since=%s&max=%d", paramTime(since), max), &resp)
	return
}

// WalletTransactionsAddress returns all transactions relevant to the wallet.
func (c *Client) WalletTransactionsAddress(addr types.Address) (resp []wallet.Transaction, err error) {
	err = c.c.GET(fmt.Sprintf("/wallet/transactions/%s", addr), &resp)
	return
}

// WalletSign signs a transaction.
func (c *Client) WalletSign(txn types.Transaction, toSign []types.Hash256) (resp types.Transaction, err error) {
	err = c.c.POST("/wallet/sign", WalletSignRequest{txn, toSign}, &resp)
	return
}

// WalletFund funds a transaction.
func (c *Client) WalletFund(txn types.Transaction, amountSC types.Currency, amountSF uint64) (resp WalletFundResponse, err error) {
	err = c.c.POST("/wallet/fund", WalletFundRequest{txn, amountSC, amountSF}, &resp)
	return
}

// WalletSplit distributes the value in the wallet's inputs among n outputs,
// each containing per siacoins.
func (c *Client) WalletSplit(n int, per types.Currency) (resp types.Transaction, err error) {
	err = c.c.POST("/wallet/split", WalletSplitRequest{n, per}, &resp)
	return
}

// WalletSendSiacoins sends a given amount of siacoins to the destination address.
func (c *Client) WalletSendSiacoins(amount types.Currency, destination types.Address, siafunds bool) (resp WalletSendResponse, err error) {
	wsr := WalletSendRequest{"siacoins", amount, destination}
	if siafunds {
		wsr.Type = "siafunds"
	}
	err = c.c.POST("/wallet/send", wsr, &resp)
	return
}

// NewClient returns a client that communicates with a walletd server listening
// on the specified address.
func NewClient(addr, password string) *Client {
	return &Client{jape.Client{
		BaseURL:  addr,
		Password: password,
	}}
}
