# MCP client constraints: Claude Code, Claude Desktop, Codex

Research snapshot from 2026-10 (Claude Code 2.1.287, Codex rust-v0.160, go-sdk v1.8.0). Confidence tags: **V** verified from a cited source or code, **L** likely, **I** inferred. Claude Code is the primary client; Codex is secondary.

## Comparison

| | Claude Code | Claude Desktop (chat) | Codex CLI / IDE / app |
|---|---|---|---|
| Add server | `claude mcp add -s user lms -- <exe> mcp`; `.mcp.json` `{"mcpServers":{"lms":{"type":"stdio","command","args","env","timeout"}}}`. Local/user scopes live in `~/.claude.json`. V[1] | `claude_desktop_config.json` (`%APPDATA%\Claude\`; the MSIX build uses `%LOCALAPPDATA%\Packages\Claude_*\LocalCache\Roaming\Claude\`) or a `.mcpb` bundle V[5,6] | `codex mcp add lms -- <exe> mcp`, or `~/.codex/config.toml` `[mcp_servers.lms]` with command, args, env, env_vars, cwd, startup_timeout_sec, tool_timeout_sec, enabled_tools, disabled_tools, supports_parallel_tool_calls; per tool `[mcp_servers.lms.tools.T] output_token_limit, approval_mode` V[9] |
| Transports | stdio, HTTP+OAuth, SSE (deprecated) V[1] | stdio; remote only as HTTPS connectors V[4] | stdio, Streamable HTTP V[9] |
| Stdio env | full shell env V[2] | limited subset V[5] | allowlist (HOME, PATH; on Windows APPDATA…) + `env` + `env_vars` V[9] |
| Startup timeout | `MCP_TIMEOUT` 30 s V[2] | unknown | 30 s in source (docs say 10) V[9] |
| Tool timeout | `MCP_TOOL_TIMEOUT`, about 28 h; idle abort after 30 min without a response or progress; backgrounded after 2 min V[1,2] | 240 s documented; ~60 s reported for stdio L[12] | `tool_timeout_sec`; 300 s in source (docs say 60); progress only logged V[9] |
| Output cap | warns at 10k tokens; above 25k (`MAX_MCP_OUTPUT_TOKENS`) the result goes to a file and the model gets the path. `_meta["anthropic/maxResultSizeChars"]` allows up to 500k chars. V[1] | ~150k chars V[4] | 10k tokens ×1.2, truncated in the middle; configurable per tool V[9] |
| Tool loading | tool search on by default: names + server instructions upfront; descriptions and instructions cut at 2,048 chars. `alwaysLoad` opts out. V[1] | all upfront I | all MCP tools deferred; BM25 `tool_search` over name, title, description and parameter names (8 hits) V[9] |
| What the model sees | `structuredContent` only (images kept) L[3,13] | both L[13] | compact `structuredContent` only; text and images dropped V[9] |
| resource_link / PDF blob | links supported; blobs saved to disk V[3]; some reports say they are ignored L[14] | connector drops `blob` L[14] | base64 dumped as text, then truncated V[9] |
| Resources | `@lms:uri` plus list/read tools V[1] | yes V[4] | `list/read_mcp_resource` tools; no @-mentions V[9] |
| Prompts | `/mcp__lms__<name>` V[1] | yes V[7] | no V[9] |
| readOnlyHint | undocumented | read-only tools auto-permitted V[4b] | read-only tools need no approval; unannotated tools prompt V[9] |

## go-sdk v1.8.0 (V[10], with a local probe)

- Negotiates protocol versions 2026-07-28 back to 2024-11-05.
- `mcp.AddTool[In, Out]`:
  - The input schema comes from `In`. `json`/`jsonschema` tags set names and descriptions. A field without `omitempty` is required. `additionalProperties:false` is set. Arguments are validated, and a failure returns `isError`.
  - When `Out` is not `any`, it emits an outputSchema plus `StructuredContent`. JSON `TextContent` is added only when `Content` is nil.
  - A plain `error` yields `isError:true`; a `*jsonrpc.Error` yields a protocol error.
- Content types: `&mcp.ResourceLink{URI,Name,MIMEType}`, `&mcp.EmbeddedResource{…}`, `&mcp.ImageContent{Data,MIMEType}`.
- Progress: `req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: req.Params.GetProgressToken(), …})`.
- Also available: `AddResource`, `AddResourceTemplate`, `AddPrompt`, `ServerOptions.Instructions`, `Tool.Meta`. No Tasks extension yet.

## Pitfalls

1. A non-object `Out` (slice or primitive) breaks hosts built on TS SDK v1: the whole `tools/list` fails. Always return a struct.
2. Claude Code and Codex hide text `Content` when `structuredContent` exists. Put all information in the structured result; long Markdown goes in a string field.
3. Codex keeps only basic JSON Schema keywords and compacts schemas over 5 KB. State ranges, patterns and defaults in descriptions. Claude Code drops tools whose property names fall outside `[A-Za-z0-9_.-]{1,64}`.
4. Names: use snake_case ASCII of 40 characters or fewer. Codex rewrites other characters to `_`.
5. stdout carries JSON-RPC only. Keep stderr quiet.
6. Unknown arguments are rejected, so error text should say how to fix the call.
7. Each client and session spawns its own server process, alongside the scheduled sync. Shared state needs cross-process safety: SQLite WAL with busy_timeout, plus a sync lease.

## Design rules adopted (see docs/plan.md)

- About 8k tokens (≈30 KB) or less per result. Lists use `limit` (default 25, max 100) and an opaque `cursor`, and return `next_cursor`, `has_more` and `total`. Text uses a page range or `offset` + `max_chars` and returns `next_offset`. Always flag truncation.
- Sync is asynchronous: `sync_start` returns a `job_id` in under 1 s and reuses a running job; `sync_status` reports phase, counts and errors. Read tools serve the index with `synced_at`/`stale` and never sync implicitly.
- Files: return extracted text per page with `--- page N ---` markers plus the absolute local path, which Claude Code can open. No embedded blobs. `resource_link` is optional extra.
- 15–25 tools, using filters instead of near-duplicate tools. Descriptions are 1,000 characters or less, with synonyms in the first sentence. Server instructions are 1,500 characters or less.
- Annotations on every tool: `readOnlyHint:true`, `destructiveHint:false`, `idempotentHint:true`, `openWorldHint:false`. Sync only touches the local cache, so it is marked read-only too, which avoids Codex approval prompts.
- Suggest `supports_parallel_tool_calls = true` and `tool_timeout_sec = 120` in the Codex config.

## Open questions

- Is Claude Code still "structuredContent only" in the current version? (The related issues were closed as "not planned".)
- Does Claude Code reliably show a linked or embedded PDF to the model? Until confirmed, rely on extracted text + local path.
- Which defaults does the user's Codex version use (10/60 s vs 30/300 s)? Set the values explicitly in config.

## References

[1] https://code.claude.com/docs/en/mcp · [2] https://code.claude.com/docs/en/env-vars · [3] https://github.com/anthropics/claude-code/blob/main/CHANGELOG.md · [4] https://claude.com/docs/connectors/building/index · [4b] https://claude.com/docs/connectors/building/review-criteria · [5] modelcontextprotocol/modelcontextprotocol docs 2026-07-28 (connect-local-servers, debugging) · [6] https://github.com/modelcontextprotocol/mcpb/blob/main/MANIFEST.md · [7] https://support.claude.com/en/articles/10949351 · [9] github.com/openai/codex @a759874 (codex-rs config/mcp_types.rs, codex-mcp, core/mcp_tool_call.rs, tools/json_schema) · [10] github.com/modelcontextprotocol/go-sdk v1.8.0 (mcp/server.go, shared.go, docs/server.md) · [12] claude-code issues #22542 #44032 #63379 #78478 #26073 · [13] #45575 #55677 #59480 #64316 · [14] #53453 #94746
