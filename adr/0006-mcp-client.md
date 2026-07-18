# ADR-0006: MCP Client (Model Context Protocol)

| Field | Value |
|-------|--------|
| **Status** | Accepted / Implemented |
| **Date** | 2026-07-17 |
| **Deciders** | Project owner |
| **Tags** | mcp, tools, web-search, plugins, tavily, extensibility |
| **Extends** | ADR-0001 (tools + loop), ADR-0005 (tool suite + loop policy) |

## Context

### What harnesses do for web search

In 2025–2026 the ecosystem converged on a few patterns:

| Pattern | How it works | Examples |
|---------|----------------|----------|
| **Native tool → search API** | First-class `web_search` tool in the harness; backend is Tavily / Brave / SerpAPI / etc. | Many coding agents; Hermes “native web” backend |
| **MCP client → search MCP server** | Harness speaks MCP; external process (often `npx tavily-mcp`) exposes `tavily_search`, extract, etc. | Goose + Tavily MCP; OpenCode + Tavily or SearXNG MCP |
| **Skills / CLI bundle** | Skill packs that document or wrap search CLI; planner discovers via skill load | Tavily Agent Skills + CLI |
| **Self-hosted meta-search** | MCP or tool to SearXNG / similar; no SaaS key | Privacy-first OpenCode setups |
| **Browser automation** | Playwright/Puppeteer tools (heavier; not “search”) | Browser-use stacks |

**Tavily + MCP is the common easy path** for production agent web search: API key + official MCP server + host registration. Some harnesses also ship a **native** `web_search` that calls Tavily (or similar) without MCP for a tighter loop.

Marble today has a rich **built-in** tool suite (ADR-0005) but **no** web search and **no** plugin surface. Hardcoding Tavily as a first-class tool would solve search once; adopting **MCP as a client** solves search *and* the long tail (GitHub, docs, calendars, internal APIs) without growing `internal/tools` forever.

ADR-0001 already parked “MCP / plugin system” and “web search” as later ADRs. This ADR takes both by making Marble an **MCP host/client**.

### Why not only a native `web_search`?

| Approach | Pros | Cons |
|----------|------|------|
| Native Tavily tool only | Simple, one API, low latency | Couples Marble to one vendor; every new integration is code |
| MCP only | Ecosystem leverage; swap Tavily ↔ SearXNG ↔ others via config | Process/lifecycle complexity; schema tax; protocol edge cases |
| **MCP first + optional native later** | Extensibility now; can still add a thin native wrapper if needed | Two paths if we overbuild both |

**Decision direction:** MCP client first. Document **Tavily MCP** as the recommended web-search server for operators. Do **not** bake Tavily API calls into Marble core in this ADR (optional future convenience ADR).

## Decision

1. Implement Marble as an **MCP client (host)** that connects to zero or more MCP servers.
2. **Discover tools, resources, and prompts** from connected servers (**Q11**).
3. **Merge** MCP tools into the same agent tool registry as built-ins for the model loop (OpenAI function-calling wire), with **namespacing** (`mcp_<server>_<tool>`).
4. Configure servers via **`$MEMORY/mcp.json`** (primary) + **`--mcp-config`** override; Cursor-compatible `mcpServers` map; secrets via env.
5. **v1 transports: both stdio and HTTP/SSE** (**Q4** — not stdio-only).
6. Treat MCP tool results like built-in tool results (truncation, DB event dual-write, loop caps).
7. **Web search** via MCP recipe (Tavily/SearXNG); **no** native `web_search` in this ADR.
8. Process-global connections; limp-safe; degrade on server failure; soft cap **64** MCP tools; **60s** call timeout.
9. Keep all built-ins; health JSON + UI chip; prefer small maintained Go client.

## Decisions locked (Q1–Q15)

Source: `adr/0006-answers.json` (`2026-07-18T01:34:43.803Z`).

