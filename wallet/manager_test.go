package wallet_test

import (
	"errors"
	"testing"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/v2/internal/testutil"
	"go.sia.tech/walletd/v2/wallet"
	"go.uber.org/zap/zaptest"
)

func TestHealth(t *testing.T) {
	log := zaptest.NewLogger(t)
	n, genesis := testutil.V2Network()
	cn := testutil.NewConsensusNode(t, n, genesis, log)
	cm := cn.Chain

	wm, err := wallet.NewManager(cm, cn.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	if err := wm.Health(); !errors.Is(err, wallet.ErrNotSyncing) {
		t.Fatalf("expected error %q, got %q", wallet.ErrNotSyncing, err)
	}

	cn.MineBlocks(t, types.VoidAddress, 1)

	if err := wm.Health(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
