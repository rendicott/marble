# ADR-0026: Transcript density — compact tools, message timestamps, thinking status

| Field | Value |
|-------|--------|
| **Status** | **Accepted** — implemented |
| **Date** | 2026-08-08 |
| **Accepted** | 2026-08-13 |
| **Author** | — |
| **Deciders** | Project owner |
| **Tags** | ui, transcript, tools, timestamps, progress, density, web |
| **Extends** | ADR-0010 (turn progress / step log), ADR-0005 (agent loop), ADR-0001 (inner loop) |
| **Review UI** | [0026-review.html](0026-review.html) |
| **Answers** | [0026-answers.json](0026-answers.json) (`2026-08-13T13:57:11.405Z`) — all Q1–Q10 locked |

## Summary

Make long multi-tool sessions readable in the web transcript:

1. **Compact tool result rows by default** (one-line chips; expand on demand).  
2. **Visible timestamps** on user, assistant, and tool headers.  
3. **Live “thinking / working” status** during a turn **plus** **persisted, collapsed thinking rows** in history (same expand pattern as tools).

Wire/API stays additive where possible. Primary work is **web UI** (`internal/web/static/`); harness already has `created_at`, tool messages, turn progress SSE, and harness advisories. Persisted thinking may need a small durable channel (see Design C).

## Context & pain

Today (`bubbleEl` in `app.js`):

| Surface | Behavior |
|---------|----------|
| **User / assistant** | Full bubbles; **no timestamp** despite `Message.created_at` on the wire |
| **Tool** | Full-width mono bubble with role = tool name and **full compacted result body** always open |
| **Turn progress** | Footer card (ADR-0010) with phase / iter / current tool — good mid-flight, collapses at end |
| **Harness** | Ephemeral-looking advisory chips in-transcript (`role: harness`) |
| **Order** | user → many tool bubbles → (live turn card) → assistant |

After a 15–40 tool turn the transcript is **mostly green tool boxes**. The human conversation (user + final assistant) is hard to scan. Operators also cannot tell *when* a reply was written without Session Info / MD.

### Observed pain

| Pain | Why it matters |
|------|----------------|
| Tool results dominate height | Every `tool_name → result…` is a full bubble; long shell/grep tails are worst |
| No datestamps | “Was this today or last week?” requires digging; multi-day sessions feel timeless |
| Quiet gaps mid-turn | Between model call and tools, only header pill / turn card updates; chat scroll area looks frozen |
| Progress card vs tools | Live card is good; permanent tool clutter after turn is the leftover mess |
| Expand-everything default | Power users want detail; default density should favor narrative |

## Goals

1. **Default-compact tools** in the web transcript: one line summary; click/tap to expand full body.  
2. **Timestamps** on user, assistant, and tool compact headers (readable, locale-friendly).  
3. **Thinking / working statuses** live during a busy turn **and** retained in history as collapsed rows (like tools).  
4. **Expand all / collapse all** for tools **and** thinking rows; single-row click expands only that row.  
5. **Remember expand preference** in localStorage first.  
6. **Keep diagnostics**: Session Info, expand-all, and copy remain possible.  
7. **No breaking GET/SSE** for older clients; additive fields only if thinking is dual-written.

## Non-goals

| Non-goal | Rationale |
|----------|-----------|
| Remove tool messages from MD / SQLite | Audit trail stays; tool compaction is presentation |
| Token streaming of assistant drafts | Separate concern |
| Wonderstand immersion chrome | Android has its own surfaces |
| Hiding tools from the model history | Transcript UI only |
| Full activity dashboard across sessions | Out of scope |
| Replacing ADR-0010 turn card | Card stays; density work is complementary |
| Turn-level “N tools” mega-accordion in v1 | Per-tool compact rows only (**Q2**) |

## Design

### A. Compact tool rows (default) — **Q1 locked**

**Collapsed (default):**

```text
┌─────────────────────────────────────────────────────────┐
│ ▸ shell_execute · 3:40 PM · “ls -la …”            [expand]│
└─────────────────────────────────────────────────────────┘
```

**Expanded:**