| ID | Decision |
|----|----------|
| **Q1** | `$MEMORY/mcp.json` primary; `--mcp-config` override |
| **Q2** | Cursor-compatible `mcpServers` map |
| **Q3** | `mcp_<server>_<tool>` sanitized names |
| **Q4** | **stdio and HTTP/SSE both in v1** (not stdio deferred) |
| **Q5** | No native `web_search` in this ADR; MCP recipe for Tavily/SearXNG |
| **Q6** | MCP works in limp mode (independent of SQLite) |
| **Q7** | Process-global MCP connections; tools for all sessions |
| **Q8** | Per-server `enabled` flag; no remote config download |
| **Q9** | Soft cap **64** MCP tools total |
| **Q10** | **60s** default MCP tool call timeout |
| **Q11** | v1 includes **tools, resources, and prompts** (not tools-only) |
| **Q12** | Health JSON + small UI chip `MCP: n servers`; settings UI later |
| **Q13** | Prefer small maintained Go client; else minimal JSON-RPC subset in-tree |
| **Q14** | Do not fail harness start if MCP fails; degrade + health |
| **Q15** | Keep all built-ins; namespace prevents collision |

## Architecture

```
                    ┌──────────────────────────┐
                    │     Agent loop (0005)    │
                    │  Specs() = builtins+MCP  │
                    └────────────┬─────────────┘
                                 │
              ┌──────────────────┼──────────────────┐
              ▼                  ▼                  ▼
       ┌────────────┐    ┌────────────┐    ┌────────────────┐
       │ Built-ins  │    │ MCP bridge │    │ (future native │
       │ tools/*    │    │  namespace  │    │  web_search)   │
       └────────────┘    └──────┬─────┘    └────────────────┘
                                │
              ┌─────────────────┼─────────────────┐
              ▼                 ▼                 ▼
       stdio: tavily-mcp   stdio: other      HTTP/SSE MCP
       (npx / binary)      (git, docs, …)    (url + headers)
```

### Package sketch

```
internal/
  mcp/
    client.go            # session manager: start/stop servers (stdio + HTTP/SSE)
    transport_stdio.go
    transport_http.go    # Streamable HTTP / SSE (v1, Q4)
    catalog.go           # tools + resources + prompts catalog
    call.go              # tools/call, resources/read, prompts/get
    config.go            # parse mcp.json / servers block
  tools/
    registry.go          # Specs() merges builtins + mcp tools
```

### Tool naming — **Q3 locked**

| Scheme | Example | Status |
|--------|---------|--------|
| `mcp_<server>_<tool>` | `mcp_tavily_tavily_search` | **Locked** |

Sanitize: `[a-zA-Z0-9_]{1,64}` after mapping. Built-ins unchanged (**Q15**).

### Config surface — **Q1/Q2/Q8 locked**

**File:** `$MEMORY/mcp.json` primary; `--mcp-config path` override; `--mcp-disable` kill switch.

```json
{
  "mcpServers": {
    "tavily": {
      "command": "npx",
      "args": ["-y", "tavily-mcp@latest"],
      "env": {
        "TAVILY_API_KEY": "${TAVILY_API_KEY}"
      },
      "enabled": true
    },
    "remote_example": {
      "url": "https://example.invalid/mcp",
      "transport": "http",
      "headers": {
        "Authorization": "Bearer ${MCP_TOKEN}"
      },
      "enabled": false
    }
  }
}
```

| Concern | Decision |
|---------|----------|
| Config location | `$MEMORY/mcp.json`; `--mcp-config` override |
| Shape | Cursor-compatible `mcpServers` map |
| Env interpolation | `${VAR}` from process environment only (no shell) |
| Enable | Per-server `enabled`; no remote config download |
| Secrets | Env only for v1 (not SQLite plaintext) |
| Transports | **stdio** (`command`/`args`) and **HTTP/SSE** (`url` + optional headers) |

### Lifecycle

| Event | Behavior |
|-------|----------|
| Harness start | Parse config; connect enabled servers (spawn stdio / open HTTP); list tools, resources, prompts |
| Failure to start | Log + health field; continue without that server (**Q14** non-fatal) |
| Tool call | `tools/call` with timeout **60s** default (**Q10**) |
| Resource | `resources/list` / `resources/read` exposed to agent (via MCP-backed tools or bridge helpers) |
| Prompt | `prompts/list` / `prompts/get` available to harness (inject or tool-wrap as needed) |
| Harness shutdown | SIGTERM stdio children; close HTTP sessions |
| Reload | v1: restart harness; later: SIGHUP / API reload |

### Agent loop integration (ADR-0005)

- Process-global MCP manager (**Q7**); same catalog for all sessions.
- Works in limp mode (**Q6**).
- `Registry.Specs()` = builtin specs + MCP tools (cap **64** MCP tools, **Q9**).
- `Execute`: names starting with `mcp_` → MCP bridge; else built-in.
- MCP tools count toward tool rounds, soft wall, result truncation.
- Resources/prompts: surface as additional namespaced tools (e.g. `mcp_<server>_resource_read`) and/or harness-side prompt templates — implementer chooses minimal agent-visible surface that covers list+read/get.

