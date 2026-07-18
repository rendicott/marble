# ADR-0013: System Prompt Viewer & Every-Turn Context (“soul”)

| Field | Value |
|-------|--------|
| **Status** | Proposed |
| **Date** | 2026-07-18 |
| **Deciders** | Project owner |
| **Tags** | ui, system-prompt, soul, context, settings |
| **Extends** | ADR-0001 (system prompt), ADR-0005 (agent loop), ADR-0007 (Settings UI chrome) |

## Context

Marble’s agent loop always includes a **system** message (today: compiled-in `defaultSystemPrompt` in `internal/session/session.go`). That text defines tools, safety-ish defaults, web-research policy (search → `web_fetch`), mpub, etc.

Operators currently **cannot see** that prompt in the UI. They also cannot add **durable, operator-owned context** that should be present **every turn** (persona, house rules, project preferences) without editing Go and rebuilding — a pattern often called **`soul.md`** / operator soul / “always-on context.”

We want:

1. **Visibility** of the immutable harness system prompt (eyeball next to Settings gear).  
2. **Customization** of an additional **every-turn context** block (editable), distinct from the immutable core.  
3. Clear **tooltip** explaining how the two layers compose.

## Goals

1. **👁 eyeball** control in the sidebar head next to **⚙** (Settings).  
2. **Modal** (consistent with Settings / Explorer / Session info) showing:  
   - **System prompt** — full current text, **read-only / immutable** from the UI.  
   - **Every-turn context** (“soul”) — multi-line editor; save/load durable.  
   - **Tooltip / help** explaining composition and immutability.  
3. On each model call, history includes:  
   `immutable system prompt` + (if non-empty) `soul / every-turn context` + rest of conversation (and ephemeral advisories as today).  
4. Soul is **process-wide** (all user sessions) unless open Q chooses per-session.  
5. Limp-safe: if soul is file- or DB-backed, prefer a path that still works when SQLite is limp.

## Non-goals (v1)

- Editing or overriding the compiled system prompt from the UI  
- Per-message “sticky notes” or temporary system injects  
- Multi-soul profiles / switching (defer)  
- Full prompt-engineering IDE (diff, version history UI) — optional later  
- Changing system-agent compaction prompts in this ADR  

## Decision (proposed)

### 1. Entry point

Sidebar head order (**rec**):

`Sessions · 📁 · 👁 · ⚙ · +`

- **👁** opens **Prompt / Soul** modal (`title="System prompt & every-turn context"`).  
- Does not replace Settings.

### 2. Modal layout

```
┌ System prompt & context ──────────── [?] [Save soul] [×] ┐
│  (?) tooltip: explains immutable vs soul, every-turn     │
│                                                          │
│ ── System prompt (immutable) ──────────────────────────  │
│  [ read-only monospace text area / pre ]                 │
│  “Shipped with Marble — not editable here.”              │
│                                                          │
│ ── Every-turn context (soul) ──────────────────────────  │
│  [ editable textarea ]                                   │
│  “Sent on every model call after the system prompt.”     │
│                                                          │
│  Preview composition (optional collapsible):             │
│  1. System prompt (immutable)                            │
│  2. Every-turn context (if non-empty)                    │
│  3. Conversation + tools…                                │
└──────────────────────────────────────────────────────────┘
```

**Tooltip content (rec):**

> The **system prompt** is built into the harness (tools, policies, defaults). It is shown for transparency and cannot be changed from this UI.  
> **Every-turn context** (soul) is optional text *you* write — house rules, persona, standing preferences. When non-empty it is injected on **every** model call right after the system prompt. Leave empty for stock Marble behavior. Saved under memory (not in the chat transcript).

### 3. Data model — two layers

| Layer | Source of truth | Mutable in UI | When applied |
|-------|-----------------|---------------|--------------|
| **System prompt** | Go constant / binary build (`defaultSystemPrompt`) | **No** | Always as first `role=system` (or sole system base) |
| **Every-turn context (soul)** | Durable store (file and/or DB — open Q) | **Yes** | Every model call when non-empty |

**Composition for each `Chat()` prompt construction:**

```
[system]  immutable harness system prompt
[system]  every-turn context (soul)     ← omit if blank
[…history user/assistant/tool…]
[system]  ephemeral harness advisories  ← existing loop injects (context warn, etc.)
```

**Rec:** soul as a **second system message** (clear separation; easy to omit when empty) rather than string-concat into the first system message (harder to audit). Open Q if single concatenated system is preferred for some models.

### 4. Storage (proposed)

**Rec: file-first under memory leaf** (works in limp mode):

```
$MEMORY/soul.md
```

