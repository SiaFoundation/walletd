---
default: minor
---

# Add ed25519 key store

Adds an optional ed25519 signing key store for integrators to store arbitrary private keys for signing transactions. It allows for both generating private keys on the server and importing private keys. 


*The endpoint will return 404 if the `--public` CLI flag is set. It is only recommended for use on localhost. It is not used by the UI.*

```go

client := api.NewClient(walletAddr, walletdPassword)

pubKey, err := client.GenerateSigningKey()
if err != nil {
    panic(err)
}

sig, err := client.SignHash(pubKey, hash)
if err != nil {
    panic(err)
}
```
