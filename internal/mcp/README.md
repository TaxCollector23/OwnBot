# MCP connections

The daemon can connect to configured **Streamable HTTP** MCP servers. It initializes supported handshake-era protocol versions (`2025-03-26`, `2025-06-18`, and `2025-11-25`), discovers tools, and exposes only explicitly allowlisted tools with a read/write classification. Remote metadata is treated as untrusted. Each tool call is schema-validated and passes through the engine's dispatch and approval policy; writes always require approval.

The daemon does not currently expose an OAuth sign-in flow or persist provider tokens. An HTTP `401` or `403` marks the runtime session as requiring re-authentication; subsequent calls fail closed. The low-level HTTP client accepts a runtime token callback, but the daemon connection API does not provide one, so the daemon cannot complete provider re-authentication in this build.

The public provider catalog contains these official hosted servers. Each requires provider OAuth and is reported unavailable until sign-in is implemented:

| Provider | Endpoint | Verified provider documentation |
|---|---|---|
| Google Calendar | `https://calendarmcp.googleapis.com/mcp/v1` | [Google Workspace configuration](https://developers.google.com/workspace/guides/configure-mcp-servers) · [Calendar endpoint and tools](https://developers.google.com/workspace/calendar/api/v3/reference/mcp) |
| Gmail | `https://gmailmcp.googleapis.com/mcp/v1` | [Google Workspace configuration](https://developers.google.com/workspace/guides/configure-mcp-servers) · [Gmail endpoint and tools](https://developers.google.com/workspace/gmail/api/reference/mcp) |
| Notion | `https://mcp.notion.com/mcp` | [Notion MCP client guide](https://github.com/makenotion/notion-cookbook/blob/main/docs/mcp-client-integration.md) |
| Calendly | `https://mcp.calendly.com` | [Calendly MCP server and OAuth requirements](https://developer.calendly.com/docs/mcp/calendly-mcp-server) |

Google lists Calendar and Gmail in its Workspace Developer Preview; both require Google Cloud setup and OAuth 2.0. Calendly requires OAuth 2.1 with PKCE and dynamic client registration. No provider account was authenticated or used by local tests. The MCP `2026-07-28` protocol lifecycle is not implemented.

The package also includes a local stdio client, but the daemon API refuses to start stdio servers until OS isolation protects local credentials. A stdio subprocess, if used directly by another caller, runs with that caller's OS permissions; environment scrubbing is not a sandbox.
