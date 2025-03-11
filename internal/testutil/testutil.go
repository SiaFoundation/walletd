package testutil

import (
	"net"
	"path/filepath"
	"testing"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/syncer"
	"go.sia.tech/coreutils/testutil"
	"go.sia.tech/walletd/v2/persist/sqlite"
	"go.uber.org/zap"
)

type (
	// A ConsensusNode is a test harness for starting a bare-bones consensus node.
	ConsensusNode struct {
		Store  *sqlite.Store
		Chain  *chain.Manager
		Syncer *syncer.Syncer
	}
)

// WaitForSync waits for the store to sync to the current tip of the chain manager.
func (cn *ConsensusNode) WaitForSync(tb testing.TB) {
	tb.Helper()

	for i := 0; i < 1000; i++ {
		index, err := cn.Store.LastCommittedIndex()
		if err != nil {
			tb.Fatal(err)
		} else if index == cn.Chain.Tip() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	tb.Fatal("timeout waiting for sync")
}

// MineBlocks mines n blocks, sending the rewards to addr.
func (cn *ConsensusNode) MineBlocks(tb testing.TB, addr types.Address, n int) {
	tb.Helper()

	for i := 0; i < n; i++ {
		testutil.MineBlocks(tb, cn.Chain, addr, 1)
		cn.WaitForSync(tb)
	}
}

// NewConsensusNode creates a new ConsensusNode.
func NewConsensusNode(tb testing.TB, n *consensus.Network, genesis types.Block, log *zap.Logger) *ConsensusNode {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { l.Close() })

	dbstore, tipState, err := chain.NewDBStore(chain.NewMemDB(), n, genesis)
	if err != nil {
		tb.Fatal(err)
	}
	cm := chain.NewManager(dbstore, tipState)

	store, err := sqlite.OpenDatabase(filepath.Join(tb.TempDir(), "walletd.sqlite"), log.Named("sqlite3"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { store.Close() })

	peerStore, err := sqlite.NewPeerStore(store)
	if err != nil {
		tb.Fatal(err)
	}

	s := syncer.New(l, cm, peerStore, gateway.Header{
		GenesisID:  genesis.ID(),
		UniqueID:   gateway.GenerateUniqueID(),
		NetAddress: l.Addr().String(),
	})
	tb.Cleanup(func() { s.Close() })
	go s.Run()

	return &ConsensusNode{
		Store:  store,
		Chain:  cm,
		Syncer: s,
	}
}

// V1Network returns a test network and genesis block.
func V1Network() (*consensus.Network, types.Block) {
	return testutil.Network()
}

// V2Network returns a test network and genesis block with early V2 hardforks
func V2Network() (*consensus.Network, types.Block) {
	return testutil.V2Network()
}
