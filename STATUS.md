# Implementation status

Audit: 2026-10-26. This file describes verified code, not release readiness. The user confirmed the earlier draft as `docs/ownbot-backend-design.md` and confirmed the previously removed workflow remains excluded. Milestone rows come from the finish prompt. The supplied draft has no numbered threat table; concrete security requirements will be tested without inventing one.

## Baseline verification

- `go test -count=1 ./...`: PASS (MCP, models, server; command and web packages have no tests).
- `go vet ./...`: PASS.
- `go build -o /tmp/ownbot-baseline ./cmd/ownbotd`: PASS.
- `node --check web/dashboard.js`: PASS.
- `go test -race -count=1 ./...`: PASS.
- Real provider accounts, clean-machine setup, cellular pairing, update signing, and physical hardware have not been verified.

## Requirements

| Requirement | Status | Evidence and remaining work |
| --- | --- | --- |
| R1 Models | partial | `internal/models/openai.go`, `probe.go`; `TestModelProbeUsesEphemeralKeyAndPersistsCapability`, `TestModelProbeRestrictsWhenAnyCapabilityFails`. Mock capability checks work; no agent routing and no live Ollama/llama.cpp/API-provider tests. |
| R2 Remote | missing | Local bearer token exists (`TestSensitiveReadRequiresTokenAndStatusIsLoopbackOnly`); pairing, device-bound sessions, revocation, and remote network tests are absent. |
| R3 Setup | missing | `cmd/ownbotd/main.go` starts a daemon; there is no setup wizard or CLI/GUI fresh-install harness. |
| R4 Prompt versions | partial | `internal/server/server.go` saves versions; no run records, next-run application, or rollback tests. |
| R5 Tool toggles | partial | Agent toggle fields and MCP transport allowlist tests exist; no agent policy layer that refuses forced calls. |
| R6 Account integrations | missing | MCP clients and disabled server metadata exist. No provider OAuth or connected account lifecycle. |
| R7 Automations | partial | SQLite automation/job metadata exists; no scheduler, worker, retries, recovery execution, or external idempotent writes. |
| R8 Safety | partial | `TestApprovalDecisionIsSingleUseAndAudited` and MCP authorization tests cover individual components. No complete policy/taint/runtime/audit enforcement path. |

## Milestone items

| Item | Status | Evidence and remaining work |
| --- | --- | --- |
| M0-M1 SQLite/WAL daemon | partial | `server.New` sets WAL and persists configuration; `TestAgentPersistence`. Config loading, structured logging, graceful shutdown missing. |
| M0-M1 HTTP/WebSocket + Unix socket | partial | Loopback HTTP and embedded web assets exist; no Unix socket or WebSocket events. |
| M0-M1 Model gateway | partial | Chat, tool mapping, JSON format, SSE streaming; `TestChatNormalizesToolArgumentsAndDoesNotExposeErrorBody`, `TestStreamFiltersThinkTagsAcrossChunks`. Provider-specific/runtime integration and live checks incomplete. |
| M0-M1 Agent runtime and limits | missing | Stored configuration is not executable; no bounded loop, context trimming, or persisted run/prompt version. |
| M0-M1 Tool registry/risk schemas | missing | MCP metadata lists are not a validated registry. |
| M0-M1 MCP host | partial | `internal/mcp/stdio.go`, `http.go`; transport tests pass. Not connected to daemon execution; subprocesses are not OS-sandboxed. |
| M0-M1 Agent toggles | partial | Configuration fields exist; runtime and policy enforcement absent. |
| M0-M1 Web search | missing | No provider interface or implementation. |
| M0-M1 Policy and standing grants | missing | MCP callbacks refuse calls when absent; no central policy or standing approvals. |
| M0-M1 Taint tracking | missing | No runtime taint state. |
| M0-M1 Approval state machine/push | partial | Queue, approve/deny API, single-use test; no action execution or device push. |
| M0-M1 Tool audit/export | partial | Configuration/approval events exist; no complete redacted tool-call audit or export command. |
| M2 Secrets vault | missing | A protected local API-token file is not a provider secrets vault. |
| M2 Device pairing/QR | missing | No keypair registration or one-time pairing flow. |
| M2 Sessions/WebAuthn/TOTP | missing | No device-bound sessions, passkeys, or step-up. |
| M2 Device revoke/rate limits/auth audit | missing | No device registry or auth-event audit. |
| M2 Outbound tunnel setup | missing | No tested tunnel support or remote-access documentation. |
| M2 GUI/CLI parity | missing | Dashboard exists; `ownbot` CLI does not. |
| M3 Capability probing/restricted pipelines | partial | Mock probing and stored flags exist; restricted pipeline execution missing. |
| M3 Durable scheduler/retries | partial | Job rows and unique key exist; no execution/recovery tests. |
| M3 Typed pipelines | missing | Pipeline is unvalidated JSON. |
| M3 Calendar via public MCP | missing | Public endpoint documented in `internal/mcp/README.md`; no authenticated connection or lifecycle tests. |
| M3 Email via public MCP | missing | Public endpoint documented; no authenticated connection, taint, or send policy. |
| M4 Notion via public MCP | missing | Public endpoint documented; no OAuth/scoping/lifecycle tests. |
| M4 Calendly via public MCP | missing | Public endpoint documented; no OAuth/plan handling/lifecycle tests. |
| M4 Removed connector/pipeline | excluded | User explicitly reconfirmed removal on 2026-10-26; excluded from acceptance checks. |
| M5 Signed install/update/rollback | missing | No release signing or updater. |
| M5 Backup/restore | missing | No commands or tested restore path. |
| M5 Threat-table regression tests | missing | Draft has no threat table; component tests do not establish complete coverage of its security requirements. |
| CLI Interactive/unattended setup | missing | No `cmd/ownbot`. |
| CLI Required commands/help | missing | npm currently exposes only `ownbotd`. |
| CLI systemd/launchd services | missing | No service definitions. |
| Packaging npm/Homebrew/platforms | partial | Development npm package builds from Go source. No published registry package, brew formula, signatures, or clean-platform runtime verification. |
| Testing Fake model/MCP/fault harnesses | partial | In-process HTTP mocks and a stdio helper exist; provider/pipeline/fresh-install harnesses absent. |
| Testing Soak and setup timing | missing | No scheduled end-to-end flow to measure. |
| Documentation | partial | README, TODO, release gate, MCP notes exist; install/CLI/remote/security/limitations docs absent. |

## Needs human verification

- After implementation, authorize provider OAuth locally and exercise the account lifecycle; no credentials need to be sent in chat.
- Run the eventual clean-install harness on the M2 Mac mini and FreeBSD PC, then pair and revoke a phone on cellular.
- Configure release trust/signing and verify install/update/rollback before public deployment.
