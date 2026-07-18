# ADR-0012: Native `web_fetch` Tool (URL retrieval after search)

| Field | Value |
|-------|--------|
| **Status** | Accepted / Implemented |
| **Date** | 2026-07-18 |
| **Deciders** | Project owner |
| **Tags** | tools, http, web, tavily, mcp, research |
| **Extends** | ADR-0005 (tool suite), ADR-0006 (MCP / Tavily recipe), ADR-0011 (multi-provider search — deferred) |
| **Answers** | `adr/0012-answers.json` (`2026-07-18T14:03:35.491Z`) |
| **Related** | Operators already use `mcp_tavily_tavily_search` (returns titles + **URLs**) |

## Context

### How research works today in Marble

1. **Search** via MCP (typically Tavily): `mcp_tavily_tavily_search` returns structured hits including **Title** and **URL**.  
2. **Deeper retrieval** often goes back through Tavily: `tavily_extract`, `tavily_crawl`, `tavily_map`, `tavily_research` — vendor-side fetch/summarize, extra latency/cost, timeouts.  
3. Marble has **no first-class HTTP fetch** of an arbitrary URL.

### Desired pattern

```
search (Tavily/Brave/…)  →  list of real URLs + snippets
        ↓
web_fetch(url)           →  harness GETs page → markdown (or raw JSON)
        ↓
model reasons / cites URL
```

**Search:** discovery + ranked URLs.  
**`web_fetch`:** direct retrieval for grounding; Tavily extract/crawl only as fallback.

## Goals

1. Built-in **`web_fetch`** — one URL per call (**Q1**, **Q2**).  
2. **HTML/text → markdown** for the model (**Q3**); **JSON content-type → raw body** (**Q4**).  
3. System prompt: use web search if available, then **`web_fetch` for deeper analysis** (**Q8**).  
4. Soft-discourage Tavily extract when fetch works; still allow on failure (**Q9**).  
5. **Allow LAN/private** fetches for home lab (**Q5**); still block cloud metadata / dangerous link-local.  
6. Max **5 redirects**, re-validate each hop (**Q7**). No custom headers/cookies v1 (**Q6**).

## Non-goals (v1)

- Full headless browser / Playwright  
- Native `web_search` normalizer (**Q10** deferred)  
- Session URL cache (**Q11** deferred)  
- Custom headers/cookies (**Q6**)  
- Special mpub import path (**Q12** — fetch then `mpub_publish`)  
- Multi-provider search policy (ADR-0011 deferred)

## Decision

1. **Tool name:** `web_fetch` (**Q1**).  
2. **Arity:** single `url` per call; model loops for more (**Q2**).  
3. **Extraction:**
   - HTML / text-like → **render as markdown** for the model (**Q3**).  
   - `application/json` (and `+json`) → **raw** body (truncated by `max_tool_result`) (**Q4**).  
   - Other binary → short error / skip body.  
4. **Safety:**
   - Schemes: `http` / `https` only.  
   - **Allow private/LAN** (RFC1918, etc.) for lab use (**Q5**).  
   - **Still block** cloud metadata and obvious SSRF sinks (e.g. `169.254.169.254`, AWS/GCP/Azure metadata hostnames) even when LAN is allowed.  
   - Redirects: max **5**; re-check scheme/host policy each hop (**Q7**).  
   - No custom Authorization/cookies (**Q6**).  
   - Timeout ~15–30s; honor turn cancel context (ADR-0010).  
   - Download byte cap; return truncated to max tool result with notice.  
5. **Prompt policy (**Q8**, **Q9**):**  
   - System prompt: *use web search if available, then use `web_fetch` native tool for deeper analysis if needed*.  
   - Prefer fetch over Tavily extract when fetch works; extract/crawl/research still allowed on failure or multi-page needs.  
6. **Defer:** native `web_search` wrapper (**Q10**), fetch cache (**Q11**), mpub special-case (**Q12**).  
7. Prefer `web_fetch` over shell `curl` for model-driven retrieval.

## Decisions locked (Q1–Q12)

| ID | Decision |
|----|----------|
| **Q1** | Tool name: **`web_fetch`** |
| **Q2** | Single URL per call; model loops |
| **Q3** | Fetch and **render as markdown** (HTML/text) |
| **Q4** | **JSON comes back raw** (not markdownized) |
| **Q5** | **Allow LAN** / private network fetch |
| **Q6** | Defer custom headers/cookies |
| **Q7** | Max **5** redirects; re-validate host each hop |
| **Q8** | System prompt: web search if available, then **`web_fetch` for deeper analysis** |
| **Q9** | Soft discourage extract when fetch works; allow on failure |
| **Q10** | Defer native `web_search` normalizer |
| **Q11** | Defer session URL cache |
| **Q12** | No mpub special case — fetch then `mpub_publish` |

## Tool schema

```json
{
  "name": "web_fetch",
  "description": "HTTP(S) GET a URL. HTML/text is returned as markdown; JSON is returned raw. Prefer after web search returns real URLs. Do not invent URLs. Use Tavily extract/crawl only when fetch fails or multi-page crawl is needed.",
  "parameters": {
    "type": "object",
    "properties": {
      "url": { "type": "string", "description": "Absolute http or https URL" },
      "max_bytes": { "type": "integer", "description": "Optional download cap" }
    },
    "required": ["url"]
  }
}
```

### Output shape (illustrative)

```text
url: https://example.com/page
final_url: https://example.com/page
status: 200
content_type: text/html
format: markdown
chars: 12040 (truncated from …)
---
# Title
…markdown body…
```

For JSON:

```text
…
content_type: application/json
format: raw
---
{"key": ...}
```

## Implementation sketch

1. `internal/tools/webfetch.go` — validate URL, LAN-allow + metadata block, redirects, `http.Client` + turn `ctx`.  
2. HTML → markdown (light dependency or stdlib-assisted).  
3. JSON path: pass through raw bytes as string (utf-8), truncate.  
4. Register in `specs.go` + `Execute`.  
5. System prompt line per **Q8**.  
6. Tests: public URL httptest; metadata IP blocked; private IP **allowed**; redirect chain; JSON raw.

## Consequences

### Positive

- Search discovers URLs; fetch grounds answers without vendor extract by default.  
- LAN allow supports home-lab docs/APIs.  
- Clear JSON vs document handling.

### Trade-offs

- LAN allow increases SSRF surface vs public-only (mitigate with metadata blocklist).  
- Markdown quality varies by site HTML.  
- No browser JS execution.

### Risks

| Risk | Mitigation |
|------|------------|
| SSRF to cloud metadata | Explicit blocklist independent of LAN allow |
| Huge pages / JSON | Byte + char caps |
| Prompt injection in page text | Same as any tool output |

## Acceptance criteria

- [x] Open questions answered (`0012-answers.json`)  
- [x] LAN allow + JSON raw + markdown HTML locked  
- [x] `web_fetch` implemented with tests  
- [x] System prompt updated (**Q8**)  
- [x] Implemented  

## References

- `adr/0012-answers.json`  
- ADR-0006 MCP / Tavily  
- ADR-0010 turn cancel  
- ADR-0011 multi-provider search (deferred)  
