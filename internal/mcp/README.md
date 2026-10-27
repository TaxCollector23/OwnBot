# MCP connections

OwnBot includes a Go MCP client for explicitly configured **local stdio** servers using the MCP `2025-11-25` initialization protocol. The daemon can store disabled configurations but does not yet start them. Using the client requires an explicit command and tool allowlist, plus a read/write classification for every tool to expose.

Each call also requires a runtime `AuthorizeCall` decision, including tools classified as read. A tool name allowlist alone never authorizes an invocation. The daemon does not pass vault credentials to child processes. The subprocess receives only a small environment allowlist, with credential-like names rejected. It still runs as the daemon's OS user and can access files that user can access; this is environment scrubbing, not an OS sandbox. Configure only local commands you trust.

A Streamable HTTP client is implemented separately in this package, but it is **not connected to the daemon API or runtime**. Its current authentication support is limited to an explicitly configured in-memory bearer token; OAuth authorization and credential persistence are not implemented.

## Public provider MCP servers

| Provider | Hosted endpoint | Authentication and constraints |
|---|---|---|
| Google Calendar | `https://calendarmcp.googleapis.com/mcp/v1` | Streamable HTTP and OAuth 2.0. Google labels it Developer Preview and requires Workspace Developer Preview enrollment, a Google Cloud project, enabled Calendar/MCP APIs, and OAuth client setup. [Configuration guide](https://developers.google.com/workspace/calendar/api/guides/configure-mcp-server) · [Endpoint and tool reference](https://developers.google.com/workspace/calendar/api/v3/reference/mcp) |
| Gmail | `https://gmailmcp.googleapis.com/mcp/v1` | Streamable HTTP and OAuth 2.0; also in Google Workspace Developer Preview. Its published tools search/read threads, list labels/drafts, label threads/messages, and create drafts. There is no send-email tool in the published list; a draft must be reviewed and sent separately in Gmail. [Configuration guide](https://developers.google.com/workspace/gmail/api/guides/configure-mcp-server) · [Endpoint and tool reference](https://developers.google.com/workspace/gmail/api/reference/mcp) |
| Notion | `https://mcp.notion.com/mcp` (Streamable HTTP); `/sse` is a legacy SSE endpoint | Hosted by Notion; OAuth 2.0 Authorization Code with PKCE and token refresh are required for a full client. [Official client guide](https://github.com/makenotion/notion-cookbook/blob/main/docs/mcp-client-integration.md) · [Official server repository](https://github.com/makenotion/notion-mcp-server) |
| Calendly | `https://mcp.calendly.com` | Hosted by Calendly; OAuth 2.1 Authorization Code + PKCE and Dynamic Client Registration are required. Personal access tokens and static client credentials are not supported by this MCP server. [Calendly MCP documentation](https://developer.calendly.com/docs/mcp/calendly-mcp-server) |

These provider endpoints use Streamable HTTP and cannot be connected directly through OwnBot's stdio transport. OwnBot has not authenticated to, started, or verified any provider account. No provider credentials or live services were used in local tests.
