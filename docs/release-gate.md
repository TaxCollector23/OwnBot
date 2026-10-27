# Release gate

OwnBot is under development. Do not publish an installable release or claim these checks pass without recording evidence from the relevant environment.

- [ ] Capability probe: Ollama, llama.cpp, and an API provider; failed models cannot enter agent loops.
- [ ] Pair a phone on cellular through an outbound-only tunnel; revoke it and confirm the next request fails.
- [ ] Two fresh installs on the lowest supported hardware: GUI-only and SSH-only.
- [ ] Prompt edit, next-run version, and rollback.
- [ ] Disabled web search and MCP calls are refused by the server.
- [ ] Google Calendar, email, Notion, and Calendly connect/read/write/disconnect through public MCP servers.
- [ ] Restart a scheduled pipeline mid-run and retry without duplicate events.
- [ ] Every external write requires approval; hostile email or other external content cannot cause unapproved writes; every tool call is audited.
- [ ] Threat tests pass, including unauthorized requests, replay and revocation, cross-site requests, credential disclosure, prompt injection, and duplicate writes.
- [ ] Signed updates install and roll back.
- [ ] Twenty consecutive scheduled runs create no duplicates or unapproved writes; measure setup time to first approved event against the 15-minute target.

Provider connectivity, physical hardware, mobile access, signing keys, and public releases require separate verification.
