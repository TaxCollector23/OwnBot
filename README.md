# OwnYourBot

Run agents on your own computer and review their work in a local dashboard. Alpha LAN pairing lets you open that dashboard from another device on the same Wi-Fi network. Internet and cellular access are future work.

## Install from npm

Requires Node.js 18+ and Go 1.24+.

```sh
npm install -g ownyourbot@alpha
ownyourbot daemon start
```

The dashboard opens at `http://127.0.0.1:8765/dashboard`. Its local API token is stored at `~/.ownbot/api.token`.
When the CLI opens the dashboard, it uses a one-time loopback-only link to establish a 30-minute owner session in that browser tab. The API token is not placed in the link. If you open the URL manually and see a locked dashboard, run `ownyourbot daemon start` on the host to open an owner session.

## Pair a device on the same Wi-Fi

On the host computer, start the daemon with LAN access:

```sh
ownyourbot daemon start --lan
```

If it is already running without LAN access, restart it first:

```sh
ownyourbot daemon stop
ownyourbot daemon start --lan
```

The CLI prints the HTTPS LAN address and certificate fingerprint, then opens the local dashboard with an owner session. Create a one-use pairing link in **LAN access**. Open that link on the device you want to pair and give the device a name. The host dashboard lists paired devices and can revoke them.

The local API defaults to `127.0.0.1:8765`. If another service is using that port, select a free loopback port, for example `ownyourbot daemon start --lan --listen 127.0.0.1:18765`, and open `http://127.0.0.1:18765/dashboard` on the host. Stop that daemon with `ownyourbot daemon stop --url http://127.0.0.1:18765` before restarting it.

The self-signed HTTPS certificate needs to be checked and trusted on each browser device. Follow [the LAN access guide](docs/lan-access.md) before pairing. Pairing works only over the same local network; it does not provide cellular or public internet access.

## Run from source

Requires Go 1.24 or later.

```sh
go build -o ./bin/ownbotd ./cmd/ownbotd
go build -o ./bin/ownbot ./cmd/ownbot
./bin/ownbot daemon start
```

See [TODO.md](TODO.md), [the LAN access guide](docs/lan-access.md), and the [release gate](docs/release-gate.md) for implementation and verification status.
