package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/syncer"
	"go.sia.tech/walletd/api"
	"go.sia.tech/walletd/build"
	"go.sia.tech/walletd/config"
	"go.sia.tech/walletd/keys"
	"go.sia.tech/walletd/persist/sqlite"
	"go.sia.tech/walletd/wallet"
	"go.sia.tech/web/walletd"
	"go.uber.org/zap"
	"lukechampine.com/upnp"
)

func tryConfigPaths() []string {
	if str := os.Getenv(configFileEnvVar); str != "" {
		return []string{str}
	}

	paths := []string{
		"walletd.yml",
	}
	if str := os.Getenv(dataDirEnvVar); str != "" {
		paths = append(paths, filepath.Join(str, "walletd.yml"))
	}

	switch runtime.GOOS {
	case "windows":
		paths = append(paths, filepath.Join(os.Getenv("APPDATA"), "walletd", "walletd.yml"))
	case "darwin":
		paths = append(paths, filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "walletd", "walletd.yml"))
	case "linux", "freebsd", "openbsd":
		paths = append(paths,
			filepath.Join(string(filepath.Separator), "etc", "walletd", "walletd.yml"),
			filepath.Join(string(filepath.Separator), "var", "lib", "walletd", "walletd.yml"), // old default for the Linux service
		)
	}
	return paths
}

func defaultDataDirectory(fp string) string {
	// use the provided path if it's not empty
	if fp != "" {
		return fp
	}

	// check for databases in the current directory
	if _, err := os.Stat("walletd.db"); err == nil {
		return "."
	} else if _, err := os.Stat("walletd.sqlite3"); err == nil {
		return "."
	}

	// default to the operating system's application directory
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), "walletd")
	case "darwin":
		return filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "walletd")
	case "linux", "freebsd", "openbsd":
		return filepath.Join(string(filepath.Separator), "var", "lib", "walletd")
	default:
		return "."
	}
}

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

func runNode(ctx context.Context, cfg config.Config, log *zap.Logger, enableDebug bool) error {
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
	case "anagami":
		network, genesisBlock = chain.TestnetAnagami()
		bootstrapPeers = syncer.AnagamiBootstrapPeers
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
	go s.Run()

	wm, err := wallet.NewManager(cm, store, wallet.WithLogger(log.Named("wallet")), wallet.WithIndexMode(cfg.Index.Mode), wallet.WithSyncBatchSize(cfg.Index.BatchSize))
	if err != nil {
		return fmt.Errorf("failed to create wallet manager: %w", err)
	}
	defer wm.Close()

	apiOpts := []api.ServerOption{
		api.WithLogger(log.Named("api")),
		api.WithPublicEndpoints(cfg.HTTP.PublicEndpoints),
		api.WithBasicAuth(cfg.HTTP.Password),
	}
	if enableDebug {
		apiOpts = append(apiOpts, api.WithDebug())
	}
	if cfg.KeyStore.Enabled {
		km, err := keys.NewManager(store, cfg.KeyStore.Secret)
		if err != nil {
			return fmt.Errorf("failed to create key manager: %w", err)
		}
		defer km.Close()

		apiOpts = append(apiOpts, api.WithKeyManager(km))
	}
	api := api.NewServer(cm, s, wm, apiOpts...)
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

	log.Info("node started", zap.String("network", network.Name), zap.Stringer("syncer", syncerListener.Addr()), zap.Stringer("http", httpListener.Addr()), zap.String("version", build.Version()), zap.String("commit", build.Commit()))
	<-ctx.Done()
	log.Info("shutting down")
	return nil
}
