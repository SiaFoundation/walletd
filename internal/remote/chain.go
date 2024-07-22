package remote

import (
	"fmt"
	"sync"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/walletd/api"
	"go.uber.org/zap"
)

type (
	// A ChainManager is a chain.Manager that uses a remote API to interact with
	// the consensus set.
	ChainManager struct {
		client          *api.Client
		refreshInterval time.Duration

		mu    sync.Mutex
		state consensus.State
		fee   types.Currency
	}
)

// UpdatesSince returns any consensus updates since the given index, up to a
// maximum of max updates.
func (cm *ChainManager) UpdatesSince(index types.ChainIndex, limit int) ([]chain.RevertUpdate, []chain.ApplyUpdate, error) {
	return cm.client.ConsensusUpdates(index, limit)
}

// BestIndex returns the chain index for the given height, or false if the height
// is not available.
func (cm *ChainManager) BestIndex(height uint64) (types.ChainIndex, bool) {
	index, err := cm.client.ConsensusIndex(height)
	if err != nil {
		return types.ChainIndex{}, false
	}
	return index, true
}

// AddBlocks is a no-op for the remote chain manager.
func (cm *ChainManager) AddBlocks(blocks []types.Block) error {
	return nil // remote chain manager is read-only
}

// Tip returns the current chain index.
func (cm *ChainManager) Tip() types.ChainIndex {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.state.Index
}

// TipState returns the current state of the consensus set.
func (cm *ChainManager) TipState() consensus.State {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.state
}

// RecommendedFee returns the recommended fee per byte.
func (cm *ChainManager) RecommendedFee() types.Currency {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.fee
}

// PoolTransactions returns the transactions in the transaction pool.
func (cm *ChainManager) PoolTransactions() []types.Transaction {
	txns, _, err := cm.client.TxpoolTransactions()
	if err != nil {
		return nil
	}
	return txns
}

// V2PoolTransactions returns the transactions in the transaction pool.
func (cm *ChainManager) V2PoolTransactions() []types.V2Transaction {
	_, txns, err := cm.client.TxpoolTransactions()
	if err != nil {
		return nil
	}
	return txns
}

// AddPoolTransactions adds a list of transactions to the transaction pool.
func (cm *ChainManager) AddPoolTransactions(txns []types.Transaction) (bool, error) {
	err := cm.client.TxpoolBroadcast(txns, nil)
	return false, err // the bool is "known", but we don't have that information
}

// AddV2PoolTransactions adds a list of transactions to the transaction pool.
func (cm *ChainManager) AddV2PoolTransactions(index types.ChainIndex, txns []types.V2Transaction) (bool, error) {
	err := cm.client.TxpoolBroadcast(nil, txns)
	return false, err // the bool is "known", but we don't have that information
}

// UnconfirmedParents returns the parents of a transaction that are currently in the transaction pool.
func (cm *ChainManager) UnconfirmedParents(txn types.Transaction) []types.Transaction {
	parents, err := cm.client.TxpoolParents(txn)
	if err != nil {
		return nil
	}
	return parents
}

func (cm *ChainManager) updateState() error {
	state, err := cm.client.ConsensusTipState()
	if err != nil {
		return fmt.Errorf("failed to get consensus tip state: %w", err)
	}

	fee, err := cm.client.TxpoolFee()
	if err != nil {
		return fmt.Errorf("failed to get transaction pool fee: %w", err)
	}

	cm.mu.Lock()
	cm.state = state
	cm.fee = fee
	cm.mu.Unlock()
	return nil
}

// OnReorg registers a callback to be called when a reorganization occurs.
// This currently polls since the client does not expose a reorg callback yet.
func (cm *ChainManager) OnReorg(fn func(types.ChainIndex)) (cancel func()) {
	t := time.NewTicker(cm.refreshInterval)
	done := make(chan struct{})
	go func() {
		defer t.Stop()
		for {
			select {
			case <-t.C:
				fn(cm.Tip())
			case <-done:
				return
			}
		}
	}()
	return func() {
		close(done)
	}
}

// NewChainManager creates a new ChainManager.
func NewChainManager(client *api.Client, log *zap.Logger, opts ...ChainManagerOption) (*ChainManager, error) {
	cm := &ChainManager{
		client:          client,
		refreshInterval: 10 * time.Second,
	}
	for _, opt := range opts {
		opt(cm)
	}

	err := cm.updateState()
	if err != nil {
		return nil, err
	}

	go func() {
		t := time.NewTicker(cm.refreshInterval)
		defer t.Stop()

		for range t.C {
			err := cm.updateState()
			if err != nil {
				log.Error("failed to update chain state", zap.Error(err))
			}
		}
	}()
	return cm, nil
}
