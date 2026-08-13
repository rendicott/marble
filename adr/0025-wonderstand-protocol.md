# ADR-0025: Wonderstand Protocol — Structured I/O between harness and Android client

| Field | Value |
|-------|--------|
| **Status** | **Accepted** (ready to implement) |
| **Date** | 2026-08-08 |
| **Accepted** | 2026-08-08 |
| **Author** | — |
| **Deciders** | Project owner |
| **Tags** | wonderstand, protocol, presentation, immersion, TTS, SSE, android, client |
| **Extends** | ADR-0024 (Wonderstand pointer), ADR-0010 (turn/SSE), ADR-0019 (attachments), ADR-0013 (soul/system); peer design in **marble-wonderstand** ADR-0001 |
| **Review UI** | [0025-review.html](0025-review.html) |
| **Sketch** | [docs/wonderstand-protocol-sketch.md](../docs/wonderstand-protocol-sketch.md) |
| **Answers** | [0025-answers.json](0025-answers.json) (`2026-08-08T16:07:31.856Z`) — all Q1–Q8 locked |

## Summary

Define a **versioned, optional presentation block** on **assistant** messages so Wonderstand can drive immersion (TTS + full-screen visuals + scrubber) without fragile markdown heuristics. The wire contract is **additive**: existing `content` markdown remains the source of truth for the web UI and older clients. Wonderstand advertises capability; the harness may enrich replies later; both sides tolerate missing fields.

## Context & device pain

Wonderstand (ADR-0024 / marble-wonderstand ADR-0001) already ships against today's harness APIs:

| Surface | Shape today |
|---------|-------------|
| Create session | `POST /api/sessions` body `{"title":"…"}` |
| Send turn | `POST /api/sessions/{id}/messages` `{"content":"…","attachment_ids":[…]}` |
| Hydrate | `GET /api/sessions/{id}` → `messages[]` with `id`, `role`, `content`, `attachments[]`, … |
| Live | `GET /api/sessions/{id}/events` SSE `Event{type, message?, tool?, turn?, status?, …}` |

`session.Message` (harness):

```go
type Message struct {
    ID, Role, Content string
    Attachments []UIAttachment `json:"attachments,omitempty"`
    // … tool fields, actor, timestamps
}
```

Immersion is **client-side only**: prompt envelope on every turn + `ArtifactParser` over raw markdown/fences/attachments ([immersion-timeline.md](../../marble-wonderstand/docs/immersion-timeline.md)).

### Observed pain

| Pain | Why it matters |
|------|----------------|
| **TTS reads raw markdown** | System TTS speaks `**bold**`, `# headings`, table pipes, fence markers |
| **SVG fences blank on app** | Fence parse / WebView load races; no first-class visual payload |
| **Heuristic ArtifactParser** | Order/rank of prose vs table vs image drifts; hard to test; model format changes break UX |
| **No stable phase IDs** | Scrubber / swipe cannot address “beat 2” across rehydrate and partial SSE |
| **Prompt envelope only** | Visual bias is client-prepended text; harness does not know client capabilities; web sessions get the same envelope if shared |

ADR-0024 already listed optional later work: `X-Marble-Client`, soul snippet, structured `artifacts[]`. This ADR makes that **contract-first**.

## Goals

1. **Stable immersion contract:** ordered **phases** with stable `id`s for scrubber, swipe, and rehydrate.  
2. **TTS-safe speech:** plain `speech_text` per phase (no markdown).  
3. **First-class visuals:** `svg` | `html` | `image_attachment_id` | `table` (structured) without depending on fence heuristics.  
4. **Optional prose:** `prose_markdown` when studio / captions need rich text.  
5. **Client advertise:** Wonderstand declares `client: {name, protocol}` on createSession and postMessage so the harness can branch later.  
6. **Backward compatible:** omit `presentation` → behavior unchanged; web UI ignores unknown fields; markdown `content` still required for web.  
7. **Additive storage/SSE:** optional JSON column / field on messages and the same object on SSE `message` events.  
8. **Phased delivery:** design (this ADR) → advertise/passthrough → prompt pack → validate/enrich — no big-bang harness rewrite.

## Non-goals

