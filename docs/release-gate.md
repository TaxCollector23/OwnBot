# Release gate

OwnBot is under development. Do not publish an installable release or claim these checks pass without recording evidence from the relevant environment.

- [ ] Capability probe: Ollama, llama.cpp, and an API provider; failed models cannot enter agent loops.
- [ ] On the same Wi-Fi, pair a real second laptop, iPhone, and Android device; verify certificate trust/fingerprint, dashboard access, and that revocation rejects the next request.
- [ ] Verify behavior across a host firewall, guest Wi-Fi/client isolation, VPN routing, and a changed LAN IP using a real second device.
- [ ] Implement outbound tunnel or relay support, then pair a phone on cellular; revoke it and confirm the next request fails.
- [ ] Two fresh installs on the lowest supported hardware: GUI-only and SSH-only.
- [ ] Prompt edit, next-run version, and rollback.
- [ ] Disabled web search and MCP calls are refused by the server.
- [ ] Google Calendar, email, Notion, and Calendly connect/read/write/disconnect through public MCP servers.
- [ ] Restart a scheduled pipeline mid-run and retry without duplicate events.
- [ ] Every external write requires approval; hostile email or other external content cannot cause unapproved writes; every tool call is audited.
- [ ] Threat tests pass, including unauthorized requests, replay and revocation, cross-site requests, credential disclosure, prompt injection, and duplicate writes.
- [ ] Signed updates install and roll back.
- [ ] Twenty consecutive scheduled runs create no duplicates or unapproved writes; measure setup time to first approved event against the 15-minute target.

Automated LAN route/dashboard coverage is not evidence of a physical device connection. A real second peer is still required to verify same-Wi-Fi certificate trust, mobile browsers, and network isolation. Provider connectivity, service installation, physical hardware, tunnel-based mobile access, signing keys, and public releases also require separate verification.
