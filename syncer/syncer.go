package syncer

import (
	"errors"
	"io"
	"log"
	"net"
	"reflect"
	"sync"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	"lukechampine.com/frand"
)

// A ChainManager manages blockchain state.
type ChainManager interface {
	History() ([32]types.BlockID, error)
	BlocksForHistory(blocks []types.Block, history []types.BlockID) ([]types.Block, error)
	Block(id types.BlockID) (types.Block, bool)
	AddBlocks(blocks []types.Block) error
	Tip() types.ChainIndex
	TipState() consensus.State

	PoolTransaction(txid types.TransactionID) (types.Transaction, bool)
	AddPoolTransactions(txns []types.Transaction) error
}

// PeerInfo contains metadata about a peer.
type PeerInfo struct {
	FirstSeen    time.Time     `json:"firstSeen"`
	LastConnect  time.Time     `json:"lastConnect,omitempty"`
	SyncedBlocks uint64        `json:"syncedBlocks,omitempty"`
	SyncDuration time.Duration `json:"syncDuration,omitempty"`
}

// A PeerStore stores peers and bans.
type PeerStore interface {
	AddPeer(peer string)
	Peers() []string
	UpdatePeerInfo(peer string, fn func(*PeerInfo))
	PeerInfo(peer string) (PeerInfo, bool)

	// Ban temporarily bans one or more IPs. The addr should either be a single
	// IP with port (e.g. 1.2.3.4:5678) or a CIDR subnet (e.g. 1.2.3.4/16).
	Ban(addr string, duration time.Duration, reason string)
	Banned(peer string) bool
}

// Subnet normalizes the provided CIDR subnet string.
func Subnet(cidr string) string {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "" // shouldn't happen
	}
	return ip.Mask(ipnet.Mask).String() + cidr
}

type config struct {
	MaxInboundPeers            int
	MaxOutboundPeers           int
	MaxInflightRPCs            int
	ConnectTimeout             time.Duration
	ShareNodesTimeout          time.Duration
	SendBlockTimeout           time.Duration
	RelayHeaderTimeout         time.Duration
	RelayTransactionSetTimeout time.Duration
	PeerDiscoveryInterval      time.Duration
	SyncInterval               time.Duration
	Logger                     *log.Logger
}

// An Option modifies a Syncer's configuration.
type Option func(*config)

// WithMaxInboundPeers sets the maximum number of inbound connections. The
// default is 8.
func WithMaxInboundPeers(n int) Option {
	return func(c *config) { c.MaxInboundPeers = n }
}

// WithMaxOutboundPeers sets the maximum number of outbound connections. The
// default is 8.
func WithMaxOutboundPeers(n int) Option {
	return func(c *config) { c.MaxOutboundPeers = n }
}

// WithMaxInflightRPCs sets the maximum number of concurrent RPCs per peer. The
// default is 3.
func WithMaxInflightRPCs(n int) Option {
	return func(c *config) { c.MaxInflightRPCs = n }
}

// WithConnectTimeout sets the timeout when connecting to a peer. The default is
// 5 seconds.
func WithConnectTimeout(d time.Duration) Option {
	return func(c *config) { c.ConnectTimeout = d }
}

// WithShareNodesTimeout sets the timeout for the ShareNodes RPC. The default is
// 5 seconds.
func WithShareNodesTimeout(d time.Duration) Option {
	return func(c *config) { c.ShareNodesTimeout = d }
}

// WithSendBlockTimeout sets the timeout for the SendBlock RPC. The default is
// 60 seconds.
func WithSendBlockTimeout(d time.Duration) Option {
	return func(c *config) { c.SendBlockTimeout = d }
}

// WithRelayHeaderTimeout sets the timeout for the RelayHeader RPC. The default
// is 5 seconds.
func WithRelayHeaderTimeout(d time.Duration) Option {
	return func(c *config) { c.RelayHeaderTimeout = d }
}

// WithRelayTransactionSetTimeout sets the timeout for the RelayTransactionSet
// RPC. The default is 60 seconds.
func WithRelayTransactionSetTimeout(d time.Duration) Option {
	return func(c *config) { c.RelayTransactionSetTimeout = d }
}