### Security

| Risk | Mitigation |
|------|------------|
| Untrusted MCP server | Operator-configured only; `enabled` flag |
| Env secret leakage to child | Pass only listed `env` keys |
| Tool name collision | Namespacing |
| Remote HTTP MCP | TLS URL + header secrets from env; same local-operator trust model |
| Prompt injection via search results | Tool-result truncation |

MCP is **not** a sandbox. Stdio servers run as the harness user; HTTP servers are network trust.

### Web search recipe (docs, not core code) — **Q5**

```json
"tavily": {
  "command": "npx",
  "args": ["-y", "tavily-mcp@latest"],
  "env": { "TAVILY_API_KEY": "${TAVILY_API_KEY}" }
}
```

Alternatives: SearXNG MCP, Brave Search MCP. Native `web_search` deferred.

### Resources & prompts (MCP) — **Q11 locked**

| Capability | v1 |
|------------|-----|
| `tools/list` + `tools/call` | **Yes** |
| Resources (`resources/list`, `resources/read`) | **Yes** |
| Prompts (`prompts/list`, `prompts/get`) | **Yes** |
| Sampling (server → host LLM) | **No** (still out of scope) |

## Consequences

### Positive

- Web search and dozens of integrations without core churn.  
- Ecosystem configs (Tavily MCP, etc.) copy-pasteable.  
- Aligns Marble with modern harness expectations (Goose, Cursor, OpenCode-class).  
- Built-ins stay ownable and fast; MCP is the extension plane.

### Trade-offs

- Node/`npx` dependency for many popular servers (document; allow bare binaries).  
- Extra processes and cold-start latency.  
- Larger tool schema for the model (context tax).  
- Protocol surface area (JSON-RPC, notifications, partial failures).

### Risks

| Risk | Mitigation |
|------|------------|
| MCP protocol drift | Pin client library / implement narrow subset tests |
| Tool dump blows context | Cap tools per server; truncate descriptions |
| Zombie MCP children | Process group + shutdown hooks (same as BG tasks) |
| Operator confusion (which tool?) | Namespacing + health lists connected servers |

## Out of scope

- Marble as an MCP **server** (exporting Marble tools to other hosts)  
- OAuth / remote multi-user MCP auth  
- Marketplace UI for browsing servers  
- Native hardcoded Tavily client (future optional ADR)  
- Browser/CDP automation  
- Sampling (server-initiated LLM calls through host)  

## Implementation order (after accept)

1. `internal/mcp` config parse + `${ENV}` expand (`mcpServers`, stdio + url entries).  
2. Stdio transport: spawn, JSON-RPC initialize, list tools/resources/prompts.  
3. HTTP/SSE transport: connect, same capability discovery.  
4. Catalog → namespaced `model.ToolSpec` (+ resource/prompt agent surface).  
5. tools/call + resources/read + prompts/get with 60s timeout.  
6. Merge into `tools.Registry.Specs` / `Execute` (64-tool soft cap).  
7. Health JSON + UI chip; degrade on failure.  
8. Shutdown cleanup (stdio process groups + HTTP close).  
9. Docs: Tavily recipe, sample `mcp.json`, HTTP example, security notes.  
10. Tests: mock stdio server; HTTP mock; name collision; env expand.

## Open questions

**None blocking.** All Q1–Q15 locked — see **Decisions locked**.

## Acceptance criteria

- [x] Industry context (native vs MCP vs skills) accepted  
- [x] MCP client scope accepted (**tools + resources + prompts**; **stdio + HTTP/SSE**)  
- [x] Config shape + Tavily recipe accepted  
- [x] Naming + security model accepted  
- [x] Q1–Q15 locked  
- [x] Ready to implement  

## Changelog

| Date | Change |
|------|--------|
| 2026-07-17 | Initial proposal |
| 2026-07-18 | Q1–Q15 locked; status → **Accepted**; v1 = stdio+HTTP/SSE; tools+resources+prompts |

## References

- Model Context Protocol specification (tools, stdio, streamable HTTP)  
- Tavily MCP / agent search ecosystem (Goose, OpenCode, Hermes patterns)  
- ADR-0001 out-of-scope: MCP / web search  
- ADR-0005 expanded tool suite + loop  
- Marble tools registry: `internal/tools`  
