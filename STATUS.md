# Release status

Audit: 2026-10-27. OwnYourBot remains an alpha. Repository tests and package checks are local development evidence, not a clean-machine or live-service release sign-off.

## Verified in this checkout

- `npm test` passes, including the Go tests and dashboard authentication, runtime, and LAN pairing regressions.
- `npm pack --dry-run` succeeds for `ownyourbot@0.1.0-alpha.4`.
- LAN daemon startup supports `ownyourbot daemon start --lan`; device pairing and revocation have automated server and dashboard coverage.
- The [isolated LAN lab workflow](https://github.com/TaxCollector23/OwnBot/actions/runs/40557519399) passed on commit `93e3221`. It exercised the real HTTPS listener across two Linux Docker bridge containers, including loopback isolation, pairing, replay rejection, separate device credentials, revocation, and restart persistence.
- CLI and daemon cross-compile for macOS, Linux, FreeBSD, and Windows targets. Runtime installation was not tested on each OS.
- The CLI exposes `setup`, `connect`, `daemon start`, `daemon stop`, `inspect`, and `help`. Setup and capability-probe fixture checks pass.
- The hosted landing remains static; it points users to their local daemon and does not provide cellular or public internet access.

## Still requires verification or implementation

- **Same-Wi-Fi physical test:** verify a second laptop, iPhone, and Android device can trust the self-signed certificate in real browsers, pair, use the dashboard, and lose access after revocation. The Linux bridge lab does not verify physical Wi-Fi, browser trust, mobile devices, router client isolation, or host firewall behavior.
- **Cellular/public access:** tunnel or relay support is future work; pairing currently targets the host's private LAN address.
- **Service installation:** LaunchAgent/systemd installation and clean GUI-only/SSH-only installs remain deferred.
- **Provider OAuth:** credential storage and real connect/read/write/disconnect flows for Calendar, email, Notion, or Calendly remain deferred.
- Live model quality and capability probes on Ollama, llama.cpp, LM Studio, vLLM, or hosted APIs.
- Durable scheduled execution across restart, full threat-model coverage, signed updates and rollback, twenty scheduled runs without duplicates, and the fifteen-minute setup target.

See [TODO.md](TODO.md), [docs/lan-access.md](docs/lan-access.md), and [docs/release-gate.md](docs/release-gate.md) before a stable release. Do not publish this alpha as meeting the full product acceptance gate.
