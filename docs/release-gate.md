# Release gate

OwnBot is under development. Do not publish an installable release or claim these checks pass without recording evidence from the relevant environment.

- [ ] Capability probe: Ollama, LM Studio, Jan local models, llama.cpp, and vLLM; failed models cannot enter agent loops. The first-run wizard must reject cloud-backed model IDs.
- [ ] From a clean machine on every supported platform, install the published npm tarball with no Go toolchain; verify that both checksum-checked binaries install, launch, and upgrade without losing existing configuration.
- [ ] On the same Wi-Fi, pair a real second laptop, iPhone, and Android device; verify certificate trust/fingerprint, dashboard access, and that revocation rejects the next request.
- [ ] Verify behavior across a host firewall, guest Wi-Fi/client isolation, VPN routing, and a changed LAN IP using a real second device.
- [ ] Confirm the release has no tunnel, relay, public listener, or port-forward path; cellular and public internet access stay unsupported.
- [ ] Two fresh installs on the lowest supported hardware: GUI-only and SSH-only.
- [ ] Prompt edit, next-run version, and rollback.
- [ ] Disabled web search and MCP calls are refused by the server.
- [ ] With real Notion and Calendly accounts, verify OAuth sign-in plus read/write/disconnect; Google Calendar and Gmail remain gated on the local Google Cloud OAuth client setup in TODO.md.
- [ ] Restart a scheduled pipeline mid-run and retry without duplicate events.
- [ ] Every external write requires approval; hostile email or other external content cannot cause unapproved writes; every tool call is audited.
- [ ] Threat tests pass, including unauthorized requests, replay and revocation, cross-site requests, credential disclosure, prompt injection, and duplicate writes.
- [ ] Signed updates install and roll back.
- [ ] Twenty consecutive scheduled runs create no duplicates or unapproved writes; measure setup time to first approved event against the 15-minute target.

Automated LAN route/dashboard coverage is not evidence of a physical device connection. A real second peer is still required to verify same-Wi-Fi certificate trust, mobile browsers, and network isolation. Service installation, physical hardware, signing keys, and public releases also require separate verification. Cellular and public internet access are intentionally unsupported.
