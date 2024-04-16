package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"

	"go.sia.tech/core/types"
	cwallet "go.sia.tech/coreutils/wallet"
	"go.sia.tech/walletd/api"
	"go.sia.tech/walletd/build"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/term"
	"lukechampine.com/flagg"
)

func check(context string, err error) {
	if err != nil {
		log.Fatalf("%v: %v", context, err)
	}
}

func getAPIPassword() string {
	apiPassword := os.Getenv("WALLETD_API_PASSWORD")
	if apiPassword != "" {
		fmt.Println("env: Using WALLETD_API_PASSWORD environment variable")
	} else {
		fmt.Print("Enter API password: ")
		pw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		check("Could not read API password:", err)
		apiPassword = string(pw)
	}
	return apiPassword
}

var (
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

func main() {
	log.SetFlags(0)

	var gatewayAddr, apiAddr, dir, network, seed string
	var upnp, bootstrap bool

	var minerAddrStr string
	var minerBlocks int

	rootCmd := flagg.Root
	rootCmd.Usage = flagg.SimpleUsage(rootCmd, rootUsage)
	rootCmd.StringVar(&gatewayAddr, "addr", ":9981", "p2p address to listen on")
	rootCmd.StringVar(&apiAddr, "http", "localhost:9980", "address to serve API on")
	rootCmd.StringVar(&dir, "dir", ".", "directory to store node state in")
	rootCmd.StringVar(&network, "network", "mainnet", "network to connect to")
	rootCmd.BoolVar(&upnp, "upnp", true, "attempt to forward ports and discover IP with UPnP")
	rootCmd.BoolVar(&bootstrap, "bootstrap", true, "attempt to bootstrap the network")
	rootCmd.StringVar(&seed, "seed", "", "testnet seed")
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

	log.Println("walletd", build.Version())
	switch cmd {
	case rootCmd:
		if len(cmd.Args()) != 0 {
			cmd.Usage()
			return
		}

		if err := os.MkdirAll(dir, 0700); err != nil {
			log.Fatal(err)
		}

		apiPassword := getAPIPassword()
		l, err := net.Listen("tcp", apiAddr)
		if err != nil {
			log.Fatal(err)
		}

		// configure console logging note: this is configured before anything else
		// to have consistent logging. File logging will be added after the cli
		// flags and config is parsed
		consoleCfg := zap.NewProductionEncoderConfig()
		consoleCfg.TimeKey = "" // prevent duplicate timestamps
		consoleCfg.EncodeTime = zapcore.RFC3339TimeEncoder
		consoleCfg.EncodeDuration = zapcore.StringDurationEncoder
		consoleCfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
		consoleCfg.StacktraceKey = ""
		consoleCfg.CallerKey = ""
		consoleEncoder := zapcore.NewConsoleEncoder(consoleCfg)

		// only log info messages to console unless stdout logging is enabled
		consoleCore := zapcore.NewCore(consoleEncoder, zapcore.Lock(os.Stdout), zap.NewAtomicLevelAt(zap.DebugLevel))
		logger := zap.New(consoleCore, zap.AddCaller())
		defer logger.Sync()
		// redirect stdlib log to zap
		zap.RedirectStdLog(logger.Named("stdlib"))

		n, err := newNode(gatewayAddr, dir, network, upnp, bootstrap, logger)
		if err != nil {
			log.Fatal(err)
		}
		defer n.Close()

		log.Println("p2p: Listening on", n.s.Addr())
		stop := n.Start()
		log.Println("api: Listening on", l.Addr())
		go startWeb(l, n, apiPassword)
		signalCh := make(chan os.Signal, 1)
		signal.Notify(signalCh, os.Interrupt)
		<-signalCh
		log.Println("Shutting down...")
		stop()
	case versionCmd:
		if len(cmd.Args()) != 0 {
			cmd.Usage()
			return
		}
		log.Println("Commit Hash:", build.Commit())
		log.Println("Commit Date:", build.Time())
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

		c := api.NewClient("http://"+apiAddr+"/api", getAPIPassword())
		runCPUMiner(c, minerAddr, minerBlocks)
	}
}
