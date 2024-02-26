package persist_test

import (
	"testing"

	"go.sia.tech/coreutils/syncer"
	"go.sia.tech/walletd/wallet"
	"go.uber.org/zap"
)

func testWalletStore(t *testing.T, storeFn func(t *testing.T, log *zap.Logger) wallet.Store) {
	t.Run("reorg", testWalletReorg(storeFn))
	t.Run("ephemeralBalance", testWalletEphemeralBalance(storeFn))
	t.Run("v2", testWalletV2(storeFn))
	t.Run("addresses", testWalletAddresses(storeFn))
}

func testPeerStore(t *testing.T, storeFn func(t *testing.T, log *zap.Logger) syncer.PeerStore) {
	t.Run("peers", testPeers(storeFn))
	t.Run("banPeer", testBanPeer(storeFn))
}
