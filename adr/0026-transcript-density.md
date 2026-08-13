# ADR-0026: Transcript density — compact tools, message timestamps, thinking status

| Field | Value |
|-------|--------|
| **Status** | **Proposed** (awaiting review Q1–Q10) |
| **Date** | 2026-08-08 |
| **Author** | — |
| **Deciders** | Project owner |
| **Tags** | ui, transcript, tools, timestamps, progress, density, web |
| **Extends** | ADR-0010 (turn progress / step log), ADR-0005 (agent loop), ADR-0001 (inner loop) |
| **Review UI** | [0026-review.html](0026-review.html) |
| **Answers** | *(none yet — fill `0026-answers.json` after review)* |

## Summary

Make long multi-tool sessions readable in the web transcript:

1. **Compact tool result rows by default** (one-line chips; expand on demand).  
2. **Visible timestamps** on user and assistant messages (and optionally tools).  
3. **Lightweight “thinking / working” status lines** during a turn so the chat feels alive between tool bubbles and the final answer—without dumping another wall of text.

Wire/API stays additive. Primary work is **web UI** (`internal/web/static/`); harness already has `created_at`, tool messages, turn progress SSE, and harness advisories.

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
2. **Timestamps** on user and assistant bubbles (readable, locale-friendly).  
3. **Thinking / working statuses** visible in-chat from time to time during a busy turn (not only the header pill).  
4. **Remember expand preference** optionally (session or localStorage).  
5. **No harness API break**: hydrate still returns full tool messages; compaction is UI.  
6. **Keep diagnostics**: Session Info, expand-all, and copy remain possible.

## Non-goals

| Non-goal | Rationale |
|----------|-----------|
| Remove tool messages from MD / SQLite | Audit trail stays; this ADR is presentation |
| Token streaming of assistant drafts | Separate concern |
| Wonderstand immersion chrome | Android has its own surfaces |
| Hiding tools from the model history | Transcript UI only |
| Full activity dashboard across sessions | Out of scope |
| Replacing ADR-0010 turn card | Card stays; density work is complementary |

## Design

### A. Compact tool rows (default)

**Collapsed (default):**

```text
┌─────────────────────────────────────────────────────────┐
│ ▸ shell_execute · 0.4s · exit 0 · “ls -la …”     [expand]│
└─────────────────────────────────────────────────────────┘
```

**Expanded:**

```text
┌─────────────────────────────────────────────────────────┐
│ ▾ shell_execute · 0.4s                            [collapse]
│ shell_execute → total 12 … (full body as today)         │
└─────────────────────────────────────────────────────────┘
```

| Element | Source |
|---------|--------|
| Tool name | `message.tool_name` or leading token of content |
| One-line preview | First ~120 chars of content, single-line |
| Duration | Optional: only if we add timing later; **v1 may omit** and use count only |
| Expand | Click header row or chevron; body toggles |

**Grouping (optional, Q):** after a turn completes, optionally fold **all tools since last user message** into one “N tools” strip with nested expand. Recommendation: **v1 per-tool compact rows**; group strip as stretch goal if density still high.

**Settings:**

- Default: **collapsed**  
- “Expand tools by default” toggle in session chrome or Settings (**Q**)  
- “Expand all / collapse all” for current transcript  

**Attachments on tool messages** (screenshots): keep a **thumbnail chip always visible** even when body collapsed (**Q**).

### B. Message timestamps

Wire already has `created_at` (RFC3339) on UI messages. **Render in the bubble chrome:**

```text
assistant                    12:31 PM · Aug 8
────────────────────────────────────────────
Markdown body…
```

| Role | Show time? |
|------|------------|
| user | **Yes** |
| assistant | **Yes** |
| tool | Optional small time on compact row (**Q**) |
| harness / error | Optional or omit |

**Format (recommendation):**

- Same calendar day as “now”: **time only** (`3:42 PM`)  
- Else: **short date + time** (`Aug 8, 3:42 PM`)  
- Full ISO on `title` tooltip for copy/debug  
- Use browser locale via `Intl.DateTimeFormat`  

Do **not** require backend changes for v1.

### C. Thinking / working statuses

Operators want intermittent “the agent is thinking” feedback **in the transcript stream**, not only:

- header pill (`calling_model` / `running`)  
- ADR-0010 turn card at the bottom  

**Recommendation: ephemeral status strip** pinned near the live turn card (or as lightweight inline chips), driven by existing **`turn` SSE** + coarse **`status`**, not new durable messages.

```text
… tool chip …
… tool chip …
  ⋯ thinking · calling model · i7 · 12s
  ⋯ running shell_execute · “grep -R …”
[ turn progress card ]
```

