package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/syncer"
	"go.sia.tech/jape"
	"go.sia.tech/walletd/api"
	"go.sia.tech/walletd/build"
	"go.sia.tech/walletd/config"
	"go.sia.tech/walletd/persist/sqlite"
	"go.sia.tech/walletd/wallet"
	"go.sia.tech/web/walletd"
	"go.uber.org/zap"
	"lukechampine.com/upnp"
)

func setupUPNP(ctx context.Context, port uint16, log *zap.Logger) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	d, err := upnp.Discover(ctx)
	if err != nil {
		return "", fmt.Errorf("couldn't discover UPnP router: %w", err)
	} else if !d.IsForwarded(port, "TCP") {
		if err := d.Forward(uint16(port), "TCP", "walletd"); err != nil {
			log.Debug("couldn't forward port", zap.Error(err))
		} else {
			log.Debug("upnp: forwarded p2p port", zap.Uint16("port", port))
		}
	}
	return d.ExternalIP()
}

func runNode(ctx context.Context, cfg config.Config, log *zap.Logger) error {
	var network *consensus.Network
	var genesisBlock types.Block
	var bootstrapPeers []string
	switch cfg.Consensus.Network {
	case "mainnet":
		network, genesisBlock = chain.Mainnet()
		bootstrapPeers = syncer.MainnetBootstrapPeers
	case "zen":
		network, genesisBlock = chain.TestnetZen()
		bootstrapPeers = syncer.ZenBootstrapPeers
	default:
		return errors.New("invalid network: must be one of 'mainnet', 'zen', or 'anagami'")
	}

	bdb, err := coreutils.OpenBoltChainDB(filepath.Join(cfg.Directory, "consensus.db"))
	if err != nil {
		return fmt.Errorf("failed to open consensus database: %w", err)
	}
	defer bdb.Close()

	dbstore, tipState, err := chain.NewDBStore(bdb, network, genesisBlock)
	if err != nil {
		return fmt.Errorf("failed to create chain store: %w", err)
	}
	cm := chain.NewManager(dbstore, tipState)

	syncerListener, err := net.Listen("tcp", cfg.Syncer.Address)
	if err != nil {
		return fmt.Errorf("failed to listen on %q: %w", cfg.Syncer.Address, err)
	}
	defer syncerListener.Close()

	httpListener, err := net.Listen("tcp", cfg.HTTP.Address)
	if err != nil {
		return fmt.Errorf("failed to listen on %q: %w", cfg.HTTP.Address, err)
	}
	defer httpListener.Close()

	syncerAddr := syncerListener.Addr().String()
	if cfg.Syncer.EnableUPnP {
		_, portStr, _ := net.SplitHostPort(cfg.Syncer.Address)
		port, err := strconv.ParseUint(portStr, 10, 16)
		if err != nil {
			return fmt.Errorf("failed to parse syncer port: %w", err)
		}

		ip, err := setupUPNP(context.Background(), uint16(port), log)
		if err != nil {
			log.Warn("failed to set up UPnP", zap.Error(err))
		} else {
			syncerAddr = net.JoinHostPort(ip, portStr)
		}
	}

	// peers will reject us if our hostname is empty or unspecified, so use loopback
	host, port, _ := net.SplitHostPort(syncerAddr)
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		syncerAddr = net.JoinHostPort("127.0.0.1", port)
	}

	store, err := sqlite.OpenDatabase(filepath.Join(cfg.Directory, "walletd.sqlite3"), log.Named("sqlite3"))
	if err != nil {
		return fmt.Errorf("failed to open wallet database: %w", err)
	}
	defer store.Close()

	if cfg.Syncer.Bootstrap {
		for _, peer := range bootstrapPeers {
			if err := store.AddPeer(peer); err != nil {
				return fmt.Errorf("failed to add bootstrap peer %q: %w", peer, err)
			}
		}
		for _, peer := range cfg.Syncer.Peers {
			if err := store.AddPeer(peer); err != nil {
				return fmt.Errorf("failed to add peer %q: %w", peer, err)
			}
		}
	}

	ps, err := sqlite.NewPeerStore(store)
	if err != nil {
		return fmt.Errorf("failed to create peer store: %w", err)
	}

	header := gateway.Header{
		GenesisID:  genesisBlock.ID(),
		UniqueID:   gateway.GenerateUniqueID(),
		NetAddress: syncerAddr,
	}

	s := syncer.New(syncerListener, cm, ps, header, syncer.WithLogger(log.Named("syncer")))
	defer s.Close()
	go s.Run(ctx)

	wm, err := wallet.NewManager(cm, store, wallet.WithLogger(log.Named("wallet")), wallet.WithIndexMode(cfg.Index.Mode), wallet.WithSyncBatchSize(cfg.Index.BatchSize))
	if err != nil {
		return fmt.Errorf("failed to create wallet manager: %w", err)
	}

	api := jape.BasicAuth(cfg.HTTP.Password)(api.NewServer(cm, s, wm))
	web := walletd.Handler()
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api") {
				r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api")
				api.ServeHTTP(w, r)
				return
			}
			web.ServeHTTP(w, r)
		}),
		ReadTimeout: 10 * time.Second,
	}
	defer server.Close()
	go server.Serve(httpListener)

	log.Info("node started", zap.Stringer("syncer", syncerListener.Addr()), zap.Stringer("http", httpListener.Addr()), zap.String("version", build.Version()), zap.String("commit", build.Commit()))
	<-ctx.Done()
	log.Info("shutting down")
	return nil
}
