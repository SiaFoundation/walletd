package txpool

import (
	"fmt"
	"sync"

	"go.sia.tech/core/chain"
	"go.sia.tech/core/types"
)

// A TransactionValidator validates transactions.
type TransactionValidator interface {
	ValidateTransactionSet(txns []types.Transaction) error
}

// A TxPool holds transactions that may be included in future blocks.
type TxPool struct {
	txns      map[types.TransactionID]types.Transaction
	tv        TransactionValidator
	validated bool
	mu        sync.Mutex
}

func (tp *TxPool) revalidate() {
	if tp.validated {
		return
	}

	// TODO

	tp.validated = true
}

func (tp *TxPool) superset(txns []types.Transaction) []types.Transaction {
	return txns
}

// AddTransactionSet validates a transaction set and adds it to the pool. If any
// transaction references ephemeral parent outputs, those parent outputs must be
// created by transactions in the transaction set or already in the pool.
func (tp *TxPool) AddTransactionSet(txns []types.Transaction) error {
	if err := tp.tv.ValidateTransactionSet(tp.superset(txns)); err != nil {
		return fmt.Errorf("transaction set is invalid: %w", err)
	}
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.revalidate()
	for _, txn := range txns {
		tp.txns[txn.ID()] = txn
	}
	return nil
}

// Transaction returns the transaction with the specified ID, if it is currently
// in the pool.
func (tp *TxPool) Transaction(id types.TransactionID) (types.Transaction, bool) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.revalidate()
	txn, ok := tp.txns[id]
	return txn, ok
}

// Transactions returns the transactions currently in the pool.
func (tp *TxPool) Transactions() []types.Transaction {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.revalidate()
	txns := make([]types.Transaction, 0, len(tp.txns))
	for _, txn := range tp.txns {
		txns = append(txns, txn)
	}
	return txns
}

// ProcessChainApplyUpdate implements chain.Subscriber.
func (tp *TxPool) ProcessChainApplyUpdate(cau *chain.ApplyUpdate, _ bool) error {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	// delete confirmed txns
	for _, txn := range cau.Block.Transactions {
		delete(tp.txns, txn.ID())
	}

	tp.validated = false
	return nil
}

// ProcessChainRevertUpdate implements chain.Subscriber.
func (tp *TxPool) ProcessChainRevertUpdate(cru *chain.RevertUpdate) error {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	// put reverted txns back in the pool
	for _, txn := range cru.Block.Transactions {
		tp.txns[txn.ID()] = txn
	}

	tp.validated = false
	return nil
}

// RecommendedFee returns the recommended fee (per weight unit) to ensure a high
// probability of inclusion in the next block.
func (tp *TxPool) RecommendedFee() types.Currency {
	// TODO: calculate based on current pool, prior blocks, and absolute min/max
	return types.NewCurrency64(1000)
}

// UnconfirmedParents returns the transactions in the pool that are referenced by txn.
func (tp *TxPool) UnconfirmedParents(txn types.Transaction) []types.Transaction {
	outputToParent := make(map[types.SiacoinOutputID]types.Transaction)
	for _, txn := range tp.txns {
		for j := range txn.SiacoinOutputs {
			outputToParent[txn.SiacoinOutputID(j)] = txn
		}
	}
	var parents []types.Transaction
	seen := make(map[types.TransactionID]bool)
	for _, sci := range txn.SiacoinInputs {
		if parent, ok := outputToParent[sci.ParentID]; ok {
			if txid := parent.ID(); !seen[txid] {
				seen[txid] = true
				parents = append(parents, parent)
			}
		}
	}
	return parents
}

// New creates a new transaction pool.
func New(tv TransactionValidator) *TxPool {
	return &TxPool{
		txns: make(map[types.TransactionID]types.Transaction),
		tv:   tv,
	}
}
