package main

import (
	"context"
	"errors"
	"fmt"
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
	"lukechampine.com/flagg"
)

const (
	apiPasswordEnvVar = "WALLETD_API_PASSWORD"
	configFileEnvVar  = "WALLETD_CONFIG_FILE"
	dataDirEnvVar     = "WALLETD_DATA_DIR"
	logFileEnvVar     = "WALLETD_LOG_FILE_PATH"
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
	Directory:     os.Getenv(dataDirEnvVar),
	AutoOpenWebUI: true,
	HTTP: config.HTTP{
		Address:         "localhost:9980",
		Password:        os.Getenv(apiPasswordEnvVar),
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
			Path:    os.Getenv(logFileEnvVar),
		},
		StdOut: config.StdOut{
			Enabled:    true,
			Format:     "human",
			EnableANSI: runtime.GOOS != "windows",
		},
	},
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

// checkFatalError prints an error message to stderr and exits with a 1 exit code. If err is nil, this is a no-op.
func checkFatalError(context string, err error) {
	if err == nil {
		return
	}
	os.Stderr.WriteString(fmt.Sprintf("%s: %s\n", context, err))
	os.Exit(1)
}

// tryLoadConfig tries to load the config file. It will try multiple locations
// based on GOOS starting with PWD/walletd.yml. If the file does not exist, it will
// try the next location. If an error occurs while loading the file, it will
// print the error and exit. If the config is successfully loaded, the path to
// the config file is returned.
func tryLoadConfig() string {
	for _, fp := range tryConfigPaths() {
		if err := config.LoadFile(fp, &cfg); err == nil {
			return fp
		} else if !errors.Is(err, os.ErrNotExist) {
			checkFatalError("failed to load config file", err)
		}
	}
	return ""
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

func initStdoutLog(colored bool, levelStr string) *zap.Logger {
	level := parseLogLevel(levelStr)
	core := zapcore.NewCore(humanEncoder(colored), zapcore.Lock(os.Stdout), level)
	return zap.New(core, zap.AddCaller())
}

func main() {
	log := initStdoutLog(cfg.Log.StdOut.EnableANSI, cfg.Log.Level)
	defer log.Sync()

	// attempt to load the config file, command line flags will override any
	// values set in the config file
	configPath := tryLoadConfig()
	if configPath != "" {
		log.Info("loaded config file", zap.String("path", configPath))
	}
	// set the data directory to the default if it is not set
	cfg.Directory = defaultDataDirectory(cfg.Directory)

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

		if cfg.Directory != "" {
			checkFatalError("failed to create data directory", os.MkdirAll(cfg.Directory, 0700))
		}

		mustSetAPIPassword()

		checkFatalError("failed to parse index mode", cfg.Index.Mode.UnmarshalText([]byte(indexModeStr)))

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
			checkFatalError("failed to open log file", err)
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

		checkFatalError("failed to run node", runNode(ctx, cfg, log, enableDebug))
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
		checkFatalError("failed to parse mnemonic phrase", cwallet.SeedFromPhrase(&seed, recoveryPhrase))
		addr := types.StandardUnlockHash(cwallet.KeyFromSeed(&seed, 0).PublicKey())

		fmt.Println("Recovery Phrase:", recoveryPhrase)
		fmt.Println("Address", addr)
	case configCmd:
		if len(cmd.Args()) != 0 {
			cmd.Usage()
			return
		}

		buildConfig(configPath)
	case mineCmd:
		if len(cmd.Args()) != 0 {
			cmd.Usage()
			return
		}

		minerAddr, err := types.ParseAddress(minerAddrStr)
		checkFatalError("failed to parse miner address", err)
		mustSetAPIPassword()
		c := api.NewClient("http://"+cfg.HTTP.Address+"/api", cfg.HTTP.Password)
		runCPUMiner(c, minerAddr, minerBlocks)
	}
}
