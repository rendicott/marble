# ADR-0029: Reliable image attachments from URLs

| Field | Value |
|-------|--------|
| **Status** | **Accepted** (2026-09-14) — implemented (M1 + chip source hint) |
| **Date** | 2026-08-22 |
| **Accepted** | 2026-09-14 |
| **Author** | — |
| **Deciders** | Project owner |
| **Tags** | attachments, images, tools, web_fetch, message_attach, ssrf, provenance, multimodal |
| **Extends** | ADR-0019 (session attachments / blobs), ADR-0012 (`web_fetch`), ADR-0005 (tool suite), ADR-0003 (SQLite + blobs), ADR-0016 (secrets / env pattern where stock APIs appear later) |
| **Related** | ADR-0011 (web search MCP — deferred policy), ADR-0025 (Wonderstand presentation / `image_attachment_id`), ADR-0027 (TTS stages audio attachments — parallel pattern), ADR-0024 (Wonderstand client) |
| **Does not supersede** | ADR-0019 inbound paste/upload; ADR-0012 text-oriented `web_fetch` remains for HTML/JSON pages |
| **Review UI** | [0029-review.html](0029-review.html) |
| **Sketch** | [docs/image-attach-from-url-sketch.md](../docs/image-attach-from-url-sketch.md) |
| **Answers** | [0029-answers.json](0029-answers.json) (`2026-09-14T20:22:06.830Z`) — all Q1–Q10 locked |

## Summary

Agents often **discover** image URLs (web search MCP with `include_images`, page scrape, user paste, stock APIs) but cannot **reliably** turn them into durable chat artifacts. Today the only first-class path is **`message_attach` from a workspace file path**. Getting from `https://…/tiger.jpg` to a session `attachment_id` requires a brittle multi-step dance (shell/`web_fetch` misuse → write file → `message_attach`) that models skip or botch.

This ADR locks **foundational harness support** so any client (web UI, Wonderstand, API consumers) can depend on:

1. Agent tool **`attach_from_url`** that **HTTP(S) GETs** image URL(s), validates bytes, and **stages chat attachment(s)**.  
2. **HTTP API** `POST …/attachments/from_url` in the **same first wave (M1)** as the tool.  
3. **Provenance** via attachment **`meta_json`** (source URL, content-type, optional credit fields).  
4. **SSRF / size / MIME policy** aligned with `web_fetch` and ADR-0019 sniff rules (LAN allowed; SVG rejected on this tool; default **8 MiB**).  
5. Clear **non-goals**: not a stock-photo product, not automatic Tavily→UI injection, not Wonderstand-only.

Downstream products (immersion, prompt packs, stock providers) become **thin consumers** of `attachment_id` instead of inventing per-client hotlink logic.

## Context & pain

### What works today (ADR-0019)

| Path | Result |
|------|--------|
| User paste / upload | Staged attachment; multimodal history when `cap_images` |
| `message_attach` + workspace `path` | Durable chat chip + blob |
| Peer `computer_screenshot` | Bytes → `StageChatAttachment` |
| TTS (ADR-0027) | Synth bytes → audio attachment (trusted path) |
| Markdown `![](https://…)` in assistant text | **Some** clients (e.g. Wonderstand) may hotlink; **not** a harness attachment; not durable; not in blob store |

### What fails

| Situation | Failure mode |
|-----------|----------------|
| Tavily / search returns `images: ["https://…"]` | URLs live only in **tool_result** JSON unless the model copies them |
| Prompt says “attach images” | No one-shot tool; model invents shell curls or drops images |
| `web_fetch(image_url)` | Designed for **HTML→markdown / JSON text**, not binary image staging |
| Hotlink in final markdown | Breaks offline, expires, no GC, weak rights story, inconsistent across clients |
| Want `presentation.visual.image_attachment_id` | Requires a real id from ADR-0019 store — URL alone is insufficient |

### Design principle (same as TTS)

**Bytes in the session store beat ephemeral URLs.**  
Discovery may be MCP/search; **canonical display artifact** is `attachment_id` (+ optional provenance).

## Goals

