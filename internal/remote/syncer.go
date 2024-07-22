package remote

import (
	"context"

	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/syncer"
	"go.sia.tech/walletd/api"
	"go.uber.org/zap"
)

// A Syncer is a syncer.Syncer that uses a remote API to broadcast transactions.
type Syncer struct {
	client *api.Client
	log    *zap.Logger
}

// Addr no-op
func (s *Syncer) Addr() string { return "" }

// Peers no-op
func (s *Syncer) Peers() []*syncer.Peer { return nil }

// PeerInfo no-op
func (s *Syncer) PeerInfo(addr string) (syncer.PeerInfo, error) { return syncer.PeerInfo{}, nil }

// Connect no-op
func (s *Syncer) Connect(ctx context.Context, addr string) (*syncer.Peer, error) { return nil, nil }

// BroadcastHeader no-op
func (s *Syncer) BroadcastHeader(bh gateway.BlockHeader) {}

// BroadcastV2BlockOutline no-op
func (s *Syncer) BroadcastV2BlockOutline(bo gateway.V2BlockOutline) {}

// BroadcastTransactionSet broadcasts a set of transactions using the remote
// API
func (s *Syncer) BroadcastTransactionSet(txns []types.Transaction) {
	if err := s.client.TxpoolBroadcast(txns, nil); err != nil {
		s.log.Error("failed to broadcast transaction set", zap.Error(err))
	}
}

// BroadcastV2TransactionSet broadcasts a set of v2 transactions using the remote
// API
func (s *Syncer) BroadcastV2TransactionSet(index types.ChainIndex, txns []types.V2Transaction) {
	if err := s.client.TxpoolBroadcast(nil, txns); err != nil {
		s.log.Error("failed to broadcast v2 transaction set", zap.Error(err))
	}
}

// NewSyncer creates a new Syncer.
func NewSyncer(client *api.Client, log *zap.Logger) *Syncer {
	return &Syncer{
		client: client,
		log:    log,
	}
}
