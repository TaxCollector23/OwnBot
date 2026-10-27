# OwnBot backend design

Authoritative source: the earlier user-supplied draft, confirmed 2026-10-26. The removed connector and its workflow have been omitted as explicitly requested. Account integrations use public MCP servers per the user. Milestone and test ordering comes from the finish prompt. This draft has no numbered section 7 or threat table; security coverage must be traced to the concrete security requirements below.

Status: draft v0.1. "Ownbot" is a working name.
A daemon that runs on a user's own PC. It drives any LLM, runs tool-using agents and automations against the user's accounts, and is controlled securely from a phone or laptop on any network. Setup works from a GUI or from a CLI over SSH.
Non-goals (v1): multi-user or team deployment, a hosted cloud agent, training or fine-tuning models.
```
 Phone / laptop (GUI client, CLI)
          |  authenticated tunnel
 +--------v---------------------------------------------+
 | ownbotd (single daemon, systemd service)             |
 |                                                      |
 |  API server (HTTP + WebSocket)                       |
 |    |                                                 |
 |  Auth/Device registry   Policy engine   Approvals    |
 |    |                        |              |         |
 |  Agent runtime -------- Tool registry ------+        |
 |    |                     |      |      |             |
 |  Model gateway     MCP host  Native                  |
 |    |                        connectors                |
 |  Scheduler / job queue      (isolated process)       |
 |                                                      |
 |  SQLite (config, runs, queue, audit)  Secrets vault  |
 +--------+----------------------+----------------------+
          |                      |
   Model runtimes         External services
   (Ollama, llama.cpp,    (Google, Notion, Calendly,
    vLLM, LM Studio,       email, web search)
    API providers)
```
The GUI and CLI are both clients of the same local API. Nothing is configurable from one that is not configurable from the other.
- Single static binary, run by systemd (Linux) or launchd (macOS). Recommended language: Go (cross-compiles, runs on ARM mini PCs, one binary). Rust is a valid alternative.
- Listens on a unix socket and loopback only. Remote access comes through the tunnel (4.8), never a public port.
- GUI: a web app served by the daemon. A desktop wrapper is optional.
- CLI: ownbot setup, ownbot model, ownbot agent, ownbot connect, ownbot pair, ownbot logs. setup is an interactive wizard that also accepts flags and a config file for unattended installs.
- One internal interface (chat, tool calls, structured output, streaming) with adapters for OpenAI-compatible endpoints (Ollama, llama.cpp server, vLLM, LM Studio) and hosted API providers.
- Capability probe on model selection: checks schema-valid JSON output, single tool call, correct tool choice among several, and argument accuracy. Stores a score and capability flags per model.
- Models below a score threshold run in restricted mode: no multi-step agent loops, only fixed pipelines (4.6) with schema-constrained output.
- Use constrained decoding (grammar/JSON schema) where the runtime supports it. This guarantees valid shape, not correct tool choice.
- Handle <think> tags and model-specific tool-call formats inside the adapter, not in the agent loop.
- An agent = system prompt (versioned) + model + enabled tools + limits.
- Loop: model call, tool calls through the policy engine, results back, until done or limits hit.
- Limits per run: max steps, max tokens, wall-clock timeout. Context trimming when the window fills.
- System prompt edits create a new version; runs record the version used.
- Toggles are agent config: web_search: on|off, mcp: on|off (plus a per-server allowlist).
- Tool registry: one schema per tool, with a risk class: read, write_reversible, write_irreversible.
- MCP host: MCP client over stdio and streamable HTTP. Servers run as subprocesses with a scrubbed environment and no access to the vault. Each server's tools are individually enable-able.
- Web search: a provider interface. Self-hosted SearXNG or an API-key provider. Search requests leave the device.
- Native connectors: see section 6.
- Default rules: read allowed, write_* requires approval.
- Users can grant standing approval per tool or per automation.
- Taint tracking: content from untrusted sources (email bodies, web pages, MCP results) marks the run as tainted. While tainted, write tools always require approval, whatever the standing grants say. Tainted content never modifies the system prompt or the tool list.
- Rate limits per connector.
- Triggers: cron, polling, webhooks (Calendly, where the plan allows), manual.
- Automations are pipelines of typed steps. An LLM step parses or classifies with schema-constrained output. Other steps are deterministic code. Open-ended agent loops are optional. Pipelines are more reliable on small models because per-step errors compound across long agent loops.
- Durable job queue in SQLite. Retries with exponential backoff. Every job has an idempotency key.
- SQLite in WAL mode: config, prompt versions, runs, steps, approvals, jobs, sync state, device registry, audit log.
- Secrets (OAuth refresh tokens, API keys) in the OS keyring, or an encrypted file (age or libsodium) where no keyring exists. Secrets are never put in prompts, logs, or tool results.
- Transport: outbound-only tunnel. v1 supports user-supplied Tailscale/WireGuard or Cloudflare Tunnel. No port forwarding.
- Pairing: ownbot pair shows a QR code with a one-time token (short expiry). The client generates a device keypair; the daemon stores its public key.
- Sessions: short-lived tokens bound to the device key. Passkeys (WebAuthn) for the GUI. Optional TOTP step-up for approvals and settings changes.
- Devices: list and revoke from GUI or CLI. Failed-auth rate limiting. All auth events audited.
Append-only table of every tool call: run id, agent, tool, redacted args, redacted result, policy decision, approver, timestamp. Exportable.
Signed releases, verified before install. Update channel is opt-in per release stream.
1. Remote access: bring-your-own tunnel (v1 default) vs a hosted relay. A relay is a cloud service in the connection path, even if end-to-end encrypted.
3. Language: Go (recommended) vs Rust.
4. License and packaging: open core vs fully open source; preconfigured hardware or software only.
5. Default model per hardware tier: set after the capability probe is built and tested on real hardware.
- Calendly API plans and auth: Calendly Help Center, "Calendly API overview".
- Local model tool-calling figures come from third-party posts that use their own test setups: d-central.tech and promptquorum.com. Re-test on the target hardware.
