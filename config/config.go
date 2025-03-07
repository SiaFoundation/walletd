package config

import (
	"bytes"
	"fmt"
	"os"

	"go.sia.tech/walletd/wallet"
	"gopkg.in/yaml.v3"
)

type (
	// HTTP contains the configuration for the HTTP server.
	HTTP struct {
		Address         string `yaml:"address,omitempty"`
		Password        string `yaml:"password,omitempty"`
		PublicEndpoints bool   `yaml:"publicEndpoints,omitempty"`
	}

	// Syncer contains the configuration for the consensus set syncer.
	Syncer struct {
		Address    string   `yaml:"address,omitempty"`
		Bootstrap  bool     `yaml:"bootstrap,omitempty"`
		EnableUPnP bool     `yaml:"enableUPnP,omitempty"`
		Peers      []string `yaml:"peers,omitempty"`
	}

	// KeyStore contains the configuration for the key store.
	KeyStore struct {
		Enabled bool   `yaml:"enabled,omitempty"`
		Secret  string `yaml:"secret,omitempty"`
	}

	// Consensus contains the configuration for the consensus set.
	Consensus struct {
		Network string `yaml:"network,omitempty"`
	}

	// Index contains the configuration for the blockchain indexer
	Index struct {
		Mode      wallet.IndexMode `yaml:"mode,omitempty"`
		BatchSize int              `yaml:"batchSize,omitempty"`
	}

	// LogFile configures the file output of the logger.
	LogFile struct {
		Enabled bool   `yaml:"enabled,omitempty"`
		Level   string `yaml:"level,omitempty"` // override the file log level
		Format  string `yaml:"format,omitempty"`
		// Path is the path of the log file.
		Path string `yaml:"path,omitempty"`
	}

	// StdOut configures the standard output of the logger.
	StdOut struct {
		Level      string `yaml:"level,omitempty"` // override the stdout log level
		Enabled    bool   `yaml:"enabled,omitempty"`
		Format     string `yaml:"format,omitempty"`
		EnableANSI bool   `yaml:"enableANSI,omitempty"` //nolint:tagliatelle
	}

	// Log contains the configuration for the logger.
	Log struct {
		Level  string  `yaml:"level,omitempty"` // global log level
		StdOut StdOut  `yaml:"stdout,omitempty"`
		File   LogFile `yaml:"file,omitempty"`
	}

	// Config contains the configuration for the host.
	Config struct {
		Name          string `yaml:"name,omitempty"`
		Directory     string `yaml:"directory,omitempty"`
		AutoOpenWebUI bool   `yaml:"autoOpenWebUI,omitempty"`

		HTTP      HTTP      `yaml:"http,omitempty"`
		Consensus Consensus `yaml:"consensus,omitempty"`
		Syncer    Syncer    `yaml:"syncer,omitempty"`
		Log       Log       `yaml:"log,omitempty"`
		Index     Index     `yaml:"index,omitempty"`
		KeyStore  KeyStore  `yaml:"keystore,omitempty"`
	}
)

// LoadFile loads the configuration from the provided file path.
// If the file does not exist, an error is returned.
// If the file exists but cannot be decoded, the function will attempt
// to upgrade the config file.
func LoadFile(fp string, cfg *Config) error {
	buf, err := os.ReadFile(fp)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	r := bytes.NewReader(buf)
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)

	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("failed to decode config file: %w", err)
	}
	return nil
}
