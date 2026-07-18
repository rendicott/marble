# ADR-0013: System Prompt Viewer & Every-Turn Context (“soul”)

| Field | Value |
|-------|--------|
| **Status** | Accepted (ready to implement) |
| **Date** | 2026-07-18 |
| **Deciders** | Project owner |
| **Tags** | ui, system-prompt, soul, context, settings |
| **Extends** | ADR-0001 (system prompt), ADR-0005 (agent loop), ADR-0007 (Settings UI chrome) |
| **Answers** | `adr/0013-answers.json` (`2026-07-18T17:53:01.519Z`) |

## Context

Marble’s agent loop always includes a **system** message (compiled-in `defaultSystemPrompt`). Operators cannot see that prompt in the UI and cannot add durable **every-turn** operator context (persona / house rules / “soul.md”) without rebuilding.

## Goals

1. **👁 icon** next to Settings (icon only — **Q1**).  
2. Modal shows **immutable system prompt** + **editable every-turn soul**.  
3. Tooltip **and** short blurb explain composition (**Q14**).  
4. Soul injected every model call for **user** sessions when non-empty.  
5. File-backed under memory leaf; limp-safe; live apply after Save.

## Non-goals (v1)

- Editing the compiled system prompt  
- Per-session souls  
- Soul inject for system agents / compaction  
- Multi-profile souls / version history UI  

## Decision

1. **Entry:** sidebar `Sessions · 📁 · 👁 · ⚙ · +` — eyeball is **icon only** (**Q1**, **Q2**).  
2. **Modal title:** “System prompt & every-turn context” (**Q3**).  
3. **System prompt:** read-only; source = live binary `defaultSystemPrompt`; UI cannot change it.  
4. **Soul:** editable; stored **only** at `$MEMORY/soul.md` (**Q5**, **Q8**).  
5. **Composition** on each user-session model call (**Q4**, **Q6**, **Q7**):  
   ```
   [system] immutable harness system prompt
   [system] soul contents          ← second system message; omit if blank
   … history user/assistant/tool …
   [system] ephemeral advisories   ← existing loop behavior
   ```  
   No soul inject for system-agent / compaction sessions.  
6. **API:** `GET/PUT /api/prompt` — GET returns system + soul; PUT accepts **`soul` only** (reject system_prompt) (**Q9**).  
7. **Max soul size:** **64 KiB** (**Q10**); show char count + cap in editor (**Q12**).  
8. **Live apply** after Save (re-read file or short TTL); no restart (**Q11**).  
9. **Dirty close:** confirm discard (**Q13**).  
10. **Help:** `?` tooltip **and** short blurb under title (**Q14**).  
11. Soul is **not** written into session Markdown as chat turns.

## Decisions locked (Q1–Q14)

| ID | Decision |
|----|----------|
| **Q1** | Just icon (👁) |
| **Q2** | Order: Sessions · folder · eyeball · gear · + |
| **Q3** | Modal title: System prompt & every-turn context |
| **Q4** | Second system message when non-empty |
| **Q5** | Global `$MEMORY/soul.md` for all user sessions |
| **Q6** | No soul inject for system agents / compaction |
| **Q7** | Omit soul message entirely when blank |
| **Q8** | `$MEMORY/soul.md` only for v1 |
| **Q9** | `GET/PUT /api/prompt` — PUT soul only |
| **Q10** | 64 KiB max soul |
| **Q11** | Live apply after Save — no restart |
| **Q12** | Char count + cap in soul editor |
| **Q13** | Confirm discard if dirty on close |
| **Q14** | Tooltip on ? **and** short blurb under title |

## UI sketch

```
┌ System prompt & every-turn context ── [?] [Save soul] [×] ┐
│ Blurb: system = immutable harness text; soul = optional   │
│ every-turn context you control (not part of chat MD).     │
│                                                           │
│ ── System prompt (immutable) ───────────────────────────  │
│ [read-only monospace]                                     │
│                                                           │
│ ── Every-turn context (soul) ────── n / 65536 ──────────  │
│ [editable textarea]                                       │
│ Path: $MEMORY/soul.md                                     │
└───────────────────────────────────────────────────────────┘
```

**Tooltip (?):** System prompt is built into Marble and cannot be edited here. Every-turn context (soul) is optional operator text injected on every model call after the system prompt when non-empty. Leave empty for stock behavior. Not stored in the chat transcript.

## API

### `GET /api/prompt`

```json
{
  "system_prompt": "…",
  "soul": "…",
  "soul_path": "soul.md",
  "soul_path_abs": "/…/.marble/soul.md",
  "soul_max_chars": 65536,
  "immutable": true
}
```

### `PUT /api/prompt`

```json
{ "soul": "…" }
```

- Reject unknown fields / `system_prompt` with **400**.  
- Reject if `len(soul) > 65536` with **400**.  
- Write `$MEMORY/soul.md` (create if missing). Empty string deletes or writes empty file such that inject is omitted.

## Loop integration

- Export `session.SystemPrompt() string`.  
- `ReadSoul` / `WriteSoul` on memory store.  
- In `runTurn` prompt build for **user** sessions only: after base system message, if soul trimmed non-empty, insert `{Role: system, Content: soul}`.  
- Re-read soul each turn or cache with short TTL after Save.  
- System agents: no soul.

## Implementation sketch

1. `SystemPrompt()` + soul file IO under memory root.  
2. `GET/PUT /api/prompt`.  
3. Inject second system message in user-session turns.  
4. UI: 👁 button, modal, load/save, char count, dirty confirm, tooltip + blurb.  
5. Tests: omit empty; inject when set; PUT rejects system_prompt; size cap; system agent free of soul.

## Consequences

### Positive

- Transparency into harness system prompt.  
- Operator personalization without rebuild.  
- Clear immutable vs soul split.

### Trade-offs

- Soul tokens every turn when large (cap 64 KiB).  
- Soul can contradict system prompt (system still first; operator responsibility).

## Acceptance criteria

- [x] Open questions answered (`0013-answers.json`)  
- [x] Eyeball + modal + soul file + injection order locked  
- [ ] Ready to implement  

## References

- `internal/session/session.go` — `defaultSystemPrompt`  
- ADR-0007 Settings gear  
- `adr/0013-answers.json`  
