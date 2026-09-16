# ADR-0031: Subprocess context injection — pick-and-choose session context for agent runs

| Field | Value |
|-------|--------|
| **Status** | **Proposed** (2026-09-16) — open for review |
| **Date** | 2026-09-16 |
| **Author** | — |
| **Deciders** | Project owner |
| **Tags** | subprocess, agents, context, session, memory, call-agent-process, turn-routing, presets |
| **Extends** | ADR-0014 (`call_agent_process`), ADR-0030 (agent-process presets + turn send-routing), ADR-0018 (Selectable Models / `TurnOpts`), ADR-0026 (transcript density), ADR-0022 (long-turn efficiency) |
| **Review UI** | [0031-review.html](0031-review.html) |

## Summary

`call_agent_process` (ADR-0014) and routed turns (ADR-0030) spawn a subprocess **cold**: the child receives only the prompt string — no session transcript, no digest, no memory, no files-read-so-far. The operator can hand context by pasting it into the prompt, but that is manual, lossy, and impossible to do reliably.

This ADR adds an explicit, **pick-and-choose** `context` control to subprocess agent runs: a **list of named context sources** assembled into a single, size-capped block and injected into the child invocation. Default is **off** (`none`), preserving today's cost and behavior. The operator chooses at three granularities: **per call**, **per session**, and **default** (preset- or global-level).

## Context & pain

### Current state (verified)

| Area | Today |
|------|-------|
| Tool | `call_agent_process` builds `agentproc.Request{ Prompt, CWD, Model, ExtraArgs, … }` from args only (`internal/tools/agentproc.go`); `Prompt` is the only free-form text |
| Driver | `claude -p "<prompt>"`, `grok -p "<prompt>"`, `opencode run "<prompt>"` (`internal/agentproc/driver.go`, `opencode.go`) |
| TurnContext | Already carries `Compact(style, keepLast)`, `HistorySnippet()`, and `ReadPaths` (`internal/tools/registry.go`), set by the session loop (`internal/session/loop.go`) — **none are consumed by agentproc** |
| Routed turns | ADR-0030 sends the wrapped user message to a preset; no context injection |
| Config | `agent_process.json` = global caps; `agent_presets` SQLite = presets (ADR-0030 schema v8) |
| Session | `sessions.model_id` + `sessions.agent_preset_id` exist (ADR-0018/0030); no subprocess-context field |

### What is awkward today

| Situation | Failure mode |
|-----------|--------------|
| "Run this in claude, but it needs to know what we've been doing" | Operator/agent pastes a paraphrased summary; lossy, and the model may omit it |
| "Hand off a coding push that spans the whole session" | No way to include history + memory without a wall of paste |
| "Cheap throwaway run that shouldn't see history" | No way — because nothing is ever passed, this is actually fine today |
| "I want memory results too, not just the transcript" | Impossible; memory tools aren't bridged into the child at all |

The core gap: **context is all-or-nothing and it defaults to nothing**, with no structured way to opt in, cap it, or scope it.

## Goals

1. **Structured opt-in** — a `context` source-list on subprocess runs (`none` by default).
2. **Composable sources** — transcript (compact / full), memory search, and files-read-this-turn are independently selectable.
3. **Bounded & safe** — every source is capped; the assembled block has a total cap; secrets never cross (memory/session already sanitize; no raw keys).
4. **Three-layer resolution** — per-call → per-session → default (preset/global), mirroring ADR-0030's resolver.
5. **Both paths** — the `call_agent_process` tool **and** ADR-0030 routed turns honor the same mechanism.
6. **No recompile for delivery** — the assembled block is a plain, clearly-delimited text section, driver-agnostic.

## Non-goals (v1)

| Non-goal | Why |
|----------|-----|
| Auto-selecting context per task (full `auto` routing) | Explicit selection only (same stance as ADR-0018/0030); a shallow `auto` shorthand is allowed but not a smart router |
| Injecting *tool outputs* or *attachments* from the current turn | `read_paths` covers files; tool-output/attachment injection is a later source |
| Bidirectional context (child writes back into session) | Child output is already collected (ADR-0014/0030); no structured merge |
| Vector/RAG retrieval over the whole session | `memory_search` is the only retrieval in v1 |
| Persisting a "context snapshot" for reuse across many runs | Deferred (a separate follow-on; see Alternatives) |
| Streaming context into the child incrementally | One-shot block at spawn |

## Design

### A. Context sources

`context` is a **list**, not an enum, so sources compose. Each source becomes a clearly-labeled section in the assembled block.