// WithPeerDiscoveryInterval sets the frequency at which the syncer attempts to
// discover and connect to new peers. The default is 5 seconds.
func WithPeerDiscoveryInterval(d time.Duration) Option {
	return func(c *config) { c.PeerDiscoveryInterval = d }
}

// WithSyncInterval sets the frequency at which the syncer attempts to sync with
// peers. The default is 5 seconds.
func WithSyncInterval(d time.Duration) Option {
	return func(c *config) { c.SyncInterval = d }
}

// WithLogger sets the logger used by a Syncer. The default is a logger that
// outputs to io.Discard.
func WithLogger(l *log.Logger) Option {
	return func(c *config) { c.Logger = l }
}

// A Syncer synchronizes blockchain data with peers.
type Syncer struct {
	l      net.Listener
	cm     ChainManager
	pm     PeerStore
	header gateway.Header
	config config
	log    *log.Logger // redundant, but convenient

	mu      sync.Mutex
	peers   map[string]*gateway.Peer
	synced  map[string]bool
	strikes map[string]int
}

type rpcHandler struct {
	s        *Syncer
	blockBuf []types.Block
}

func (h *rpcHandler) PeersForShare() (peers []string) {
	peers = h.s.pm.Peers()
	if len(peers) > 10 {
		frand.Shuffle(len(peers), reflect.Swapper(peers))
		peers = peers[:10]
	}
	return peers
}

func (h *rpcHandler) Block(id types.BlockID) (types.Block, error) {
	b, ok := h.s.cm.Block(id)
	if !ok {
		return types.Block{}, errors.New("block not found")
	}
	return b, nil
}

func (h *rpcHandler) BlocksForHistory(history [32]types.BlockID) ([]types.Block, bool, error) {
	blocks, err := h.s.cm.BlocksForHistory(h.blockBuf, history[:])
	return blocks, len(blocks) == len(h.blockBuf), err
}

func (h *rpcHandler) RelayHeader(bh types.BlockHeader, origin *gateway.Peer) {
	if _, ok := h.s.cm.Block(bh.ID()); ok {
		return // already seen
	} else if _, ok := h.s.cm.Block(bh.ParentID); !ok {
		h.s.log.Printf("peer %v relayed a header with unknown parent (%v); triggering a resync", origin, bh.ParentID)
		h.s.mu.Lock()
		h.s.synced[origin.Addr] = false
		h.s.mu.Unlock()
		return
	} else if cs := h.s.cm.TipState(); bh.ParentID != cs.Index.ID {
		// block extends a sidechain, which peer (if honest) believes to be the
		// heaviest chain
		h.s.log.Printf("peer %v relayed a header that does not attach to our tip; triggering a resync", origin)
		h.s.mu.Lock()
		h.s.synced[origin.Addr] = false
		h.s.mu.Unlock()
		return
	} else if err := consensus.ValidateHeader(cs, bh); err != nil {
		h.s.ban(origin, err) // inexcusable
		return
	}

	// header is valid and attaches to our tip; request + validate full block
	if b, err := origin.SendBlock(bh.ID(), h.s.config.SendBlockTimeout); err != nil {
		// log-worthy, but not ban-worthy
		h.s.log.Printf("couldn't retrieve new block %v after header relay from %v: %v", bh.ID(), origin, err)
		return
	} else if err := h.s.cm.AddBlocks([]types.Block{b}); err != nil {
		h.s.ban(origin, err)
		return
	}

	h.s.relayHeader(bh, origin) // non-blocking
}

func (h *rpcHandler) RelayTransactionSet(txns []types.Transaction, origin *gateway.Peer) {
	// if we've already seen these transactions, don't relay them again
	for _, txn := range txns {
		if _, ok := h.s.cm.PoolTransaction(txn.ID()); !ok {
			goto add
		}
	}
	return

add:
	if err := h.s.cm.AddPoolTransactions(txns); err != nil {
		// too risky to ban here (txns are probably just outdated), but at least
		// log it if we think we're synced
		if b, ok := h.s.cm.Block(h.s.cm.Tip().ID); ok && time.Since(b.Timestamp) < 2*h.s.cm.TipState().BlockInterval() {
			h.s.log.Printf("received an invalid transaction set from %v: %v", origin, err)
		}
		return
	}
	h.s.relayTransactionSet(txns, origin) // non-blocking
}

