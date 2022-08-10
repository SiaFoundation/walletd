package wallet

import (
	"crypto/ed25519"
	"time"

	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
)

// A SiacoinElement is a SiacoinOutput along with its ID.
type SiacoinElement struct {
	types.SiacoinOutput
	ID             types.SiacoinOutputID
	MaturityHeight types.BlockHeight
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
	Height types.BlockHeight
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

	Transaction(id types.TransactionID) (Transaction, bool, error)
	Transactions(since time.Time, max int) ([]Transaction, error)
	UnspentOutputs() ([]SiacoinElement, error)

	SeedIndex() (uint64, error)
	SetSeedIndex(index uint64) error
	AddressInfo(addr types.UnlockHash) (SeedAddressInfo, bool, error)
	AddAddress(info SeedAddressInfo) error
	Addresses() ([]types.UnlockHash, error)
}

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

// StandardTransactionSignature is the most common form of TransactionSignature.
// It covers the entire transaction and references the first (typically the
// only) public key.
func StandardTransactionSignature(id crypto.Hash) types.TransactionSignature {
	return types.TransactionSignature{
		ParentID:       id,
		CoveredFields:  types.FullCoveredFields,
		PublicKeyIndex: 0,
	}
}

// AppendTransactionSignature appends a TransactionSignature to txn and signs it
// with key.
func AppendTransactionSignature(txn *types.Transaction, txnSig types.TransactionSignature, key ed25519.PrivateKey) {
	txn.TransactionSignatures = append(txn.TransactionSignatures, txnSig)
	sigIndex := len(txn.TransactionSignatures) - 1
	hash := txn.SigHash(sigIndex, types.FoundationHardforkHeight+1)
	txn.TransactionSignatures[sigIndex].Signature = ed25519.Sign(key, hash[:])
}
