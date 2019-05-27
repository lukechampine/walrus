walrus
======

[![GoDoc](https://godoc.org/lukechampine.com/walrus?status.svg)](https://godoc.org/lukechampine.com/walrus)
[![Go Report Card](https://goreportcard.com/badge/lukechampine.com/walrus)](https://goreportcard.com/report/lukechampine.com/walrus)

`walrus` is a Sia wallet server that can be used as a traditional hot wallet or
as part of a cold wallet/watch-only wallet setup. It presents a low-level,
performant API that is suitable for both user-facing apps and exchanges. In
particular, `walrus` strives to integrate smoothly with Sia-compatible hardware
wallets such as the [Ledger Nano S](https://github.com/LedgerHQ/nanos-app-sia).

If you are an end-user who simply wants to store your siacoins, you probably
want [`walrus-cli`](https://github.com/lukechampine/walrus-cli), a CLI frontend
for `walrus`.

API docs for the server are available [here](https://lukechampine.com/docs/walrus).


## Usage

To start the server in hot wallet mode, first you'll need to generate a seed
with `walrus seed`. (Don't be alarmed: `walrus` seeds are only 15 words long.)
Then start the server with `walrus start` and enter your seed at the prompt. You
can bypass the prompt by storing your seed in the `WALRUS_SEED` environment
variable. You may then use the hot wallet API routes.

To start the server in watch-only mode, run `walrus start --watch-only`. You may
then use the watch-only API routes.