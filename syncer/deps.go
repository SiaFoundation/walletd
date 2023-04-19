package syncer

import (
	"encoding/json"
	"net"
	"os"
	"sync"
	"time"
)

type peerBan struct {
	Expiry time.Time `json:"expiry"`
	Reason string    `json:"reason"`
}

// EphemeralPeerStore implements PeerStore with an in-memory map.
type EphemeralPeerStore struct {
	peers map[string]PeerInfo
	bans  map[string]peerBan
	mu    sync.Mutex
}

func (pm *EphemeralPeerStore) banned(peer string) bool {
	host, _, err := net.SplitHostPort(peer)
	if err != nil {
		return false // shouldn't happen
	}
	subnet := func(subnet string) string {
		ip, ipnet, err := net.ParseCIDR(host + subnet)
		if err != nil {
			return "" // shouldn't happen
		}
		return ip.Mask(ipnet.Mask).String() + subnet
	}

	subs := []string{
		peer,          //  1.2.3.4:5678
		subnet("/32"), //  1.2.3.4:*
		subnet("/24"), //  1.2.3.*
		subnet("/16"), //  1.2.*
		subnet("/8"),  //  1.*
		subnet("/0"),  //  *
	}
	for _, s := range subs {
		if b, ok := pm.bans[s]; ok {
			if time.Until(b.Expiry) <= 0 {
				delete(pm.bans, s)
			} else {
				return true
			}
		}
	}
	return false
}

// AddPeer implements PeerStore.
func (pm *EphemeralPeerStore) AddPeer(peer string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if _, ok := pm.peers[peer]; !ok {
		pm.peers[peer] = PeerInfo{FirstSeen: time.Now()}
	}
}

// Peers implements PeerStore.
func (pm *EphemeralPeerStore) Peers() []string {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	var peers []string
	for p := range pm.peers {
		if !pm.banned(p) {
			peers = append(peers, p)
		}
	}
	return peers
}

// UpdatePeerInfo implements PeerStore.
func (pm *EphemeralPeerStore) UpdatePeerInfo(peer string, fn func(*PeerInfo)) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	info, ok := pm.peers[peer]
	if !ok {
		return
	}
	fn(&info)
	pm.peers[peer] = info
}

// PeerInfo implements PeerStore.
func (pm *EphemeralPeerStore) PeerInfo(peer string) (PeerInfo, bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	info, ok := pm.peers[peer]
	return info, ok
}

// Ban implements PeerStore.
func (pm *EphemeralPeerStore) Ban(peer string, duration time.Duration, reason string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	// canonicalize
	if _, ipnet, err := net.ParseCIDR(peer); err == nil {
		peer = ipnet.String()
	}
	pm.bans[peer] = peerBan{Expiry: time.Now().Add(duration), Reason: reason}
}

// Banned implements PeerStore.
func (pm *EphemeralPeerStore) Banned(peer string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.banned(peer)
}

// NewEphemeralPeerStore initializes an EphemeralPeerStore.
func NewEphemeralPeerStore() *EphemeralPeerStore {
	return &EphemeralPeerStore{
		peers: make(map[string]PeerInfo),
		bans:  make(map[string]peerBan),
	}
}

// JSONPeerStore implements PeerStore with a JSON file on disk.
type JSONPeerStore struct {
	*EphemeralPeerStore
	path string
}

func (pm *JSONPeerStore) load() error {
	f, err := os.Open(pm.path)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	defer f.Close()

	var p struct {
		Peers map[string]PeerInfo `json:"peers"`
		Bans  map[string]peerBan  `json:"bans"`
	}
	if err := json.NewDecoder(f).Decode(&p); err != nil {
		return err
	}
	pm.EphemeralPeerStore.peers = p.Peers
	pm.EphemeralPeerStore.bans = p.Bans
	return nil
}

func (pm *JSONPeerStore) save() error {
	pm.EphemeralPeerStore.mu.Lock()
	defer pm.EphemeralPeerStore.mu.Unlock()
	var p struct {
		Peers map[string]PeerInfo `json:"peers"`
		Bans  map[string]peerBan  `json:"bans"`
	}
	for addr, info := range pm.EphemeralPeerStore.peers {
		p.Peers[addr] = info
	}
	for subnet, ban := range pm.EphemeralPeerStore.bans {
		p.Bans[subnet] = ban
	}
	js, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(pm.path+"_tmp", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0660)
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
	} else if err := os.Rename(pm.path+"_tmp", pm.path); err != nil {
		return err
	}
	return nil
}

// AddPeer implements PeerStore.
func (pm *JSONPeerStore) AddPeer(peer string) {
	pm.EphemeralPeerStore.AddPeer(peer)
	pm.save()
}

// Ban implements PeerStore.
func (pm *JSONPeerStore) Ban(peer string, duration time.Duration, reason string) {
	pm.EphemeralPeerStore.Ban(peer, duration, reason)
	pm.save()
}

// NewJSONPeerStore returns a JSONPeerStore backed by the specified file.
func NewJSONPeerStore(path string) (*JSONPeerStore, error) {
	pm := &JSONPeerStore{
		EphemeralPeerStore: NewEphemeralPeerStore(),
		path:               path,
	}
	return pm, pm.load()
}