func (s *Syncer) ban(p *gateway.Peer, err error) {
	p.SetErr(errors.New("banned"))
	s.pm.Ban(p.ConnAddr, 24*time.Hour, err.Error())

	host, _, err := net.SplitHostPort(p.ConnAddr)
	if err != nil {
		return // shouldn't happen
	}
	// add a strike to each subnet
	for subnet, maxStrikes := range map[string]int{
		Subnet(host + "/32"): 2,   // 1.2.3.4:*
		Subnet(host + "/24"): 8,   // 1.2.3.*
		Subnet(host + "/16"): 64,  // 1.2.*
		Subnet(host + "/8"):  512, // 1.*
	} {
		s.mu.Lock()
		ban := (s.strikes[subnet] + 1) >= maxStrikes
		if ban {
			delete(s.strikes, subnet)
		} else {
			s.strikes[subnet]++
		}
		s.mu.Unlock()
		if ban {
			s.pm.Ban(subnet, 24*time.Hour, "too many strikes")
		}
	}
}

func (s *Syncer) runPeer(p *gateway.Peer) {
	s.pm.AddPeer(p.Addr)
	s.pm.UpdatePeerInfo(p.Addr, func(info *PeerInfo) {
		info.LastConnect = time.Now()
	})
	s.mu.Lock()
	s.peers[p.Addr] = p
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.peers, p.Addr)
		s.mu.Unlock()
	}()

	h := &rpcHandler{
		s:        s,
		blockBuf: make([]types.Block, 10),
	}
	inflight := make(chan struct{}, s.config.MaxInflightRPCs)
	for {
		if p.Err() != nil {
			return
		}
		id, stream, err := p.AcceptRPC()
		if err != nil {
			p.SetErr(err)
			return
		}
		inflight <- struct{}{}
		go func() {
			defer stream.Close()
			// NOTE: we do not set any deadlines on the stream. If a peer is
			// slow, fine; we don't need to worry about resource exhaustion
			// unless we have tons of peers.
			if err := p.HandleRPC(id, stream, h); err != nil {
				s.log.Printf("incoming RPC %v from peer %v failed: %v", id, p, err)
			}
			<-inflight
		}()
	}
}

func (s *Syncer) relayHeader(h types.BlockHeader, origin *gateway.Peer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.peers {
		if p == origin {
			continue
		}
		go p.RelayHeader(h, s.config.RelayHeaderTimeout)
	}
}

func (s *Syncer) relayTransactionSet(txns []types.Transaction, origin *gateway.Peer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.peers {
		if p == origin {
			continue
		}
		go p.RelayTransactionSet(txns, s.config.RelayTransactionSetTimeout)
	}
}

func (s *Syncer) acceptLoop() error {
	allowConnect := func(peer string) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.pm.Banned(peer) {
			return errors.New("banned")
		}
		var in int
		for _, p := range s.peers {
			if p.Inbound {
				in++
			}
		}
		// TODO: subnet-based limits
		if in >= s.config.MaxInboundPeers {
			return errors.New("too many inbound peers")
		}
		return nil
	}

	for {
		conn, err := s.l.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer conn.Close()
			p, err := func() (*gateway.Peer, error) {
				if err := allowConnect(conn.RemoteAddr().String()); err != nil {
					return nil, err
				}
				return gateway.AcceptPeer(conn, s.header)
			}()
			if err == nil {
				s.log.Printf("accepted inbound connection from %v", conn.RemoteAddr())
				s.runPeer(p)
			} else {
				s.log.Printf("failed to accept inbound connection from %v: %v", conn.RemoteAddr(), err)
			}
		}()
	}
}

