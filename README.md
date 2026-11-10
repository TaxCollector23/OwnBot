# ownbot

[Docs](https://ownbot-demo.vercel.app/) · [GitHub Pages](https://taxcollector23.github.io/OwnBot/)

Sources: [CLI and daemon](cmd/) · [Documentation site](demo-site/) · [Video](demo-site/scripts/explainer/)

OwnBot runs open-weight models on your computer and lets you control agents from a dashboard on another device on the same private Wi-Fi or Ethernet network. Inference and tool execution stay on the host. Cellular and public internet access are not supported.

## Quick start

```sh
npm install -g ownbot
ownbot setup
```

The npm package is not currently published, and its matching GitHub release assets do not exist yet. These commands will work after the first release. Public installation is gated on the checks in [STATUS.md](STATUS.md) and [the release gate](docs/release-gate.md).

During setup, select Ollama, LM Studio, or Jan; choose or download a local model; review the available permissions; then prepare the host certificate and scan the one-use QR code. Setup probes the model before it creates the agent. A model that fails any required tool-use capability check cannot be used by that agent. Tools start disabled, and every tool call remains subject to the server's runtime policy and approval checks.

Read the [first-run guide](docs/first-run.md) for runtime-specific steps, download behavior, non-interactive flags, and certificate trust on phones.

## Build from a checkout

For development on macOS or Linux, build the two native Go binaries and run setup from the checkout:

```sh
git clone https://github.com/TaxCollector23/OwnBot.git
cd OwnBot
mkdir -p bin
go build -o bin/ownbot ./cmd/ownbot
go build -o bin/ownbotd ./cmd/ownbotd
./bin/ownbot setup
```

This contributor workflow needs Go 1.24 or newer. The intended npm package installs verified platform binaries and does not require Go; `npm run test:install` exercises that installer with a local fixture without Go in `PATH`.

## LAN and privacy

The dashboard stores its browser token in `sessionStorage`, so it survives reloads in that tab and is removed when the tab session ends. The host API token never goes to a paired device. Pair links are short-lived and one-use. LAN uses HTTPS with a host-local certificate; phones may require installing the exported certificate after comparing its SHA-256 fingerprint with the host terminal. Never continue through a browser certificate warning. See the [LAN guide](docs/lan-access.md).

## Docs

[First run](docs/first-run.md) · [Hardware and model checks](docs/hardware.md) · [Inspect and audit](docs/inspect-and-audit.md) · [Review approvals](docs/approvals.md) · [Daemon service](docs/service.md) · [LAN access](docs/lan-access.md) · [Release status](STATUS.md) · [Testing sandbox](docs/testing-sandbox.md) · [Writing docs](docs/WRITING-DOCS.md) · [GitHub Pages guide](https://taxcollector23.github.io/OwnBot/)
