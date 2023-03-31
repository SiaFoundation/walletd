package syncer

import (
	"encoding/json"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
)

// A DiscardLogger discards all log messages.
type DiscardLogger struct{}

// LogFailedConnect implements Logger.
func (DiscardLogger) LogFailedConnect(peer string, inbound bool, err error) {}

// LogFailedRPC implements Logger.
func (DiscardLogger) LogFailedRPC(p *gateway.Peer, rpc string, err error) {}

// LogBannedPeer implements Logger.
func (DiscardLogger) LogBannedPeer(p *gateway.Peer, err error) {}

// LogDiscoveredNodes implements Logger.
func (DiscardLogger) LogDiscoveredNodes(p *gateway.Peer, nodes []string) {}

// LogSyncStart implements Logger.
func (DiscardLogger) LogSyncStart(p *gateway.Peer) {}

// LogSyncProgress implements Logger.
func (DiscardLogger) LogSyncProgress(p *gateway.Peer, numBlocks int, tip types.ChainIndex) {}

// LogSyncFinish implements Logger.
func (DiscardLogger) LogSyncFinish(p *gateway.Peer) {}

// A StdLogger logs messages to a *log.Logger.
type StdLogger log.Logger

func (l *StdLogger) printf(fmt string, args ...interface{}) {
	(*log.Logger)(l).Printf(fmt, args...)
}

// LogFailedConnect implements Logger.
func (l *StdLogger) LogFailedConnect(peer string, inbound bool, err error) {
	if inbound {
		l.printf("failed to accept inbound connection from %v: %v", peer, err)
	} else {
		l.printf("failed to form outbound connect to %v: %v", peer, err)
	}
}

// LogFailedRPC implements Logger.
func (l *StdLogger) LogFailedRPC(p *gateway.Peer, rpc string, err error) {
	l.printf("failed to call RPC %q on peer %v: %v", rpc, p, err)
}

// LogBannedPeer implements Logger.
func (l *StdLogger) LogBannedPeer(p *gateway.Peer, err error) {
	l.printf("banned peer %v: %v", p, err)
}

// LogDiscoveredNodes implements Logger.
func (l *StdLogger) LogDiscoveredNodes(p *gateway.Peer, nodes []string) {
	l.printf("discovered %v nodes via peer %v", len(nodes), p)
}

// LogSyncStart implements Logger.
func (l *StdLogger) LogSyncStart(p *gateway.Peer) {
	l.printf("starting sync with peer %v", p)
}

// LogSyncProgress implements Logger.
func (l *StdLogger) LogSyncProgress(p *gateway.Peer, numBlocks int, tip types.ChainIndex) {
	l.printf("synced %v blocks with peer %v (tip now %v)", numBlocks, p, tip)
}

// LogSyncFinish implements Logger.
func (l *StdLogger) LogSyncFinish(p *gateway.Peer) {
	l.printf("finished sync with peer %v", p)
}

// EphemeralPeerManager implements PeerManager with an in-memory map.
type EphemeralPeerManager struct {
	peers map[string]struct{}
	bans  map[string]time.Time
	mu    sync.Mutex
}

func (pm *EphemeralPeerManager) banned(peer string) bool {
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
		if expiry, ok := pm.bans[s]; ok {
			if time.Until(expiry) <= 0 {
				delete(pm.bans, s)
			} else {
				return true
			}
		}
	}
	return false
}

// Peers implements PeerManager.
func (pm *EphemeralPeerManager) Peers() []string {
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

// AddPeer implements PeerManager.
func (pm *EphemeralPeerManager) AddPeer(peer string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.peers[peer] = struct{}{}
}

// Banned implements PeerManager.
func (pm *EphemeralPeerManager) Banned(peer string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.banned(peer)
}

// Ban implements PeerManager.
func (pm *EphemeralPeerManager) Ban(peer string, duration time.Duration) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	// canonicalize
	if _, ipnet, err := net.ParseCIDR(peer); err == nil {
		peer = ipnet.String()
	}
	pm.bans[peer] = time.Now().Add(duration)
}

// NewEphemeralPeerManager initializes an EphemeralPeerManager with an initial
// set of bootstrap peers.
func NewEphemeralPeerManager(bootstrap []string) *EphemeralPeerManager {
	peers := make(map[string]struct{})
	for _, peer := range bootstrap {
		peers[peer] = struct{}{}
	}
	return &EphemeralPeerManager{
		peers: peers,
	}
}

type jsonPeer struct {
	Addr string `json:"addr"`
}

type jsonBan struct {
	Subnet string    `json:"subnet"`
	Expiry time.Time `json:"expiry"`
}

// JSONPeerManager implements PeerManager with a JSON file on disk.
type JSONPeerManager struct {
	*EphemeralPeerManager
	path string
}

func (pm *JSONPeerManager) load() error {
	f, err := os.Open(pm.path)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	defer f.Close()

	var p struct {
		Peers []jsonPeer `json:"peers"`
		Bans  []jsonBan  `json:"bans"`
	}
	if err := json.NewDecoder(f).Decode(&p); err != nil {
		return err
	}
	for _, peer := range p.Peers {
		pm.EphemeralPeerManager.peers[peer.Addr] = struct{}{}
	}
	for _, ban := range p.Bans {
		if time.Until(ban.Expiry) > 0 {
			pm.EphemeralPeerManager.bans[ban.Subnet] = ban.Expiry
		}
	}
	return nil
}

func (pm *JSONPeerManager) save() error {
	var p struct {
		Peers []jsonPeer `json:"peers"`
		Bans  []jsonBan  `json:"bans"`
	}
	for addr := range pm.EphemeralPeerManager.peers {
		p.Peers = append(p.Peers, jsonPeer{addr})
	}
	for subnet, expiry := range pm.EphemeralPeerManager.bans {
		p.Bans = append(p.Bans, jsonBan{subnet, expiry})
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

// AddPeer implements PeerManager.
func (pm *JSONPeerManager) AddPeer(peer string) {
	pm.EphemeralPeerManager.AddPeer(peer)
	pm.save()
}

// Ban implements PeerManager.
func (pm *JSONPeerManager) Ban(peer string, duration time.Duration) {
	pm.EphemeralPeerManager.Ban(peer, duration)
	pm.save()
}

// NewJSONPeerManager returns a JSONPeerManager backed by the specified file.
func NewJSONPeerManager(path string, bootstrap []string) (*JSONPeerManager, error) {
	pm := &JSONPeerManager{
		EphemeralPeerManager: NewEphemeralPeerManager(bootstrap),
		path:                 path,
	}
	return pm, pm.load()
}
