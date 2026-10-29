# ownbot demo handoff

The demo website is maintained in `demo-site/` in this repository. It replaces the earlier local-only demo source.

- Live site: https://ownbot-demo.vercel.app
- Demos and guides: https://ownbot-demo.vercel.app/demos
- README starts with links to the sources.
- Current CLI recording: disposable daemon, no models installed or invoked. Transcript and recording/render scripts are included.
- Flyover: refreshed landing, approved preview, 14.5 seconds at 60 fps. Animation engine and MIT license are included.
- QA: `docs/qa-2026-10-28.md`; Go and dashboard regressions pass, browser suite 15 passed / 3 macOS LAN skips.
- This completes the demo-site handoff, not the full product release gate. Live provider/device/hardware and signed-update validation remain in `TODO.md` and `STATUS.md`.

See `demo-site/README.md` for serving, recording, rendering, and deployment. Deploy only that static folder; never copy daemon state or credentials into it.

## Website refinement review

Five GPT-6 Luna agents at max effort covered design, implementation, end-to-end QA, accessibility, and maintainer review. The website now has visible mobile navigation, a sticky docs index, labeled demos, and copy controls with a manual keyboard fallback. Final verification: 172 local assertions and 173 hosted assertions, zero failures. See `docs/website-qa-2026-10-28.md` and `demo-site/scripts/test-site.cjs`. Website code: `a7789ae`; production: https://ownbot-demo.vercel.app.

## Local workflow features · 2026-10-29

- `ownbot inspect check [--json]`: read-only setup and permission diagnostics; no probes or provider calls.
- `ownbot inspect audit export [--run ID] [--event TYPE] [--out FILE]`: complete cursor-paginated history, exact filters, and output replacement only after successful export. Tool bodies are redacted; metadata must still be reviewed before sharing.
- Read-only agent metadata now includes MCP server bindings. Inspection help no longer advertises the unavailable logs API.
- Added real CLI/disposable-daemon E2E fixtures and native-OS CI execution; no model installation. See `docs/qa-2026-10-29.md` for exact results and remaining gates.
