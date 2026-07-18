# ADR-0010: Agent Loop Transparency & Turn Control

| Field | Value |
|-------|--------|
| **Status** | Accepted / Implemented |
| **Date** | 2026-07-18 |
| **Deciders** | Project owner |
| **Tags** | ui, agent-loop, observability, control, sse |
| **Extends** | ADR-0001 (inner loop), ADR-0005 (tool rounds / advisories), ADR-0008 (session diagnostics) |
| **Answers** | `adr/0010-answers.json` (`2026-07-18T08:20:10.670Z`) |

## Context

When a user message kicks off a non-trivial turn, the agent loop may:

- Call the model many times (up to hard max tool iterations, default **80**)
- Run tools (shell, FS, MCP, background tasks, …)
- Sit in long model calls or long tool executions

Today the UI mostly surfaces a header status pill (`running` / `calling_model` / `idle`). That is not enough to answer:

- **Which iteration / tool round** are we on (e.g. 7 / soft 65 / hard 80)?
- **What phase** — waiting on model vs executing a tool vs finishing?
- **Which tool** is running, and with what short args preview?
- **How long** has this phase / the whole turn been running (is it hung)?
- **Can I stop** before it runs `rm` / a bad MCP call / burns another 30s model call?

Tool start/result SSE events already exist (`type: "tool"`), and compact tool bubbles appear *after* a tool finishes. Mid-flight and multi-round progress are effectively opaque. There is **no cancel/stop-turn** API: `runTurn` uses a 15-minute context timeout only; `busy` blocks new messages until the goroutine ends.

## Goals

1. **In-chat progress** for the active turn: phase, iteration, elapsed times, current tool, context usage, last model latency.  
2. **Header / pill enrichment** so glanceable status matches the progress card.  
3. **Stop turn** control: cooperative cancel of the current turn (model wait + tool when possible).  
4. **Live updates** over existing SSE (extend event shape; no second stream).  
5. **Turn step history:** accumulate steps during the turn; **collapse at turn end** with UI expand to inspect (**Q3**). Not dumped into session Markdown body as full assistant prose.

## Non-goals (v1)

- Token streaming of model tokens (partial assistant draft) — separate ADR if needed  
- Per-tool interactive approval gates (“allow this shell?”) — stop is coarse cancel  
- Pausing and later resuming the same turn mid-tool  
- Cross-session global activity dashboard  
- Killing arbitrary host processes outside the harness-managed tool/shell path  
- Auto stop-and-close when closing a busy session (defer — **Q11**)  

## Decision

1. **Surfaces:** transcript **footer progress card** (detail) **and** enriched **header pill** (**Q1**).  
2. **System agents:** same progress UI + Stop (**Q2**).  
3. **Step log:** append structured steps during the turn; when the turn ends, **collapse** the card to a compact summary; operator can **expand** to inspect steps for that turn (**Q3**). Do not write the full step log into session Markdown transcript body (tool bubbles remain as today).  
4. Emit structured **`turn` SSE events**; keep coarse `status` events (**Q13**).  
5. **`GET /api/sessions/{id}/progress`** for hydrate + **`POST /api/sessions/{id}/stop`** for cancel (**Q14**, **Q8**, **Q10**).  
6. Progress content: args preview **200** chars (**Q4**); last model latency (**Q5**); context usage % (**Q6**); last tool name + **120**-char result tail (**Q7**).  
7. **Stop:** immediate, no confirm (**Q8**); label **Stop** (**Q10**); mid-shell **kill process group** SIGTERM→SIGKILL best-effort (**Q9**); keep partial tool bubbles (**Q12**).  
8. **Close while busy:** remain **409**; document Stop first (**Q11**).  
9. Loop checks `ctx.Done()` between model/tool steps; pass cancel into model client + shell (and other tools as wired).

## Decisions locked (Q1–Q14)

| ID | Decision |
|----|----------|
| **Q1** | Both — footer card + enriched header pill |
| **Q2** | Same progress + stop for system-agent sessions |
| **Q3** | Steps persist during turn; **collapse at end**; expand to inspect |
| **Q4** | 200-char single-line args preview |
| **Q5** | Show last completed model latency |
| **Q6** | Show estimated context usage % |
| **Q7** | Last tool: name + 120-char result tail |
| **Q8** | No confirm on Stop |
| **Q9** | Kill shell process group (SIGTERM → SIGKILL) |
| **Q10** | Button label: **Stop** |
| **Q11** | Keep 409 on close while busy; Stop first |
| **Q12** | Keep partial tool bubbles on stop |
| **Q13** | New SSE type `turn`; keep coarse `status` |
| **Q14** | GET `/progress` + SSE `turn` |

## UI sketch

### While busy (transcript footer)

```
┌ Turn in progress ──────────────────────────── [■ Stop] ┐
│ Phase:  calling model · iter 4 / hard 80               │
│ Tools:  round 3 · soft 65                              │
│ Elapsed turn: 1m 12s · phase: 18s                      │
│ Context: ~42%   · last model: 820 ms                   │
│ Last tool: shell_execute                               │
│   $ apt-get install -y nginx     [args ≤200]           │
│   → …done reading package lists…  [result ≤120]        │
│ Steps: 7 (live)                                        │
│   · i3 model 410ms                                     │
│   · tool shell_execute start                           │
│   · …                                                  │
└────────────────────────────────────────────────────────┘
```

### After turn ends (collapsed)

```
┌ Turn complete · 1m 12s · 4 iters · 3 tools  [▸ expand] ┐
└────────────────────────────────────────────────────────┘
```

Expanded shows the step list for inspection (same session view; not necessarily forever across reloads unless we persist — see implementation note).

### Header pill (enrich)

