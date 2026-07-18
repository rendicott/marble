# ADR-0008: Session Info Feature

| Field | Value |
|-------|--------|
| **Status** | Accepted (ready to implement) |
| **Date** | 2026-07-18 |
| **Deciders** | Project owner |
| **Tags** | ui, sessions, diagnostics, observability |
| **Extends** | ADR-0002 (session lifecycle), ADR-0003 (session_events / tokens), ADR-0005 (loop stats) |
| **Answers** | `adr/0008-answers.json` (`2026-07-18T07:26:53.188Z`) |

## Context

Operators currently see a **session list** (title, id, message count, closed/dirty flags) and a **chat transcript**. They cannot easily answer:

- When was this session created / last active / closed?  
- Which **model** and **workspace** was it using?  
- How many **tokens** (reported + estimated) were spent?  
- What **tools** ran, how often, and did any fail?  
- Where is the on-disk **Markdown** path?  
- Is the session **busy**, dirty, or system-owned?  

ADR-0003 already dual-writes rich `session_events` (kinds, tokens, latency, tool names). That data is underused in the UI. A **Session info** surface should expose it without opening SQLite or log files.

## Goals

1. Let the operator open **structured info** for the **active** (or selected) session.  
2. Surface **metadata**, **usage aggregates**, and a **compact activity timeline** from DB events when available.  
3. Degrade gracefully in **limp mode** / no DB: show summary from live memory + Markdown front matter.  
4. Provide **copy actions** (session id, md path, absolute paths) for ops workflows.  
5. Stay lightweight — not a full analytics product.

## Non-goals (v1)

- Cross-session analytics dashboards  
- Editing session history / rewriting events  
- Billing / cost tables (token pricing deferred — **Q8**)  
- Real-time flame graphs or per-token streaming debug  
- Rename title from info panel (**Q11**)  
- JSON export download of info payload (**Q12**)  

## Decision

1. **Entry:** header **ⓘ** for the active session **and** session list context menu “Session info” (**Q1**).  
2. **Surface:** **modal** (consistent with Settings / Explorer) (**Q2**).  
3. **Open:** **explicit only** — never auto-open on select (**Q3**).  
4. Backend: `GET /api/sessions/{id}/info` returning metadata + aggregates + recent events (+ tool histogram, blob count).  
5. Prefer **SQLite aggregates** from `sessions` + `session_events` when DB is writable; else fall back to live registry + MD front matter.  
6. **Recent events:** default **N=30** (**Q4**); error strings included, truncated to **500** chars (**Q5**).  
7. **Blob spill count:** include when cheap (`COUNT` where `blob_id` set) (**Q6**).  
8. **System-agent sessions** use the same panel (**Q7**).  
9. **Limp / no DB:** metadata only; `partial: true`; no fake token sums or fabricated timeline (**Q9**).  
10. **Close session** action in panel with existing list confirm (**Q10**).  
11. **Defer:** token cost/pricing, rename-from-info, JSON export (**Q8**, **Q11**, **Q12**).  
12. UI updates live while the session is busy (SSE-triggered refresh + slow poll backup).  
13. No new DB tables required for v1 (query existing schema).

## Decisions locked (Q1–Q12)

| ID | Decision |
|----|----------|
| **Q1** | Header ⓘ for active session + list context menu “Session info” |
| **Q2** | Modal (Settings / Explorer consistency) |
| **Q3** | Explicit open only — never auto-open |
| **Q4** | 30 recent events |
| **Q5** | Error strings yes, truncate to 500 chars |
| **Q6** | Blob spill COUNT when cheap |
| **Q7** | Same panel for system-agent sessions |
| **Q8** | Defer token cost / pricing |
| **Q9** | Limp: metadata only, `partial: true`, no fake tokens |
| **Q10** | Close session from panel with existing confirm |
| **Q11** | Defer rename-from-info |
| **Q12** | Defer JSON export of info |

## Entry points

| Control | Placement |
|---------|-----------|
| **ⓘ** button | Main chat header next to session title / status pill |
| Context menu | Session list row → “Session info” |

## UI sketch

```
┌ Session info ──────────────────────────── [↻] [×] ┐
│ Title: nginx renew certs                            │
│ Id:    k7m2q9a3f2          [Copy]                   │
│ Status: active · busy                               │
│ Created:  2026-07-18T01:10:00Z                      │
│ Updated:  2026-07-18T02:22:11Z                      │
│ Closed:   —                                         │
│ Model:    Qwen/Qwen3.5-122B-A10B-FP8                │
│ Workspace:/home/…/marble                            │
│ Markdown: session/k7m2q9a3f2.md   [Copy abs]        │
│                                                     │
│ ── Usage ─────────────────────────────────────────  │
│ Messages (UI): 24                                   │
│ Events (DB):   61                                   │
│ Tokens in  reported / est:  18240 / 19102           │
│ Tokens out reported / est:   4102 /  4300           │
│ Tool calls: 12  · errors: 1                         │
│ Blob spills: 2                                      │
│ Avg model latency: 820 ms                           │
│                                                     │
│ ── Recent activity ───────────────────────────────  │
│ 02:22 model_call     410ms  in=200 out=40           │
│ 02:22 tool_call      shell_execute                  │
│ 02:22 tool_result    shell_execute  ok              │
│ 02:21 user_message                                  │
│ …                                                   │
│              [Close session]                        │
└─────────────────────────────────────────────────────┘
```

