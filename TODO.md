# TODO

- Set up Google's Calendar MCP developer preview and a Google Cloud OAuth client later. Configure it locally; do not commit credentials. Test connect, read, approved write, and disconnect with a real account.
- Select the minimum supported hardware and a model after measuring the capability probe on an M2 Mac mini. A passing probe does not guarantee accuracy or prevent hallucinations.
- Test remote access using a phone on cellular once pairing is implemented.
- Complete and verify the checks in [docs/release-gate.md](docs/release-gate.md) before publishing.
- Connect and test the public MCP servers for Calendar, email, Notion, and Calendly. Add streamable HTTP and the providers' OAuth flows, and enforce per-tool approvals before enabling writes.
- Build and test the scheduler, agent runtime, and audit trail end to end.
- Test a clean install on the M2 Mac mini and FreeBSD PC. Package signed, verifiable releases for npm and Homebrew after the release checks pass.
