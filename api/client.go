package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"time"

	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/types"
	"go.sia.tech/walletd/wallet"
)

// A Client provides methods for interacting with a walletd API server.
type Client struct {
	BaseURL      string
	AuthPassword string
}

func (c *Client) req(method string, route string, data, resp interface{}) error {
	var body io.Reader
	if data != nil {
		js, _ := json.Marshal(data)
		body = bytes.NewReader(js)
	}
	req, err := http.NewRequest(method, fmt.Sprintf("%v%v", c.BaseURL, route), body)
	if err != nil {
		panic(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("", c.AuthPassword)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer io.Copy(ioutil.Discard, r.Body)
	defer r.Body.Close()
	if r.StatusCode != 200 {
		err, _ := ioutil.ReadAll(r.Body)
		return errors.New(string(err))
	}
	if resp == nil {
		return nil
	}
	return json.NewDecoder(r.Body).Decode(resp)
}

func (c *Client) get(route string, r interface{}) error     { return c.req("GET", route, nil, r) }
func (c *Client) post(route string, d, r interface{}) error { return c.req("POST", route, d, r) }
func (c *Client) put(route string, d interface{}) error     { return c.req("PUT", route, d, nil) }
func (c *Client) delete(route string) error                 { return c.req("DELETE", route, nil, nil) }

// ConsensusTip returns the current tip index.
func (c *Client) ConsensusTip() (resp ChainIndex, err error) {
	err = c.get("/consensus/tip", &resp)
	return
}

// SyncerPeers returns the current peers of the syncer.
func (c *Client) SyncerPeers() (resp []SyncerPeerResponse, err error) {
	err = c.get("/syncer/peers", &resp)
	return
}

// SyncerConnect adds the address as a peer of the syncer.
func (c *Client) SyncerConnect(addr string) (err error) {
	err = c.post("/syncer/connect", SyncerConnectRequest{addr}, nil)
	return
}

// WalletBalance returns the current wallet balance.
func (c *Client) WalletBalance() (resp WalletBalanceResponse, err error) {
	err = c.get("/wallet/balance", &resp)
	return
}

// WalletAddress returns an address controlled by the wallet.
func (c *Client) WalletAddress() (resp types.UnlockHash, err error) {
	err = c.get("/wallet/address", &resp)
	return
}

// WalletAddresses the addresses controlled by the wallet.
func (c *Client) WalletAddresses() (resp []types.UnlockHash, err error) {
	err = c.get("/wallet/addresses", &resp)
	return
}

// WalletOutputs returns the set of unspent outputs controlled by the wallet.
func (c *Client) WalletOutputs() (resp []wallet.SiacoinElement, err error) {
	err = c.get("/wallet/outputs", &resp)
	return
}

// WalletTransaction returns the transaction with the given ID.
func (c *Client) WalletTransaction(id types.TransactionID) (resp wallet.Transaction, err error) {
	err = c.get(fmt.Sprintf("/wallet/transaction/"+id.String()), &resp)
	return
}

// WalletTransactions returns all transactions relevant to the wallet.
func (c *Client) WalletTransactions(since time.Time, max int) (resp []wallet.Transaction, err error) {
	err = c.get(fmt.Sprintf("/wallet/transactions?since=%s&max=%d", since.Format(time.RFC3339), max), &resp)
	return
}

// WalletTransactionsAddress returns all transactions relevant to the wallet.
func (c *Client) WalletTransactionsAddress(addr types.UnlockHash) (resp []wallet.Transaction, err error) {
	err = c.get("/wallet/transactions/"+addr.String(), &resp)
	return
}

// WalletSign signs a transaction.
func (c *Client) WalletSign(txn types.Transaction, toSign []crypto.Hash) (resp types.Transaction, err error) {
	err = c.post("/wallet/sign", WalletSignRequest{txn, toSign}, &resp)
	return
}

// WalletSiacoins sends the given amount of siacoins to the destination.
func (c *Client) WalletSiacoins(amount types.Currency, destination types.UnlockHash) (resp WalletSiacoinsResponse, err error) {
	err = c.post(fmt.Sprintf("/wallet/siacoins?amount=%s&desination=%s", amount, destination), nil, &resp)
	return
}

// NewClient returns a client that communicates with a walletd server listening
// on the specified address.
func NewClient(addr, password string) *Client {
	return &Client{
		BaseURL:      addr,
		AuthPassword: password,
	}
}