| Kind | Source today | Transcript treatment |
|------|----------------|----------------------|
| Phase | `turn.phase` / `status` | “Thinking…” / “Calling model…” / “Running tool…” |
| Tool | `turn.current_tool` | Name + short args |
| Harness advisory | `type: harness` | Keep as today or demote into status strip (**Q**) |
| Step log | `turn.steps` | Still in expandable turn card; do not mirror every step into transcript |

**Cadence:** update in place (single live status line), not a new bubble every second—avoids clutter equal to tools. On turn idle: **remove** the live line (or leave last line grayed for 2s then drop).

**Optional later (not p1):** model “reasoning” / provider thinking tokens if/when exposed—separate ADR.

### D. Visual hierarchy (target)

```
user ………………… 3:40 PM
  tools ▸ file_read · …
  tools ▸ grep · …          ← compact
  tools ▸ shell_execute · …
  ⋯ thinking · calling model   ← live only while busy
assistant …………… 3:41 PM
  (answer markdown)
```

## Implementation sketch

| Layer | Change |
|-------|--------|
| **CSS** | `.bubble.tool.collapsed`, `.msg-time`, `.thinking-status` |
| **app.js `bubbleEl`** | Timestamp span; tool header + details/`<details>` or JS toggle |
| **app.js SSE** | Maintain one `#thinking-status` node from `turn` / `status` events |
| **Settings** | Optional `ui.tools_expanded_default` (localStorage first; Settings key later) |
| **Harness** | None required for v1; optional later: tool duration on tool messages |

### Suggested PR slice

| PR | Scope |
|----|--------|
| **T1** | Timestamps on user/assistant |
| **T2** | Compact tool rows + expand/collapse + expand-all |
| **T3** | Live thinking status line from turn/status SSE |
| **T4** | Preference + polish (attachments on collapsed tools, tool timestamps) |

## Open questions (Q1–Q10)

| ID | Question | Recommendation |
|----|----------|----------------|
| **Q1** | Default tool density? | **Collapsed** one-line; click to expand body |
| **Q2** | Group tools per turn into one “N tools” accordion? | **No in v1** — per-tool compact rows; revisit if still too tall |
| **Q3** | Expand-all control placement? | Transcript toolbar near session actions + keyboard optional later |
| **Q4** | Persist “tools expanded by default”? | **localStorage** first; Settings key if we already have UI prefs pattern |
| **Q5** | Timestamp style? | Relative-to-today: time only same day; else short date+time; full ISO in `title` |
| **Q6** | Timestamps on tool rows too? | **Yes, subtle** on the compact header (helps multi-hour turns) |
| **Q7** | Thinking status: in-transcript line vs only turn card? | **Both** — keep turn card; add single live “⋯ thinking / running tool” line above it |
| **Q8** | Persist thinking lines after turn ends? | **No** — ephemeral; history is tools + final assistant (+ harness if kept) |
| **Q9** | Demote harness advisories into the status strip? | **Keep harness bubbles** for now (errors/important); only phase noise is ephemeral |
| **Q10** | Screenshots / attachments on collapsed tools? | **Always show image chips** in header even when body collapsed |

## Alternatives considered

| Alternative | Why not default |
|-------------|-----------------|
| Hide tools entirely unless Session Info | Loses mid-turn narrative and post-hoc debug |
| Only turn-card steps (no tool bubbles) | Breaks “what did it just run?” after collapse; MD still has tools |
| Server-side omit tool content from GET | Breaks other clients / rehydrate fidelity |
| New durable “status” role messages | Pollutes MD and hydrate; use ephemeral DOM |
| Relative-only times (“2m ago”) | Harder for multi-day sessions; absolute preferred |

## Risks

| Risk | Mitigation |
|------|------------|
| Operators miss a failed tool | Expanded state on error / non-zero exit if detectable; else expand-all |
| Click fatigue | Expand-all; optional default-expand setting |
| Timezone confusion | Browser local zone + ISO tooltip |
| Status line + turn card redundancy | Status = one glanceable sentence; card = full detail |

## Success metrics

- Multi-tool turn (20+ tools) keeps user+assistant messages on screen without endless scroll through green boxes.  
- User and assistant bubbles show a clear local timestamp.  
- During a live turn, an operator sees updating “thinking / running X” without opening Session Info.  
- Full tool body still one click away; Session Info / MD unchanged.

## See also

- [ADR-0010 Agent loop transparency](0010-agent-loop-transparency.md)  
- [ADR-0005 Tools and agent loop](0005-tools-and-agent-loop.md)  
- Web: `internal/web/static/app.js` (`bubbleEl`, turn card), `style.css` (`.bubble.tool`)

## Changelog

| Date | Note |
|------|------|
| 2026-08-08 | **Proposed** — transcript density: compact tools, timestamps, thinking status; Q1–Q10 open |
