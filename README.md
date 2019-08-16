walrus
======

[![GoDoc](https://godoc.org/lukechampine.com/walrus?status.svg)](https://godoc.org/lukechampine.com/walrus)
[![Go Report Card](https://goreportcard.com/badge/lukechampine.com/walrus)](https://goreportcard.com/report/lukechampine.com/walrus)

`walrus` is a Sia wallet server. It presents a low-level, performant API that is
suitable for both user-facing apps and exchanges. The server itself does not
store seeds or private keys, and therefore cannot sign transactions; these
responsibilities are handled by the client. Accordingly, `walrus` works well
with Sia-compatible hardware wallets such as the [Ledger Nano
S](https://github.com/LedgerHQ/nanos-app-sia).

API docs for the server are available [here](https://lukechampine.com/docs/walrus).

A client for `walrus` is available [here](https://lukechampine.com/walrus-cli).
The client facilitates constructing, signing, and broadcasting transactions, and
supports both hot wallets and hardware wallets.
