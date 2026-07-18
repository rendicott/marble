# ADR-0012: Native `web_fetch` Tool (URL retrieval after search)

| Field | Value |
|-------|--------|
| **Status** | Proposed |
| **Date** | 2026-07-18 |
| **Deciders** | Project owner |
| **Tags** | tools, http, web, tavily, mcp, research |
| **Extends** | ADR-0005 (tool suite), ADR-0006 (MCP / Tavily recipe), ADR-0011 (multi-provider search — deferred) |
| **Related** | Operators already use `mcp_tavily_tavily_search` (returns titles + **URLs**) |

## Context

### How research works today in Marble

1. **Search** via MCP (typically Tavily): `mcp_tavily_tavily_search` returns structured hits including **Title** and **URL** (confirmed in live `session_events`).  
2. **Deeper retrieval** often goes back through Tavily: `tavily_extract`, `tavily_crawl`, `tavily_map`, `tavily_research` — vendor-side fetch/summarize, extra latency/cost, and sometimes timeouts (`tavily_research` has hit context deadlines).  
3. Marble has **no first-class HTTP fetch** of an arbitrary URL. Workspace `file_read` cannot load `https://…`. Shell `curl` works only if shell is enabled and policy allows — not a clean, model-friendly tool.

### Desired pattern (industry common)

```
search (Tavily/Brave/…)  →  list of real URLs + snippets
        ↓
web_fetch(url)           →  harness GETs page, returns text/markdown excerpt
        ↓
model reasons / cites URL
```

**Search provider’s job:** discovery + ranked URLs (and short snippets).  
**Harness job:** **direct retrieval** of chosen URLs for grounding, without round-tripping content through the search vendor when a simple GET + extract is enough.

This complements ADR-0006 (MCP for search) and does **not** replace Tavily; it **reduces overuse of extract/crawl/research** for ordinary “open this link” steps.

### Why not only Tavily extract?

| Approach | Pros | Cons |
|----------|------|------|
| Tavily extract/crawl only | Good JS-heavy / anti-bot pages; vendor parsing | Cost, latency, vendor lock-in, timeouts on heavy research tools |
| Shell `curl` only | Already available | Policy noise, no size/timeout UX, easy to dump binary/HTML |
| **Native `web_fetch`** | Predictable limits, SSRF policy, text extraction, tool schema for the model | Won’t beat a full browser; need allow/deny network rules |

## Goals

1. Add a **built-in tool** (proposed name: `web_fetch`) that retrieves **one URL** (or a small batch — open Q) over HTTP(S).  
2. Return **text-oriented** content suitable for the model (markdown or plain text), with hard **size / time** caps.  
3. Teach the agent (system prompt + tool description) the preferred pipeline:  
   **Tavily search → pick URLs → `web_fetch`**, not “search then always extract via Tavily.”  
4. **Require search results to surface real URLs** before fetch (model must not invent URLs; tool rejects non-http(s)).  
5. Safe defaults: no SSRF to link-local/metadata, honor redirects carefully, no credential stuffing.

## Non-goals (v1)

- Full headless browser / Playwright (JS-rendered SPAs) — later if needed  
- Replacing Tavily MCP entirely  
- Implementing multi-provider search policy (ADR-0011 deferred)  
- POST/PUT APIs or authenticated scraping vault  
- PDF/binary OCR pipeline (optional later; v1 may skip or stub non-HTML)

## Decision (proposed)

### 1. Native tool: `web_fetch`

```json
{
  "name": "web_fetch",
  "description": "HTTP(S) GET a URL and return extracted text for the model. Prefer after web search returns real URLs (e.g. mcp_tavily_tavily_search). Do not invent URLs. Use Tavily extract/crawl only when plain fetch fails or page needs vendor-side rendering.",
  "parameters": {
    "type": "object",
    "properties": {
      "url": { "type": "string", "description": "Absolute http or https URL from search results or user" },
      "max_bytes": { "type": "integer", "description": "Optional cap on downloaded body (default harness limit)" },
      "extract": { "type": "string", "enum": ["text", "markdown", "raw_html_snippet"], "description": "Extraction mode (default markdown or text)" }
    },
    "required": ["url"]
  }
}
```

**Rec defaults:** `extract=markdown` (or readable text), default max body **1–2 MiB** download, return body truncated to **`max_tool_result`** (existing ~32k chars) with note.

### 2. Pipeline policy (system prompt + tool description)

Hard requirement for agent behavior:

1. **Discover** with search MCP (`tavily_search` / future Brave) — results **must include concrete `https://` URLs**.  
2. **Select** 1–N URLs from those results (or user-supplied).  
3. **`web_fetch`** each chosen URL for content.  
4. Use **Tavily extract/crawl/research** only when:  
   - fetch fails (403/JS shell/empty), or  
   - multi-page crawl/map is explicitly needed, or  
   - user asks for Tavily research mode.

Do **not** call Tavily extract by default for every search hit.

### 3. Tavily search: “URLs first”

