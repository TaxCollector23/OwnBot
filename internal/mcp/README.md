# MCP connections

The daemon can connect to configured **Streamable HTTP** MCP servers. It initializes supported handshake-era protocol versions (`2025-03-26`, `2025-06-18`, and `2025-11-25`), discovers tools, and exposes only explicitly allowlisted tools with a read/write classification. Remote metadata is treated as untrusted. Each tool call is schema-validated and passes through the engine's dispatch and approval policy; writes always require approval.

The daemon implements authorization-code OAuth with PKCE, metadata discovery, and public dynamic client registration for compatible servers. Use `ownbot connect notion` or `ownbot connect calendly`; SSH sessions print an additional tunnel for the exact loopback callback port. Tokens remain in the private local SQLite database and are not separately encrypted. Sign-in grants no tools to agents. An HTTP `401` or `403` invalidates runtime availability; expired grants require sign-in again when refresh fails.

The public provider catalog contains these official hosted servers. Notion and Calendly can start OAuth; Google setup remains deferred:

| Provider | Endpoint | Verified provider documentation |
|---|---|---|
| Google Calendar | `https://calendarmcp.googleapis.com/mcp/v1` | [Google Workspace configuration](https://developers.google.com/workspace/guides/configure-mcp-servers) · [Calendar endpoint and tools](https://developers.google.com/workspace/calendar/api/v3/reference/mcp) |
| Gmail | `https://gmailmcp.googleapis.com/mcp/v1` | [Google Workspace configuration](https://developers.google.com/workspace/guides/configure-mcp-servers) · [Gmail endpoint and tools](https://developers.google.com/workspace/gmail/api/reference/mcp) |
| Notion | `https://mcp.notion.com/mcp` | [Notion MCP client guide](https://github.com/makenotion/notion-cookbook/blob/main/docs/mcp-client-integration.md) |
| Calendly | `https://mcp.calendly.com` | [Calendly MCP server and OAuth requirements](https://developer.calendly.com/docs/mcp/calendly-mcp-server) |

Google lists Calendar and Gmail in its Workspace Developer Preview; both require Google Cloud setup and OAuth 2.0. Calendly requires OAuth 2.1 with PKCE and dynamic client registration. No provider account was authenticated or used by local tests. The MCP `2026-07-28` protocol lifecycle is not implemented.

The package also includes a local stdio client, but the daemon API refuses to start stdio servers until OS isolation protects local credentials. A stdio subprocess, if used directly by another caller, runs with that caller's OS permissions; environment scrubbing is not a sandbox.
