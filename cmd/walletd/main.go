package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"go.sia.tech/core/types"
	cwallet "go.sia.tech/coreutils/wallet"
	"go.sia.tech/walletd/api"
	"go.sia.tech/walletd/build"
	"go.sia.tech/walletd/config"
	"go.sia.tech/walletd/wallet"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/yaml.v3"
	"lukechampine.com/flagg"
)

const (
	rootUsage = `Usage:
    walletd [flags] [action]

Run 'walletd' with no arguments to start the blockchain node and API server.

Actions:
    version     print walletd version
    seed        generate a recovery phrase
    mine        run CPU miner`

	versionUsage = `Usage:
    walletd version

Prints the version of the walletd binary.
`
	seedUsage = `Usage:
    walletd seed

Generates a secure BIP-39 recovery phrase.
`
	mineUsage = `Usage:
    walletd mine

Runs a CPU miner. Not intended for production use.
`
)

var cfg = config.Config{
	Name:          "walletd",
	Directory:     ".",
	AutoOpenWebUI: true,
	HTTP: config.HTTP{
		Address:         "localhost:9980",
		Password:        os.Getenv("WALLETD_API_PASSWORD"),
		PublicEndpoints: false,
	},
	Syncer: config.Syncer{
		Address:   ":9981",
		Bootstrap: true,
	},
	Consensus: config.Consensus{
		Network: "mainnet",
	},
	Index: config.Index{
		Mode:      wallet.IndexModePersonal,
		BatchSize: 1000,
	},
	Log: config.Log{
		Level: "info",
		File: config.LogFile{
			Enabled: true,
			Format:  "json",
			Path:    os.Getenv("WALLETD_LOG_FILE"),
		},
		StdOut: config.StdOut{
			Enabled:    true,
			Format:     "human",
			EnableANSI: runtime.GOOS != "windows",
		},
	},
}

func check(context string, err error) {
	if err != nil {
		log.Fatalf("%v: %v", context, err)
	}
}

func mustSetAPIPassword() {
	if cfg.HTTP.Password != "" {
		return
	}

	// retry until a valid API password is entered
	for {
		fmt.Println("Please choose a password to unlock walletd.")
		fmt.Println("This password will be required to access the admin UI in your web browser.")
		fmt.Println("(The password must be at least 4 characters.)")
		cfg.HTTP.Password = readPasswordInput("Enter password")
		if len(cfg.HTTP.Password) >= 4 {
			break
		}

		fmt.Println(wrapANSI("\033[31m", "Password must be at least 4 characters!", "\033[0m"))
		fmt.Println("")
	}
}

func fatalError(err error) {
	os.Stderr.WriteString(err.Error() + "\n")
	os.Exit(1)
}

// tryLoadConfig loads the config file specified by the WALLETD_CONFIG_FILE. If
// the config file does not exist, it will not be loaded.
func tryLoadConfig() {
	configPath := "walletd.yml"
	if str := os.Getenv("WALLETD_CONFIG_FILE"); str != "" {
		configPath = str
	}

	// If the config file doesn't exist, don't try to load it.
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return
	}

	f, err := os.Open(configPath)
	if err != nil {
		fatalError(fmt.Errorf("failed to open config file: %w", err))
		return
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)

	if err := dec.Decode(&cfg); err != nil {
		fmt.Println("failed to decode config file:", err)
		os.Exit(1)
	}
}

// jsonEncoder returns a zapcore.Encoder that encodes logs as JSON intended for
// parsing.
func jsonEncoder() zapcore.Encoder {
	cfg := zap.NewProductionEncoderConfig()
	cfg.EncodeTime = zapcore.RFC3339TimeEncoder
	cfg.TimeKey = "timestamp"
	return zapcore.NewJSONEncoder(cfg)
}

