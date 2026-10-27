# TODO

- Verify same-Wi-Fi pairing with a real second laptop, iPhone, and Android device; confirm the HTTPS certificate fingerprint/trust flow, dashboard access, and revocation.
- Test LAN firewall, guest Wi-Fi/client isolation, VPN routing, and a changed host IP using a real second device.
- Add outbound tunnel/relay support before claiming cellular or public internet access.
- Install and test LaunchAgent/systemd service management; complete clean GUI-only and SSH-only installs on the M2 Mac mini and FreeBSD PC.
- Set up Google's Calendar MCP developer preview and a Google Cloud OAuth client later. Configure it locally; do not commit credentials. Test connect, read, approved write, and disconnect with a real account.
- Connect and test public MCP servers for Calendar, email, Notion, and Calendly. Add provider OAuth flows and enforce per-tool approvals before enabling writes.
- Select minimum supported hardware and a model after measuring the capability probe on an M2 Mac mini. A passing probe does not guarantee accuracy or prevent hallucinations.
- Build and test the scheduler, agent runtime, and audit trail end to end.
- Complete and verify [the release gate](docs/release-gate.md) before publishing. Package signed, verifiable releases for npm and Homebrew only after the release checks pass.
