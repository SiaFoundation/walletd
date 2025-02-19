---
default: minor
---

# Added Spent Element Endpoints

Added two new endpoints `[GET] /outputs/siacoin/:id/spent` and `[GET] /outputs/siafund/:id/spent`. These endpoints will return a boolean, indicating whether the UTXO was spent, and the transaction it was spent in. These endpoints are designed to make verifying Atomic swaps easier.

#### Example Usage

````
$ curl http://localhost:9980/api/outputs/siacoin/9b89152bb967130326702c9bfb51109e9f80274ec314ba58d9ef49b881340f2f/spent
{
    spent: true,
    transaction: {}
}
```
