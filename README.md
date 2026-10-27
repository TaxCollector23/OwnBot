# OwnYourBot

Run any model on your own PC. Control it anywhere.

OwnYourBot is an early alpha for running agents on your own machine. Remote pairing, provider setup, and signed releases are not ready yet.

## Install from npm

Requires Node.js 18+ and Go 1.24+.

```sh
npm install -g ownyourbot@alpha
ownyourbot daemon start
```

This starts the local daemon in the background and opens `http://127.0.0.1:8765/dashboard`.

## Run from source

Requires Go 1.24 or later.

```sh
go build -o ./bin/ownbotd ./cmd/ownbotd
go build -o ./bin/ownbot ./cmd/ownbot
./bin/ownbot daemon start
```

The command starts the daemon in the background and opens its dashboard. It creates its local API token in `~/.ownbot/api.token`.

See [TODO.md](TODO.md) and the [release gate](docs/release-gate.md) for release status.