| Status | Pill text (example) |
|--------|---------------------|
| idle | `idle` |
| calling model | `model · i4/80 · 18s` |
| running tool | `tool shell_execute · i4` |
| stopping | `stopping…` |
| error | `error` |

## Data model

### Live turn snapshot

```go
type TurnProgress struct {
    SessionID      string     `json:"session_id"`
    Active         bool       `json:"active"`
    Phase          string     `json:"phase"` // starting | calling_model | running_tool | finishing | stopping | idle | error | complete
    Iter           int        `json:"iter"`
    IterHard       int        `json:"iter_hard"`
    ToolRounds     int        `json:"tool_rounds"`
    ToolSoft       int        `json:"tool_soft"`
    TurnStartedAt  time.Time  `json:"turn_started_at"`
    PhaseStartedAt time.Time  `json:"phase_started_at"`
    LastEventAt    time.Time  `json:"last_event_at"`
    TurnEndedAt    *time.Time `json:"turn_ended_at,omitempty"`
    ContextUsage   *float64   `json:"context_usage,omitempty"` // 0..1
    LastModelLatencyMs *int   `json:"last_model_latency_ms,omitempty"`
    CurrentTool    *ToolProg  `json:"current_tool,omitempty"`
    LastTool       *ToolProg  `json:"last_tool,omitempty"`
    Steps          []TurnStep `json:"steps,omitempty"`
    Collapsed      bool       `json:"collapsed"` // true after turn end until user expands in UI
    StopRequested  bool       `json:"stop_requested"`
    Message        string     `json:"message,omitempty"`
}

type ToolProg struct {
    ID           string `json:"id,omitempty"`
    Name         string `json:"name"`
    ArgsPreview  string `json:"args_preview,omitempty"`  // ≤200
    ResultTail   string `json:"result_tail,omitempty"`   // ≤120
    Phase        string `json:"phase"` // start | result
}

type TurnStep struct {
    At      string `json:"at"`
    Kind    string `json:"kind"` // model_call | tool_start | tool_result | advisory | stop | error | done
    Iter    int    `json:"iter,omitempty"`
    Detail  string `json:"detail,omitempty"`
    Latency *int   `json:"latency_ms,omitempty"`
    Tool    string `json:"tool,omitempty"`
}
```

### Persistence of steps (**Q3**)

| Layer | Behavior |
|-------|----------|
| **Live** | Steps array grows during turn; SSE `turn` includes steps (or delta — impl choice; full array OK if capped) |
| **Turn end** | Progress remains queryable briefly / until next turn; UI shows **collapsed** summary |
| **Session reload** | Prefer retaining **last completed turn** progress in memory on the session object until next turn starts; optional DB event `turn_summary` later if needed (not required v1) |
| **Session MD** | Do **not** dump full step log into Markdown body |

Cap steps (e.g. last **100**) to avoid huge SSE payloads.

### SSE

```json
{
  "type": "turn",
  "session_id": "…",
  "turn": { /* TurnProgress */ },
  "at": "…Z"
}
```

Emit on: turn start, each iter boundary, before/after model call, tool start/result, stop request, turn end (with `collapsed` hint / `active:false` + phase `complete`).

Keep existing `status` / `tool` / `message` events.

### HTTP

| Method | Path | Behavior |
|--------|------|----------|
| `GET` | `/api/sessions/{id}/progress` | Current or last-turn `TurnProgress` (or `{active:false}`) |
| `POST` | `/api/sessions/{id}/stop` | Cancel turn context; **202** if accepted; **409** if not busy; **404** if missing |

## Stop / cancel semantics

| Concern | Decision |
|---------|----------|
| Granularity | Whole turn cancel |
| Confirm | None (**Q8**) |
| Label | **Stop** (**Q10**) |
| Model in-flight | Cancel request context |
| Tool mid-shell | Kill process group SIGTERM → SIGKILL (**Q9**) |
| Between steps | Check `ctx.Done()` / stop flag |
| Transcript | Keep partial tool bubbles (**Q12**) |
| Close while busy | **409**; Stop first (**Q11**) |
| Continuations / system agents | Same stop API by session id (**Q2**) |

## Implementation sketch

1. **`Session` turn state** — progress, steps, `turnCancel`, `stopRequested`.  
2. **`runTurn`** — cancellable ctx; update+publish at each phase; append steps; on end mark complete/collapsed.  
3. **`Registry.Stop(id)`** — cancel + phase `stopping`.  
4. **API** — `progress` + `stop`.  
5. **Shell** — process group kill on ctx cancel.  
6. **UI** — live card + pill; Stop; collapse/expand for last turn; hydrate GET progress.  
7. **Tests** — stop between iters; step accumulation; 409 when idle.

## Consequences

### Positive

- Operators supervise multi-step turns without logs.  
- Runaway/dangerous loops can be interrupted.  
- Post-turn expand retains “what just happened” without MD clutter.

### Trade-offs

- More SSE traffic; step list must be capped.  
- Cooperative cancel not instant until all tools take ctx.  
- Collapsed history is primarily in-memory for v1 (reload may lose expand detail unless last progress retained on session).

### Risks

| Risk | Mitigation |
|------|------------|
| Stuck `busy` after cancel | Always `defer endTurn()` |
| Orphan shell | Process group kill + timeout |
| UI desync | GET `/progress` on select + SSE |
| Huge step payloads | Cap steps (100); truncate args/results |

## Acceptance criteria

- [x] Open questions answered (`0010-answers.json`)  
- [x] Progress + pill + Stop + collapse/expand agreed  
- [x] API/SSE shape agreed  
- [x] Implemented (turn SSE, GET/POST progress/stop, UI card, shell cancel)  

## References

- `internal/session/loop.go`  
- ADR-0005 soft 65 / hard 80  
- ADR-0008 session info (historical)  
- `adr/0010-answers.json`  
