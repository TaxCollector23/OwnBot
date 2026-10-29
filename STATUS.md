# Release status

Updated 2026-10-28. `ownbot` is still in development. Do not treat this checkout as a signed or production release. The renamed `ownbot` package is not yet available from npm, and the documentation is live on GitHub Pages.

## Current scope

- The CLI supports local model detection and capability probing, daemon controls, and a local dashboard.
- Setup detects installed models from Ollama, LM Studio, llama.cpp, and vLLM. It does not download models or configure hosted provider keys.
- LAN mode provides HTTPS pairing for devices on the host's private network. It does not provide cellular or public internet access.
- macOS and Linux user-level service management is implemented; verify it on the target machine before relying on it for unattended operation.

## Verified evidence

- Native Go tests passed on macOS, Linux, and Windows; FreeBSD binaries cross-built in [CI](https://github.com/TaxCollector23/OwnBot/actions/runs/40739144312). Cross-building does not verify FreeBSD runtime behavior.
- The isolated two-container [LAN lab](https://github.com/TaxCollector23/OwnBot/actions/runs/40739144275) passed pairing, replay, revocation, and restart persistence checks.
- A disposable local npm tarball install built both binaries and verified SSH owner-link redemption, replay rejection, daemon inspection, and shutdown with a service-manager stub. This did not install a real login service.
- Ollama and llama.cpp each passed all four real capability checks on a hosted CPU runner; see [measured model evidence](docs/hardware.md). This does not establish answer quality or target-hardware fit.
- Scheduler fixtures cover twenty completed jobs, approval waits, cancellation recovery, live tool revocation, and uncertain-write handling. Real provider runs remain unverified.

## Release work remaining

- Verify a clean SSH-only setup and dashboard access through the printed SSH tunnel flow.
- Test certificate trust, pairing, dashboard use, and revocation with real second-laptop and mobile devices on Wi-Fi.
- Add and verify cellular/public access before documenting it as supported.
- Validate local model probes on target hardware and complete real account integration, automation, security, update, and rollback checks in the [release gate](docs/release-gate.md).
- Publish signed artifacts and the `ownbot` npm package only after the applicable release checks pass.

Automated tests and the isolated LAN lab do not establish physical-device, target-hardware, or public-release readiness. Check the [LAN guide](docs/lan-access.md) and [TODO](TODO.md) for details.
