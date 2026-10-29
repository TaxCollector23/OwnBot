# Inspect local setup and audit history

Use OwnBot's inspection commands to review local configuration and saved audit events. They are read-only: they do not install models, run agent tasks, or approve pending actions. A local readiness report does not prove that a model or provider will respond to a future request.

## Check local setup

```sh
ownbot inspect check [--json] [--url URL] [--data-dir DIR]
```

The check reviews available daemon, model, agent, tool, MCP, and pending-approval metadata and returns actionable findings. It reads metadata only: it does not probe models, run inference, call provider tools, discover MCP tools, or change configuration.

With `--json`, the report contains an `overall_status` and a `checks` array. Each check has a name, status (`ok`, `info`, `warning`, or `unknown`), summary, and optional details or action. Checks cover the daemon, models, agents, tools, MCP servers, and approvals. An info-only report exits successfully; a warning or unknown result exits nonzero while retaining the report on stdout.

An older daemon may not return MCP-server association metadata. When that visibility is missing, the check says it cannot verify each agent's server allowlist rather than assuming access.

## Export audit history

```sh
ownbot inspect audit export [--run RUN_ID] [--event EVENT_TYPE] [--out FILE] [--url URL] [--data-dir DIR]
```

The command exports matching stored events as newest-first JSONL. `--run` and `--event` match the event's exact run ID and event type. Export reads events; it does not replay or change them. Each request retrieves up to 500 events and follows an ID cursor to older pages. Events added during the export appear on the next export.

Without `--out`, output goes to stdout. A later-page failure can leave partial JSONL on stdout and returns an error. With `--out`, OwnBot writes a temporary file next to the destination and replaces it only after all pages succeed. A fetch or write failure leaves an existing destination unchanged. On Unix, the temporary file is mode `0600` and same-directory replacement is atomic.

Tool-call argument and result bodies are redacted and their hashes are retained. Other event metadata, including caller-supplied idempotency keys, may contain private data. Review exports before sharing.

The CLI rejects pages that violate the requested filter or cursor. If an older daemon ignores these options, update and restart it before retrying. A late-page failure can still leave partial stdout; `--out` keeps the previous destination unchanged on failure.

## Share a bug report safely

Include the OwnBot version, operating system, exact command and options, and a short error excerpt. Redact API tokens, provider keys, prompts, model responses, MCP arguments, private endpoint URLs, local paths, device names, idempotency keys, and identifying hashes. Do not attach a full audit export to a public issue.

## Label the evidence

Say whether a result came from a fixture or isolated test, a local daemon, or a live model/provider. A fixture or local metadata check is not evidence of live model inference or provider connectivity.
