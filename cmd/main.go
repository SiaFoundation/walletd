package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime/debug"

	"golang.org/x/term"
)

var commit = func() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				if modified {
					return setting.Value[:8] + " (modified)"
				}
				return setting.Value[:8]
			}
		}
	}
	return "?"
}()

var modified = func() bool {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.modified" {
				return setting.Value == "true"
			}
		}
	}
	return false
}()

var timestamp = func() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.time" {
				return setting.Value
			}
		}
	}
	return "?"
}()

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

func main() {
	log.SetFlags(0)
	gatewayAddr := flag.String("addr", "localhost:0", "p2p address to listen on")
	apiAddr := flag.String("http", "localhost:9980", "address to serve API on")
	dir := flag.String("dir", ".", "directory to store node state in")
	flag.Parse()

	log.Println("walletd v0.1.0")
	if flag.Arg(0) == "version" {
		log.Println("Commit Hash:", commit)
		log.Println("Commit Date:", timestamp)
		return
	}

	apiPassword := getAPIPassword()
	l, err := net.Listen("tcp", *apiAddr)
	if err != nil {
		log.Fatal(err)
	}

	n, err := newNode(*gatewayAddr, *dir)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("p2p: Listening on", n.s.Addr())
	go n.s.Run()
	log.Println("api: Listening on", l.Addr())
	go startWeb(l, n, apiPassword)

	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt)
	<-signalCh
	log.Println("Shutting down...")
	n.Close()
	l.Close()
}