| Non-goal | Rationale |
|----------|-----------|
| Full harness implementation in this ADR | Contract + plan only |
| Breaking change to `content` or attachment APIs | Web + older Wonderstand must keep working |
| Replacing web transcript rendering | Markdown remains primary for operator cockpit |
| Forcing models to emit JSON on every turn in M1 | Passthrough first; prompt/enrich later |
| Secrets, OAuth redesign, new auth modes | Unrelated |
| iOS / multi-client protocol negotiation beyond advertise | Wonderstand v1 focus |
| Streaming token-level presentation | Turn-complete / final assistant message only in p1 (**Q5** locked); progressive mid-turn deferred |
| User-message presentation | Protocol 1 is **assistant only** (**Q4** locked) |
| Perfect auto-extraction of phases from arbitrary markdown | M3 may offer best-effort enrich; heuristics stay client fallback |
| New tool required for presentation in p1 | No — fence extract + enrich path (**Q2** locked) |

## Protocol overview

```
Wonderstand                         marble-harness
    │                                      │
    │  POST /sessions  + client advertise  │
    │  POST …/messages + client advertise  │
    │ ──────────────────────────────────►  │
    │                                      │  runTurn (markdown content as today)
    │                                      │  optional: enrich presentation (M2+)
    │  SSE message { content, presentation?}│
    │  GET session { messages[].presentation?}
    │ ◄──────────────────────────────────  │
    │  ImmersionPlayer prefers presentation│
    │  else ArtifactParser fallback        │
```

**Version:** integer `protocol_version` inside the presentation object. Wire advertise uses `client.protocol` (same major). This ADR defines **protocol 1**.

## Wire shapes

### 1. Client advertise (request body, additive)

On **`POST /api/sessions`** and **`POST /api/sessions/{id}/messages`**, optional:

```json
{
  "title": "ws: package status",
  "client": {
    "name": "wonderstand",
    "protocol": 1
  }
}
```

```json
{
  "content": "…user text…",
  "attachment_ids": ["att_…"],
  "client": {
    "name": "wonderstand",
    "protocol": 1
  }
}
```

| Field | Type | Notes |
|-------|------|--------|
| `client.name` | string | e.g. `wonderstand`; web may omit or send `web` |
| `client.protocol` | int | Max presentation protocol the client understands (1) |

**Header (optional, complementary):** keep/extend `X-Marble-Client: wonderstand` for logs/metrics (ADR-0024). **Body `client` is normative** for protocol negotiation (**Q8**). Header-only ⇒ name without protocol (no enrichment assumptions).

**Sticky client (**Q7**):** `createSession` sets the session default; each `postMessage` may override for that turn (last post wins for enrichment).

Harness **must ignore** unknown `client` fields. If absent → treat as classic client (no presentation expectations).

### 2. Presentation block (message field, additive)

Optional on UI `Message` with **`role: assistant` only** in protocol 1 (**Q4**):

```json
{
  "id": "msg_…",
  "role": "assistant",
  "content": "# Status\n\nPackage **in transit**…\n\n| Field | Value |\n|---|---|\n| Carrier | USPS |\n\n```svg\n<svg…/>\n```\n",
  "attachments": [
    { "id": "att_abc", "name": "tracking.png", "mime": "image/png", "kind": "image" }
  ],
  "presentation": {
    "protocol_version": 1,
    "phases": [
      {
        "id": "p1",
        "speech_text": "Your package is in transit with USPS.",
        "prose_markdown": "Package **in transit** with USPS.",
        "visual": {
          "kind": "table",
          "table": {
            "headers": ["Field", "Value"],
            "rows": [["Carrier", "USPS"], ["ETA", "Thu"]]
          }
        }
      },
      {
        "id": "p2",
        "speech_text": "Here is the tracking page screenshot.",
        "visual": {
          "kind": "image_attachment_id",
          "image_attachment_id": "att_abc"
        }
      },
      {
        "id": "p3",
        "speech_text": "Route sketch from origin to local hub.",
        "visual": {
          "kind": "svg",
          "svg": "<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 100 40\">…</svg>"
        }
      }
    ]
  }
}
```

### 3. Schema (normative for protocol 1)

```text
Presentation {
  protocol_version: int          // 1
  phases: Phase[]                // ordered immersion beats; may be empty
}

Phase {
  id: string                     // stable within message; client scrubber key
  speech_text: string            // plain text for TTS (required if phase is spoken)
  prose_markdown?: string        // optional rich caption / studio
  visual?: Visual                // omit = speech-only / hold previous visual (Q3 locked)
}

Visual {
  kind: "svg" | "html" | "image_attachment_id" | "table"
  // exactly one payload field matching kind:
  svg?: string                   // full <svg>…</svg> document fragment
  html?: string                  // sandboxed HTML card (no scripts expected)
  image_attachment_id?: string   // must reference message.attachments[] or session attachment
  table?: {
    headers: string[]
    rows: string[][]             // rectangular; cells plain text
  }
}
```

