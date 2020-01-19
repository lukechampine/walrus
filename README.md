walrus
======

[![GoDoc](https://godoc.org/lukechampine.com/walrus?status.svg)](https://godoc.org/lukechampine.com/walrus)
[![Go Report Card](https://goreportcard.com/badge/lukechampine.com/walrus)](https://goreportcard.com/report/lukechampine.com/walrus)

`walrus` is a Sia wallet server. It presents a low-level, performant API that is
suitable for private, professional, and commercial use. The server itself does not
store seeds or private keys, and therefore cannot sign transactions; these
responsibilities are handled by the client. Accordingly, `walrus` works well
with Sia-compatible hardware wallets such as the [Ledger Nano
S](https://github.com/LedgerHQ/nanos-app-sia).

API docs for the server are available [here](https://lukechampine.com/docs/walrus).

A client for `walrus` is available [here](https://github.com/lukechampine/walrus-cli).
The client facilitates constructing, signing, and broadcasting transactions, and
supports both hot wallets and hardware wallets.


## Running a `walrus` server

If you plan to expose your `walrus` API to the public internet, it is highly
recommended that you add HTTPS and HTTP Basic Authentication via a reverse
proxy. Without these security measures, an attacker would still be unable to
access your private keys, but they *could* potentially trick you into losing
funds. Better safe than sorry.

In addition, if you want to access your wallet via a browser (such as [Sia
Central's Lite Wallet](https://wallet.siacentral.com)), you will need to
enable CORS. Refer to the following documentation based on your reverse proxy:

- Nginx:
    - [HTTPS](https://gist.github.com/cecilemuller/a26737699a7e70a7093d4dc115915de8)
    - [Basic Auth](https://docs.nginx.com/nginx/admin-guide/security-controls/configuring-http-basic-authentication/)
    - [CORS](https://enable-cors.org/server_nginx.html)
- Apache:
    - [HTTPS](https://www.digitalocean.com/community/tutorials/how-to-secure-apache-with-let-s-encrypt-on-ubuntu-18-04)
    - [Basic Auth](https://httpd.apache.org/docs/2.4/howto/auth.html)
    - [CORS](https://enable-cors.org/server_apache.html)
- Caddy:
    - [HTTPS](https://caddyserver.com/v1/docs/automatic-https)
    - [Basic Auth](https://caddyserver.com/v1/docs/basicauth)
    - [CORS](https://enable-cors.org/server_caddy.html)