1. **One reliable agent step:** URL (or small list) → validated image bytes → `attachment_id` + tool result JSON the model can cite.  
2. **Reuse ADR-0019** storage, GET, sniff, session GC — do not invent a second blob system.  
3. **Safe by default:** SSRF controls (share/extend `web_fetch` policy), max bytes, image MIME allowlist, redirect limits.  
4. **Provenance:** persist at least `source_url` (and optional title/credit/license later) without blocking v1 if metadata is sparse.  
5. **Client-agnostic:** web transcript chips and any future immersion path both consume the same ids.  
6. **Optional HTTP surface** for harness-native callers (tests, scripts, future non-agent UI).  
7. **Document** prompt/skill guidance: prefer `attach_from_url` over shell download for images.  
8. **Phased:** tool first → provenance/API polish → optional stock providers / batch helpers later.

## Non-goals

| Non-goal | Why |
|----------|-----|
| Wonderstand immersion UX / double-tap / tables | Client ADR territory; this only supplies attachments |
| Auto-inject every Tavily image into the UI | Policy + spam + rights; leave to prompts/skills or a later ADR |
| Replacing MCP web search | Search stays MCP (ADR-0011); this is **fetch+stage** after discovery |
| Full stock marketplace (Unsplash/Pexels product) | Optional later provider module; v1 is **generic URL attach** |
| Guaranteeing copyright clearance | Tool can store **claimed** license metadata; operator policy still required |
| PDF / video / arbitrary binary | Stay on ADR-0019 image (and existing doc) allowlists |
| Changing `web_fetch` into a binary downloader for all types | Keep `web_fetch` text-oriented; new tool owns image bytes |
| Re-hosting Unsplash in violation of hotlink-only ToS | Provider-specific rules are a **later** concern; generic tool may still fetch many CDNs — document risk |
| Model image generation (DALL·E etc.) | Different feature |

## Decision drivers

1. **Reliability over cleverness** — one tool call beats three.  
2. **Parity with screenshots/TTS** — all “show the user media” paths converge on `StageChatAttachment`.  
3. **Security** — URL fetch is SSRF-shaped; must not open cloud metadata or unbounded downloads.  
4. **Commercial cleanliness** — core harness ships the **capability**; search/stock keys remain operator choice.  
5. **Additive** — existing `message_attach(path)` and user upload unchanged.

## Proposed design

### A. Agent tool: `attach_from_url` (**Q1** locked)

```text
attach_from_url({
  "url": "https://example.com/photo.jpg",   // single URL, or
  "urls": ["https://a.jpg", "https://b.jpg"], // batch, max 4 (Q6)
  "name": "siberian-tiger.jpg",             // optional display name (single)
  "alt": "Siberian tiger in snow",          // optional; stored in provenance / UI hint
  "source_page": "https://…",               // optional page where image was found
  "credit": "Photo: …",                     // optional free-text attribution
  "license": "…"                            // optional free-text; not validated in v1
})
```

Require exactly one of `url` or `urls` (non-empty). Batch failures are **per-URL** in the result array (partial success OK).

**Behavior:**

1. Validate URL scheme `http`/`https` only.  
2. Apply **SSRF policy** (same family as ADR-0012 `web_fetch`: block cloud metadata / dangerous link-local; **LAN/private allowed** — **Q2**).  
3. GET with timeout, **max 5 redirects** (**Q3**), re-validate host after each hop; default **`max_bytes` = 8 MiB** (**Q10**), clamped to ADR-0019 image max if lower.  
4. **Sniff** magic bytes + Content-Type; accept **image** kinds only (png/jpeg/webp/gif). **Reject SVG** on this tool (**Q4**).  
5. `StageChatAttachment(sessionID, name, data)` → `id`, `mime`, `kind`.  
6. Persist **provenance** on attachment row **`meta_json`** (**Q5**).  
7. Emit durable UI chip (`OnChatAttachment`) like `message_attach`.  
8. Return tool JSON (single) or `{ "ok": true, "results": [ … ] }` (batch):

```json
{
  "ok": true,
  "attachment_id": "att_…",
  "mime": "image/jpeg",
  "kind": "image",
  "bytes": 183422,
  "name": "siberian-tiger.jpg",
  "source_url": "https://…",
  "note": "chat attachment (durable); cite attachment_id in reply if showing the image"
}
```

