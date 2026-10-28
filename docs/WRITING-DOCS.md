# Writing documentation

Keep user docs short, task-led, and accurate to the current CLI. Use `ownbot` for the project, executable, and commands. Use `~/.ownbot` only for the daemon's local state path.

Before changing setup instructions, inspect the current command help and implementation:

```sh
go run ./cmd/ownbot --help
go run ./cmd/ownbot daemon --help
go run ./cmd/ownbot setup --help
```

Describe what the command actually does. Separate automatic detection from user-installed requirements; say when a model is probed, downloaded, or not selected. Never call the daemon's local API token a provider key. Explain when it is generated, where it lives, and how the CLI uses it. Do not ask users to paste secrets into docs, issues, or source files.

Keep README.md as a short entry point. Put detailed procedures in `docs/`, link to them from the README and the Pages site, and update both when a command or behavior changes. Preserve the LAN boundary: pairing is for devices on the same private network; it does not provide cellular or public internet access. Keep unverified platform and release behavior clearly labeled.

The Pages site is dependency-free static HTML under `docs-site/`. Keep it usable on narrow screens, keyboard accessible, and readable without JavaScript. The Pages workflow publishes only that directory. Check internal links after editing:

```sh
python3 scripts/check-doc-links.py
```