```text
┌─────────────────────────────────────────────────────────┐
│ ▾ shell_execute · 3:40 PM                       [collapse]
│ shell_execute → total 12 … (full body as today)         │
└─────────────────────────────────────────────────────────┘
```

| Element | Source |
|---------|--------|
| Tool name | `message.tool_name` or leading token of content |
| One-line preview | First ~120 chars of content, single-line |
| Timestamp | `created_at` subtle on header (**Q6**) |
| Expand | Click header row or chevron; body toggles |

**Grouping:** **No** turn-level accordion in v1 (**Q2**).

**Controls (**Q3**):** Expand all / Collapse all in transcript toolbar near session actions. Expands/collapses **tools and thinking** rows.

**Settings (**Q4**):** Default collapsed; remember expand-default in **localStorage** first.

**Attachments (**Q10**):** image chips **always visible** on the header even when body collapsed.

### B. Message timestamps — **Q5 / Q6 locked**

Wire already has `created_at` (RFC3339) on UI messages.

| Role | Show time? |
|------|------------|
| user | **Yes** |
| assistant | **Yes** |
| tool | **Yes**, subtle on compact header |
| thinking (history) | **Yes**, subtle on compact header |
| harness / error | optional / omit |

**Format (**Q5**):**

- Same calendar day as “now”: **time only** (locale)  
- Else: **short date + time**  
- Full ISO on `title` tooltip  

Use browser locale via `Intl.DateTimeFormat`. No backend change for timestamps.

### C. Thinking / working status — **Q7 / Q8 locked**

#### Live (while busy) — **Q7**

Keep ADR-0010 **turn progress card**.  
**Plus** one live “⋯ thinking / running tool” line **above** the turn card, driven by `turn` / `status` SSE (update in place).

```text
… tool chip …
  ⋯ thinking · calling model · i7 · 12s     ← live, updates in place
[ turn progress card ]
```

Harness advisories stay as **full bubbles** today (**Q9**); only phase/tool noise uses the live line.

#### History after turn — **Q8 (custom, locked)**

**Persist** thinking segments in the transcript, **collapsed by default** like tool rows:

- **Expand all** expands tools **and** thinking rows.  
- **Click one thinking row** expands only that row.  
- Collapsed header: short summary (e.g. `thinking · calling model · 4s` or last phase text).  
- Expanded body: fuller phase/detail text for that segment (not the entire step log dump).

```text
user ………………… 3:40 PM
  ▸ thinking · calling model · 3.2s
  ▸ file_read · 3:40 PM · …
  ▸ thinking · calling model · 1.1s
  ▸ shell_execute · 3:40 PM · …
assistant …………… 3:41 PM
```

**Persistence approach (implementation choice, prefer additive):**

| Option | Notes |
|--------|--------|
| **A (preferred for v1 UI-only)** | Client materializes thinking rows from live SSE into **session-scoped local state**, then on idle **appends collapsed history chips** that rehydrate only while tab is open — **lost on full page reload** unless B |
| **B (durable)** | Dual-write lightweight rows: UI-only `role: thinking` (or harness subtype) **not** injected into model history; MD HTML comment or skip MD body; SQLite event optional |

**Recommendation for implement:** start with **client-side history chips from the live stream** so tools stay zero-backend; if reload loss is unacceptable in QA, add **B** (UI message with `role: thinking` excluded from model prompt rebuild). Do **not** put thinking text into model-facing history.

**Cadence while live:** still **one** updating live line (not a bubble per second). On meaningful phase transitions (e.g. calling_model → running_tool → calling_model), **commit** the previous live segment as a collapsed history row, then continue the live line for the new phase. On turn idle, commit final segment and clear the live line.

### D. Visual hierarchy (target)

```
user ………………… 3:40 PM
  ▸ thinking · calling model · 2s
  ▸ file_read · 3:40 PM · …
  ▸ thinking · calling model · 1s
  ▸ shell_execute · 3:40 PM · …
  ⋯ thinking · calling model · i3 · 4s   ← live only while busy
assistant …………… 3:41 PM
  (answer markdown)
```

## Implementation sketch

