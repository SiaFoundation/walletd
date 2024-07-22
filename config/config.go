package config

import "go.sia.tech/walletd/wallet"

type (
	// HTTP contains the configuration for the HTTP server.
	HTTP struct {
		Address  string `yaml:"address,omitempty"`
		Password string `yaml:"password,omitempty"`
	}

	Syncer struct {
		Address    string   `yaml:"address,omitempty"`
		Peers      []string `yaml:"peers,omitempty"`
		EnableUPNP bool     `yaml:"enableUPnP,omitempty"`
		Bootstrap  bool     `yaml:"bootstrap,omitempty"`
	}

	RemoteConsensus struct {
		Address  string `yaml:"address,omitempty"`
		Password string `yaml:"password,omitempty"`
	}

	// Consensus contains the configuration for the consensus set.
	Consensus struct {
		Mode    string `yaml:"mode,omitempty"`
		Network string `yaml:"network,omitempty"`

		// Remote is an experimental feature that enables walletd to connect to a
		// remote consensus set for scanning the chain state.
		Remote RemoteConsensus `yaml:"remote,omitempty"`
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
		Syncer    Syncer    `yaml:"syncer,omitempty"`
		Consensus Consensus `yaml:"consensus,omitempty"`
		Log       Log       `yaml:"log,omitempty"`
		Index     Index     `yaml:"index,omitempty"`
	}
)
