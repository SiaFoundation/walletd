---
default: major
---

# Simplified response of consensus updates endpoint

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