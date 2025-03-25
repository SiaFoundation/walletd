---
default: minor
---

# Add support for custom networks

Adds support for loading custom network parameters from a local file. This makes it easier to setup local testnets for development. A network file can be specified by using a file path for the `--network` CLI flag. The file should be JSON or YAML formatted with the following structure:

```json
{
  "network": {
    "name": "",
    "initialCoinbase": "0",
    "minimumCoinbase": "0",
    "initialTarget": "0000000000000000000000000000000000000000000000000000000000000000",
    "blockInterval": 0,
    "maturityDelay": 0,
    "hardforkDevAddr": {
      "height": 0,
      "oldAddress": "000000000000000000000000000000000000000000000000000000000000000089eb0d6a8a69",
      "newAddress": "000000000000000000000000000000000000000000000000000000000000000089eb0d6a8a69"
    },
    "hardforkTax": {
      "height": 0
    },
    "hardforkStorageProof": {
      "height": 0
    },
    "hardforkOak": {
      "height": 0,
      "fixHeight": 0,
      "genesisTimestamp": "0001-01-01T00:00:00Z"
    },
    "hardforkASIC": {
      "height": 0,
      "oakTime": 0,
      "oakTarget": "0000000000000000000000000000000000000000000000000000000000000000"
    },
    "hardforkFoundation": {
      "height": 0,
      "primaryAddress": "000000000000000000000000000000000000000000000000000000000000000089eb0d6a8a69",
      "failsafeAddress": "000000000000000000000000000000000000000000000000000000000000000089eb0d6a8a69"
    },
    "hardforkV2": {
      "allowHeight": 0,
      "requireHeight": 0
    }
  },
  "genesis": {
    "parentID": "0000000000000000000000000000000000000000000000000000000000000000",
    "nonce": 0,
    "timestamp": "0001-01-01T00:00:00Z",
    "siacoinOutputs": [
        
    ],
    "siafundOutputs": [
        {
            "value": 10000,
            "address": "000000000000000000000000000000000000000000000000000000000000000089eb0d6a8a69"
        }
    ],
    "minerPayouts": null,
    "transactions": null
  }
}
```