- Plain UTF-8 markdown/text.  
- Empty file or missing ⇒ no every-turn inject.  
- `GET/PUT /api/soul` (or `/api/prompt`) reads/writes file with size cap (e.g. **32–64 KiB**).  
- Optional dual-write key in SQLite later; not required for v1.

**Why not only DB:** limp mode; operators can edit `soul.md` offline; matches “soul.md” mental model.

### 5. API

| Method | Path | Behavior |
|--------|------|----------|
| `GET` | `/api/prompt` | `{ system_prompt, soul, soul_path, soul_max_chars, immutable: true }` |
| `PUT` | `/api/prompt` or `/api/soul` | Body `{ "soul": "…" }` only; reject attempts to set system prompt (**400**) |

System prompt text is always the **live binary’s** constant (so UI matches what the process actually uses).

### 6. Loop integration

- On session create / load: history still starts with immutable system message only (as today).  
- **At each model call** (`runTurn` when building `prompt`): after copying history, if soul non-empty, insert soul system message immediately after the first system message (or prepend pair).  
- **Do not** persist soul text into session Markdown transcript as user/assistant turns.  
- **Do not** rewrite historical system message in MD when soul changes.  
- Reload soul from disk (or short TTL cache) so Save applies to subsequent turns without restart (**rec: read file each turn or 2s TTL**).

### 7. Security / size

- Cap soul length; reject PUT over cap.  
- Same trust model as Settings (local operator).  
- Soul can contain instructions that steer the agent — operator responsibility (document in tooltip).

## Open questions

### Placement & chrome

1. **Icon:** 👁 only vs “Prompt” text button?  
   *Rec: 👁 icon with title/tooltip.*  
2. **Order:** `… 📁 👁 ⚙ +` vs `… 📁 ⚙ 👁 +`?  
   *Rec: eyeball immediately left of gear.*  
3. **Modal name in title bar?**  
   *Rec: “System prompt & every-turn context”.*  

### Soul semantics

4. **Second system message vs concatenated single system?**  
   *Rec: second system message when non-empty.*  
5. **Scope:** all sessions vs per-session soul?  
   *Rec: process/memory-global (`$MEMORY/soul.md`) for v1.*  
6. **System agents** (compaction) — inject soul too?  
   *Rec: no — user sessions only; compaction keeps its own prompt.*  
7. **Empty soul:** omit message entirely (not empty system)?  
   *Rec: omit entirely.*  

### Storage & API

8. **Store:** `$MEMORY/soul.md` vs SQLite settings key vs both?  
   *Rec: `$MEMORY/soul.md` only for v1.*  
9. **API path:** `/api/prompt` (GET both, PUT soul) vs separate `/api/soul`?  
   *Rec: `GET/PUT /api/prompt` with PUT soul-only.*  
10. **Max soul size?**  
    *Rec: 64 KiB.*  
11. **Live apply after Save without restart?**  
    *Rec: yes (re-read file / short TTL).*  

### UI details

12. **Show character count / cap in editor?**  
    *Rec: yes.*  
13. **Confirm discard on close if dirty?**  
    *Rec: yes.*  
14. **Collapsible “how composition works” instead of only tooltip?**  
    *Rec: tooltip on [?] **and** short static blurb under the title.*  

## Implementation sketch (post-accept)

1. Export `SystemPrompt()` from session package (read-only string).  
2. `memory.SoulPath` / `ReadSoul` / `WriteSoul` under `$MEMORY/soul.md`.  
3. `GET/PUT /api/prompt`.  
4. `runTurn`: inject soul system message after base system when building prompt.  
5. UI: `#btn-prompt` 👁, modal, load/save, tooltip.  
6. Tests: soul omitted when empty; injected when set; PUT rejects system_prompt field; size cap.

## Consequences

### Positive

- Transparency into harness behavior.  
- Operator personalization without rebuilds.  
- Clear separation: product policy (immutable) vs operator soul (editable).

### Trade-offs

- Extra tokens every turn when soul is large.  
- Operators can contradict system prompt via soul (document; system still first).  

### Risks

| Risk | Mitigation |
|------|------------|
| Soul too large | Cap + char counter |
| Stale soul after external edit | Re-read / short TTL |
| Confusion with session MD | Tooltip: not part of chat transcript |

## Acceptance criteria

- [ ] Open questions answered or defaulted  
- [ ] Eyeball + modal UX agreed  
- [ ] Storage + injection order agreed  
- [ ] Ready to implement  

## References

- `internal/session/session.go` — `defaultSystemPrompt`  
- ADR-0007 Settings gear placement  
- ADR-0005 agent loop history construction  
- Community “soul.md” / always-on operator context patterns  
