# Release status

Updated 2026-11-10. The first-run implementation is present in this checkout, but OwnBot remains a development build. No npm package or matching public binary release has been published. Do not treat this checkout as a signed or production release.

## Implemented in this checkout

- The interactive first-run wizard detects local Ollama, LM Studio, Jan, llama.cpp, and vLLM endpoints; reports available host memory, GPU telemetry, and free model-storage space; recommends from a small local-model shortlist; and only creates an agent after a capability probe passes.
- Ollama and LM Studio can download their recommended models with visible progress. Jan setup verifies that a model ID maps to metadata and non-empty local weight files; Jan model downloads still happen in Jan Hub.
- Tool permissions come from the connected tools list, start disabled, and show risk labels. Local runtime credentials are stored in OwnBot's private local state and are not returned by the API.
- Successful setup enables LAN access, prints the certificate fingerprint and a one-use pairing QR, and opens the mobile dashboard flow. LAN access stays on the private network; relay, public internet, cellular, and router port-forward access are unsupported.
- `npm install -g ownbot` is prepared to download checksum-verified, prebuilt binaries for the supported release targets without requiring Go. It cannot install until matching GitHub release assets and the npm package are published.
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
- `node scripts/sandbox/test.cjs` passed against scripted loopback fixtures. `scripts/sandbox/record-demo.cjs` completed a task, approval, and result flow without model weights and saved a 9.3-second mobile-sized browser recording at `demo-site/scripts/explainer/output/ownbot-sandbox-demo.webm`.
- `npm audit` and `npm audit --omit=dev` both reported zero vulnerabilities after updating the test-only Playwright dependency.

The installer smoke test uses a local fixture; the actual published npm tarball, GitHub release download, package-manager behavior, and installation/upgrade on each supported OS are not verified.

## Release work remaining

- Run the browser suite in Linux CI to cover the three pinned-TLS pairing cases skipped by the local macOS runner.
- On the same Wi-Fi, test certificate trust, pairing, dashboard use, and revocation with a real second laptop, iPhone, and Android device. Verify the host firewall, guest Wi-Fi/client isolation, VPN routing, and host IP-change behavior with a real peer.
- Complete clean GUI-only and SSH-only installs, service-manager checks, model probes on target hardware, and real model downloads.
- Complete the real Notion and Calendly account checks, Google OAuth setup, scheduler restart/retry, approval/audit/security checks, and signed update plus rollback verification in the [release gate](docs/release-gate.md).
- Install the published npm package from a clean environment on every supported platform and verify checksum failures, install, upgrade, and configuration preservation.
- Implement and verify release signatures plus signed update installation and rollback, then publish binary artifacts and the npm package only after applicable release-gate evidence is recorded.

Automated Go, browser-independent dashboard, installer-fixture, release-builder, and workflow-fixture tests do not establish physical-device, production-install, target-hardware, or public-release readiness. See the [first-run guide](docs/first-run.md), [LAN guide](docs/lan-access.md), [release gate](docs/release-gate.md), and [TODO](TODO.md).

Historical QA from 2026-10-29 remains in [docs/qa-2026-10-29.md](docs/qa-2026-10-29.md); it predates this first-run work and does not replace the checks above.
