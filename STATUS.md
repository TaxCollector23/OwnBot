# Release status

Updated 2026-11-10. The `v0.1.0-alpha.4` GitHub prerelease includes SHA-256-checksummed platform archives for manual testing. The artifacts are unsigned; OwnBot remains a development build and is not a signed or production release. The npm package has not been published.

## Implemented in this checkout

- The interactive first-run wizard detects local Ollama, LM Studio, Jan, llama.cpp, and vLLM endpoints; reports available host memory, GPU telemetry, and free model-storage space; recommends from a small local-model shortlist; and only creates an agent after a capability probe passes.
- Ollama and LM Studio can download their recommended models with visible progress. Jan setup verifies that a model ID maps to metadata and non-empty local weight files; Jan model downloads still happen in Jan Hub.
- Tool permissions come from the connected tools list, start disabled, and show risk labels. Local runtime credentials are stored in OwnBot's private local state and are not returned by the API.
- Successful setup enables LAN access, prints the certificate fingerprint and a one-use pairing QR, and opens the mobile dashboard flow. LAN access stays on the private network; relay, public internet, cellular, and router port-forward access are unsupported.
- `npm install -g ownbot` is prepared to download checksum-verified, prebuilt binaries for the supported release targets without requiring Go. The matching GitHub prerelease assets now exist, but npm publication is still required before this install command can work.
- The release pipeline checks artifact hashes but does not yet sign artifacts or verify signatures; release signing and signed update/rollback checks remain incomplete.
- The dashboard reports when a submitted task is running. Results are still returned synchronously; token-by-token streaming is not implemented.

## Local verification on 2026-11-10

- `npm test` passed: the complete Go test suite and dashboard auth, runtime, and LAN regressions.
- `npm run test:install` passed on macOS arm64 using mocked release assets and an install subprocess without Go in `PATH`.
- `node scripts/test-local-workflows.cjs` passed 60 checks with locally built CLI and daemon binaries, including approval decisions, replay behavior, and 580 exported audit records.
- `python3 -m unittest scripts.release.test_pipeline` passed both release-pipeline unit tests.
- The release builder created and verified checksum manifests and archives for all six supported targets: macOS arm64/amd64, Linux arm64/amd64, Windows amd64, and FreeBSD amd64.
- A Windows amd64 cross-build and native macOS CLI/daemon builds passed.
- `git diff --check` passed.
- `OWNBOTD_BINARY=/tmp/ownbot-daemon-final npm run test:browser` passed 15 Playwright cases across 1440 px desktop and 375/390 px phone viewports. Three LAN pairing cases were intentionally skipped on macOS because routes to its own private address resolve through `lo0`; the workflow delegates that pinned-TLS pairing coverage to Linux CI.
- The local demo-site browser check passed 260 assertions at 320, 375, 768, and 1440 px, including navigation, search, keyboard access, and no-JavaScript reading. `python3 scripts/check-doc-links.py` passed after updating the Markdown and site guides.
- `node scripts/sandbox/test.cjs` passed against scripted loopback fixtures. The dashboard recorder saved a 9.3-second mobile-sized browser recording, and the terminal recorder completed the real CLI task, approval, and resume flow against those fixtures; neither used model weights. The recordings are in `demo-site/scripts/explainer/output/`.
- `npm audit` and `npm audit --omit=dev` both reported zero vulnerabilities after updating the test-only Playwright dependency.
- GitHub Actions published `v0.1.0-alpha.4` with six platform archives. The downloaded release files passed both the release manifest verifier and `shasum -a 256 -c SHA256SUMS`.

The installer smoke test uses a local fixture. The npm tarball, package-manager behavior, and install/upgrade on each supported OS are not verified. The GitHub alpha release archives have SHA-256 checksums but no signatures.

## Release work remaining

- Run the browser suite in Linux CI to cover the three pinned-TLS pairing cases skipped by the local macOS runner.
- On the same Wi-Fi, test certificate trust, pairing, dashboard use, and revocation with a real second laptop, iPhone, and Android device. Verify the host firewall, guest Wi-Fi/client isolation, VPN routing, and host IP-change behavior with a real peer.
- Complete clean GUI-only and SSH-only installs, service-manager checks, model probes on target hardware, and real model downloads.
- Complete the real Notion and Calendly account checks, Google OAuth setup, scheduler restart/retry, approval/audit/security checks, and signed update plus rollback verification in the [release gate](docs/release-gate.md).
- Publish and install the npm package from a clean environment on every supported platform; verify checksum failures, install, upgrade, and configuration preservation.
- Implement and verify release signatures plus signed update installation and rollback before any production release. The current alpha prerelease has checksum files but no signatures.

Automated Go, browser-independent dashboard, installer-fixture, release-builder, and workflow-fixture tests do not establish physical-device, production-install, target-hardware, or public-release readiness. See the [first-run guide](docs/first-run.md), [LAN guide](docs/lan-access.md), [release gate](docs/release-gate.md), and [TODO](TODO.md).

Historical QA from 2026-10-29 remains in [docs/qa-2026-10-29.md](docs/qa-2026-10-29.md); it predates this first-run work and does not replace the checks above.