**Rules (p1):**

1. `content` markdown **remains required** for assistant turns that the web UI will show; presentation is enrichment, not a replacement.  
2. `speech_text` must be plain language suitable for TTS (no fences, minimal punctuation noise).  
3. `phase.id` unique within the message; recommend short opaque ids (`p1`, `p2` or ULIDs).  
4. `image_attachment_id` must resolve via existing attachment GET; do not embed base64 in presentation.  
5. Unknown `visual.kind` → client skips visual, still speaks `speech_text` if present.  
6. Higher `protocol_version` than client supports → **ignore entire `presentation`** and fall back to ArtifactParser (**Q1** locked — fail closed).

### 4. SSE

Existing `Event` with `type: "message"` carries the full `Message` object today. **Additive only:** when `presentation` is set on the stored message, it appears on the same `message` payload. No new SSE event type required for M1–M2.

**p1:** attach `presentation` only on the **final assistant message** of the turn (**Q5** locked). Progressive mid-turn phase deltas deferred.

### 5. Persistence (SQLite, additive)

Prefer a nullable column or JSON blob on the dual-write path used for UI messages, e.g.:

```sql
-- sketch; exact migration number TBD at implement time
ALTER TABLE … ADD COLUMN presentation_json TEXT;  -- NULL = absent
```

Or store inside existing message JSON if messages are already blobbed. **Must not** require rewriting old rows; NULL/absent ≡ no presentation.

Session-level optional cache of last advertised client:

```text
Session.client_name, Session.client_protocol  // optional, for soul/metrics
```

No requirement that advertise is durable across process restart for M1 (in-memory on Session is enough).

## Production behavior by milestone

| Milestone | Client | Harness |
|-----------|--------|---------|
| **M0** | — | This ADR + sketch + review |
| **M1** | Send `client` on create + postMessage; prefer `presentation` if present else ArtifactParser | Accept/ignore `client`; pass through / store `presentation` if model or tool already produced it (no requirement to generate yet) |
| **M2** | Keep envelope; optionally slim when protocol≥1 | When `client.name=wonderstand` && `protocol≥1`, inject **Wonderstand prompt pack** (soul snippet or turn preamble) steering markdown quality + TTS-friendly structure (**Q2**) |
| **M3** | Prefer validated presentation; parser is fallback | Prefer optional **` ```presentation `** fence in `content` (debuggable) → parse into `presentation` field; heuristic enrich as backup; **validate/normalize** (ids, attachment refs, size caps). No new tool required in p1 (**Q2**) |

### Prompt pack (M2 sketch)

Not a separate API: harness system/soul branch when client advertises Wonderstand, e.g.:

```text
If the user is on Wonderstand (immersion client), structure the final answer so a
presentation JSON could be derived: short TTS-friendly paragraphs; concrete
visuals (svg fence / table / image attachment). Prefer lead-with-visual.
Do not omit normal markdown — web and logs still need content.
Optional: emit a single ```presentation fenced JSON block matching protocol 1
(phases with id, speech_text, visual) for harness M3 extract.
```

**Production path (**Q2** locked):** M2 prompt pack steers quality; M3 prefers fence-in-content → `presentation` field (strip fence from spoken path); heuristic enrich backup; no new tool in p1.

### Size / safety caps (**Q6** locked)

| Cap | Value |
|-----|-------|
| Max phases per message | **32** |
| Max `speech_text` per phase | **2 KiB** |
| Max `svg` / `html` bytes per phase | **256 KiB** |
| Max total `presentation_json` | **1 MiB** |
| On overflow | **Clamp + log**; do **not** fail the whole turn |
| HTML | Client WebView no-JS (existing Wonderstand policy); harness does not execute |

## Client algorithm (normative sketch)

```text
on assistant message M:
  if M.presentation exists AND M.presentation.protocol_version <= client.supported:
    timeline = phases_to_timeline(M.presentation.phases)
    // phase without visual → hold previous visual (Q3)
    // phase.id → scrubber index; speech_text → TTS; visual → screen
  else:
    // missing, invalid, or newer protocol_version (Q1 fail closed)
    timeline = ArtifactParser.parseAssistant(M)
  play(timeline)
```

Parser remains for: older harness, web-authored sessions, failed validation, protocol mismatch.

## Alternatives considered

