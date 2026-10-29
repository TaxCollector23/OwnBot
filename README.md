# ownbot

[Demo](https://ownbot-demo.vercel.app/demos) · [Docs](https://taxcollector23.github.io/OwnBot/)

Sources: [CLI and daemon](cmd/) · [Website and videos](demo-site/) · [Recording scripts](demo-site/scripts/)

Configure agents in the CLI. Use the dashboard to run tasks, review results, and approve writes. The daemon stays on your computer; local-network pairing works only on the same private Wi-Fi or Ethernet network. Cellular and public internet access are not supported.

## Install from source

Requires Node.js 18+ and Go 1.24+.

```sh
git clone https://github.com/TaxCollector23/OwnBot.git
cd OwnBot
npm install -g . --allow-scripts=ownbot
ownbot setup
```

`setup` detects installed models in Ollama, LM Studio, llama.cpp, or vLLM, probes the selected model, then creates a text-only agent with no tools. It does not download a model or use a hosted provider key. If no model appears, install a local runtime, download a model you choose, then run setup again. For a model already installed, headless setup can use `ownbot setup --yes --model MODEL_ID`.

Open the dashboard at <http://127.0.0.1:8765/dashboard>. The CLI starts the daemon and manages its local API token automatically. The token is daemon security for local CLI/dashboard access; it is not a model-provider key. It is generated under `~/.ownbot/` and does not need to be created or copied for CLI setup.

## Use over SSH

On the host, install from source as above and run `ownbot setup`. Start `ownbot daemon start` from the SSH session. If it cannot open a browser, the CLI prints a one-time owner link and an SSH tunnel command. Keep the tunnel open, then open that link on your laptop. The token remains on the host.

For LAN pairing, use `ownbot daemon start --lan` and follow the [LAN guide](docs/lan-access.md). Pairing is limited to the same private network.

## Docs

[Homebrew](docs/homebrew.md) · [Hardware and model checks](docs/hardware.md) · [Daemon service](docs/service.md) · [LAN access](docs/lan-access.md) · [Release status](STATUS.md) · [Writing docs](docs/WRITING-DOCS.md) · [GitHub Pages guide](https://taxcollector23.github.io/OwnBot/)

After the `ownbot` package is published to npm, install it with `npm install -g ownbot@alpha`.
