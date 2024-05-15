# [![Sia](https://sia.tech/assets/banners/sia-banner-expanded-walletd.png)](http://sia.tech)

[![GoDoc](https://godoc.org/go.sia.tech/walletd?status.svg)](https://godoc.org/go.sia.tech/walletd)

## Overview

`walletd` is the flagship Sia wallet, suitable for miners, exchanges, and
everyday hodlers. Its client-server architecture gives you the flexibility
to access your funds from anywhere, on any device, without compromising the
security of your private keys. The server is agnostic, so you can derive
those keys from a 12-word seed phrase, a legacy (`siad`) 28-word phrase, a
Ledger hardware wallet, or another preferred method. Like other Foundation
node software, `walletd` ships with a slick embedded UI, but developers can
easily build headless integrations leveraging its powerful JSON API. Whether
you're using a single address or millions, `walletd` scales to your needs.

Setup guides are available at https://docs.sia.tech

### Index Mode
`walletd` supports three different index modes for different use cases.

**Personal**

In "personal" index mode, `walletd` will only index addresses that are registered in the
wallet. This mode is recommended for most users, as it provides a good balance between
comprehensiveness and resource usage for personal wallets. This is the default 
mode for `walletd`.

When adding addresses with existing history on chain, users will need to manually 
initiate a rescan to index the new transactions. This can take some to complete,
depending on the number of blocks that need to be scanned. When adding addresses 
with no existing history, a rescan is not necessary.

**Full**

In "full" index mode, `walletd` will index the entire blockchain including all addresses
and UTXOs. This is the most comprehensive mode, but it also requires the most 
resources. This mode is recommended for exchanges or wallet builders that need 
to support a large or unknown number of addresses.

**None**

In "none" index mode, `walletd` will treat the database as read-only and not 
index any new data. This mode is only useful in situations where another process
is managing the database and `walletd` is only being used to read data.

## Configuration

`walletd` can be configured in multiple ways. Some settings, like the API password,
can be configured via environment variable. Others, like the API port, and data
directory, can be set via command line flags. To simplify more complex configurations,
`walletd` can also be configured via a YAML file.

The priority of configuration settings is as follows:
1. Command line flags
2. YAML file
3. Environment variables

### Default Ports
+ `9980` UI and API
+ `9981` Sia consensus

### Environment Variables
+ `WALLETD_API_PASSWORD` - The password required to access the API

### Command Line Flags
```
-addr string
	p2p address to listen on (default ":9981")
-bootstrap
	attempt to bootstrap the network (default true)
-dir string
	directory to store node state in (default "/Users/n8maninger/Downloads/walletd-tmp")
-http string
	address to serve API on (default "localhost:9980")
-index.batch int
	max number of blocks to index at a time. Increasing this will increase scan speed, but also increase memory and cpu usage. (default 64)
-index.mode string
	address index mode (personal, full, none) (default "full")
-network string
	network to connect to (default "mainnet")
-upnp
	attempt to forward ports and discover IP with UPnP
```

### YAML
All configuration settings can be set in a YAML file. The file should be named 
`walletd.yaml` in the working directory. All fields are optional.
```yaml
directory: /etc/walletd
autoOpenWebUI: true
http:
  address: :9980
  password: sia is cool
consensus:
  network: mainnet
  gatewayAddress: :9981
  bootstrap: false
  enableUPnP: false
index:
  mode: personal # personal, full, none ("full" will index the entire blockchain, "personal" will only index addresses that are registered in the wallet, "none" will treat the database as read-only and not index any new data)
  batchSize: 64 # max number of blocks to index at a time (increasing this will increase scan speed, but also increase memory and cpu usage)
log:
  level: info # global log level
  stdout:
    enabled: true # enable logging to stdout
    level: debug # override the global log level for stdout
    enableANSI: false
    format: human # human or JSON
  file:
    enabled: true # enable logging to a file
    level: debug # override the global log level for the file
    path: /var/log/walletd.log
    format: json # human or JSON
```

## Building
`walletd` uses SQLite for its persistence. A gcc toolchain is required to build `walletd`

```sh
go generate ./...
CGO_ENABLED=1 go build -o bin/ -tags='netgo timetzdata' -trimpath -a -ldflags '-s -w' ./cmd/walletd
```

## Docker Image
`walletd` includes a Dockerfile for building a Docker image. For building and 
running `walletd` within a Docker container. The image can also be pulled from `ghcr.io/siafoundation/walletd`.

```sh
docker run -d \
	--name walletd \
	-p 127.0.0.1:9980:9980 \
	-p 9981:9981 \
	-v /data:/data \
	ghcr.io/siafoundation/walletd:latest
```

### Docker Compose
```yml
services:
  walletd:
    image: ghcr.io/siafoundation/walletd:latest
    ports:
      - 127.0.0.1:9980:9980/tcp
      - 9981:9981/tcp
    volumes:
      - /data:/data
    restart: unless-stopped
```

### Building

```sh
docker buildx build --platform linux/amd64,linux/arm64 -t ghcr.io/siafoundation/walletd:master .
```