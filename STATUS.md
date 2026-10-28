# Release status

Updated 2026-10-28. `ownbot` is still in development. Do not treat this checkout as a signed or production release. The renamed `ownbot` package is not yet available from npm, and the GitHub Pages workflow has not been deployed.

## Current scope

- The CLI supports local model detection and capability probing, daemon controls, and a local dashboard.
- Setup detects installed models from Ollama, LM Studio, llama.cpp, and vLLM. It does not download models or configure hosted provider keys.
- LAN mode provides HTTPS pairing for devices on the host's private network. It does not provide cellular or public internet access.
- macOS and Linux user-level service management is implemented; verify it on the target machine before relying on it for unattended operation.

## Release work remaining

- Verify a clean SSH-only setup and dashboard access through the printed SSH tunnel flow.
- Test certificate trust, pairing, dashboard use, and revocation with real second-laptop and mobile devices on Wi-Fi.
- Add and verify cellular/public access before documenting it as supported.
- Validate local model probes on target hardware and complete real account integration, automation, security, update, and rollback checks in the [release gate](docs/release-gate.md).
- Publish signed artifacts and the `ownbot` npm package only after the applicable release checks pass.

Automated tests and the isolated LAN lab do not establish physical-device, target-hardware, or public-release readiness. Check the [LAN guide](docs/lan-access.md) and [TODO](TODO.md) for details.