func (s *Syncer) peerLoop(closeChan <-chan struct{}) error {
	numOutbound := func() (n int) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, p := range s.peers {
			if !p.Inbound {
				n++
			}
		}
		return
	}
	peersForConnect := func() (peers []string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, p := range s.pm.Peers() {
			// TODO: don't include port in comparison
			if _, ok := s.peers[p]; !ok {
				peers = append(peers, p)
			}
		}
		// TODO: weighted random selection?
		frand.Shuffle(len(peers), reflect.Swapper(peers))
		return peers
	}
	discoverPeers := func() {
		// try up to three randomly-chosen peers
		var peers []*gateway.Peer
		s.mu.Lock()
		for _, p := range s.peers {
			peers = append(peers, p)
			if len(peers) >= 3 {
				break
			}
		}
		s.mu.Unlock()
		for _, p := range peers {
			nodes, err := p.ShareNodes(s.config.ShareNodesTimeout)
			if err != nil {
				continue
			}
			for _, n := range nodes {
				s.pm.AddPeer(n)
			}
		}
	}

	ticker := time.NewTicker(s.config.PeerDiscoveryInterval)
	defer ticker.Stop()
	sleep := func() bool {
		select {
		case <-ticker.C:
			return true
		case <-closeChan:
			return false
		}
	}
	for fst := true; fst || sleep(); fst = false {
		if numOutbound() >= s.config.MaxOutboundPeers {
			continue
		}
		candidates := peersForConnect()
		if len(candidates) == 0 {
			discoverPeers()
			continue
		}
		for _, p := range candidates {
			if numOutbound() >= s.config.MaxOutboundPeers {
				break
			}
			if _, err := s.Connect(p); err == nil {
				s.log.Printf("formed outbound connection to %v", p)
			} else {
				s.log.Printf("failed to form outbound connection to %v: %v", p, err)
			}
		}
	}
	return nil
}

func (s *Syncer) syncLoop(closeChan <-chan struct{}) error {
	peersForSync := func() (peers []*gateway.Peer) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, p := range s.peers {
			if s.synced[p.Addr] {
				continue
			}
			peers = append(peers, p)
			if len(peers) >= 3 {
				break
			}
		}
		return
	}

	ticker := time.NewTicker(s.config.SyncInterval)
	defer ticker.Stop()
	sleep := func() bool {
		select {
		case <-ticker.C:
			return true
		case <-closeChan:
			return false
		}
	}
	for fst := true; fst || sleep(); fst = false {
		for _, p := range peersForSync() {
			history, err := s.cm.History()
			if err != nil {
				return err // generally fatal
			}
			s.mu.Lock()
			s.synced[p.Addr] = true
			s.mu.Unlock()
			s.log.Printf("starting sync with %v", p)
			oldTip := s.cm.Tip()
			oldTime := time.Now()
			lastPrint := time.Now()
			startTime, startHeight := oldTime, oldTip.Height
			err = p.SendBlocks(history, func(blocks []types.Block) error {
				if err := s.cm.AddBlocks(blocks); err != nil {
					return err
				}
				endTime, endHeight := time.Now(), s.cm.Tip().Height
				s.pm.UpdatePeerInfo(p.Addr, func(info *PeerInfo) {
					info.SyncedBlocks += endHeight - startHeight
					info.SyncDuration += endTime.Sub(startTime)
				})
				startTime, startHeight = endTime, endHeight
				if time.Since(lastPrint) > 30*time.Second {
					s.log.Printf("syncing with %v, tip now %v (avg %.2f blocks/s)", p, s.cm.Tip(), float64(s.cm.Tip().Height-oldTip.Height)/endTime.Sub(oldTime).Seconds())
					lastPrint = time.Now()
				}
				return nil
			})
			totalBlocks := s.cm.Tip().Height - oldTip.Height
			if err != nil {
				s.log.Printf("sync with %v failed after %v blocks: %v", p, totalBlocks, err)
				continue
			}

			// if we extended our best chain, rebroadcast our new tip
			if newTip := s.cm.Tip(); newTip != oldTip {
				s.log.Printf("finished syncing %v blocks with %v, tip now %v", p, totalBlocks, newTip)
				if b, ok := s.cm.Block(newTip.ID); ok {
					s.relayHeader(b.Header(), p) // non-blocking
				}
			} else {
				s.log.Printf("finished syncing with %v, tip unchanged", p)
			}
		}
	}
	return nil
}

