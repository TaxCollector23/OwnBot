# Decisions

- Keep Go and the existing SQLite implementation; preserve the working code and avoid a framework rewrite.
- Keep the supplied landing page as the visual baseline; dashboard changes must use its typography, paper/ink/blue palette, and square controls.
- Use public MCP servers for account integrations; this follows the user's explicit service-integration direction.
- Keep provider credentials out of requests for human input; OAuth setup remains deferred in TODO.md as requested.
- Require passing acceptance checks before release deployment; a passing build alone does not establish product readiness.
- Use the earlier user-supplied draft as `docs/ownbot-backend-design.md`; the user confirmed this source on 2026-10-26.
- Keep the previously removed workflow excluded from source and acceptance checks; the user reconfirmed removal on 2026-10-26.
- Trace threat tests to the draft's concrete security requirements; it has no numbered threat table to reproduce.