**Batch (Q6 locked):** support `urls[]` with **max 4** in **M1** (not deferred).

### B. Relationship to existing tools

| Tool | Role after this ADR |
|------|---------------------|
| MCP search (`include_images`) | **Discover** candidate URLs |
| `web_fetch` | **Read pages** (HTML/JSON text) |
| **`attach_from_url`** | **Materialize** image URL → attachment |
| `message_attach` | Workspace path → attachment (unchanged) |
| `attach_file` | Ephemeral workspace chip (unchanged) |

System prompt / tool description one-liner:

> After search returns image URLs, call `attach_from_url` then reference `attachment_id` (or markdown that clients resolve). Do not shell-curl images into the workspace unless the URL tool fails.

### C. HTTP API (**Q9** locked — **M1**)

```http
POST /api/sessions/{id}/attachments/from_url
{ "url": "https://…", "name": "…", "alt": "…", "credit": "…" }
// or { "urls": ["https://a", "https://b"] }  // max 4
→ 201 { "attachment_id", "mime", "kind", "bytes", "source_url" }
// batch → 201 { "results": [ … ] } with per-URL ok/error
```

Auth same as session API. Ships **with the tool in M1** (not deferred to M2). Enables tests and non-agent clients without spinning a turn.

GET attachment bytes remain ADR-0019 (`/api/sessions/{id}/attachments/{attId}` or equivalent).

### D. Provenance storage (**Q5** locked: `meta_json`)

Minimum viable:

| Field | Required v1 |
|-------|-------------|
| `source_url` | **Yes** (the fetched URL, after redirects final URL if available) |
| `fetched_at` | Yes |
| `content_type` / mime | Yes (sniffed) |
| `name`, `alt`, `credit`, `license`, `source_page` | Optional |

**Locked:** DB column / field **`meta_json`** on the attachment row (not display-name-only; not a separate table in v1).

UI: web chip tooltip / footer “Source: …” when meta present. Clients may ignore.

### E. Security & limits

| Control | Locked |
|---------|--------|
| Schemes | `http`, `https` only |
| SSRF | Reuse/extend `web_fetch` blocker list; **LAN allowed**; block cloud metadata |
| Redirects | **5**; re-check host after each hop |
| Size | Default **8 MiB**; `Content-Length` pre-check when present; stream with hard max |
| MIME | Sniff wins over `Content-Type`; **images only**; **no SVG** |
| Rate | Per-session / per-turn cap on tool calls (e.g. 5–10) to limit abuse |
| Auth to origin | No arbitrary custom headers in v1 (avoid open proxy with cookies) |

### F. Caching (**Q7** locked)

| Phase | Behavior |
|--------|----------|
| **M1** | **No cache** — each successful fetch stages a new attachment |
| **Later (optional)** | Session hash(final_url) reuse if cost/latency hurts |

TTS already caches by content hash; images can follow that pattern in a later milestone if needed.

### G. Model-visible vs UI-only (**Q8** locked)

ADR-0019 `message_attach` is **durable UI**, not fully re-injected as a giant tool result image by default.

**Locked for `attach_from_url`:**

- Same as `message_attach`: **chip + id in tool result text**;  
- Multimodal re-include for **vision models** follows existing attachment / `marble-att://` rules — **no second pipeline** in v1 unless gaps appear.

### H. Failure modes (tool returns structured error, not panic)

| Case | Tool result |
|------|-------------|
| SSRF blocked | `ok: false`, `error: "url_blocked"` |
| Timeout / DNS | `error: "fetch_failed"` |
| Too large | `error: "too_large"` |
| Not an image | `error: "unsupported_type"` |
| 404 | `error: "http_status", "status": 404` |

Model can fall back to prose or SVG without crashing the turn.

## Phased delivery