| Alternative | Why not default |
|-------------|-----------------|
| Client-only forever (envelope + parser) | Already pain; scrubber/TTS/SVG reliability blocked |
| Replace `content` with structured-only | Breaks web UI and tools that expect markdown |
| New `/api/wonderstand/*` surface | Duplicates sessions/messages; prefer additive fields |
| SSE-only presentation (not on GET hydrate) | Scrubber/reopen session would lose beats |
| XML/SSML in content | Harder for web; TTS engines vary; keep plain `speech_text` |
| Force JSON tool `emit_presentation` only | Useful later; not required for M1 passthrough |

## Decisions locked (Q1–Q8)

| ID | Decision | Locked |
|----|----------|--------|
| **Q1** | Newer `protocol_version` than client | **Ignore entire presentation** + ArtifactParser fallback (fail closed) |
| **Q2** | How presentation is produced (M2/M3) | **M2 prompt pack** steers markdown; **M3** prefer optional ` ```presentation ` fence → field, heuristic enrich backup; **no new tool in p1** |
| **Q3** | Phase without `visual` | **Hold previous** visual (immersion-timeline spirit) |
| **Q4** | User-message presentation | **No in protocol 1** — assistant only |
| **Q5** | Progressive mid-turn phases via SSE | **Defer** — presentation on **final assistant message** only in p1 |
| **Q6** | Size caps | **32** phases; **2 KiB** speech_text/phase; **256 KiB** svg/html/phase; **1 MiB** total; **clamp + log**, do not fail turn |
| **Q7** | Sticky client | **createSession** sets default; each **postMessage** may override (last post wins for enrichment) |
| **Q8** | Body `client` vs `X-Marble-Client` | **Body normative** for protocol; header optional metrics; header-only ⇒ name without protocol (no enrichment assumptions) |

*Source: [0025-answers.json](0025-answers.json) (`2026-08-08T16:07:31.856Z`).*

## Implementation plan

| ID | Scope | Owner |
|----|--------|--------|
| **M0** | This ADR, review HTML, sketch doc, link from 0024 | Docs |
| **M1** | API: accept `client` on create/postMessage; optional store on Session; `presentation` passthrough on Message + SQLite + SSE + GET hydrate; no generation | Harness + Wonderstand client consume |
| **M2** | Wonderstand prompt pack / soul branch when advertised; client may reduce envelope duplication | Harness prompt + Wonderstand settings |
| **M3** | Validate phases (ids, caps, attachment refs); optional fence extract / enrich; metrics | Harness; client hardened player |

### Suggested PR slice (post-accept)

| PR | Scope |
|----|--------|
| **W0** | Types + JSON tags; ignore unknown; tests for decode |
| **W1** | Persist `presentation_json`; UIMessages + SSE include field |
| **W2** | Advertise plumbing + session sticky client |
| **W3** | Prompt pack when wonderstand+protocol≥1 |
| **W4** | Validate/caps + optional fence extract |
| **W5** | Wonderstand app: prefer presentation, scrubber by `phase.id` |

## Success metrics

- Immersion scrubber can jump to phase `id` after full rehydrate without re-parsing markdown.  
- TTS speaks `speech_text` without markdown tokens on protocol-1 messages.  
- SVG/HTML phases render from structured fields (not only fence scan).  
- Web UI unchanged when `presentation` absent or present (ignores field).  
- Old Wonderstand builds still work (no required request fields).

## Risks

| Risk | Mitigation |
|------|------------|
| Model omits presentation | Parser fallback; M2 prompt; M3 enrich |
| Huge SVG in JSON | Caps (**Q6** locked); attachment for large rasters |
| Dual source of truth (content vs phases) drift | content remains canonical for web; presentation is view model |
| Protocol drift Android vs harness | `protocol_version` + advertise; fail closed (**Q1** locked) |

## See also

- [ADR-0024 Wonderstand client (pointer)](0024-wonderstand-client.md)  
- marble-wonderstand [ADR-0001](../../marble-wonderstand/adr/0001-wonderstand-client.md), [immersion-timeline.md](../../marble-wonderstand/docs/immersion-timeline.md), [api-client.md](../../marble-wonderstand/docs/api-client.md)  
- [ADR-0019 Multimodal attachments](0019-multimodal-attachments.md)  
- [ADR-0010 Agent loop transparency / SSE](0010-agent-loop-transparency.md)  
- [docs/wonderstand-protocol-sketch.md](../docs/wonderstand-protocol-sketch.md)

## Changelog

| Date | Note |
|------|------|
| 2026-08-08 | **Proposed** — protocol 1 sketch; M0–M3 plan; Q1–Q8 open for review |
| 2026-08-08 | **Accepted** — locked Q1–Q8 (`2026-08-08T16:07:31.856Z`); ready for W0–W5 implementation |
