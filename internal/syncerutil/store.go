package syncerutil

import (
	"encoding/json"
	"net"
	"os"
	"sync"
	"time"

	"go.sia.tech/walletd/syncer"
)

type peerBan struct {
	Expiry time.Time `json:"expiry"`
	Reason string    `json:"reason"`
}

// EphemeralPeerStore implements PeerStore with an in-memory map.
type EphemeralPeerStore struct {
	peers map[string]syncer.PeerInfo
	bans  map[string]peerBan
	mu    sync.Mutex
}

func (eps *EphemeralPeerStore) peerBanned(peer string) bool {
	host, _, err := net.SplitHostPort(peer)
	if err != nil {
		return false // shouldn't happen
	}
	for _, s := range []string{
		peer,                        //  1.2.3.4:5678
		syncer.Subnet(host + "/32"), //  1.2.3.4:*
		syncer.Subnet(host + "/24"), //  1.2.3.*
		syncer.Subnet(host + "/16"), //  1.2.*
		syncer.Subnet(host + "/8"),  //  1.*
	} {
		if b, ok := eps.bans[s]; ok {
			if time.Until(b.Expiry) <= 0 {
				delete(eps.bans, s)
			} else {
				return true
			}
		}
	}
	return false
}

// AddPeer implements PeerStore.
func (eps *EphemeralPeerStore) AddPeer(peer string) {
	eps.mu.Lock()
	defer eps.mu.Unlock()
	if _, ok := eps.peers[peer]; !ok {
		eps.peers[peer] = syncer.PeerInfo{FirstSeen: time.Now()}
	}
}

// Peers implements PeerStore.
func (eps *EphemeralPeerStore) Peers() []string {
	eps.mu.Lock()
	defer eps.mu.Unlock()
	var peers []string
	for p := range eps.peers {
		if !eps.peerBanned(p) {
			peers = append(peers, p)
		}
	}
	return peers
}

// UpdatePeerInfo implements PeerStore.
func (eps *EphemeralPeerStore) UpdatePeerInfo(peer string, fn func(*syncer.PeerInfo)) {
	eps.mu.Lock()
	defer eps.mu.Unlock()
	info, ok := eps.peers[peer]
	if !ok {
		return
	}
	fn(&info)
	eps.peers[peer] = info
}

// PeerInfo implements PeerStore.
func (eps *EphemeralPeerStore) PeerInfo(peer string) (syncer.PeerInfo, bool) {
	eps.mu.Lock()
	defer eps.mu.Unlock()
	info, ok := eps.peers[peer]
	return info, ok
}

// Ban implements PeerStore.
func (eps *EphemeralPeerStore) Ban(peer string, duration time.Duration, reason string) {
	eps.mu.Lock()
	defer eps.mu.Unlock()
	// canonicalize
	if _, ipnet, err := net.ParseCIDR(peer); err == nil {
		peer = ipnet.String()
	}
	eps.bans[peer] = peerBan{Expiry: time.Now().Add(duration), Reason: reason}
}

// Banned implements PeerStore.
func (eps *EphemeralPeerStore) Banned(peer string) bool {
	eps.mu.Lock()
	defer eps.mu.Unlock()
	return eps.peerBanned(peer)
}

// NewEphemeralPeerStore initializes an EphemeralPeerStore.
func NewEphemeralPeerStore() *EphemeralPeerStore {
	return &EphemeralPeerStore{
		peers: make(map[string]syncer.PeerInfo),
		bans:  make(map[string]peerBan),
	}
}

type jsonPersist struct {
	Peers map[string]syncer.PeerInfo `json:"peers"`
	Bans  map[string]peerBan         `json:"bans"`
}

// JSONPeerStore implements PeerStore with a JSON file on disk.
type JSONPeerStore struct {
	*EphemeralPeerStore
	path     string
	lastSave time.Time
}

func (jps *JSONPeerStore) load() error {
	f, err := os.Open(jps.path)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	defer f.Close()
	var p jsonPersist
	if err := json.NewDecoder(f).Decode(&p); err != nil {
		return err
	}
	jps.EphemeralPeerStore.peers = p.Peers
	jps.EphemeralPeerStore.bans = p.Bans
	return nil
}

func (jps *JSONPeerStore) save() error {
	jps.EphemeralPeerStore.mu.Lock()
	defer jps.EphemeralPeerStore.mu.Unlock()
	if time.Since(jps.lastSave) < 5*time.Second {
		return nil
	}
	defer func() { jps.lastSave = time.Now() }()
	p := jsonPersist{
		Peers: jps.EphemeralPeerStore.peers,
		Bans:  jps.EphemeralPeerStore.bans,
	}
	js, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(jps.path+"_tmp", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0660)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(js); err != nil {
		return err
	} else if f.Sync(); err != nil {
		return err
	} else if f.Close(); err != nil {
		return err
	} else if err := os.Rename(jps.path+"_tmp", jps.path); err != nil {
		return err
	}
	return nil
}

// AddPeer implements PeerStore.
func (jps *JSONPeerStore) AddPeer(peer string) {
	jps.EphemeralPeerStore.AddPeer(peer)
	jps.save()
}

// UpdatePeerInfo implements PeerStore.
func (jps *JSONPeerStore) UpdatePeerInfo(peer string, fn func(*syncer.PeerInfo)) {
	jps.EphemeralPeerStore.UpdatePeerInfo(peer, fn)
	jps.save()
}

// Ban implements PeerStore.
func (jps *JSONPeerStore) Ban(peer string, duration time.Duration, reason string) {
	jps.EphemeralPeerStore.Ban(peer, duration, reason)
	jps.save()
}

// NewJSONPeerStore returns a JSONPeerStore backed by the specified file.
func NewJSONPeerStore(path string) (*JSONPeerStore, error) {
	jps := &JSONPeerStore{
		EphemeralPeerStore: NewEphemeralPeerStore(),
		path:               path,
	}
	return jps, jps.load()
}
