package wallet

import (
	"crypto/ed25519"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/siad/modules"
)

// A SiacoinElement is a SiacoinOutput along with its ID.
type SiacoinElement struct {
	types.SiacoinOutput
	ID             types.SiacoinOutputID
	MaturityHeight uint64
}

// A SiafundElement is a SiafundOutput along with its ID.
type SiafundElement struct {
	types.SiafundOutput
	ID types.SiafundOutputID
}

// A FileContractElement is a FileContract along with its ID.
type FileContractElement struct {
	types.FileContract
	ID types.FileContractID
}

type ChainIndex struct {
	ID     types.BlockID
	Height uint64
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

// A Store stores information needed by a wallet.
type Store interface {
	modules.ConsensusSetSubscriber
	ConsensusChangeID() (modules.ConsensusChangeID, error)

	Transaction(id types.TransactionID) (Transaction, error)
	Transactions(since time.Time, max int) ([]Transaction, error)
	UnspentSiacoinOutputs() ([]SiacoinElement, error)
	UnspentSiafundOutputs() ([]SiafundElement, error)

	SeedIndex() (uint64, error)
	SetSeedIndex(index uint64) error
	AddressInfo(addr types.Address) (SeedAddressInfo, error)

	AddAddress(info SeedAddressInfo) error
	Addresses() ([]types.Address, error)
	TransactionsByAddress(addr types.Address) ([]Transaction, error)
}

// StandardUnlockConditions returns the standard unlock conditions for a single
// Ed25519 key.
func StandardUnlockConditions(pub types.PublicKey) types.UnlockConditions {
	return types.UnlockConditions{
		PublicKeys: []types.UnlockKey{{
			Algorithm: types.SpecifierEd25519,
			Key:       pub[:],
		}},
		SignaturesRequired: 1,
	}
}

// StandardAddress returns the standard address for an Ed25519 key.
func StandardAddress(pub types.PublicKey) types.Address {
	return StandardUnlockConditions(pub).UnlockHash()
}

// StandardTransactionSignature is the most common form of TransactionSignature.
// It covers the entire transaction and references the first (typically the
// only) public key.
func StandardTransactionSignature(id types.Hash256) types.TransactionSignature {
	return types.TransactionSignature{
		ParentID:       id,
		CoveredFields:  types.CoveredFields{WholeTransaction: true},
		PublicKeyIndex: 0,
	}
}

// AppendTransactionSignature appends a TransactionSignature to txn and signs it
// with key.
func AppendTransactionSignature(txn *types.Transaction, txnSig types.TransactionSignature, key ed25519.PrivateKey) {
	txn.Signatures = append(txn.Signatures, txnSig)
	sigIndex := len(txn.Signatures) - 1
	hash := txn.ID()
	txn.Signatures[sigIndex].Signature = ed25519.Sign(key, hash[:])
}
