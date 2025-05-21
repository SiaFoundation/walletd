## 2.6.0 (2025-05-21)

### Features

#### Changed all endpoints that return Siacoin or Siafund elements to also return the number of confirmations

```json
{
    {
        "id": "5fb7f9ef38dfeeeb4d8c0c1f105452511f0e966dec1ce545e490f5eee46d166f",
        "stateElement": {
        "leafIndex": 25490,
        "merkleProof": [
            "9175d0ea4dbdecd0517bd275afd98250438193429d0dc7493672217464f3bfa3",
            "ab87ecba97723b67e42027dd8d2ad5a51ab48c3cd1b38dc461805b266b1fa728",
            "6cb7dcc6300e8344b17012b36fe64a0d7e1678d54736d1fd910c7c9665b273b9"
        ]
        },
        "siacoinOutput": {
        "value": "344000",
        "address": "000000000000000000000000000000000000000000000000000000000000000089eb0d6a8a69"
        },
        "maturityHeight": 7437,
        "confirmations": 6
    }
}
```

## 2.5.0 (2025-05-16)

### Features

- Added [GET] /health to check the health of the walletd node
- Consensus checkpoint and block endpoints now support lookups by height

## 2.4.1 (2025-05-14)

### Fixes

- Fix race in txpool broadcast

## 2.4.0 (2025-05-14)

### Features

- Added GET /consensus/checkpoint/:id which returns the block and its consensus state.

#### Return transaction sets from broadcast endpoint

This lets integrators get the IDs of the created UTXOs, addresses of inputs, and IDs of the transactions

### Fixes

#### Update core to v0.12.2 and coreutils to v0.13.4

These releases include additional JSON convenience fields

## 2.3.0 (2025-05-12)

### Features

- Added CLI flag to disable log locations `--log.file.enabled=false` `--log.stdout.enabled=false`
- Added CLI flag to set log level `--log.level=debug`

#### Added `[POST] /check/addresses` to check for addresses that have been seen on chain

This endpoint is useful for scanning the chain for look-aheads when in full index mode

## 2.2.1 (2025-04-24)

### Fixes

- Update core to v0.11.0 and coreutils to v0.13.1

## 2.2.0 (2025-04-22)

### Features

#### Address endpoints can now exclude transaction pool utxos

## Transaction broadcasts can now discover parents already in the transaction pool.

#### Add support for custom networks

