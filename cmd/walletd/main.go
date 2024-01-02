package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/wallet"
	"golang.org/x/term"
	"lukechampine.com/flagg"
	"lukechampine.com/frand"
)

var commit = "?"
var timestamp = "?"

func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	modified := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			commit = setting.Value[:8]
		case "vcs.time":
			timestamp = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if modified {
		commit += " (modified)"
	}
}

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
		if err != nil {
			log.Fatal(err)
		}
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

Testnet Actions:
    seed        generate a seed
    mine        run CPU miner
    balance     view wallet balance
    send        send a simple transaction
    txns        view transaction history
`
	versionUsage = `Usage:
    walletd version

Prints the version of the walletd binary.
`
	seedUsage = `Usage:
    walletd seed

Generates a secure testnet seed.
`
	mineUsage = `Usage:
    walletd mine

Runs a testnet CPU miner.
`
	balanceUsage = `Usage:
    walletd balance

Displays testnet balance.
`
	sendUsage = `Usage:
    walletd send [flags] [amount] [address]

Sends a simple testnet transaction.
`
	txnsUsage = `Usage:
    walletd txns

Lists testnet transactions and miner rewards.
`
	txpoolUsage = `Usage:
    walletd txpool

Lists unconfirmed testnet transactions in the txpool.
Note that only transactions relevant to the wallet are shown.
`
)

func main() {
	log.SetFlags(0)

	var prometheusAddr, gatewayAddr, apiAddr, dir, network, seed string
	var upnp, v2 bool

	rootCmd := flagg.Root
	rootCmd.Usage = flagg.SimpleUsage(rootCmd, rootUsage)
	rootCmd.StringVar(&prometheusAddr, "prometheus", "notset", "address to serve prometheus API on")
	rootCmd.StringVar(&gatewayAddr, "addr", ":9981", "p2p address to listen on")
	rootCmd.StringVar(&apiAddr, "http", "localhost:9980", "address to serve API on")
	rootCmd.StringVar(&dir, "dir", ".", "directory to store node state in")
	rootCmd.StringVar(&network, "network", "mainnet", "network to connect to")
	rootCmd.BoolVar(&upnp, "upnp", true, "attempt to forward ports and discover IP with UPnP")
	rootCmd.StringVar(&seed, "seed", "", "testnet seed")
	versionCmd := flagg.New("version", versionUsage)
	seedCmd := flagg.New("seed", seedUsage)
	mineCmd := flagg.New("mine", mineUsage)
	balanceCmd := flagg.New("balance", balanceUsage)
	sendCmd := flagg.New("send", sendUsage)
	sendCmd.BoolVar(&v2, "v2", false, "send a v2 transaction")
	txnsCmd := flagg.New("txns", txnsUsage)
	txpoolCmd := flagg.New("txpool", txpoolUsage)
	dbCheckCmd := flagg.New("checkdb", "check consensus.db for errors")

	cmd := flagg.Parse(flagg.Tree{
		Cmd: rootCmd,
		Sub: []flagg.Tree{
			{Cmd: versionCmd},
			{Cmd: seedCmd},
			{Cmd: mineCmd},
			{Cmd: balanceCmd},
			{Cmd: sendCmd},
			{Cmd: txnsCmd},
			{Cmd: txpoolCmd},
			{Cmd: dbCheckCmd},
		},
	})

	log.Println("walletd v0.1.0")
	promIsSet := prometheusAddr != "notset"

	switch cmd {
	case rootCmd:
		if len(cmd.Args()) != 0 {
			cmd.Usage()
			return
		}
		apiPassword := getAPIPassword()
		l, err := net.Listen("tcp", apiAddr)
		if err != nil {
			log.Fatal(err)
		}
		n, err := newNode(gatewayAddr, dir, network, upnp)
		if err != nil {
			log.Fatal(err)
		}
		log.Println("p2p: Listening on", n.s.Addr())
		stop := n.Start()
		log.Println("api: Listening on", l.Addr())
		go startWeb(l, n, apiPassword, "/api")
		if promIsSet {
			l2, err := net.Listen("tcp", prometheusAddr)
			if err != nil {
				log.Fatal(err)
			}
			log.Println("prometheus api: Listening on", l2.Addr())
			go startWeb(l2, n, apiPassword, "/prometheus")
		}
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
		log.Println("Commit Hash:", commit)
		log.Println("Commit Date:", timestamp)

	case seedCmd:
		if len(cmd.Args()) != 0 {
			cmd.Usage()
			return
		}
		seed := frand.Bytes(8)
		var entropy [32]byte
		copy(entropy[:], seed)
		addr := types.StandardUnlockHash(wallet.NewSeedFromEntropy(&entropy).PublicKey(0))
		fmt.Printf("Seed:    %x\n", seed)
		fmt.Printf("Address: %v\n", strings.TrimPrefix(addr.String(), "addr:"))

	case mineCmd:
		if len(cmd.Args()) != 0 {
			cmd.Usage()
			return
		}
		seed := loadTestnetSeed(seed)
		c := initTestnetClient(apiAddr, network, seed)
		runTestnetMiner(c, seed)

	case balanceCmd:
		if len(cmd.Args()) != 0 {
			cmd.Usage()
			return
		}
		seed := loadTestnetSeed(seed)
		c := initTestnetClient(apiAddr, network, seed)
		b, err := c.Wallet("primary").Balance()
		check("Couldn't get balance:", err)
		out := fmt.Sprint(b.Siacoins)
		if !b.ImmatureSiacoins.IsZero() {
			out += fmt.Sprintf(" + %v immature", b.ImmatureSiacoins)
		}
		poolGained, poolLost := testnetTxpoolBalance(c, seed)
		if !poolGained.IsZero() || !poolLost.IsZero() {
			if poolGained.Cmp(poolLost) >= 0 {
				out += fmt.Sprintf(" + %v unconfirmed", poolGained.Sub(poolLost))
			} else {
				out += fmt.Sprintf(" - %v unconfirmed", poolLost.Sub(poolGained))
			}
		}
		fmt.Println(out)

	case sendCmd:
		if len(cmd.Args()) != 2 {
			cmd.Usage()
			return
		}
		seed := loadTestnetSeed(seed)
		c := initTestnetClient(apiAddr, network, seed)
		amount, err := types.ParseCurrency(cmd.Arg(0))
		check("Couldn't parse amount:", err)
		dest, err := types.ParseAddress(cmd.Arg(1))
		check("Couldn't parse recipient address:", err)
		sendTestnet(c, seed, amount, dest, v2)

	case txnsCmd:
		if len(cmd.Args()) != 0 {
			cmd.Usage()
			return
		}
		seed := loadTestnetSeed(seed)
		c := initTestnetClient(apiAddr, network, seed)
		printTestnetEvents(c, seed)

	case txpoolCmd:
		if len(cmd.Args()) != 0 {
			cmd.Usage()
			return
		}
		seed := loadTestnetSeed(seed)
		c := initTestnetClient(apiAddr, network, seed)
		printTestnetTxpool(c, seed)

	case dbCheckCmd:
		if len(cmd.Args()) != 0 {
			cmd.Usage()
			return
		}
		testnetCheckDB(dir)
	}
}
