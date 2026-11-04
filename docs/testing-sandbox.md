# Virtual testing sandbox

## Interactive local demo

`scripts/sandbox/run.cjs` starts an actual OwnBot daemon with disposable state and two deterministic loopback fixtures. It configures a simulated model named `ownbot-sandbox-fake-v1`, runs the daemon's model capability probe, and connects a fake MCP `create_note` write tool. No model weights, provider keys, external services, or dependency installers are used by the entrypoint.

Use a daemon binary already built for this checkout:

```sh
OWNBOTD_BINARY=/absolute/path/to/ownbotd node scripts/sandbox/run.cjs
```

Optionally pass the CLI binary too if you want to run CLI commands against the same isolated data directory:

```sh
OWNBOTD_BINARY=/absolute/path/to/ownbotd OWNBOT_BINARY=/absolute/path/to/ownbot node scripts/sandbox/run.cjs
```

The process chooses free loopback ports, writes daemon state and logs under a private temporary directory, and prints the path to `sandbox-ready.json`. That JSON has mode `0600` and contains the owner token, one-time dashboard bootstrap URL, API URLs, model ID, tool ID, and private data directory. The startup output does not include the owner token or bootstrap URL. Open `dashboardURL` from the private manifest to use the ready-to-run demo agent. Submit a task such as **Create a note about testing OwnBot locally.** The simulated model requests the write; the real daemon waits for dashboard approval, then resumes and shows a deterministic summary. Press Ctrl-C to stop the daemon and remove the temporary directory.

The model endpoint returns scripted probe responses and one scripted tool call. It does not run inference. The MCP fixture records one in-memory sample note and never writes outside its process. All fixture listeners bind to `127.0.0.1`; the daemon listener also binds to loopback.

## GitHub virtual sandbox

The **Virtual sandbox** workflow starts a disposable GitHub-hosted Ubuntu VM, builds the real OwnBot CLI and daemon, runs the deterministic sandbox fixture smoke test, then runs `scripts/test-local-workflows.cjs` against the real binaries. Start it from **Actions → Virtual sandbox → Run workflow**, then choose the branch to test. It is manual-only and has a 15-minute job limit.

The harness uses deterministic HTTP fixtures on loopback for an OpenAI-compatible model endpoint and an MCP server. It exercises CLI configuration, capability probing, tool discovery and binding, approval and denial, explicit run resume, diagnostics, and redacted audit pagination. The fixture returns scripted responses; this does not run model weights or verify inference quality.

The workflow does not configure provider API keys or user account credentials, and the fixture calls do not reach external model or MCP services. The runner has read-only repository permission. The daemon state and generated binaries stay on the temporary runner, which GitHub disposes of after the job. No files or artifacts are uploaded; the workflow summary reports the job result and test scope.

This is a virtual Linux test machine, not a physical computer. It does not verify macOS or Windows behavior, real provider connectivity, service installation, hardware performance, or connections from a second device. The native multi-OS checks and isolated LAN lab remain separate workflows.

## Verified run

On 2026-11-02, [sandbox run 41591098077](https://github.com/TaxCollector23/OwnBot/actions/runs/41591098077) passed all 60 workflow checks on commit `3bf3c23`. This is fixture-backed harness evidence, not real-model or physical-device qualification.