| Layer | Change |
|-------|--------|
| **CSS** | `.bubble.tool.collapsed`, `.bubble.thinking`, `.msg-time`, `.thinking-live` |
| **app.js `bubbleEl`** | Timestamps; tool header + expand; thinking row same pattern |
| **app.js SSE** | Live thinking line from `turn`/`status`; on phase change / idle commit collapsed thinking rows |
| **Expand all** | Toolbar control; toggles all `.tool` + `.thinking` collapsibles |
| **localStorage** | `marble.toolsExpandedDefault` (or similar) |
| **Harness** | Optional later for durable thinking rows; not required if client-only history is accepted |

### Suggested PR slice

| PR | Scope |
|----|--------|
| **T1** | Timestamps on user/assistant (+ tool headers) |
| **T2** | Compact tools + expand/collapse + expand-all/collapse-all |
| **T3** | Live thinking line + commit collapsed thinking history on phase/idle |
| **T4** | localStorage pref + attachment chips on collapsed tools |

## Decisions locked (Q1–Q10)

| ID | Decision | Locked |
|----|----------|--------|
| **Q1** | Default tool density | **Collapsed** one-line chip; click header to expand body |
| **Q2** | Group tools into one accordion? | **No in v1** — per-tool compact rows only |
| **Q3** | Expand-all placement | Transcript toolbar near session actions |
| **Q4** | Remember expand-default | **localStorage** first; Settings only if needed |
| **Q5** | Timestamp format | Same day: time only (locale); else short date+time; ISO in `title` |
| **Q6** | Tool row timestamps? | **Yes**, subtle on compact header |
| **Q7** | Thinking live surface | **Both** — ADR-0010 turn card + live “⋯ thinking / running tool” line above it |
| **Q8** | Persist thinking after turn? | **Yes** — collapsed in history like tools; expand-all includes thinking; single click expands one row only |
| **Q9** | Harness advisories | **Keep bubbles** as today; phase/tool noise only on status/thinking path |
| **Q10** | Screenshots when tool collapsed | **Always show** attachment/image chips on header |

*Source: [0026-answers.json](0026-answers.json) (`2026-08-13T13:57:11.405Z`).*

## Alternatives considered

| Alternative | Why not default |
|-------------|-----------------|
| Hide tools entirely unless Session Info | Loses mid-turn narrative and post-hoc debug |
| Only turn-card steps (no tool bubbles) | Breaks “what did it just run?” after collapse; MD still has tools |
| Server-side omit tool content from GET | Breaks other clients / rehydrate fidelity |
| Ephemeral-only thinking (old rec) | Rejected by **Q8** — operators want collapsed history |
| Relative-only times (“2m ago”) | Harder for multi-day sessions; absolute preferred |
| Turn-level tool accordion | Deferred; per-tool compact first (**Q2**) |

## Risks

| Risk | Mitigation |
|------|------------|
| Operators miss a failed tool | Expanded state on error / non-zero exit if detectable; else expand-all |
| Click fatigue | Expand-all; optional default-expand setting |
| Timezone confusion | Browser local zone + ISO tooltip |
| Status line + turn card redundancy | Live line = one glanceable sentence; card = full detail |
| Thinking history noise | Collapse by default; commit on phase boundaries only, not every SSE tick |
| Reload loses client-only thinking | Optional durable role later if needed |

## Success metrics

- Multi-tool turn (20+ tools) keeps user+assistant messages on screen without endless scroll through green boxes.  
- User, assistant, and tool headers show clear local timestamps.  
- During a live turn, an operator sees updating “thinking / running X” without opening Session Info.  
- After the turn, collapsed thinking rows remain inspectable via expand-all or single expand.  
- Full tool body still one click away; Session Info / model history unchanged.

## See also

- [ADR-0010 Agent loop transparency](0010-agent-loop-transparency.md)  
- [ADR-0005 Tools and agent loop](0005-tools-and-agent-loop.md)  
- Web: `internal/web/static/app.js` (`bubbleEl`, turn card), `style.css` (`.bubble.tool`)

## Changelog

| Date | Note |
|------|------|
| 2026-08-08 | **Proposed** — transcript density: compact tools, timestamps, thinking status; Q1–Q10 open |
| 2026-08-13 | **Accepted** — locked Q1–Q10 (`2026-08-13T13:57:11.405Z`); Q8 = persist collapsed thinking like tools |