## Data model (API)

### `GET /api/sessions/{id}/info`

```json
{
  "session": {
    "id": "…",
    "title": "…",
    "status": "active|closed",
    "busy": false,
    "dirty": false,
    "loaded": true,
    "created_at": "…Z",
    "updated_at": "…Z",
    "closed_at": null,
    "model": "…",
    "workspace": "…",
    "md_path": "session/….md",
    "md_path_abs": "/home/…/.marble/session/….md",
    "message_count": 24,
    "system": false
  },
  "usage": {
    "event_count": 61,
    "user_messages": 10,
    "assistant_messages": 10,
    "model_calls": 12,
    "tool_calls": 12,
    "tool_results": 12,
    "errors": 1,
    "blob_count": 2,
    "tokens_in_reported": 18240,
    "tokens_out_reported": 4102,
    "tokens_in_est": 19102,
    "tokens_out_est": 4300,
    "latency_ms_sum": 9840,
    "latency_ms_avg": 820,
    "latency_ms_max": 2100
  },
  "tools": [
    { "name": "shell_execute", "calls": 5, "errors": 0 },
    { "name": "file_read", "calls": 4, "errors": 0 }
  ],
  "recent_events": [
    {
      "seq": 61,
      "ts": "…Z",
      "kind": "model_call",
      "tool_name": null,
      "tokens_in_reported": 200,
      "tokens_out_reported": 40,
      "latency_ms": 410,
      "error": null
    }
  ],
  "source": "db|memory|markdown",
  "partial": false
}
```

### Aggregation rules

| Metric | Source |
|--------|--------|
| Identity / status / times | `sessions` row or live `Summary` + MD front matter |
| Token sums | `SUM` of `session_events` token columns |
| Tool histogram | `kind IN (tool_call,tool_result)` group by `tool_name` |
| Errors | `kind = 'error'` or non-null `error` |
| Latency | from `model_call` rows with `latency_ms` |
| Blob spills | `COUNT` where `blob_id IS NOT NULL` (**Q6**) |
| Recent events | last **30** events by `seq` DESC (**Q4**); errors truncated to 500 chars (**Q5**) |

**Do not** return full message/tool bodies in v1 info payload (keep panel light). Full content stays in chat UI / MD / event detail later.

### Fallback when DB limp / missing (**Q9**)

| Field | Fallback |
|-------|----------|
| Metadata | Live session + MD front matter |
| Usage tokens | Empty / zero with `partial: true` |
| Timeline | Empty — no reconstruction from UI transcript |

## Live updates

While info panel is open and session is `busy`:

- On SSE `status` / `message` / `tool` events for that session, refresh info  
- Slow poll `GET …/info` every **5s** as backup  

## Actions in panel

| Action | Behavior |
|--------|----------|
| Copy session id | Clipboard |
| Copy relative / absolute MD path | Clipboard |
| Refresh | Force reload |
| **Close session** | Existing confirm flow, then close API (**Q10**) |
| Open in explorer | Jump explorer to MD file if under workspace — **only if** memory is inside workspace (often **not**); else disabled with tooltip |

## Security

- Same trust model as rest of UI (local/Tailscale).  
- Info endpoint must not leak other sessions’ data (id scoped).  
- Paths returned are already known to the operator’s machine.  

## Implementation sketch

1. `db.SessionInfo(id)` — SQL aggregates + recent events + blob count.  
2. `session.Registry.Info(id)` — merge live busy/dirty + DB + MD.  
3. `GET /api/sessions/{id}/info`.  
4. UI: header ⓘ + modal; render sections; list context menu.  
5. Wire SSE/poll refresh; Close session with confirm.  
6. Tests: aggregates with fixture events; limp fallback.

## Consequences

### Positive

- Operators diagnose stuck/expensive sessions quickly.  
- Leverages existing dual-write telemetry.  
- Improves trust in token and tool accounting.  

### Trade-offs

- Aggregates only as good as event logging coverage.  
- Another modal (accepted for consistency).  

### Risks

| Risk | Mitigation |
|------|------------|
| Heavy SQL on large sessions | Indexes already on `(session_id, seq)`; limit recent events to 30 |
| Stale busy flag | Prefer live registry for `busy` |
| Info spam in UI | Explicit open only |

## Acceptance criteria

- [x] Entry + surface agreed (header ⓘ + list menu; modal)  
- [x] API shape agreed  
- [x] Fallback behavior agreed (metadata only when limp)  
- [x] Open questions answered (`0008-answers.json`)  
- [x] Implemented (`GET /api/sessions/{id}/info`, modal UI, tests)  

## References

- ADR-0003 `sessions` / `session_events` schema  
- ADR-0002 session Markdown front matter  
- Current UI session list + SSE event stream  
- `adr/0008-answers.json`  
