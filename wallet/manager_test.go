package wallet_test

import (
	"errors"
	"testing"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/v2/internal/testutil"
	"go.sia.tech/walletd/v2/wallet"
)

func TestHealth(t *testing.T) {
	n, genesis := testutil.V2Network()
	tn := newTestNode(t, n, genesis)
	wm := tn.manager

	if err := wm.Health(); !errors.Is(err, wallet.ErrNotSyncing) {
		t.Fatalf("expected error %q, got %q", wallet.ErrNotSyncing, err)
	}

	tn.MineBlocks(t, types.VoidAddress, 1)

	if err := wm.Health(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
