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
	"go.sia.tech/walletd/internal/remote"
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

// runNode sets up the node prerequisites and starts the walletd server.
// It will block until the context is canceled.
func runNode(ctx context.Context, cfg config.Config, log *zap.Logger) error {
	store, err := sqlite.OpenDatabase(filepath.Join(cfg.Directory, "walletd.sqlite3"), log.Named("sqlite3"))
	if err != nil {
		return fmt.Errorf("failed to open wallet database: %w", err)
	}
	defer store.Close()

	walletOpts := []wallet.Option{
		wallet.WithStore(store),
		wallet.WithLogger(log.Named("wallet")),
		wallet.WithIndexMode(cfg.Index.Mode),
		wallet.WithSyncBatchSize(cfg.Index.BatchSize),
	}
	var apiOpts []api.ServerOption

	if cfg.Consensus.Mode == "remote" {
		client := api.NewClient(cfg.Consensus.Remote.Address, cfg.Consensus.Remote.Password)
		network, err := client.ConsensusNetwork()
		if err != nil {
			return fmt.Errorf("failed to get consensus network: %w", err)
		} else if network.Name != cfg.Consensus.Network {
			return fmt.Errorf("remote consensus network is %q, expected %q", network.Name, cfg.Consensus.Network)
		}

		cm, err := remote.NewChainManager(client, log.Named("chain"))
		if err != nil {
			return fmt.Errorf("failed to create chain manager: %w", err)
		}

		s := remote.NewSyncer(client, log.Named("syncer"))

		apiOpts = append(apiOpts, api.WithChainManager(cm, "remote"), api.WithSyncer(s))
		walletOpts = append(walletOpts, wallet.WithChainManager(cm))
	} else {
		var network *consensus.Network
		var genesisBlock types.Block
		switch cfg.Consensus.Network {
		case "mainnet":
			network, genesisBlock = chain.Mainnet()
			cfg.Syncer.Peers = append(cfg.Syncer.Peers, syncer.MainnetBootstrapPeers...)
		case "zen":
			network, genesisBlock = chain.TestnetZen()
			cfg.Syncer.Peers = append(cfg.Syncer.Peers, syncer.ZenBootstrapPeers...)
		case "anagami":
			network, genesisBlock = TestnetAnagami()
			cfg.Syncer.Peers = append(cfg.Syncer.Peers, syncer.AnagamiBootstrapPeers...)
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
			return fmt.Errorf("failed to start syncer listener on %s: %w", cfg.Syncer.Address, err)
		}
		defer syncerListener.Close()

		syncerAddr := syncerListener.Addr().String()
		if cfg.Syncer.EnableUPNP {
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

		ps, err := sqlite.NewPeerStore(store)
		if err != nil {
			return fmt.Errorf("failed to create peer store: %w", err)
		}
		for _, peer := range cfg.Syncer.Peers {
			if err := ps.AddPeer(peer); err != nil {
				log.Warn("failed to add peer", zap.String("address", peer), zap.Error(err))
			}
		}

		log.Debug("starting syncer", zap.String("syncer address", syncerAddr))
		s := syncer.New(syncerListener, cm, ps, gateway.Header{
			GenesisID:  genesisBlock.ID(),
			UniqueID:   gateway.GenerateUniqueID(),
			NetAddress: syncerAddr,
		}, syncer.WithLogger(log.Named("syncer")))
		go s.Run()

		walletOpts = append(walletOpts, wallet.WithChainManager(cm))
		apiOpts = append(apiOpts, api.WithChainManager(cm, "local"), api.WithSyncer(s))
	}

	wm, err := wallet.NewManager(walletOpts...)
	if err != nil {
		return fmt.Errorf("failed to create wallet manager: %w", err)
	}
	defer wm.Close()

	apiOpts = append(apiOpts, api.WithWalletManager(wm))

	webListener, err := net.Listen("tcp", cfg.HTTP.Address)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", cfg.HTTP.Address, err)
	}
	defer webListener.Close()

	api := jape.BasicAuth(cfg.HTTP.Password)(api.NewServer(apiOpts...))
	web := walletd.Handler()
	s := &http.Server{
		ReadTimeout: 15 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api") {
				r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api")
				api.ServeHTTP(w, r)
				return
			}
			web.ServeHTTP(w, r)
		}),
	}
	defer s.Close()

	go func() {
		if err := s.Serve(webListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal("failed to start HTTP server", zap.Error(err))
		}
	}()
	log.Info("walletd started", zap.String("http.address", cfg.HTTP.Address), zap.String("network", cfg.Consensus.Network), zap.Stringer("index.mode", cfg.Index.Mode), zap.String("version", build.Version()))
	<-ctx.Done()
	log.Info("shutting down")
	return nil
}