- Tool description for search (or a small system-prompt line) should say:  
  **“Return and prefer results that include absolute URLs; list URL per hit before summarizing.”**  
- We do **not** reimplement Tavily; we **document** that Marble’s research loop assumes search hits carry `URL: …` (as current Tavily MCP already does in practice).  
- Optional later: thin wrapper `web_search` that normalizes MCP output to `{title, url, snippet}[]` only (out of scope unless Q locks it).

### 4. Safety (SSRF + abuse)

| Rule | Proposal |
|------|----------|
| Schemes | `http` and `https` only |
| Block | localhost, link-local, private RFC1918, metadata IPs (`169.254.169.254`), raw file URLs — **open Q on private LAN** for home lab |
| Redirects | Follow limited count (e.g. 5); re-check final host against blocklist |
| Methods | GET (and HEAD optional); no custom method in v1 |
| Headers | Browser-like User-Agent; no arbitrary Authorization header in v1 (open Q) |
| Size / time | Download cap + **15–30s** timeout; cancel with turn context (ADR-0010) |
| Content-type | Prefer HTML/text/markdown/JSON; reject or stub obvious binary |

### 5. Output shape

Return a structured text blob, e.g.:

```text
url: https://example.com/page
final_url: https://example.com/page
status: 200
content_type: text/html
chars: 12040 (truncated from 80000)
---
# Extracted title
…markdown or text…
```

Log as normal `tool_call` / `tool_result` events (no full HTML in DB if over inline max — existing blob spill).

### 6. Relationship to shell

- Prefer `web_fetch` over `shell_execute` + curl for model-driven retrieval.  
- Shell remains available for edge cases when shell is enabled.

## Implementation sketch (post-accept)

1. `internal/tools/webfetch.go` — validate URL, SSRF checks, `http.Client` with timeout + turn `ctx`.  
2. HTML → text/markdown: prefer small dependency (e.g. go-readability / html2text) or stdlib strip — **open Q**.  
3. Register in `specs.go` + `Execute` switch.  
4. System prompt one-liner: search → URLs → `web_fetch`; Tavily extract secondary.  
5. Tests: allowlist public URL with httptest; block `169.254.169.254` and `127.0.0.1` (unless Q allows).  
6. Docs: README tools list + MCP research recipe update.

## Open questions

### Scope & API

1. **Tool name:** `web_fetch` vs `http_get` vs `fetch_url`?  
   *Rec: `web_fetch`.*  
2. **Batch:** single URL only vs up to N (e.g. 3) per call?  
   *Rec: single URL v1; model loops for more.*  
3. **Extraction library:** readability-style vs naive tag strip vs raw HTML snippet only?  
   *Rec: readability or equivalent if light; else text strip + title.*  
4. **JSON responses:** pretty-print JSON as text when `content-type` is JSON?  
   *Rec: yes, truncated.*  

### Safety

5. **Private network fetch** (home lab `10.x`, `192.168.x`)?  
   *Rec: block by default; optional `--web-fetch-allow-private` or settings flag later.*  
6. **Custom headers / cookies?**  
   *Rec: defer v1.*  
7. **Max redirects?**  
   *Rec: 5.*  

### Product / prompt

8. **Default research path** in system prompt as above?  
   *Rec: yes.*  
9. **Discourage Tavily extract** when `web_fetch` succeeds — soft wording only?  
   *Rec: soft; extract still allowed on failure.*  
10. **Normalize search via native `web_search`?**  
    *Rec: defer (ADR-0011 / later); MCP search is enough if URLs are visible.*  
11. **Cache** fetched URLs in `$MEMORY` or blob store for the session?  
    *Rec: defer; optional later.*  
12. **Same tool for mpub “import URL”?**  
    *Rec: agent can fetch then `mpub_publish`; no special case v1.*  

## Consequences

### Positive

- Cheaper/faster grounding: search for URLs, fetch locally.  
- Less dependency on Tavily research/extract timeouts for simple pages.  
- Clearer tool responsibilities; better citations (model saw real URL + body).  

### Trade-offs

- Won’t render heavy client-side apps.  
- Another network egress path to secure.  
- HTML quality varies by site.

### Risks

| Risk | Mitigation |
|------|------------|
| SSRF | Block private/metadata; redirect re-validation |
| Huge pages | Byte + char caps; truncation notice |
| Prompt injection in page text | Same as any tool output; optional fence in result |
| Model invents URLs | Tool + prompt: only fetch user/search URLs; 404s teach discipline |

## Acceptance criteria

- [ ] Open questions answered or defaulted  
- [ ] `web_fetch` tool shipped with SSRF tests  
- [ ] System prompt documents **search → URL → web_fetch** (Tavily extract secondary)  
- [ ] Manual: Tavily search returns URLs; fetch pulls readable text for a public doc page  
- [ ] Ready to implement  

## References

- Live Tavily search results include `URL: https://…` per hit  
- ADR-0006: MCP for search; no native web_search in that ADR  
- ADR-0011: multi-provider search policy (deferred)  
- ADR-0010: turn cancel should cancel in-flight fetch  
