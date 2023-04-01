package syncer

import (
	"errors"
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
	Block(id types.BlockID) (types.Block, error)
	AddBlocks(blocks []types.Block) error
	Tip() types.ChainIndex
	TipState() consensus.State
}

// A TransactionPool manages uncommitted transactions.
type TransactionPool interface {
	AddTransactionSet(txns []types.Transaction) error
}

// A PeerManager manages a set of known peers.
type PeerManager interface {
	Peers() []string
	AddPeer(peer string)
	Banned(peer string) bool
	// Ban temporarily bans one or more IPs. The addr should either be a single
	// IP with port (e.g. 1.2.3.4:5678) or a CIDR subnet (e.g. 1.2.3.4/16).
	Ban(addr string, duration time.Duration)
}

// A Logger logs events related to a Syncer.
type Logger interface {
	LogFailedConnect(peer string, inbound bool, err error)
	LogFailedRPC(p *gateway.Peer, rpc string, err error)
	LogBannedPeer(p *gateway.Peer, err error)
	LogDiscoveredNodes(p *gateway.Peer, nodes []string)
	LogSyncStart(p *gateway.Peer)
	LogSyncProgress(p *gateway.Peer, numBlocks int, tip types.ChainIndex)
	LogSyncFinish(p *gateway.Peer)
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
	Logger                     Logger
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

// WithLogger sets the logger used by a Syncer. The default is a DiscardLogger.
func WithLogger(l Logger) Option {
	return func(c *config) { c.Logger = l }
}

// A Syncer synchronizes blockchain data with peers.
type Syncer struct {
	l      net.Listener
	cm     ChainManager
	tp     TransactionPool
	pm     PeerManager
	log    Logger // redundant, but convenient
	header gateway.Header
	config config

	mu     sync.Mutex
	peers  map[string]*gateway.Peer
	synced map[string]bool
}

type rpcHandler struct {
	s        *Syncer
	blockBuf []types.Block
}

func (h *rpcHandler) PeersForShare() (peers []string) {
	peers = h.s.pm.Peers()
	frand.Shuffle(len(peers), reflect.Swapper(peers))
	if len(peers) > 10 {
		peers = peers[:10]
	}
	return peers
}

func (h *rpcHandler) Block(id types.BlockID) (types.Block, error) {
	return h.s.cm.Block(id)
}

func (h *rpcHandler) BlocksForHistory(history [32]types.BlockID) ([]types.Block, bool, error) {
	blocks, err := h.s.cm.BlocksForHistory(h.blockBuf, history[:])
	return blocks, len(blocks) == len(h.blockBuf), err
}

func (h *rpcHandler) RelayHeader(bh types.BlockHeader, origin *gateway.Peer) {
	if _, err := h.s.cm.Block(bh.ID()); err == nil {
		return // already seen
	} else if err := consensus.ValidateHeader(h.s.cm.TipState(), bh); err != nil {
		h.s.ban(origin, err)
		return
	} else if _, err := h.s.cm.Block(bh.ParentID); err != nil {
		// trigger a resync
		h.s.mu.Lock()
		h.s.synced[origin.Addr] = false
		h.s.mu.Unlock()
		return
	}

	// request + validate full block
	if b, err := origin.SendBlock(bh.ID(), h.s.config.SendBlockTimeout); err != nil {
		h.s.ban(origin, err)
		return
	} else if err := h.s.cm.AddBlocks([]types.Block{b}); err != nil {
		h.s.ban(origin, err)
		return
	}

	h.s.relayHeader(bh, origin) // non-blocking
}

func (h *rpcHandler) RelayTransactionSet(txns []types.Transaction, origin *gateway.Peer) {
	if err := h.s.tp.AddTransactionSet(txns); err != nil {
		h.s.ban(origin, err)
		return
	}
	h.s.relayTransactionSet(txns, origin) // non-blocking
}

func (s *Syncer) ban(p *gateway.Peer, err error) {
	p.SetErr(errors.New("banned"))
	s.pm.Ban(p.Addr, 24*time.Hour)
	s.log.LogBannedPeer(p, err)
}

func (s *Syncer) runPeer(p *gateway.Peer) {
	s.pm.AddPeer(p.Addr)
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
			defer s.Close()
			if err := p.HandleRPC(id, stream, h); err != nil {
				s.ban(p, err)
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
			if err := allowConnect(conn.RemoteAddr().String()); err != nil {
				s.log.LogFailedConnect(conn.RemoteAddr().String(), true, err)
				return
			}
			p, err := gateway.AcceptPeer(conn, s.header)
			if err != nil {
				s.log.LogFailedConnect(conn.RemoteAddr().String(), true, err)
				return
			}
			s.runPeer(p)
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
		// TODO: weighted random selection?
		peers = s.pm.Peers()
		frand.Shuffle(len(peers), reflect.Swapper(peers))
		if len(peers) > 8 {
			peers = peers[:8]
		}
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
				s.log.LogFailedRPC(p, "ShareNodes", err)
				continue // TODO: disconnect?
			}
			s.log.LogDiscoveredNodes(p, nodes)
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
		if numOutbound() >= 8 {
			continue
		}
		candidates := peersForConnect()
		if len(candidates) == 0 {
			discoverPeers()
			continue
		}
		for _, p := range candidates {
			if _, err := s.Connect(p); err != nil {
				s.log.LogFailedConnect(p, false, err)
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
			s.log.LogSyncStart(p)
			oldTip := s.cm.Tip()
			if err := p.SendBlocks(history, func(blocks []types.Block) error {
				if err := s.cm.AddBlocks(blocks); err != nil {
					return err
				}
				s.log.LogSyncProgress(p, len(blocks), s.cm.Tip())
				return nil
			}); err != nil {
				s.log.LogFailedRPC(p, "SendBlocks", err)
				continue
			}
			s.log.LogSyncFinish(p)

			// if we extended our best chain, rebroadcast our new tip
			if newTip := s.cm.Tip(); newTip != oldTip {
				b, err := s.cm.Block(newTip.ID)
				if err != nil {
					continue // shouldn't happen
				}
				s.relayHeader(b.Header(), nil) // non-blocking
			}
		}
	}
	return nil
}

// Run spawns goroutines for accepting inbound connections, forming outbound
// connections, and syncing the blockchain from active peers.
func (s *Syncer) Run() error {
	errChan := make(chan error, 2)
	closeChan := make(chan struct{})
	go func() { errChan <- s.acceptLoop() }()
	go func() { errChan <- s.peerLoop(closeChan) }()
	go func() { errChan <- s.syncLoop(closeChan) }()

	err := <-errChan
	close(closeChan)
	s.Close()
	<-errChan // wait for other goroutines to exit
	<-errChan
	if errors.Is(err, net.ErrClosed) {
		return nil // graceful shutdown via s.Close
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

// Addr returns the address of the Syncer.
func (s *Syncer) Addr() string {
	return s.l.Addr().String()
}

// Close closes the Syncer.
func (s *Syncer) Close() error {
	return s.l.Close()
}

// New returns a new Syncer.
func New(l net.Listener, cm ChainManager, tp TransactionPool, pm PeerManager, header gateway.Header, opts ...Option) *Syncer {
	config := config{
		MaxInboundPeers:            8,
		MaxOutboundPeers:           16,
		MaxInflightRPCs:            3,
		ConnectTimeout:             5 * time.Second,
		ShareNodesTimeout:          5 * time.Second,
		SendBlockTimeout:           60 * time.Second,
		RelayHeaderTimeout:         5 * time.Second,
		RelayTransactionSetTimeout: 60 * time.Second,
		PeerDiscoveryInterval:      5 * time.Second,
		SyncInterval:               5 * time.Second,
		Logger:                     DiscardLogger{},
	}
	for _, opt := range opts {
		opt(&config)
	}
	return &Syncer{
		l:      l,
		cm:     cm,
		tp:     tp,
		pm:     pm,
		log:    config.Logger,
		header: header,
		config: config,
		peers:  make(map[string]*gateway.Peer),
		synced: make(map[string]bool),
	}
}
