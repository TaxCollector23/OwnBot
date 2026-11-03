# Virtual testing sandbox

The **Virtual sandbox** workflow starts a disposable GitHub-hosted Ubuntu VM, builds the real OwnBot CLI and daemon, and runs `scripts/test-local-workflows.cjs` against them. Start it from **Actions → Virtual sandbox → Run workflow**, then choose the branch to test. It is manual-only and has a 15-minute job limit.

The harness uses deterministic HTTP fixtures on loopback for an OpenAI-compatible model endpoint and an MCP server. It exercises CLI configuration, capability probing, tool discovery and binding, approval and denial, explicit run resume, diagnostics, and redacted audit pagination. The fixture returns scripted responses; this does not run model weights or verify inference quality.

The workflow does not configure provider API keys or user account credentials, and the fixture calls do not reach external model or MCP services. The runner has read-only repository permission. The daemon state and generated binaries stay on the temporary runner, which GitHub disposes of after the job. No files or artifacts are uploaded; the workflow summary reports the job result and test scope.

This is a virtual Linux test machine, not a physical computer. It does not verify macOS or Windows behavior, real provider connectivity, service installation, hardware performance, or connections from a second device. The native multi-OS checks and isolated LAN lab remain separate workflows.

## Verified run

On 2026-11-02, [sandbox run 41591098077](https://github.com/TaxCollector23/OwnBot/actions/runs/41591098077) passed all 60 workflow checks on commit `3bf3c23`. This is fixture-backed harness evidence, not real-model or physical-device qualification.
