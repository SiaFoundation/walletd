---
default: major
---

# Add Merkle Proof Basis to UTXO API Responses

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