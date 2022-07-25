package wallet

import (
	"crypto/ed25519"
	"time"

	"go.sia.tech/siad/types"
)

// StandardUnlockConditions returns the standard unlock conditions for a single
// Ed25519 key.
func StandardUnlockConditions(priv ed25519.PublicKey) types.UnlockConditions {
	return types.UnlockConditions{
		PublicKeys: []types.SiaPublicKey{{
			Algorithm: types.SignatureEd25519,
			Key:       priv[32:],
		}},
		SignaturesRequired: 1,
	}
}

// StandardAddress returns the standard address for an Ed25519 key.
func StandardAddress(priv ed25519.PublicKey) types.UnlockHash {
	return StandardUnlockConditions(priv).UnlockHash()
}

// A SiacoinElement is a SiacoinOutput along with its ID.
type SiacoinElement struct {
	types.SiacoinOutput
	ID             types.OutputID
	MaturityHeight uint64
}

type ChainIndex struct {
	Height uint64
	ID     types.BlockID
}

// A Transaction is an on-chain transaction relevant to a particular wallet,
// paired with useful metadata.
type Transaction struct {
	Raw       types.Transaction
	Index     ChainIndex
	ID        types.TransactionID
	Inflow    types.Currency
	Outflow   types.Currency
	Timestamp time.Time
}

type Wallet struct{}

func (Wallet) Balance() types.Currency {
	panic("unimplemented")
}

func (Wallet) UnspentOutputs() ([]SiacoinElement, error) {
	panic("unimplemented")
}

func (Wallet) Transactions(since time.Time, max int) ([]Transaction, error) {
	panic("unimplemented")
}

func New() *Wallet {
	return &Wallet{}
}
