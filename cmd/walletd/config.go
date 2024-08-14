package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.sia.tech/walletd/wallet"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// readPasswordInput reads a password from stdin.
func readPasswordInput(context string) string {
	fmt.Printf("%s: ", context)
	input, err := term.ReadPassword(int(os.Stdin.Fd()))
	if err != nil {
		fatalError(fmt.Errorf("could not read password input: %w", err))
	}
	fmt.Println("")
	return string(input)
}

func readInput(context string) string {
	fmt.Printf("%s: ", context)
	r := bufio.NewReader(os.Stdin)
	input, err := r.ReadString('\n')
	if err != nil {
		fatalError(fmt.Errorf("could not read input: %w", err))
	}
	return strings.TrimSpace(input)
}

// wrapANSI wraps the output in ANSI escape codes if enabled.
func wrapANSI(prefix, output, suffix string) string {
	if cfg.Log.StdOut.EnableANSI {
		return prefix + output + suffix
	}
	return output
}

func humanList(s []string, sep string) string {
	if len(s) == 0 {
		return ""
	} else if len(s) == 1 {
		return fmt.Sprintf(`%q`, s[0])
	} else if len(s) == 2 {
		return fmt.Sprintf(`%q %s %q`, s[0], sep, s[1])
	}

	var sb strings.Builder
	for i, v := range s {
		if i != 0 {
			sb.WriteString(", ")
		}
		if i == len(s)-1 {
			sb.WriteString("or ")
		}
		sb.WriteString(`"`)
		sb.WriteString(v)
		sb.WriteString(`"`)
	}
	return sb.String()
}

func promptQuestion(question string, answers []string) string {
	for {
		input := readInput(fmt.Sprintf("%s (%s)", question, strings.Join(answers, "/")))
		for _, answer := range answers {
			if strings.EqualFold(input, answer) {
				return answer
			}
		}
		fmt.Println(wrapANSI("\033[31m", fmt.Sprintf("Answer must be %s", humanList(answers, "or")), "\033[0m"))
	}
}

func promptYesNo(question string) bool {
	answer := promptQuestion(question, []string{"yes", "no"})
	return strings.EqualFold(answer, "yes")
}

// stdoutError prints an error message to stdout
func stdoutError(msg string) {
	if cfg.Log.StdOut.EnableANSI {
		fmt.Println(wrapANSI("\033[31m", msg, "\033[0m"))
	} else {
		fmt.Println(msg)
	}
}

