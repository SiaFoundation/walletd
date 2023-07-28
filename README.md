# [![Sia](https://sia.tech/assets/banners/sia-banner-expanded-walletd.png)](http://sia.tech)

[![GoDoc](https://godoc.org/go.sia.tech/walletd?status.svg)](https://godoc.org/go.sia.tech/walletd)

A new wallet for Sia.

`walletd` is a watch-only wallet server. It does not have access to any private
keys, only addresses derived from those keys. Its role is to watch the
blockchain for events relevant to particular addresses. The server therefore
knows which outputs are spendable by the wallet at any given time, and can
assist in constructing and broadcasting transactions spending those outputs.
However, *signing* transactions is the sole responsibility of the client.
