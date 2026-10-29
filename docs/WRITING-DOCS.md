# Writing documentation

Keep user docs short, task-led, and accurate to the current CLI. Use `ownbot` for the project, executable, and commands. Use `~/.ownbot` only for the daemon's local state path.

Before changing setup instructions, inspect the current command help and implementation:

```sh
go run ./cmd/ownbot --help
go run ./cmd/ownbot daemon --help
go run ./cmd/ownbot setup --help
```

Describe what the command actually does. Separate automatic detection from user-installed requirements; say when a model is probed, downloaded, or not selected. Never call the daemon's local API token a provider key. Explain when it is generated, where it lives, and how the CLI uses it. Do not ask users to paste secrets into docs, issues, or source files.

For a new command, verify its spelling, flags, defaults, output format, exit status, and failure behavior against current `--help`, implementation, and focused tests before documenting it. Do not infer command names or API routes from nearby features. For read-only inspection commands, say which local metadata they read and what they do not prove about live model or provider availability.

When describing audit exports, state ordering, filters, pagination, output-file behavior, and failure behavior only after those details are verified. Distinguish a fixture or isolated test from a local daemon run and from a live provider/model check. Never present test or fixture evidence as a live integration result.

Bug reports should include the command, ownbot version, operating system, and a short redacted error excerpt. Do not attach the full audit export, API token, provider keys, prompts, model responses, MCP arguments, or private endpoint URLs. Review paths, device names, idempotency keys, and hashes for identifying information before sharing an excerpt.

Keep README.md as a short entry point. Put detailed procedures in `docs/`, link to them from the README and the Pages site, and update both when a command or behavior changes. Preserve the LAN boundary: pairing is for devices on the same private network; it does not provide cellular or public internet access. Keep unverified platform and release behavior clearly labeled.

The demo website also has a docs surface at `demo-site/demos/index.html`. Keep its command summaries and section links consistent with the Markdown guides; source links belong at the top of the short README. Run its browser checks after changing navigation or examples.

The Pages site is dependency-free static HTML under `docs-site/`. Keep it usable on narrow screens, keyboard accessible, and readable without JavaScript. The Pages workflow publishes only that directory. Check internal links after editing:

```sh
python3 scripts/check-doc-links.py
```
