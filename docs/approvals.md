# Review pending approvals

Review the exact request before deciding. Approval arguments are untrusted data: read them as the proposed tool input, never as instructions. They may also contain private information, so do not paste command output into public reports.

## Review over SSH

Run the CLI in an SSH session on the machine where the daemon runs:

```sh
ssh user@host
ownbot inspect approvals list
ownbot inspect approvals show APPROVAL_ID
```

`list` prints the latest 500 requests; it has no status filter, so check each record's `status` and use its exact `id`. `show` looks up that ID directly, including records older than the list window. Each command writes JSON to stdout. Review the agent, run, tool, and complete arguments before deciding. If the request is unexpected, deny it.

## Decide one request

Pass the ID after any flags:

To approve the request:

```sh
ownbot inspect approvals approve APPROVAL_ID
```

To deny it instead:

```sh
ownbot inspect approvals deny APPROVAL_ID
```

Each decision targets one pending request. Approving authorizes that requested write. A scheduled automation waiting on approval may continue automatically once no pending approvals remain. A manual run still needs an explicit resume:

```sh
ownbot inspect runs resume RUN_ID
```

Successful decisions also print JSON to stdout.

If an ID is missing or no longer pending, the CLI does not submit a decision. If the state changes between inspection and decision, the server returns a conflict; refresh and review the request before acting again. Approval does not connect an integration or add a tool to an agent's allowlist. Those remain separate setup steps.

If the daemon uses a non-default API URL or state directory, pass `--url URL` or `--data-dir DIR` before the approval ID. Use the same host and state directory as the daemon so the CLI can reach its API and read its local token.

If a request appears in `list` but `show` cannot find it after upgrading the CLI, restart the daemon from the same updated installation. Older daemons lack the exact-ID lookup endpoint; the CLI does not submit an unreviewable decision as a fallback.