// humanEncoder returns a zapcore.Encoder that encodes logs as human-readable
// text.
func humanEncoder(showColors bool) zapcore.Encoder {
	cfg := zap.NewProductionEncoderConfig()
	cfg.EncodeTime = zapcore.RFC3339TimeEncoder
	cfg.EncodeDuration = zapcore.StringDurationEncoder

	if showColors {
		cfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
	} else {
		cfg.EncodeLevel = zapcore.CapitalLevelEncoder
	}

	cfg.StacktraceKey = ""
	cfg.CallerKey = ""
	return zapcore.NewConsoleEncoder(cfg)
}

func parseLogLevel(level string) zap.AtomicLevel {
	switch level {
	case "debug":
		return zap.NewAtomicLevelAt(zap.DebugLevel)
	case "info":
		return zap.NewAtomicLevelAt(zap.InfoLevel)
	case "warn":
		return zap.NewAtomicLevelAt(zap.WarnLevel)
	case "error":
		return zap.NewAtomicLevelAt(zap.ErrorLevel)
	default:
		fmt.Printf("invalid log level %q", level)
		os.Exit(1)
	}
	panic("unreachable")
}

func main() {
	// attempt to load the config file first, command line flags will override
	// any values set in the config file
	tryLoadConfig()

	indexModeStr := cfg.Index.Mode.String()

	var minerAddrStr string
	var minerBlocks int
	var enableDebug bool

	rootCmd := flagg.Root
	rootCmd.Usage = flagg.SimpleUsage(rootCmd, rootUsage)
	rootCmd.BoolVar(&enableDebug, "debug", false, "enable debug mode with additional profiling and mining endpoints")
	rootCmd.StringVar(&cfg.Directory, "dir", cfg.Directory, "directory to store node state in")
	rootCmd.StringVar(&cfg.HTTP.Address, "http", cfg.HTTP.Address, "address to serve API on")
	rootCmd.BoolVar(&cfg.HTTP.PublicEndpoints, "http.public", cfg.HTTP.PublicEndpoints, "disables auth on endpoints that should be publicly accessible when running walletd as a service")

	rootCmd.StringVar(&cfg.Syncer.Address, "addr", cfg.Syncer.Address, "p2p address to listen on")
	rootCmd.StringVar(&cfg.Consensus.Network, "network", cfg.Consensus.Network, "network to connect to")
	rootCmd.BoolVar(&cfg.Syncer.EnableUPnP, "upnp", cfg.Syncer.EnableUPnP, "attempt to forward ports and discover IP with UPnP")
	rootCmd.BoolVar(&cfg.Syncer.Bootstrap, "bootstrap", cfg.Syncer.Bootstrap, "attempt to bootstrap the network")

	rootCmd.StringVar(&indexModeStr, "index.mode", indexModeStr, "address index mode (personal, full, none)")
	rootCmd.IntVar(&cfg.Index.BatchSize, "index.batch", cfg.Index.BatchSize, "max number of blocks to index at a time. Increasing this will increase scan speed, but also increase memory and cpu usage.")

	versionCmd := flagg.New("version", versionUsage)
	seedCmd := flagg.New("seed", seedUsage)
	configCmd := flagg.New("config", "interactively configure walletd")

	mineCmd := flagg.New("mine", mineUsage)
	mineCmd.IntVar(&minerBlocks, "n", -1, "mine this many blocks. If negative, mine indefinitely")
	mineCmd.StringVar(&minerAddrStr, "addr", "", "address to send block rewards to (required)")

	cmd := flagg.Parse(flagg.Tree{
		Cmd: rootCmd,
		Sub: []flagg.Tree{
			{Cmd: configCmd},
			{Cmd: versionCmd},
			{Cmd: seedCmd},
			{Cmd: mineCmd},
		},
	})

	switch cmd {
	case rootCmd:
		if len(cmd.Args()) != 0 {
			cmd.Usage()
			return
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGKILL)
		defer cancel()

		if err := os.MkdirAll(cfg.Directory, 0700); err != nil {
			fatalError(fmt.Errorf("failed to create data directory: %w", err))
		}

		mustSetAPIPassword()

		var logCores []zapcore.Core
		if cfg.Log.StdOut.Enabled {
			// if no log level is set for stdout, use the global log level
			if cfg.Log.StdOut.Level == "" {
				cfg.Log.StdOut.Level = cfg.Log.Level
			}

			var encoder zapcore.Encoder
			switch cfg.Log.StdOut.Format {
			case "json":
				encoder = jsonEncoder()
			default: // stdout defaults to human
				encoder = humanEncoder(cfg.Log.StdOut.EnableANSI)
			}

			// create the stdout logger
			level := parseLogLevel(cfg.Log.StdOut.Level)
			logCores = append(logCores, zapcore.NewCore(encoder, zapcore.Lock(os.Stdout), level))
		}

		if cfg.Log.File.Enabled {
			// if no log level is set for file, use the global log level
			if cfg.Log.File.Level == "" {
				cfg.Log.File.Level = cfg.Log.Level
			}

			// normalize log path
			if cfg.Log.File.Path == "" {
				cfg.Log.File.Path = filepath.Join(cfg.Directory, "walletd.log")
			}

			// configure file logging
			var encoder zapcore.Encoder
			switch cfg.Log.File.Format {
			case "human":
				encoder = humanEncoder(false) // disable colors in file log
			default: // log file defaults to JSON
				encoder = jsonEncoder()
			}

			fileWriter, closeFn, err := zap.Open(cfg.Log.File.Path)
			if err != nil {
				fatalError(fmt.Errorf("failed to open log file: %w", err))
				return
			}
			defer closeFn()

			// create the file logger
			level := parseLogLevel(cfg.Log.File.Level)
			logCores = append(logCores, zapcore.NewCore(encoder, zapcore.Lock(fileWriter), level))
		}

		var log *zap.Logger
		if len(logCores) == 1 {
			log = zap.New(logCores[0], zap.AddCaller())
		} else {
			log = zap.New(zapcore.NewTee(logCores...), zap.AddCaller())
		}
		defer log.Sync()

		// redirect stdlib log to zap
		zap.RedirectStdLog(log.Named("stdlib"))

		if err := cfg.Index.Mode.UnmarshalText([]byte(indexModeStr)); err != nil {
			log.Fatal("failed to parse index mode", zap.Error(err))
		}

		if err := runNode(ctx, cfg, log, enableDebug); err != nil {
			log.Fatal("failed to run node", zap.Error(err))
		}
	case versionCmd:
		if len(cmd.Args()) != 0 {
			cmd.Usage()
			return
		}
		fmt.Println("walletd", build.Version())
		fmt.Println("Commit:", build.Commit())
		fmt.Println("Build Date:", build.Time())
	case seedCmd:
		if len(cmd.Args()) != 0 {
			cmd.Usage()
			return
		}
		recoveryPhrase := cwallet.NewSeedPhrase()
		var seed [32]byte
		if err := cwallet.SeedFromPhrase(&seed, recoveryPhrase); err != nil {
			log.Fatal(err)
		}
		addr := types.StandardUnlockHash(cwallet.KeyFromSeed(&seed, 0).PublicKey())

		fmt.Println("Recovery Phrase:", recoveryPhrase)
		fmt.Println("Address", addr)
	case configCmd:
		if len(cmd.Args()) != 0 {
			cmd.Usage()
			return
		}

		buildConfig()
	case mineCmd:
		if len(cmd.Args()) != 0 {
			cmd.Usage()
			return
		}

		minerAddr, err := types.ParseAddress(minerAddrStr)
		if err != nil {
			log.Fatal(err)
		}

		mustSetAPIPassword()
		c := api.NewClient("http://"+cfg.HTTP.Address+"/api", cfg.HTTP.Password)
		runCPUMiner(c, minerAddr, minerBlocks)
	}
}
