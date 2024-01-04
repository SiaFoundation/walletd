package syncer

import (
	"context"
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
	BlocksForHistory(history []types.BlockID, max uint64) ([]types.Block, uint64, error)
	Block(id types.BlockID) (types.Block, bool)
	SyncCheckpoint(index types.ChainIndex) (types.Block, consensus.State, bool)
	AddBlocks(blocks []types.Block) error
	Tip() types.ChainIndex
	TipState() consensus.State

	PoolTransaction(txid types.TransactionID) (types.Transaction, bool)
	AddPoolTransactions(txns []types.Transaction) error
	V2PoolTransaction(txid types.TransactionID) (types.V2Transaction, bool)
	AddV2PoolTransactions(basis types.ChainIndex, txns []types.V2Transaction) error
	TransactionsForPartialBlock(missing []types.Hash256) ([]types.Transaction, []types.V2Transaction)
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
func Subnet(addr, mask string) string {
	ip, ipnet, err := net.ParseCIDR(addr + mask)
	if err != nil {
		return "" // shouldn't happen
	}
	return ip.Mask(ipnet.Mask).String() + mask
}

type config struct {
	MaxInboundPeers            int
	MaxOutboundPeers           int
	MaxInflightRPCs            int
	ConnectTimeout             time.Duration
	ShareNodesTimeout          time.Duration
	SendBlockTimeout           time.Duration
	SendTransactionsTimeout    time.Duration
	RelayHeaderTimeout         time.Duration
	RelayBlockOutlineTimeout   time.Duration
	RelayTransactionSetTimeout time.Duration
	SendBlocksTimeout          time.Duration
	MaxSendBlocks              uint64
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

// WithSendBlocksTimeout sets the timeout for the SendBlocks RPC. The default is
// 120 seconds.
func WithSendBlocksTimeout(d time.Duration) Option {
	return func(c *config) { c.SendBlocksTimeout = d }
}

// WithMaxSendBlocks sets the maximum number of blocks requested per SendBlocks
// RPC. The default is 10.
func WithMaxSendBlocks(n uint64) Option {
	return func(c *config) { c.MaxSendBlocks = n }
}

// WithSendTransactionsTimeout sets the timeout for the SendTransactions RPC.
// The default is 60 seconds.
func WithSendTransactionsTimeout(d time.Duration) Option {
	return func(c *config) { c.SendTransactionsTimeout = d }
}

// WithRelayHeaderTimeout sets the timeout for the RelayHeader and RelayV2Header
// RPCs. The default is 5 seconds.
func WithRelayHeaderTimeout(d time.Duration) Option {
	return func(c *config) { c.RelayHeaderTimeout = d }
}

// WithRelayBlockOutlineTimeout sets the timeout for the RelayV2BlockOutline
// RPC. The default is 60 seconds.
func WithRelayBlockOutlineTimeout(d time.Duration) Option {
	return func(c *config) { c.RelayBlockOutlineTimeout = d }
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
	s *Syncer
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

func (h *rpcHandler) BlocksForHistory(history []types.BlockID, max uint64) ([]types.Block, uint64, error) {
	return h.s.cm.BlocksForHistory(history, max)
}

func (h *rpcHandler) Transactions(index types.ChainIndex, txnHashes []types.Hash256) (txns []types.Transaction, v2txns []types.V2Transaction, _ error) {
	if b, ok := h.s.cm.Block(index.ID); ok {
		// get txns from block
		want := make(map[types.Hash256]bool)
		for _, h := range txnHashes {
			want[h] = true
		}
		for _, txn := range b.Transactions {
			if want[txn.FullHash()] {
				txns = append(txns, txn)
			}
		}
		for _, txn := range b.V2Transactions() {
			if want[txn.FullHash()] {
				v2txns = append(v2txns, txn)
			}
		}
		return
	}
	txns, v2txns = h.s.cm.TransactionsForPartialBlock(txnHashes)
	return
}

func (h *rpcHandler) Checkpoint(index types.ChainIndex) (types.Block, consensus.State, error) {
	b, cs, ok := h.s.cm.SyncCheckpoint(index)
	if !ok {
		return types.Block{}, consensus.State{}, errors.New("checkpoint not found")
	}
	return b, cs, nil
}

func (h *rpcHandler) RelayHeader(bh gateway.BlockHeader, origin *gateway.Peer) {
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
	} else if bh.ID().CmpWork(cs.ChildTarget) < 0 {
		h.s.ban(origin, errors.New("peer sent header with insufficient work"))
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

func (h *rpcHandler) RelayV2Header(bh gateway.V2BlockHeader, origin *gateway.Peer) {
	if _, ok := h.s.cm.Block(bh.Parent.ID); !ok {
		h.s.log.Printf("peer %v relayed a v2 header with unknown parent (%v); triggering a resync", origin, bh.Parent.ID)
		h.s.mu.Lock()
		h.s.synced[origin.Addr] = false
		h.s.mu.Unlock()
		return
	}
	cs := h.s.cm.TipState()
	bid := bh.ID(cs)
	if _, ok := h.s.cm.Block(bid); ok {
		// already seen
		return
	} else if bh.Parent.ID != cs.Index.ID {
		// block extends a sidechain, which peer (if honest) believes to be the
		// heaviest chain
		h.s.log.Printf("peer %v relayed a header that does not attach to our tip; triggering a resync", origin)
		h.s.mu.Lock()
		h.s.synced[origin.Addr] = false
		h.s.mu.Unlock()
		return
	} else if bid.CmpWork(cs.ChildTarget) < 0 {
		h.s.ban(origin, errors.New("peer sent v2 header with insufficient work"))
		return
	}

	// header is sufficiently valid; relay it
	//
	// NOTE: The purpose of header announcements is to inform the network as
	// quickly as possible that a new block has been found. A proper
	// BlockOutline should follow soon after, allowing peers to obtain the
	// actual block. As such, we take no action here other than relaying.
	h.s.relayV2Header(bh, origin) // non-blocking
}

func (h *rpcHandler) RelayV2BlockOutline(bo gateway.V2BlockOutline, origin *gateway.Peer) {
	if _, ok := h.s.cm.Block(bo.ParentID); !ok {
		h.s.log.Printf("peer %v relayed a v2 outline with unknown parent (%v); triggering a resync", origin, bo.ParentID)
		h.s.mu.Lock()
		h.s.synced[origin.Addr] = false
		h.s.mu.Unlock()
		return
	}
	cs := h.s.cm.TipState()
	bid := bo.ID(cs)
	if _, ok := h.s.cm.Block(bid); ok {
		// already seen
		return
	} else if bo.ParentID != cs.Index.ID {
		// block extends a sidechain, which peer (if honest) believes to be the
		// heaviest chain
		h.s.log.Printf("peer %v relayed a v2 outline that does not attach to our tip; triggering a resync", origin)
		h.s.mu.Lock()
		h.s.synced[origin.Addr] = false
		h.s.mu.Unlock()
		return
	} else if bid.CmpWork(cs.ChildTarget) < 0 {
		h.s.ban(origin, errors.New("peer sent v2 outline with insufficient work"))
		return
	}

	// block has sufficient work and attaches to our tip, but may be missing
	// transactions; first, check for them in our txpool; then, if block is
	// still incomplete, request remaining transactions from the peer
	txns, v2txns := h.s.cm.TransactionsForPartialBlock(bo.Missing())
	b, missing := bo.Complete(cs, txns, v2txns)
	if len(missing) > 0 {
		index := types.ChainIndex{ID: bid, Height: cs.Index.Height + 1}
		txns, v2txns, err := origin.SendTransactions(index, missing, h.s.config.SendTransactionsTimeout)
		if err != nil {
			// log-worthy, but not ban-worthy
			h.s.log.Printf("couldn't retrieve missing transactions of %v after relay from %v: %v", bid, origin, err)
			return
		}
		b, missing = bo.Complete(cs, txns, v2txns)
		if len(missing) > 0 {
			// inexcusable
			h.s.ban(origin, errors.New("peer sent wrong missing transactions for a block it relayed"))
			return
		}
	}
	if err := h.s.cm.AddBlocks([]types.Block{b}); err != nil {
		h.s.ban(origin, err)
		return
	}

	// when we forward the block, exclude any txns that were in our txpool,
	// since they're probably present in our peers' txpools as well
	//
	// NOTE: crucially, we do NOT exclude any txns we had to request from the
	// sending peer, since other peers probably don't have them either
	bo.RemoveTransactions(txns, v2txns)

	h.s.relayV2BlockOutline(bo, origin) // non-blocking
}

func (h *rpcHandler) RelayV2TransactionSet(basis types.ChainIndex, txns []types.V2Transaction, origin *gateway.Peer) {
	// if we've already seen these transactions, don't relay them again
	allSeen := true
	for _, txn := range txns {
		if _, allSeen = h.s.cm.V2PoolTransaction(txn.ID()); !allSeen {
			break
		}
	}
	if allSeen {
		return
	}
	if _, ok := h.s.cm.Block(basis.ID); !ok {
		h.s.log.Printf("peer %v relayed a v2 transaction set with unknown basis (%v); triggering a resync", origin, basis)
		h.s.mu.Lock()
		h.s.synced[origin.Addr] = false
		h.s.mu.Unlock()
		return
	}
	if err := h.s.cm.AddV2PoolTransactions(basis, txns); err != nil {
		h.s.log.Printf("received an invalid transaction set from %v: %v", origin, err)
		return
	}
	h.s.relayV2TransactionSet(basis, txns, origin) // non-blocking
}

func (s *Syncer) ban(p *gateway.Peer, err error) {
	s.log.Printf("banning %v: %v", p, err)
	p.SetErr(errors.New("banned"))
	s.pm.Ban(p.ConnAddr, 24*time.Hour, err.Error())

	host, _, err := net.SplitHostPort(p.ConnAddr)
	if err != nil {
		return // shouldn't happen
	}
	// add a strike to each subnet
	for subnet, maxStrikes := range map[string]int{
		Subnet(host, "/32"): 2,   // 1.2.3.4:*
		Subnet(host, "/24"): 8,   // 1.2.3.*
		Subnet(host, "/16"): 64,  // 1.2.*
		Subnet(host, "/8"):  512, // 1.*
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

	h := &rpcHandler{s: s}
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

func (s *Syncer) relayHeader(h gateway.BlockHeader, origin *gateway.Peer) {
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

func (s *Syncer) relayV2Header(bh gateway.V2BlockHeader, origin *gateway.Peer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.peers {
		if p == origin || !p.SupportsV2() {
			continue
		}
		go p.RelayV2Header(bh, s.config.RelayHeaderTimeout)
	}
}

func (s *Syncer) relayV2BlockOutline(pb gateway.V2BlockOutline, origin *gateway.Peer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.peers {
		if p == origin || !p.SupportsV2() {
			continue
		}
		go p.RelayV2BlockOutline(pb, s.config.RelayBlockOutlineTimeout)
	}
}

func (s *Syncer) relayV2TransactionSet(index types.ChainIndex, txns []types.V2Transaction, origin *gateway.Peer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.peers {
		if p == origin || !p.SupportsV2() {
			continue
		}
		go p.RelayV2TransactionSet(index, txns, s.config.RelayTransactionSetTimeout)
	}
}

func (s *Syncer) allowConnect(peer string, inbound bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.l == nil {
		return errors.New("syncer is shutting down")
	}
	if s.pm.Banned(peer) {
		return errors.New("banned")
	}
	var in, out int
	for _, p := range s.peers {
		if p.Inbound {
			in++
		} else {
			out++
		}
	}
	// TODO: subnet-based limits
	if inbound && in >= s.config.MaxInboundPeers {
		return errors.New("too many inbound peers")
	} else if !inbound && out >= s.config.MaxOutboundPeers {
		return errors.New("too many outbound peers")
	}
	return nil
}

func (s *Syncer) alreadyConnected(peer *gateway.Peer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.peers {
		if p.UniqueID == peer.UniqueID {
			return true
		}
	}
	return false
}

func (s *Syncer) acceptLoop() error {
	for {
		conn, err := s.l.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer conn.Close()
			if err := s.allowConnect(conn.RemoteAddr().String(), true); err != nil {
				s.log.Printf("rejected inbound connection from %v: %v", conn.RemoteAddr(), err)
			} else if p, err := gateway.Accept(conn, s.header); err != nil {
				s.log.Printf("failed to accept inbound connection from %v: %v", conn.RemoteAddr(), err)
			} else if s.alreadyConnected(p) {
				s.log.Printf("rejected inbound connection from %v: already connected", conn.RemoteAddr())
			} else {
				s.runPeer(p)
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

	lastTried := make(map[string]time.Time)
	peersForConnect := func() (peers []string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, p := range s.pm.Peers() {
			// TODO: don't include port in comparison
			if _, ok := s.peers[p]; !ok && time.Since(lastTried[p]) > 5*time.Minute {
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
			if peers = append(peers, p); len(peers) >= 3 {
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
	closing := func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.l == nil
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
			if numOutbound() >= s.config.MaxOutboundPeers || closing() {
				break
			}
			if _, err := s.Connect(p); err == nil {
				s.log.Printf("formed outbound connection to %v", p)
			} else {
				s.log.Printf("failed to form outbound connection to %v: %v", p, err)
			}
			lastTried[p] = time.Now()
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
			if peers = append(peers, p); len(peers) >= 3 {
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
			var sentBlocks uint64
			addBlocks := func(blocks []types.Block) error {
				if err := s.cm.AddBlocks(blocks); err != nil {
					return err
				}
				sentBlocks += uint64(len(blocks))
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
			}
			if p.SupportsV2() {
				history := history[:]
				err = func() error {
					for {
						blocks, rem, err := p.SendV2Blocks(history, s.config.MaxSendBlocks, s.config.SendBlocksTimeout)
						if err != nil {
							return err
						} else if err := addBlocks(blocks); err != nil {
							return err
						} else if rem == 0 {
							return nil
						}
						history = []types.BlockID{blocks[len(blocks)-1].ID()}
					}
				}()
			} else {
				err = p.SendBlocks(history, s.config.SendBlocksTimeout, addBlocks)
			}
			totalBlocks := s.cm.Tip().Height - oldTip.Height
			if err != nil {
				s.log.Printf("syncing with %v failed after %v blocks: %v", p, totalBlocks, err)
			} else if newTip := s.cm.Tip(); newTip != oldTip {
				s.log.Printf("finished syncing %v blocks with %v, tip now %v", totalBlocks, p, newTip)
			} else {
				s.log.Printf("finished syncing %v blocks with %v, tip unchanged", sentBlocks, p)
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
	s.l = nil
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
	if err := s.allowConnect(addr, false); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.config.ConnectTimeout)
	defer cancel()
	// slightly gross polling hack so that we shutdown quickly
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
				s.mu.Lock()
				if s.l == nil {
					cancel()
				}
				s.mu.Unlock()
			}
		}
	}()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Now().Add(s.config.ConnectTimeout))
	defer conn.SetDeadline(time.Time{})
	p, err := gateway.Dial(conn, s.header)
	if err != nil {
		conn.Close()
		return nil, err
	} else if s.alreadyConnected(p) {
		conn.Close()
		return nil, errors.New("already connected")
	}
	go s.runPeer(p)

	// runPeer does this too, but doing it outside the goroutine prevents a race
	s.mu.Lock()
	s.peers[p.Addr] = p
	s.mu.Unlock()
	return p, nil
}

// BroadcastHeader broadcasts a header to all peers.
func (s *Syncer) BroadcastHeader(h gateway.BlockHeader) { s.relayHeader(h, nil) }

// BroadcastV2Header broadcasts a v2 header to all peers.
func (s *Syncer) BroadcastV2Header(h gateway.V2BlockHeader) { s.relayV2Header(h, nil) }

// BroadcastV2BlockOutline broadcasts a v2 block outline to all peers.
func (s *Syncer) BroadcastV2BlockOutline(b gateway.V2BlockOutline) { s.relayV2BlockOutline(b, nil) }

// BroadcastTransactionSet broadcasts a transaction set to all peers.
func (s *Syncer) BroadcastTransactionSet(txns []types.Transaction) { s.relayTransactionSet(txns, nil) }

// BroadcastV2TransactionSet broadcasts a v2 transaction set to all peers.
func (s *Syncer) BroadcastV2TransactionSet(index types.ChainIndex, txns []types.V2Transaction) {
	s.relayV2TransactionSet(index, txns, nil)
}

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
		SendTransactionsTimeout:    60 * time.Second,
		RelayHeaderTimeout:         5 * time.Second,
		RelayBlockOutlineTimeout:   60 * time.Second,
		RelayTransactionSetTimeout: 60 * time.Second,
		SendBlocksTimeout:          120 * time.Second,
		MaxSendBlocks:              10,
		PeerDiscoveryInterval:      5 * time.Second,
		SyncInterval:               5 * time.Second,
		Logger:                     log.New(io.Discard, "", 0),
	}
	for _, opt := range opts {
		opt(&config)
	}
	return &Syncer{
		l:       l,
		cm:      cm,
		pm:      pm,
		header:  header,
		config:  config,
		log:     config.Logger,
		peers:   make(map[string]*gateway.Peer),
		synced:  make(map[string]bool),
		strikes: make(map[string]int),
	}
}
