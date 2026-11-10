# TODO

- Verify the new certificate transfer/profile flow on a real second laptop, iPhone, and Android device; compare fingerprints, pair from the dashboard, and confirm revocation rejects the next request.
- Test LAN firewall, guest Wi-Fi/client isolation, VPN routing, and a changed host IP using a real second device.
- Keep cellular and public internet access unsupported for this local-only product; do not add a relay, public service, or port-forward as part of this scope.
- Install and test LaunchAgent/systemd service management; complete clean GUI-only and SSH-only installs on the M2 Mac mini and FreeBSD PC.
- Set up Google's Calendar MCP developer preview and a Google Cloud OAuth client later. Configure it locally; do not commit credentials. Test connect, read, approved write, and disconnect with a real account.
- Test public MCP sign-in and tool access with real Notion and Calendly accounts; verify provider scopes and per-tool approvals before enabling writes.
- Select minimum supported hardware and a model after measuring the capability probe on an M2 Mac mini. A passing probe does not guarantee accuracy or prevent hallucinations.
- Verify scheduler restart/retry, agent execution, approvals, and audit trails end to end with real provider accounts.
- Complete and verify [the release gate](docs/release-gate.md) before publishing. The npm installer now expects a matching checksum-verified GitHub release for each supported platform; publish neither that release nor the npm package until the gate is satisfied.

- Signed-update implementation is preserved on `codex/signed-updates`; it is not merged or released. Finish the production public-key pin and live signed install/update/rollback verification before advertising it.