| Phase | Scope | Exit criteria |
|-------|--------|----------------|
| **M0** | This ADR + review answers | **Done** — Q1–Q10 locked 2026-09-14 |
| **M1** | `attach_from_url` (single + **batch max 4**) + `meta_json` provenance + **`POST …/from_url`** + tests + prompt blurb | Agent/API: URL(s) → chip(s) in one call |
| **M2** | Web UI source/credit hint on chips; polish errors/rate limits | Meta visible in transcript |
| **M3** | Optional session URL cache | Fewer dup downloads |
| **M4** | Optional stock provider tools (Pexels/Unsplash/Commons) **or** skill-only | Rights-aware search separate from generic fetch |

Wonderstand / presentation wiring is **out of M1–M2** except as a smoke consumer: if assistant message lists `attachment_id` or markdown that resolves to attachments, existing clients improve for free.

## Consequences

### Positive

- Removes the main reliability gap between “search found images” and “chat has images.”  
- One pattern for screenshots, TTS audio, and remote photos.  
- Enables future immersion and web UI without hotlink fragility.  
- Keeps search/stock as operator-optional (commercial story intact).

### Negative / risks

- SSRF surface grows (mitigate with shared policy + tests).  
- Operators may assume “attached = licensed” — **document that it does not**.  
- Some CDNs block datacenter IPs or require Referer — tool will fail honestly.  
- Unsplash-class ToS may prefer hotlink; generic rehost can be non-compliant for that vendor — M4 provider tools must encode vendor rules.

### Neutral

- `web_fetch` stays text-centric.  
- Prompt envelopes (Wonderstand) remain useful but no longer the only lever.

## Decisions locked (Q1–Q10)

Source: `adr/0029-answers.json` (`2026-09-14T20:22:06.830Z`). **Q1–Q5, Q7–Q8, Q10** use rec; **Q6** and **Q9** are **custom** (batch + HTTP pulled into M1).

| ID | Decision | Choice |
|----|----------|--------|
| **Q1** | Tool name = **`attach_from_url`** | rec |
| **Q2** | LAN / private hosts **allowed** (block cloud metadata) | rec |
| **Q3** | Max redirects = **5** | rec |
| **Q4** | **Reject SVG** on this tool | rec |
| **Q5** | Provenance = attachment **`meta_json`** | rec |
| **Q6** | **Batch max 4 URLs in M1** | **custom** (rec was single-only until M3) |
| **Q7** | **No URL cache in M1** | rec |
| **Q8** | Vision re-inject **follows ADR-0019** | rec |
| **Q9** | **`POST …/attachments/from_url` in M1** with the tool | **custom** (rec was M2) |
| **Q10** | Default max download = **8 MiB** | rec |

### Custom decision notes

> **Q6:** First wave accepts `urls[]` (max 4) so search→attach does not need four serial tool calls.  
> **Q9:** HTTP API ships with the tool so tests/scripts/non-agent clients are unblocked immediately; shared implementation behind tool + handler.

## Implementation sketch (non-normative)

```
internal/tools/attach_url.go
  - attachFromURL(args, tc) → fetch → sniff → StageChatAttachment → OnChatAttachment
  - support url | urls (max 4)

internal/netx/ or reuse web_fetch transport
  - shared BlockedHost / redirect checks (LAN ok)

internal/db/attachments.go
  - MetaJSON on attachment row

internal/api/attachments.go
  - POST from_url (single + batch) in M1

tests: SSRF blocked host, tiny PNG happy path, batch partial fail, oversized reject, non-image reject, SVG reject
```

## References

- ADR-0019 Multimodal attachments  
- ADR-0012 `web_fetch`  
- ADR-0027 Server-side TTS (bytes → attachment pattern)  
- ADR-0025 Wonderstand protocol (`image_attachment_id`)  
- Tool specs: `message_attach`, `web_fetch` in `internal/tools/specs.go`

## Changelog

| Date | Change |
|------|--------|
| 2026-08-22 | Proposed — foundational URL→attachment reliability |
| 2026-09-14 | **Accepted** — locked Q1–Q10 (`2026-09-14T20:22:06.830Z`); Q6/Q9 custom (batch + HTTP in M1) |
| 2026-09-14 | **Implemented (M1)** — `attach_from_url` (single + batch max 4), `meta_json` provenance, `POST /api/sessions/{id}/attachments/from_url`, tests, prompt blurb; chip tooltip shows source/credit when present |