func setAPIPassword() {
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

func setDataDirectory() {
	if cfg.Directory == "" {
		cfg.Directory = "."
	}

	dir, err := filepath.Abs(cfg.Directory)
	if err != nil {
		fatalError(fmt.Errorf("failed to get absolute path of data directory: %w", err))
	}

	fmt.Println("The data directory is where walletd will store its metadata and consensus data.")
	fmt.Println("This directory should be on a fast, reliable storage device, preferably an SSD.")
	fmt.Println("")

	_, existsErr := os.Stat(filepath.Join(cfg.Directory, "walletd.sqlite3"))
	dataExists := existsErr == nil
	if dataExists {
		fmt.Println(wrapANSI("\033[33m", "There is existing data in the data directory.", "\033[0m"))
		fmt.Println(wrapANSI("\033[33m", "If you change your data directory, you will need to manually move consensus, gateway, tpool, and walletd.sqlite3 to the new directory.", "\033[0m"))
	}

	if !promptYesNo("Would you like to change the data directory? (Current: " + dir + ")") {
		return
	}
	cfg.Directory = readInput("Enter data directory")
}

func setListenAddress(context string, value *string) {
	// will continue to prompt until a valid value is entered
	for {
		input := readInput(fmt.Sprintf("%s (currently %q)", context, *value))
		if input == "" {
			return
		}

		host, port, err := net.SplitHostPort(input)
		if err != nil {
			stdoutError(fmt.Sprintf("Invalid %s port %q: %s", context, input, err.Error()))
			continue
		}

		n, err := strconv.Atoi(port)
		if err != nil {
			stdoutError(fmt.Sprintf("Invalid %s port %q: %s", context, input, err.Error()))
			continue
		} else if n < 0 || n > 65535 {
			stdoutError(fmt.Sprintf("Invalid %s port %q: must be between 0 and 65535", context, input))
			continue
		}
		*value = net.JoinHostPort(host, port)
		return
	}
}

func setAdvancedConfig() {
	if !promptYesNo("Would you like to configure advanced settings?") {
		return
	}

	fmt.Println("")
	fmt.Println("Advanced settings are used to configure walletd's behavior.")
	fmt.Println("You can leave these settings blank to use the defaults.")
	fmt.Println("")

	fmt.Println("The HTTP address is used to serve the host's admin API.")
	fmt.Println("The admin API is used to configure the host.")
	fmt.Println("It should not be exposed to the public internet without setting up a reverse proxy.")
	setListenAddress("HTTP Address", &cfg.HTTP.Address)

	fmt.Println("")
	fmt.Println("The syncer address is used to connect to the Sia network.")
	fmt.Println("It should be reachable from other Sia nodes.")
	setListenAddress("Syncer Address", &cfg.Syncer.Address)

	fmt.Println("")
	fmt.Println("Index mode determines how much of the blockchain to store.")
	fmt.Println(`"personal" mode stores events only relevant to addresses associated with a wallet.`)
	fmt.Println("To add new addresses, the wallet must be rescanned. This is the default mode.")
	fmt.Println("")
	fmt.Println(`"full" mode stores all blockchain events. This mode is useful for exchanges and shared wallet clients.`)
	fmt.Println("This mode requires significantly more disk space, but does not require rescanning when adding new addresses.")
	fmt.Println("")
	fmt.Println("This cannot be changed later without resetting walletd.")
	fmt.Printf("Currently %q\n", cfg.Index.Mode)
	mode := readInput(`Enter index mode ("personal" or "full")`)
	switch {
	case strings.EqualFold(mode, "personal"):
		cfg.Index.Mode = wallet.IndexModePersonal
	case strings.EqualFold(mode, "full"):
		cfg.Index.Mode = wallet.IndexModeFull
	default:
		fatalError(fmt.Errorf("invalid index mode: %q", mode))
	}

	fmt.Println("")
	fmt.Println("The network is the blockchain network that walletd will connect to.")
	fmt.Println("Mainnet is the default network.")
	fmt.Println("Zen is a production-like testnet.")
	fmt.Println("This cannot be changed later without resetting walletd.")
	fmt.Printf("Currently %q\n", cfg.Consensus.Network)
	cfg.Consensus.Network = readInput(`Enter network ("mainnet" or "zen")`)
}

func buildConfig() {
	// write the config file
	configPath := "walletd.yml"
	if str := os.Getenv("WALLETD_CONFIG_FILE"); str != "" {
		configPath = str
	}

	if _, err := os.Stat(configPath); err == nil {
		if !promptYesNo("walletd.yml already exists. Would you like to overwrite it?") {
			return
		}
	}

	fmt.Println("")
	setDataDirectory()

	fmt.Println("")
	setAPIPassword()

	fmt.Println("")
	setAdvancedConfig()

	// write the config file
	f, err := os.Create(configPath)
	if err != nil {
		fatalError(fmt.Errorf("failed to create config file: %w", err))
		return
	}
	defer f.Close()

	enc := yaml.NewEncoder(f)
	if err := enc.Encode(cfg); err != nil {
		fatalError(fmt.Errorf("failed to encode config file: %w", err))
		return
	} else if err := f.Sync(); err != nil {
		fatalError(fmt.Errorf("failed to sync config file: %w", err))
		return
	} else if err := f.Close(); err != nil {
		fatalError(fmt.Errorf("failed to close config file: %w", err))
		return
	}
}
