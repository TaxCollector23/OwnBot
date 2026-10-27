# OwnBot

Run any model on your own PC. Control it anywhere.

OwnBot is an early development project for running agents on your own machine. Remote pairing, provider setup, and signed releases are not ready yet.

## Run from source

Requires Go 1.24 or later.

```sh
go run ./cmd/ownbotd
```

Open `http://127.0.0.1:8765/dashboard`. The daemon creates its local API token in `~/.ownbot/api.token`.

See [TODO.md](TODO.md) and the [release gate](docs/release-gate.md) for what remains before publishing.