// Run spawns goroutines for accepting inbound connections, forming outbound
// connections, and syncing the blockchain from active peers. It blocks until an
// error occurs, upon which all connections are closed and goroutines are
// terminated. To gracefully shutdown a Syncer, close its net.Listener.
func (s *Syncer) Run() error {
	errChan := make(chan error)
	closeChan := make(chan struct{})
	go func() { errChan <- s.acceptLoop() }()
	go func() { errChan <- s.peerLoop(closeChan) }()
	go func() { errChan <- s.syncLoop(closeChan) }()
	err := <-errChan

	// when one goroutine exits, shutdown and wait for the others
	close(closeChan)
	s.l.Close()
	s.mu.Lock()
	for addr, p := range s.peers {
		p.Close()
		delete(s.peers, addr)
	}
	s.mu.Unlock()
	<-errChan
	<-errChan
	if errors.Is(err, net.ErrClosed) {
		return nil // graceful shutdown
	}
	return err
}

// Connect forms an outbound connection to a peer.
func (s *Syncer) Connect(addr string) (*gateway.Peer, error) {
	allowConnect := func(peer string) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.pm.Banned(peer) {
			return errors.New("banned")
		}
		var out int
		for _, p := range s.peers {
			if !p.Inbound {
				out++
			}
		}
		// TODO: subnet-based limits
		if out >= s.config.MaxOutboundPeers {
			return errors.New("too many outbound peers")
		}
		return nil
	}

	if err := allowConnect(addr); err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("tcp", addr, s.config.ConnectTimeout)
	if err != nil {
		return nil, err
	}
	p, err := gateway.DialPeer(conn, s.header)
	if err != nil {
		return nil, err
	}
	go s.runPeer(p)

	// runPeer does this too, but doing it outside the goroutine prevents a race
	s.mu.Lock()
	s.peers[p.Addr] = p
	s.mu.Unlock()
	return p, nil
}

// BroadcastHeader broadcasts a header to all peers.
func (s *Syncer) BroadcastHeader(h types.BlockHeader) { s.relayHeader(h, nil) }

// BroadcastTransactionSet broadcasts a transaction set to all peers.
func (s *Syncer) BroadcastTransactionSet(txns []types.Transaction) { s.relayTransactionSet(txns, nil) }

// Peers returns the set of currently-connected peers.
func (s *Syncer) Peers() []*gateway.Peer {
	s.mu.Lock()
	defer s.mu.Unlock()
	var peers []*gateway.Peer
	for _, p := range s.peers {
		peers = append(peers, p)
	}
	return peers
}

// PeerInfo returns metadata about the specified peer.
func (s *Syncer) PeerInfo(peer string) (PeerInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, ok := s.pm.PeerInfo(peer)
	return info, ok
}

// Addr returns the address of the Syncer.
func (s *Syncer) Addr() string {
	return s.l.Addr().String()
}

// New returns a new Syncer.
func New(l net.Listener, cm ChainManager, pm PeerStore, header gateway.Header, opts ...Option) *Syncer {
	config := config{
		MaxInboundPeers:            8,
		MaxOutboundPeers:           8,
		MaxInflightRPCs:            3,
		ConnectTimeout:             5 * time.Second,
		ShareNodesTimeout:          5 * time.Second,
		SendBlockTimeout:           60 * time.Second,
		RelayHeaderTimeout:         5 * time.Second,
		RelayTransactionSetTimeout: 60 * time.Second,
		PeerDiscoveryInterval:      5 * time.Second,
		SyncInterval:               5 * time.Second,
		Logger:                     log.New(io.Discard, "", 0),
	}
	for _, opt := range opts {
		opt(&config)
	}
	return &Syncer{
		l:      l,
		cm:     cm,
		pm:     pm,
		header: header,
		config: config,
		log:    config.Logger,
		peers:  make(map[string]*gateway.Peer),
		synced: make(map[string]bool),
	}
}
