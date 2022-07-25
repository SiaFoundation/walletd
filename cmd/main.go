package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"

	"go.sia.tech/siad/modules"
	"golang.org/x/term"
)

var (
	// to be supplied at build time
	githash   = "?"
	builddate = "?"
)

func check(context string, err error) {
	if err != nil {
		log.Fatalf("%v: %v", context, err)
	}
}

func getAPIPassword() string {
	apiPassword := os.Getenv("SIA_API_PASSWORD")
	if apiPassword != "" {
		fmt.Println("Using SIA_API_PASSWORD environment variable.")
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

func getWalletSeed() string {
	phrase := os.Getenv("SIA_WALLET_SEED")
	if phrase != "" {
		fmt.Println("Using SIA_WALLET_SEED environment variable")
	} else {
		fmt.Print("Enter wallet seed: ")
		pw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		check("Could not read seed phrase:", err)
		phrase = string(pw)
	}
	return phrase
}

func main() {
	log.SetFlags(0)
	gatewayAddr := flag.String("addr", ":0", "address to listen on")
	apiAddr := flag.String("http", "localhost:9980", "address to serve API on")
	dir := flag.String("dir", ".", "directory to store node state in")
	bootstrap := flag.String("bootstrap", "", "peer address or explorer URL to bootstrap from")
	flag.Parse()

	log.Println("walletd v0.1.0")
	if flag.Arg(0) == "version" {
		log.Println("Commit:", githash)
		log.Println("Build Date:", builddate)
		return
	}

	apiPassword := getAPIPassword()
	n, err := newNode(*gatewayAddr, *dir)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := n.Close(); err != nil {
			log.Println("WARN: error shutting down:", err)
		}
	}()
	log.Println("p2p: Listening on", n.g.Address())

	if *bootstrap != "" {
		log.Println("Connecting to bootstrap peer...")
		if err := n.g.Connect(modules.NetAddress(*bootstrap)); err != nil {
			log.Println(err)
		} else {
			log.Println("Success!")
		}
	}

	l, err := net.Listen("tcp", *apiAddr)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("api: Listening on", l.Addr())
	go startWeb(l, n, apiPassword)

	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt)
	<-signalCh
	log.Println("Shutting down...")
	n.Close()
	l.Close()
}
