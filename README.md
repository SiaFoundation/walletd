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
+ `WALLETD_API_PASSWORD` - The password required to access the API.
+ `WALLETD_CONFIG_FILE` - The path to the YAML configuration file. Defaults to `walletd.yml` in the working directory.
+ `WALLETD_LOG_FILE` - The path to the log file.

### Command Line Flags
```
Usage:
    walletd [flags] [action]

Run 'walletd' with no arguments to start the blockchain node and API server.

Actions:
    version     print walletd version
    seed        generate a recovery phrase
    mine        run CPU miner
Flags:
  -addr string
        p2p address to listen on (default ":9981")
  -bootstrap
        attempt to bootstrap the network (default true)
  -checkpoint
        instant-sync to a chain index, e.g. 530000::0000000000000000abb98e3b587fba3a0c4e723ac1e078e9d6a4d13d1d131a2c
  -debug
        enable debug mode with additional profiling and mining endpoints
  -dir string
        directory to store node state in (default "/Users/username/Library/Application Support/walletd")
  -http string
        address to serve API on (default "localhost:9980")
  -http.public
        disables auth on endpoints that should be publicly accessible when running walletd as a service
  -index.batch int
        max number of blocks to index at a time. Increasing this will increase scan speed, but also increase memory and cpu usage. (default 1000)
  -index.mode string
        address index mode (personal, full, none) (default "personal")
  -network string
        network to connect to; must be one of 'mainnet', 'zen', 'anagami', or the path to a custom network file for a local testnet
  -upnp
        attempt to forward ports and discover IP with UPnP
```

### YAML
All configuration settings can be set in a YAML file. The default location of that file is
- `/etc/walletd/walletd.yml` on Linux
- `~/Library/Application Support/walletd/walletd.yml` on macOS
- `%APPDATA%\SiaFoundation\walletd.yml` on Windows
- `/data/walletd.yml` in the Docker container

It can be generated using the `walletd config` command. Alternatively a local
configuration can be created manually by creating a file name `walletd.yml` in
the working directory. All fields are optional.
```yaml
directory: /etc/walletd
autoOpenWebUI: true
checkpoint: 530000::0000000000000000abb98e3b587fba3a0c4e723ac1e078e9d6a4d13d1d131a2c
http:
  address: :9980
  password: sia is cool
  publicEndpoints: false # when true, auth will be disabled on endpoints that should be publicly accessible when running walletd as a service
consensus:
  network: mainnet
syncer:
  bootstrap: false
  enableUPnP: false
  peers: []
  address: :9981
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
    checkpoint: 530000::0000000000000000abb98e3b587fba3a0c4e723ac1e078e9d6a4d13d1d131a2c
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

### Creating a local testnet

You can create a custom local testnet by creating a network.json file locally and passing the path to the `--network` CLI flag (i.e. `walletd --network="/var/lib/testnet.json"`). An example file is shown below. You can adjust the parameters of your testnet to increase mining speed and test hardfork activations.

```json
{
  "network": {
    "name": "zen",
    "initialCoinbase": "300000000000000000000000000000",
    "minimumCoinbase": "30000000000000000000000000000",
    "initialTarget": "0000000100000000000000000000000000000000000000000000000000000000",
    "blockInterval": 600000000000,
    "maturityDelay": 144,
    "hardforkDevAddr": {
      "height": 1,
      "oldAddress": "000000000000000000000000000000000000000000000000000000000000000089eb0d6a8a69",
      "newAddress": "000000000000000000000000000000000000000000000000000000000000000089eb0d6a8a69"
    },
    "hardforkTax": {
      "height": 2
    },
    "hardforkStorageProof": {
      "height": 5
    },
    "hardforkOak": {
      "height": 10,
      "fixHeight": 12,
      "genesisTimestamp": "2023-01-13T00:53:20-08:00"
    },
    "hardforkASIC": {
      "height": 20,
      "oakTime": 10000000000000,
      "oakTarget": "0000000100000000000000000000000000000000000000000000000000000000",
      "nonceFactor": 1009
    },
    "hardforkFoundation": {
      "height": 30,
      "primaryAddress": "053b2def3cbdd078c19d62ce2b4f0b1a3c5e0ffbeeff01280efb1f8969b2f5bb4fdc680f0807",
      "failsafeAddress": "000000000000000000000000000000000000000000000000000000000000000089eb0d6a8a69"
    },
    "hardforkV2": {
      "allowHeight": 112000,
      "requireHeight": 114000,
      "finalCutHeight": 116000,
    }
  },
  "genesis": {
    "parentID": "0000000000000000000000000000000000000000000000000000000000000000",
    "nonce": 0,
    "timestamp": "2023-01-13T00:53:20-08:00",
    "minerPayouts": null,
    "transactions": [
      {
        "id": "268ef8627241b3eb505cea69b21379c4b91c21dfc4b3f3f58c66316249058cfd",
        "siacoinOutputs": [
          {
            "value": "1000000000000000000000000000000000000",
            "address": "3d7f707d05f2e0ec7ccc9220ed7c8af3bc560fbee84d068c2cc28151d617899e1ee8bc069946"
          }
        ],
        "siafundOutputs": [
          {
            "value": 10000,
            "address": "053b2def3cbdd078c19d62ce2b4f0b1a3c5e0ffbeeff01280efb1f8969b2f5bb4fdc680f0807"
          }
        ]
      }
    ]
  }
}
```
