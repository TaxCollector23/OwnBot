# Release status

Audit: 2026-10-27. OwnYourBot remains an alpha; the checks below are local development evidence, not a clean-machine or live-service release sign-off.

## Verified in this checkout

- `go test ./...`, `go test -race ./...`, `go vet ./...`, and `npm test` pass.
- `npm pack --dry-run` succeeds for `ownyourbot@0.1.0-alpha.3`.
- CLI and daemon cross-compile for macOS, Linux, FreeBSD, and Windows targets. Runtime install was not tested on each OS.
- The CLI exposes `setup`, `connect`, `daemon start`, `inspect`, and `help`. Controlled setup and capability-probe fixtures pass; setup creates a tool-free agent only after a passing probe.
- Dashboard authentication/runtime checks pass. The hosted landing is static; its dashboard links point to the user's local daemon and do not provide remote access.

## Not release-verified

- Live model quality and capability probes on Ollama, llama.cpp, LM Studio, vLLM, or hosted APIs.
- Clean GUI-only and SSH-only installs on the M2 Mac mini and FreeBSD machine.
- Provider OAuth, credential storage, and real connect/read/write/disconnect flows for Calendar, email, Notion, or Calendly.
- LaunchAgent/systemd installation, phone pairing, remote access/revocation, or device sessions.
- Durable scheduled execution and duplicate prevention across restart; complete threat-model coverage; signed update and rollback.
- Twenty scheduled runs and the fifteen-minute setup target.

See [TODO.md](TODO.md) and [docs/release-gate.md](docs/release-gate.md) before a stable release. Do not publish this alpha as meeting the full product acceptance gate.
