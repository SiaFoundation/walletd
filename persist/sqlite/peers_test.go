package sqlite

import (
	"net"
	"path/filepath"
	"testing"
	"time"

	"go.sia.tech/coreutils/syncer"
	"go.uber.org/zap/zaptest"
)

func TestAddPeer(t *testing.T) {
	log := zaptest.NewLogger(t)
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "test.db"), log)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const peer = "1.2.3.4:9981"

	if err := db.AddPeer(peer); err != nil {
		t.Fatal(err)
	}

	lastConnect := time.Now().Truncate(time.Second) // stored as unix milliseconds
	syncedBlocks := uint64(15)
	syncDuration := 5 * time.Second

	err = db.UpdatePeerInfo(peer, func(info *syncer.PeerInfo) {
		info.LastConnect = lastConnect
		info.SyncedBlocks = syncedBlocks
		info.SyncDuration = syncDuration
	})
	if err != nil {
		t.Fatal(err)
	}

	info, err := db.PeerInfo(peer)
	if err != nil {
		t.Fatal(err)
	}

	if !info.LastConnect.Equal(lastConnect) {
		t.Errorf("expected LastConnect = %v; got %v", lastConnect, info.LastConnect)
	}
	if info.SyncedBlocks != syncedBlocks {
		t.Errorf("expected SyncedBlocks = %d; got %d", syncedBlocks, info.SyncedBlocks)
	}
	if info.SyncDuration != 5*time.Second {
		t.Errorf("expected SyncDuration = %s; got %s", syncDuration, info.SyncDuration)
	}
}

func TestBanPeer(t *testing.T) {
	log := zaptest.NewLogger(t)
	db, err := OpenDatabase(filepath.Join(t.TempDir(), "test.db"), log)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const peer = "1.2.3.4"

	if banned, err := db.Banned(peer); err != nil || banned {
		t.Fatal("expected peer to not be banned", err)
	}

	// ban the peer
	db.Ban(peer, time.Second, "test")

	if banned, err := db.Banned(peer); err != nil || !banned {
		t.Fatal("expected peer to be banned", err)
	}

	// wait for the ban to expire
	time.Sleep(time.Second)

	if banned, err := db.Banned(peer); err != nil || banned {
		t.Fatal("expected peer to not be banned", err)
	}

	// ban a subnet
	_, subnet, err := net.ParseCIDR(peer + "/24")
	if err != nil {
		t.Fatal(err)
	}

	t.Log("banning", subnet)
	db.Ban(subnet.String(), time.Second, "test")
	if banned, err := db.Banned(peer); err != nil || !banned {
		t.Fatal("expected peer to be banned", err)
	}
}