Adds support for loading custom network parameters from a local file. This makes it easier to setup local testnets for development. A network file can be specified by using a file path for the `--network` CLI flag. The file should be JSON formatted with the following structure:

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
      "oakTarget": "0000000100000000000000000000000000000000000000000000000000000000"
    },
    "hardforkFoundation": {
      "height": 30,
      "primaryAddress": "053b2def3cbdd078c19d62ce2b4f0b1a3c5e0ffbeeff01280efb1f8969b2f5bb4fdc680f0807",
      "failsafeAddress": "000000000000000000000000000000000000000000000000000000000000000089eb0d6a8a69"
    },
    "hardforkV2": {
      "allowHeight": 112000,
      "requireHeight": 114000
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

## 2.1.0 (2025-03-25)

### Features

#### Added Spent Element Endpoints

Added two new endpoints `[GET] /outputs/siacoin/:id/spent` and `[GET] /outputs/siafund/:id/spent`. These endpoints will return a boolean, indicating whether the UTXO was spent, and the transaction it was spent in. These endpoints are designed to make verifying Atomic swaps easier.

##### Example Usage

````
$ curl http://localhost:9980/api/outputs/siacoin/9b89152bb967130326702c9bfb51109e9f80274ec314ba58d9ef49b881340f2f/spent
{
    spent: true,
    event: {}
}
```

#### Fixes sending V2 transactions in the UI

- Fixes V2 signing for wallets that do not have siafund outputs. Fixes #247

## 2.0.0 (2025-02-21)

### Breaking Changes

#### Add Merkle Proof Basis to UTXO API Responses

Changes the response to include the Merkle proof basis for the following endpoints:
- `[GET] /addresses/:address/outputs/siacoin`
- `[GET] /addresses/:address/outputs/siafund`
- `[GET] /wallets/:id/outputs/siacoin`
- `[GET] /wallets/:id/outputs/siafund`


```json
{
    "basis": {
        "height": 1,
        "id": "f362385eea61f81627f283a31af9faf6417fbb88d53b794639a34e18515996e9"
    },
    "outputs": [
        {
            "id": "ed556177482e70822a5dcad9343efb51998425884788415349bef8eba7e063ae",
            "stateElement": {
            "leafIndex": 3,
            "merkleProof": [
                "01048fc792904f156844a5524671304d3a020861da144afa4acc6553db63c1fd",
                "33efdfaf9bb212842292ab6f298c454e1b3d412aa7beb7efdccdfccf09f5b4ee",
                "102345919e408540d240460b0d84aa2f6da9a3d8f74765fd7c6daae6e46dd7f3"
            ]
            },
            "siacoinOutput": {
                "value": "500000000000000000000000",
                "address": "fbfc3d034b1eb45f63e0087571ec1f3028a9a2f8c180381d47713e6112467d91f474059476f2"
            },
            "maturityHeight": 0
        }
    ]
}
```

#### Simplified response of consensus updates endpoint

The response of `/api/consensus/updates/:index` has been simplified to make it easier for developers to index chain state.

```json
{
	"applied": [
		{
			"update": {
				"siacoinElements": [
					{
						"siacoinElement": {
							"id": "35b81e41f594d7faeb88bd8eaac2eaa68ce99fe1c8fe5f0cba8fafa65ab3a70e",
							"stateElement": {
								"leafIndex": 0,
								"merkleProof": [
									"88052fa2d1e22e4a5542fed9686cdad3fbeccbc60d15d4fd36a7691d61add1e1"
								]
							},
							"siacoinOutput": {
								"value": "1000000000000000000000000000000000000",
								"address": "3d7f707d05f2e0ec7ccc9220ed7c8af3bc560fbee84d068c2cc28151d617899e1ee8bc069946"
							},
							"maturityHeight": 0
						},
						"created": true,
						"spent": false
					}
				],
				"siafundElementDiffs": [
					{
						"siafundElement": {
							"id": "69ad26a0fbd1a6985d2053246650bb3ba5f3491d818748b6c8562db1ddb2c45b",
							"stateElement": {
								"leafIndex": 1,
								"merkleProof": [
									"837482a39d5bf66f07bae3b89191e4375b82c9f341ce6a17e22e14e0333ab9f6"
								]
							},
							"siafundOutput": {
								"value": 10000,
								"address": "053b2def3cbdd078c19d62ce2b4f0b1a3c5e0ffbeeff01280efb1f8969b2f5bb4fdc680f0807"
							},
							"claimStart": "0"
						},
						"created": true,
						"spent": false
					}
				],
				"fileContractElementDiffs": null,
				"v2FileContractElementDiffs": null,
				"attestationElements": null,
				"chainIndexElement": {
					"id": "e23d2ee56fc5c79618ead2f8f36c1b72c6f3ec5e0f751c05e08bd6665a6ec22a",
					"stateElement": {
						"leafIndex": 2
					},
					"chainIndex": {
						"height": 0,
						"id": "e23d2ee56fc5c79618ead2f8f36c1b72c6f3ec5e0f751c05e08bd6665a6ec22a"
					}
				},
				"updatedLeaves": {},
				"treeGrowth": {},
				"oldNumLeaves": 0,
				"numLeaves": 3
			},
			"state": {
				"index": {
					"height": 0,
					"id": "e23d2ee56fc5c79618ead2f8f36c1b72c6f3ec5e0f751c05e08bd6665a6ec22a"
				},
				"prevTimestamps": [
					"2023-01-13T00:53:20-08:00",
					"0001-01-01T00:00:00Z",
					"0001-01-01T00:00:00Z",
					"0001-01-01T00:00:00Z",
					"0001-01-01T00:00:00Z",
					"0001-01-01T00:00:00Z",
					"0001-01-01T00:00:00Z",
					"0001-01-01T00:00:00Z",
					"0001-01-01T00:00:00Z",
					"0001-01-01T00:00:00Z",
					"0001-01-01T00:00:00Z"
				],
				"depth": "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
				"childTarget": "0000000100000000000000000000000000000000000000000000000000000000",
				"siafundTaxRevenue": "0",
				"oakTime": 0,
				"oakTarget": "00000000ffffffff00000000ffffffff00000000ffffffff00000000ffffffff",
				"foundationSubsidyAddress": "053b2def3cbdd078c19d62ce2b4f0b1a3c5e0ffbeeff01280efb1f8969b2f5bb4fdc680f0807",
				"foundationManagementAddress": "000000000000000000000000000000000000000000000000000000000000000089eb0d6a8a69",
				"totalWork": "1",
				"difficulty": "4294967295",
				"oakWork": "4294967297",
				"elements": {
					"numLeaves": 3,
					"trees": [
						"e1c3af98d77463b767d973f8a563947d949d06428ff145db30143a2811d10014",
						"134b1f08aec0c7fbc50203a514277d197947e3da3ab1854749bf093b56402912"
					]
				},
				"attestations": 0
			},
			"block": {
				"parentID": "0000000000000000000000000000000000000000000000000000000000000000",
				"nonce": 0,
				"timestamp": "2023-01-13T00:53:20-08:00",
				"minerPayouts": [],
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
		},
		{
			"update": {
				"siacoinElements": [
					{
						"siacoinElement": {
							"id": "ca02d6807c92f61af94e626604615fbcdb471f38fcd8f3add6c6e6e0485ce090",
							"stateElement": {
								"leafIndex": 3,
								"merkleProof": [
									"e1c3af98d77463b767d973f8a563947d949d06428ff145db30143a2811d10014",
									"134b1f08aec0c7fbc50203a514277d197947e3da3ab1854749bf093b56402912"
								]
							},
							"siacoinOutput": {
								"value": "300000000000000000000000000000",
								"address": "c5e1ca930f193cfe4c72eaed8d3bbae627f67d6c8e32c406fe692b1c00b554f4731fddf2c752"
							},
							"maturityHeight": 145
						},
						"created": true,
						"spent": false
					}
				],
				"siafundElementDiffs": null,
				"fileContractElementDiffs": null,
				"v2FileContractElementDiffs": null,
				"attestationElements": null,
				"chainIndexElement": {
					"id": "0000000028e731f0bb5d48662283bec83cca9427581b948d1036deb2b42c3006",
					"stateElement": {
						"leafIndex": 4
					},
					"chainIndex": {
						"height": 1,
						"id": "0000000028e731f0bb5d48662283bec83cca9427581b948d1036deb2b42c3006"
					}
				},
				"updatedLeaves": {},
				"treeGrowth": {
					"0": [
						"190d98a7d8ff464e57f89dc916b155455ecf927f4c74b9edf5e80c103f052bfa",
						"134b1f08aec0c7fbc50203a514277d197947e3da3ab1854749bf093b56402912"
					],
					"1": [
						"2b082bec52801c1e61e5b0d0c1f5fc3925bd24e16d2f490afeb70374828586f1"
					]
				},
				"oldNumLeaves": 3,
				"numLeaves": 5
			},
			"state": {
				"index": {
					"height": 1,
					"id": "0000000028e731f0bb5d48662283bec83cca9427581b948d1036deb2b42c3006"
				},
				"prevTimestamps": [
					"2023-01-13T08:18:19-08:00",
					"2023-01-13T00:53:20-08:00",
					"0001-01-01T00:00:00Z",
					"0001-01-01T00:00:00Z",
					"0001-01-01T00:00:00Z",
					"0001-01-01T00:00:00Z",
					"0001-01-01T00:00:00Z",
					"0001-01-01T00:00:00Z",
					"0001-01-01T00:00:00Z",
					"0001-01-01T00:00:00Z",
					"0001-01-01T00:00:00Z"
				],
				"depth": "00000000ffffffff00000000ffffffff00000000ffffffff00000000ffffffff",
				"childTarget": "0000000100000000000000000000000000000000000000000000000000000000",
				"siafundTaxRevenue": "0",
				"oakTime": 26699000000000,
				"oakTarget": "000000008052201448053c59f99803e7a8165929036cd574d91425423191387c",
				"foundationSubsidyAddress": "053b2def3cbdd078c19d62ce2b4f0b1a3c5e0ffbeeff01280efb1f8969b2f5bb4fdc680f0807",
				"foundationManagementAddress": "000000000000000000000000000000000000000000000000000000000000000089eb0d6a8a69",
				"totalWork": "4294967297",
				"difficulty": "4294967295",
				"oakWork": "8568459756",
				"elements": {
					"numLeaves": 5,
					"trees": [
						"589fb425faa23be357492394813dc575505899d42d0b23a7162e1c68f7eeb227",
						"750cc671d80aef6ee5c73344ba4e74eccda77d9f0cf51ed6237952b1d84bc336"
					]
				},
				"attestations": 0
			},
			"block": {
				"parentID": "e23d2ee56fc5c79618ead2f8f36c1b72c6f3ec5e0f751c05e08bd6665a6ec22a",
				"nonce": 10689346,
				"timestamp": "2023-01-13T08:18:19-08:00",
				"minerPayouts": [
					{
						"value": "300000000000000000000000000000",
						"address": "c5e1ca930f193cfe4c72eaed8d3bbae627f67d6c8e32c406fe692b1c00b554f4731fddf2c752"
					}
				],
				"transactions": [
					{
						"id": "1148417ad8fa6546646da6922618358210bc7a668ef7cb25f6a8a3605851bc7b",
						"arbitraryData": [
							"Tm9uU2lhAAAAAAAAAAAAAClvJjNhfcbxtEfP2yfbBM4="
						]
					}
				]
			}
		}
	],
	"reverted": null
}
```

#### Support V2 Hardfork

The V2 hardfork is scheduled to modernize Sia's consensus protocol, which has been untouched since Sia's mainnet launch back in 2014, and improve accessibility of the storage network. To ensure a smooth transition from V1, it will be executed in two phases. Additional documentation on upgrading will be released in the near future.

##### V2 Highlights
- Drastically reduces blockchain size on disk
- Improves UTXO spend policies - including HTLC support for Atomic Swaps
- More efficient contract renewals - reducing lock up requirements for hosts and renters
- Improved transfer speeds - enables hot storage

##### Phase 1 - Allow Height
- **Activation Height:** `52600` (June 6th, 2025)
- **New Features:** V2 transactions, contracts, and RHP4
- **V1 Support:** Both V1 and V2 will be supported during this phase
- **Purpose:** This period gives time for integrators to transition from V1 to V2
- **Requirements:** Users will need to update to support the hardfork before this block height

##### Phase 2 - Require Height
- **Activation Height:** `530000` (July 6th, 2025)
- **New Features:** The consensus database can be trimmed to only store the Merkle proofs
- **V1 Support:** V1 will be disabled, including RHP2 and RHP3. Only V2 transactions will be accepted
- **Requirements:** Developers will need to update their apps to support V2 transactions and RHP4 before this block height

#### Use standard locations for application data

Uses standard locations for application data instead of the current directory. This brings `walletd` in line with other system services and makes it easier to manage application data.

##### Linux, FreeBSD, OpenBSD
- Configuration: `/etc/walletd/walletd.yml`
- Data directory: `/var/lib/walletd`

##### macOS
- Configuration: `~/Library/Application Support/walletd.yml`
- Data directory: `~/Library/Application Support/walletd`

##### Windows
- Configuration: `%APPDATA%\SiaFoundation\walletd.yml`
- Data directory: `%APPDATA%\SiaFoundation\walletd`

##### Docker
- Configuration: `/data/walletd.yml`
- Data directory: `/data`

### Features

- Add basis to wallet fund endpoints
- Log startup errors to stderr

#### Add transaction construction API

Adds two new endpoints to construct transactions. This combines and simplifies the existing fund flow for simple send transactions.

See API docs for request and response bodies

### Fixes

- Added a test for migrations to ensure consistency between database schemas

## 0.8.0

This is the first stable release for the walletd app -- the new reference wallet for users and exchanges

### Breaking changes

- SiaFund support
- Ledger hardware wallet support
- Multi-wallet support
- Full index mode for exchanges and wallet integrators
- Redesigned events list
- Redesigned transaction flow
