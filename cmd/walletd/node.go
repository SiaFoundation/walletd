package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/syncer"
	"go.sia.tech/walletd/persist/sqlite"
	"go.sia.tech/walletd/wallet"
	"go.uber.org/zap"
	"lukechampine.com/upnp"
)

var mainnetBootstrap = []string{
	"108.227.62.195:9981",
	"139.162.81.190:9991",
	"144.217.7.188:9981",
	"147.182.196.252:9981",
	"15.235.85.30:9981",
	"167.235.234.84:9981",
	"173.235.144.230:9981",
	"198.98.53.144:7791",
	"199.27.255.169:9981",
	"2.136.192.200:9981",
	"213.159.50.43:9981",
	"24.253.116.61:9981",
	"46.249.226.103:9981",
	"5.165.236.113:9981",
	"5.252.226.131:9981",
	"54.38.120.222:9981",
	"62.210.136.25:9981",
	"63.135.62.123:9981",
	"65.21.93.245:9981",
	"75.165.149.114:9981",
	"77.51.200.125:9981",
	"81.6.58.121:9981",
	"83.194.193.156:9981",
	"84.39.246.63:9981",
	"87.99.166.34:9981",
	"91.214.242.11:9981",
	"93.105.88.181:9981",
	"93.180.191.86:9981",
	"94.130.220.162:9981",
}

var zenBootstrap = []string{
	"147.135.16.182:9881",
	"147.135.39.109:9881",
	"51.81.208.10:9881",
}

var anagamiBootstrap = []string{
	"147.135.16.182:9781",
	"98.180.237.163:9981",
	"98.180.237.163:11981",
	"98.180.237.163:10981",
	"94.130.139.59:9801",
	"84.86.11.238:9801",
	"69.131.14.86:9981",
	"68.108.89.92:9981",
	"62.30.63.93:9981",
	"46.173.150.154:9111",
	"195.252.198.117:9981",
	"174.174.206.214:9981",
	"172.58.232.54:9981",
	"172.58.229.31:9981",
	"172.56.200.90:9981",
	"172.56.162.155:9981",
	"163.172.13.180:9981",
	"154.47.25.194:9981",
	"138.201.19.49:9981",
	"100.34.20.44:9981",
}

type node struct {
	chainStore *coreutils.BoltChainDB
	cm         *chain.Manager

	store *sqlite.Store
	s     *syncer.Syncer
	wm    *wallet.Manager

	Start func() (stop func())
}

// Close shuts down the node and closes its database.
func (n *node) Close() error {
	n.chainStore.Close()
	return n.store.Close()
}

func newNode(addr, dir string, chainNetwork string, useUPNP, useBootstrap bool, log *zap.Logger) (*node, error) {
	var network *consensus.Network
	var genesisBlock types.Block
	var bootstrapPeers []string
	switch chainNetwork {
	case "mainnet":
		network, genesisBlock = chain.Mainnet()
		bootstrapPeers = mainnetBootstrap
	case "zen":
		network, genesisBlock = chain.TestnetZen()
		bootstrapPeers = zenBootstrap
	case "anagami":
		network, genesisBlock = TestnetAnagami()
		bootstrapPeers = anagamiBootstrap
	default:
		return nil, errors.New("invalid network: must be one of 'mainnet', 'zen', or 'anagami'")
	}

	bdb, err := coreutils.OpenBoltChainDB(filepath.Join(dir, "consensus.db"))
	if err != nil {
		return nil, fmt.Errorf("failed to open consensus database: %w", err)
	}
	dbstore, tipState, err := chain.NewDBStore(bdb, network, genesisBlock)
	if err != nil {
		return nil, fmt.Errorf("failed to create chain store: %w", err)
	}
	cm := chain.NewManager(dbstore, tipState)

	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	syncerAddr := l.Addr().String()
	if useUPNP {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if d, err := upnp.Discover(ctx); err != nil {
			log.Debug("couldn't discover UPnP router", zap.Error(err))
		} else {
			_, portStr, _ := net.SplitHostPort(addr)
			port, _ := strconv.Atoi(portStr)
			if !d.IsForwarded(uint16(port), "TCP") {
				if err := d.Forward(uint16(port), "TCP", "walletd"); err != nil {
					log.Debug("couldn't forward port", zap.Error(err))
				} else {
					log.Debug("upnp: forwarded p2p port", zap.Int("port", port))
				}
			}
			if ip, err := d.ExternalIP(); err != nil {
				log.Debug("couldn't determine external IP", zap.Error(err))
			} else {
				log.Debug("external IP is", zap.String("ip", ip))
				syncerAddr = net.JoinHostPort(ip, portStr)
			}
		}
	}
	// peers will reject us if our hostname is empty or unspecified, so use loopback
	host, port, _ := net.SplitHostPort(syncerAddr)
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		syncerAddr = net.JoinHostPort("127.0.0.1", port)
	}

	store, err := sqlite.OpenDatabase(filepath.Join(dir, "walletd.sqlite3"), log.Named("sqlite3"))
	if err != nil {
		return nil, fmt.Errorf("failed to open wallet database: %w", err)
	}

	if useBootstrap {
		for _, peer := range bootstrapPeers {
			if err := store.AddPeer(peer); err != nil {
				return nil, fmt.Errorf("failed to add bootstrap peer '%s': %w", peer, err)
			}
		}
	}

	ps, err := sqlite.NewPeerStore(store)
	if err != nil {
		return nil, fmt.Errorf("failed to create peer store: %w", err)
	}

	header := gateway.Header{
		GenesisID:  genesisBlock.ID(),
		UniqueID:   gateway.GenerateUniqueID(),
		NetAddress: syncerAddr,
	}

	s := syncer.New(l, cm, ps, header, syncer.WithLogger(log.Named("syncer")))
	wm, err := wallet.NewManager(cm, store, log.Named("wallet"))
	if err != nil {
		return nil, fmt.Errorf("failed to create wallet manager: %w", err)
	}

	return &node{
		chainStore: bdb,
		cm:         cm,
		store:      store,
		s:          s,
		wm:         wm,
		Start: func() func() {
			ch := make(chan struct{})
			go func() {
				s.Run()
				close(ch)
			}()
			return func() {
				l.Close()
				<-ch
				bdb.Close()
			}
		},
	}, nil
}