| Token | Contribution | Size default |
|-------|--------------|--------------|
| `compact` | The `session_compact` digest (via `TurnContext.Compact`) | 6000 chars |
| `full` | Recent raw transcript turns (newest first, capped) **+** the `compact` digest of anything older | 12000 chars raw + 6000 compact |
| `memory` | Top-K `memory_search` results seeded from the prompt (and recent user turns) | 4000 chars |
| `read_paths` | Paths read/written this turn (`TurnContext.ReadPaths`) | 1500 chars |

Shorthands (resolved once at spawn):

- `"none"` → `[]` (today's behavior — the default)
- `"auto"` → harness picks a shallow default: `read_paths` + `compact`; upgrades to `full`+`memory` only if the prompt references prior work (contains "this session", "what we discussed", "the tracker", "earlier", a session id, etc.)

`full` implies `compact` (its "older history" section *is* a compact); listing both is allowed but redundant. `full` and `compact` are mutually *exclusive in effect* — `full` wins if both present (no double-count).

### B. Assembly

1. For each selected source, materialize its text and trim to its per-source cap.
2. Join into one block with unambiguous delimiters:

```text
[CONTEXT — Marble session <id> · source: full]
…recent transcript…

[CONTEXT — Marble session <id> · source: memory]
…top matches…

[CONTEXT — Marble session <id> · source: read_paths]
…
```

3. Enforce a total cap (`context_max_chars`, default 16000). Oversized sources are truncated oldest-first (for transcript) / lowest-relevance-first (for memory). If the total still exceeds the cap after trimming, sources are dropped in reverse-priority order: `read_paths` → `memory` → `full` → `compact`.

Deterministic and testable: given a session and a `context` list, the block is a pure function of inputs (no hidden time/randomness), which keeps it safe to unit-test and safe to reason about.

### C. Delivery

The assembled block is delivered as a **prepended text section to `Prompt`** (before the user task), driver-agnostic — grok/claude/opencode all take a prompt positional.

- **Why prepend, not a flag:** `--append-system-prompt` exists for claude but not uniformly across drivers; a plain prompt section is one mechanism for all.
- **Why not `--resume`/CLAUDE.md:** those are child-session/file mechanisms; Marble context isn't a Claude session and shouldn't masquerade as one.
- The block is **excluded from the echoed `prompt_preview`/command** on background-task start, so it doesn't double the noise (only a `context: full+memory · 14k chars` marker is shown).

Delivery is an open question (Q4): prepend vs claude-specific `--append-system-prompt` vs a context file + pointer.

### D. Resolution order

| Priority | Source | When applied |
|----------|--------|--------------|
| 1 | Per-call `context` arg (`call_agent_process`), or `TurnOpts` context override on a routed turn | That invocation only |
| 2 | `session.subprocess_context` (set via `session_set_subprocess_context`) | Every subprocess run in that session |
| 3 | Preset `context` field (routed turns) / `agent_process.json` global default (tool calls) | Baseline |

Missing/empty at priority 1 → fall through to 2 → 3. `context_max_chars` resolves the same way (per-call → session → default).

### E. Config surface

| Layer | Storage | Set by |
|-------|---------|--------|
| Per-call | tool arg / `TurnOpts` | Agent emits it, or operator instructs |
| Per-session | `sessions.subprocess_context` (JSON: `{context:[…], max_chars}`) | New tool `session_set_subprocess_context` (mirrors `session_set_model` / `session_set_agent_preset`); empty/`"clear"` reverts |
| Preset default | `agent_presets.context` + `agent_presets.context_max_chars` (schema v9) | Settings ⚙ → Agents → preset editor |
| Global default | `agent_process.json` `context` block (global caps file) | Hand-edited; seed for presets on migrate (ADR-0030 pattern) |

Secrets: **none**. Context sources are transcript/memory/paths — already sanitized by existing paths; the block is plain text, never contains raw keys, and is never logged verbatim (only the size marker).

### F. Tools

- `session_set_subprocess_context { "context": ["full","memory"], "max_chars": 24000 }` → sets the session override; responds with the **effective** config (per-call not applicable, so it echoes session → default). Passing `"context": []` or `"clear": true` reverts to default.
- No new getter: the set response and the existing `session_info` surface carry the effective value (avoid a one-off read tool).

### G. Security & leakage

| Concern | Mitigation |
|---------|-----------|
| Leaking unrelated history into a throwaway run | Default `none`; explicit opt-in only |
| Blowing the child's context window | Per-source caps + total cap; oversized → truncate → drop lowest-priority |
| Secrets crossing the boundary | Block is transcript/memory/paths only; raw keys never in memory or session text |
| Child is a different trust domain | Context is read-only, one-way; child output still passes through ADR-0014/0030 collection (verbatim emit is a pre-existing ADR-0030 decision, unchanged) |
| Cost amplification | Context tokens are charged to the child run; capped + opt-in keeps that deliberate |

## Open questions (Q1–Q12)

| ID | Question | Rec |
|----|----------|-----|
| Q1 | `context` as a source-list (composable) vs a single enum? | Source-list |
| Q2 | Sources in v1: compact, full, memory, read_paths? | Yes, all four |
| Q3 | `full` = recent raw turns (capped) + compact of older (no double-count)? | Yes |
| Q4 | Delivery: prepend to prompt vs claude `--append-system-prompt` vs context file? | Prepend to prompt (driver-agnostic) |
| Q5 | Total cap default 16000 chars; per-source caps as above? | Yes |
| Q6 | `memory` seeded from prompt + recent user turns, top-K=5? | Yes |
| Q7 | Include `read_paths` (already in TurnContext, near-free)? | Yes |
| Q8 | Per-session override tool `session_set_subprocess_context` + echo effective config? | Yes |
| Q9 | Preset-level default as `agent_presets.context`/`context_max_chars` (schema v9), edited in Settings → Agents? | Yes |
| Q10 | Resolution: per-call → per-session → preset/global default? | Yes |
| Q11 | Routed turns (ADR-0030) honor the same resolution, via `TurnOpts` context override? | Yes |
| Q12 | Default `none` (opt-in only) to preserve today's cost/behavior? | Yes |

## Implementation sketch (non-normative)

```
internal/agentproc/
  context.go     — Source enum, ContextSpec, Assemble(session, spec) (pure fn), caps
  manager.go     — Request gains ContextSpec; RunSync/StartBackground assemble + inject before BuildArgv

internal/db/
  sessions.go    — add subprocess_context (JSON) to sessions (schema v9)
  agent_presets.go — add context + context_max_chars columns (schema v9)

internal/tools/
  agentproc.go   — accept context arg; pass through to Request
  subprocessctx.go — session_set_subprocess_context (mirror session_set_agent_preset)

internal/session/
  loop.go        — build TurnContext.ContextSources / pass session.subprocess_context into routed-turn TurnOpts

internal/api/
  settings.go    — Agents preset editor gains context fields
```

**Tests:** `Assemble` is a pure function (given fixture session + spec → deterministic block; truncation/drop order; shorthand resolution; `none` = empty); resolution order (call → session → default); routed-turn and tool both inject; default off when nothing set; no secrets in block (grep for `orb_ak_`/key patterns); marker-only in prompt_preview.

## Consequences

### Positive
- Removes the "paste a paraphrase" ritual for cross-session handoffs.
- One mechanism serves both the tool and routed turns.
- Composable + capped = predictable cost, no context-window blowouts.
- Default-off keeps cheap throwaway runs cheap and isolated.

### Negative / risks
- Context tokens are billed to the child run — must stay opt-in and capped.
- "Full" context is inherently bounded and may still omit what the child needed (no substitute for reading the repo on disk).
- A poorly-chosen `auto` heuristic could over/under-inject (mitigated: `auto` is shallow and explicit `none`/list always wins).
- Two config surfaces for the default (preset vs global) need clear precedence docs.

### Neutral
- `call_agent_process` and routed-turn semantics otherwise unchanged.
- No new sandbox/privilege model.

## Alternatives considered

- **Human-in-the-loop context pack** (preview card with section toggles before spawn): more control, more friction; can layer on later.
- **One-tap "include context?" gate**: an opt-in confirm on top of the default; complementary, not a replacement.
- **Reusable immutable context snapshot** (`ctx_<session>_vN` attach-to-many): amortizes cost for multi-agent pushes; larger scope, deferred.
- **CLAUDE.md/`--resume`**: wrong abstraction (child-session, not Marble transcript).

## References

- ADR-0014 `call_agent_process` (drivers, caps, BG, kill)
- ADR-0030 Agent-process presets & turn send-routing (presets, resolver, `TurnOpts`, session lock)
- ADR-0018 Selectable Models (`TurnOpts`, resolver, session-preference symmetry)
- ADR-0026 transcript density, ADR-0022 long-turn efficiency
- Code: `internal/tools/agentproc.go`, `internal/agentproc/{driver,manager}.go`, `internal/tools/registry.go` (TurnContext), `internal/session/loop.go`

## Changelog

| Date | Change |
|------|--------|
| 2026-09-16 | Proposed — context source-list, three-layer resolution, both subprocess paths |
