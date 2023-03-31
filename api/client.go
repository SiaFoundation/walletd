package api

import (
	"encoding/json"
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

// WalletAddAddress adds the specified address and associated metadata to the
// wallet.
func (c *Client) WalletAddAddress(addr types.Address, info json.RawMessage) (err error) {
	fmt.Println(string(info))
	err = c.c.PUT(fmt.Sprintf("/wallet/addresses/%v", addr), info)
	return
}

// WalletAddresses the addresses controlled by the wallet.
func (c *Client) WalletAddresses() (resp map[types.Address]json.RawMessage, err error) {
	err = c.c.GET("/wallet/addresses", &resp)
	return
}

// WalletBalance returns the current wallet balance.
func (c *Client) WalletBalance() (resp WalletBalanceResponse, err error) {
	err = c.c.GET("/wallet/balance", &resp)
	return
}

// WalletEvents returns all events relevant to the wallet.
func (c *Client) WalletEvents(since time.Time, max int) (resp []wallet.Event, err error) {
	err = c.c.GET(fmt.Sprintf("/wallet/events?since=%s&max=%d", paramTime(since), max), &resp)
	return
}

// WalletOutputs returns the set of unspent outputs controlled by the wallet.
func (c *Client) WalletOutputs() (sc []wallet.SiacoinElement, sf []wallet.SiafundElement, err error) {
	var resp WalletOutputsResponse
	err = c.c.GET("/wallet/outputs", &resp)
	return resp.SiacoinOutputs, resp.SiafundOutputs, err
}

// WalletReserve reserves a set outputs for use in a transaction.
func (c *Client) WalletReserve(sc []types.SiacoinOutputID, sf []types.SiafundOutputID, duration time.Duration) (err error) {
	err = c.c.POST("/wallet/reserve", WalletReserveRequest{
		SiacoinOutputs: sc,
		SiafundOutputs: sf,
	}, nil)
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
