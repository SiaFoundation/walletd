---
default: minor
---

# Add transaction construction API

Added two new endpoints to construct transactions with a few restrictions for ease of use. Clients can still use the existing fund endpoints for "advanced" transactions. All addresses in the wallet must all have either unlock conditions with a single required signature or a public key spend policy.

This is a two step process. The private keys are never transmitted to the server.

The client first calls `[POST] /api/:wallet/transaction/construct` with the recipients. The server will construct the transaction and return a list of hashes that the client needs to sign to broadcast the transaction. The client needs to match the returned public keys to their ed25519 private key and sign all of the hashes.

After signing, the client calls `[POST] /api/:wallet/transaction/construct/:id` with the array of signatures. The server will add the signatures to the transaction and broadcast it. If the client provided the correct signatures, the transaction will be added to the tpool and broadcast.

See API docs for request and response bodies