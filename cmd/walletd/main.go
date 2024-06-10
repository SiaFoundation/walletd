package main

import (
	"context"
	"fmt"
	"log"
	"net"
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
	"golang.org/x/term"
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
		Address:  "localhost:9980",
		Password: os.Getenv("WALLETD_API_PASSWORD"),
	},
	Consensus: config.Consensus{
		Network:        "mainnet",
		GatewayAddress: ":9981",
		Bootstrap:      true,
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

func getAPIPassword() string {
	apiPassword := cfg.HTTP.Password
	if apiPassword == "" {
		fmt.Print("Enter API password: ")
		pw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		check("Could not read API password:", err)
		apiPassword = string(pw)
	}
	return apiPassword
}

// stdoutFatalError prints an error message to stdout and exits with a 1 exit code.
func stdoutFatalError(msg string) {
	stdoutError(msg)
	os.Exit(1)
}

// wrapANSI wraps the output in ANSI escape codes if enabled.
func wrapANSI(prefix, output, suffix string) string {
	if cfg.Log.StdOut.EnableANSI {
		return prefix + output + suffix
	}
	return output
}

// stdoutError prints an error message to stdout
func stdoutError(msg string) {
	if cfg.Log.StdOut.EnableANSI {
		fmt.Println(wrapANSI("\033[31m", msg, "\033[0m"))
	} else {
		fmt.Println(msg)
	}
}

// tryLoadConfig loads the config file specified by the WALLETD_CONFIG_FILE. If
// the config file does not exist, it will not be loaded.
func tryLoadConfig() {
	configPath := "walletd.yml"
	if str := os.Getenv("WALLETD_CONFIG_FILE"); str != "" {
		configPath = str
	}
	fmt.Println("loading config from", configPath)

	// If the config file doesn't exist, don't try to load it.
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return
	}

	f, err := os.Open(configPath)
	if err != nil {
		stdoutFatalError("failed to open config file: " + err.Error())
		return
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)

	if err := dec.Decode(&cfg); err != nil {
		fmt.Println("failed to decode config file:", err)
		os.Exit(1)
	}
	fmt.Println("config loaded")
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

	rootCmd := flagg.Root
	rootCmd.Usage = flagg.SimpleUsage(rootCmd, rootUsage)
	rootCmd.StringVar(&cfg.Directory, "dir", cfg.Directory, "directory to store node state in")
	rootCmd.StringVar(&cfg.HTTP.Address, "http", cfg.HTTP.Address, "address to serve API on")

	rootCmd.StringVar(&cfg.Consensus.GatewayAddress, "addr", cfg.Consensus.GatewayAddress, "p2p address to listen on")
	rootCmd.StringVar(&cfg.Consensus.Network, "network", cfg.Consensus.Network, "network to connect to")
	rootCmd.BoolVar(&cfg.Consensus.EnableUPNP, "upnp", cfg.Consensus.EnableUPNP, "attempt to forward ports and discover IP with UPnP")
	rootCmd.BoolVar(&cfg.Consensus.Bootstrap, "bootstrap", cfg.Consensus.Bootstrap, "attempt to bootstrap the network")

	rootCmd.StringVar(&indexModeStr, "index.mode", indexModeStr, "address index mode (personal, full, none)")
	rootCmd.IntVar(&cfg.Index.BatchSize, "index.batch", cfg.Index.BatchSize, "max number of blocks to index at a time. Increasing this will increase scan speed, but also increase memory and cpu usage.")

	versionCmd := flagg.New("version", versionUsage)
	seedCmd := flagg.New("seed", seedUsage)

	mineCmd := flagg.New("mine", mineUsage)
	mineCmd.IntVar(&minerBlocks, "n", -1, "mine this many blocks. If negative, mine indefinitely")
	mineCmd.StringVar(&minerAddrStr, "addr", "", "address to send block rewards to (required)")

	cmd := flagg.Parse(flagg.Tree{
		Cmd: rootCmd,
		Sub: []flagg.Tree{
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
			stdoutFatalError("failed to create directory: " + err.Error())
		}

		apiPassword := getAPIPassword()
		l, err := net.Listen("tcp", cfg.HTTP.Address)
		if err != nil {
			stdoutFatalError("failed to start HTTP server: " + err.Error())
		}

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
				stdoutFatalError("failed to open log file: " + err.Error())
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

		n, err := newNode(cfg, log)
		if err != nil {
			log.Fatal("failed to create node", zap.Error(err))
		}
		defer n.Close()

		stop := n.Start()
		go startWeb(l, n, apiPassword)
		log.Info("walletd started", zap.String("version", build.Version()), zap.String("network", cfg.Consensus.Network), zap.String("commit", build.Commit()), zap.Time("buildDate", build.Time()))
		<-ctx.Done()
		log.Info("shutting down")
		stop()
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
	case mineCmd:
		if len(cmd.Args()) != 0 {
			cmd.Usage()
			return
		}

		minerAddr, err := types.ParseAddress(minerAddrStr)
		if err != nil {
			log.Fatal(err)
		}

		c := api.NewClient("http://"+cfg.HTTP.Address+"/api", getAPIPassword())
		runCPUMiner(c, minerAddr, minerBlocks)
	}
}
