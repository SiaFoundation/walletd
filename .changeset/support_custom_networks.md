---
default: minor
---

# Add support for custom networks

Adds support for loading custom network parameters from a local file. This makes it easier to setup local testnets for development. A network file can be specified by using a file path for the `--network` CLI flag. The file should be JSON or YAML formatted with the following structure:

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