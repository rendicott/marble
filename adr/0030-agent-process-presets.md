# ADR-0030: Agent-process presets & turn send-routing

| Field | Value |
|-------|--------|
| **Status** | **Accepted** (2026-09-15) — implemented (schema v8) |
| **Date** | 2026-09-14 |
| **Author** | — |
| **Deciders** | Project owner |
| **Tags** | tools, subprocess, agents, presets, settings, session, turn-routing, claude-code, grok-build, opencode, cost |
| **Extends** | ADR-0014 (`call_agent_process`), ADR-0007 (Settings UI), ADR-0018 (Selectable Models / `TurnOpts`), ADR-0005 (tool/loop), ADR-0010 (turn cancel), ADR-0022 (long-turn efficiency) |
| **Answers** | [0030-answers.json](0030-answers.json) (`2026-09-15T00:00:00.000Z`) — Q1–Q13 locked (rec) |
| **Review UI** | [0030-review.html](0030-review.html) |

## Summary

`call_agent_process` (ADR-0014) works, but it is **awkward to drive**: the only way to use it is to *prompt the Marble model* to emit the tool call with a hand-written prompt, which is too free-form, easy to miss, and impossible to trigger deterministically from the UI.

This ADR proposes turning the **ad-hoc tool** into a **first-class, explicit routing surface**:

1. **Presets** — named, editable agent-process configurations (grok, opencode, claude, …) managed in the Settings ⚙ modal, each with a driver, command, model, default args, and a **host-detected** status badge.  
2. **Explicit send-routing** — route a **turn's prompt** to a preset subprocess and **reliably monitor/collect** the result back into the transcript (reusing ADR-0014 background-task + progress machinery).  
3. **Two triggers**: a **per-turn** override (long-press / right-click the send button) and a **session lock** ("subprocess-only") via the **existing model selector** (models + detected presets, mutually exclusive).

This is the agent-process analogue of ADR-0018 (Selectable Models): a catalog of presets, an effective-resolution order, turn-scoped options, and a per-session preference — but the "model" being selected is a **local harness CLI**, not a cloud model endpoint.

## Context & pain

### Current state (verified)

| Area | Today |
|------|-------|
| Config | `$MEMORY/agent_process.json` → `agentproc.Config{Drivers map[string]DriverConfig}`; `grok` + `claude` only (`internal/agentproc/config.go`) |
| Drivers | Hardcoded `driverFor("grok"\|"claude")` switch in `internal/agentproc/driver.go`; argv built per driver, no extensibility |
| Tool | `call_agent_process` (ADR-0014): `format` enum `grok\|claude`, `prompt`/`cwd`/`workdir`/`extra_args`/`background`/`task_id`/`kill` (`internal/tools/agentproc.go`) |
| Trigger | The **model must emit** the tool call with a hand-written prompt; no UI affordance to route a turn |
| Settings | ADR-0007 modal edits DB `settings` + `mcp.json`; `agent_process.json` is hand-edited only |
| Turn options | ADR-0018 introduced `TurnOpts` (cron model pin) resolved once at `runTurn` start — no agent-process field |
| Detection | None — driver `BuildArgv` calls `exec.LookPath` and fails at fire time if the binary is missing |
| Session | `sessions.model_id` (ADR-0018) exists; no agent preset / routing columns |

### What is awkward today

| Situation | Failure mode |
|-----------|--------------|
| "Use a subprocess to keep costs down this turn" | Model must be *told* to call the tool; it may ignore, paraphrase, or use the wrong flags |
| "Run this session only on Grok" | No session-level way to force it; every turn is a fresh prompt gamble |
| "Is Claude even installed?" | Unknown until a run fails at fire time (`binary not found on PATH`) |
| "Which harnesses do I have?" | `agent_process.json` has drivers, but no host detection, no UI, no version |
| Add a new harness (opencode, codex, aider) | Requires a Go driver edit + rebuild; not operator-configurable |

The core friction: **the tool is model-mediated and prompt-mediated** when it should also be **operator-mediated and deterministic**.

## Goals

1. **Presets are first-class config**: a named catalog of agent-process presets editable in Settings ⚙ (extending ADR-0007), not a hand-edited JSON map.  
2. **Host detection**: each preset reports whether its command resolves and what version runs, surfaced as a badge in Settings.  
3. **Explicit turn routing**: an operator can **send a turn's prompt** to a preset and get the result collected back reliably (reuse ADR-0014 BG task + poll + progress + stuck detection).  
4. **Per-turn override**: long-press (touch) / right-click (desktop) the send button opens a preset chooser for **that turn only**.  
5. **Session lock**: a session can be configured to route **every** user turn to a preset (subprocess-only), with a visible indicator.  
6. **Extensible drivers**: add `opencode` (and make the driver set extendable) so "grok, opencode, claude, …" is data, not a recompile.  
7. **Safe & bounded**: reuse ADR-0014 concurrency caps, timeouts, output limits, stuck detection, and process-group kill on turn Stop.

## Non-goals (v1)

| Non-goal | Why |
|----------|-----|
| Streaming child stdout/tool-use into live turn steps | ADR-0014 already waits for exit; keep that |
| Resume/continue external agent sessions | ADR-0014 Q10 deferred; unchanged |
| Automatic model-vs-agent routing (harness decides per task) | Operator/explicit selection only (same stance as ADR-0018) |
| A full "agent marketplace" / remote agents | Local CLI harnesses only |
| Arbitrary shell presets | Still only known agent drivers; `shellExecute` remains the escape hatch |
| Per-turn cost metering of subprocess runs | Future spend ADR; out of scope |
| Changing `call_agent_process` tool semantics for model-initiated calls | Tool stays as-is; this ADR adds a routing path on top |

## Decision drivers

1. **Determinism over free-form** — an explicit send affordance beats "please call the tool".  
2. **Symmetry with ADR-0018** — presets : models :: `agent_presets` : `model_catalog`; reuse resolver + `TurnOpts` + session-preference patterns rather than invent new ones.  
3. **Operator-owned config** — presets are edited in Settings; the agent may *select* but not *define* them (symmetric with ADR-0018 KD8).  
4. **Cost is the motive** — routing keeps the expensive Marble model out of the loop for the routed turn; so the result should be emitted **verbatim**, not re-summarized by the Marble model.  
5. **Reuse, don't rebuild** — collection/monitoring already exists (`agentproc.Manager`); route turns through it.

## Proposed design

### A. Presets (generalize `drivers` → `presets`)

A **preset** is a named, operator-editable bundle that fully describes one headless harness invocation:

```jsonc
{
  "id": "grok-fast",              // slug, unique
  "driver": "grok",               // grok | claude | opencode | …
  "display_name": "Grok (fast)",
  "command": "grok",              // resolved on PATH or absolute
  "model": "",                    // optional -m/--model passthrough
  "default_args": ["--no-plan", "--effort", "medium", "--max-turns", "40"],
  "timeout_sec": 900,             // optional per-preset override (clamped to global max)
  "enabled": true,
  "notes": ""
}
```

**Key point:** presets are **richer than today's `DriverConfig`** — multiple presets can share one driver but differ in `command`, `model`, `default_args`, or `timeout_sec`. The driver (`grok`/`claude`/`opencode`) supplies *argv-building + output-parsing*; the preset supplies *the concrete invocation*.

The existing `agent_process.json` `drivers` map is a degenerate case (one preset per driver, keyed by format). This ADR **generalizes** it, keeping backward-compatible defaults.

### B. Storage & Settings UI

Presets move to a durable store and get a Settings section, mirroring ADR-0018's `model_catalog`.

- **SQLite table `agent_presets`** (schema **v8**; ADR draft said v4 while the binary was already at v7), columns: `id`, `driver`, `display_name`, `command`, `model`, `default_args` (JSON array), `timeout_sec`, `enabled`, `detected`, `detected_version`, `detected_path`, `detected_at`, `sort_order`, `notes`, timestamps.  
- **`agent_process.json`** remains the source of **process-level defaults + global caps** (`default_timeout_sec`, `max_timeout_sec`, `max_per_session`, `max_output_bytes`, `system_agents_enabled`, `stuck_after_sec`, `stuck_kill`) and is the **seed** source on first migrate (its `drivers` become initial preset rows).  
- **Settings ⚙ section "Agents / Subprocesses"**: list presets with enabled toggle + detection badge; add/edit/delete; live `detect` button. Unknown fields rejected; no secrets (env-only, ADR-0016 pattern).

### C. Host detection

- On startup (warm) and on demand (Settings "detect" / `GET …/health`): for each enabled preset, resolve `command` via `exec.LookPath` (or absolute path), run a cheap probe (`<command> --version`, short timeout ~3s), and store `detected`, `detected_version`, `detected_path`, `detected_at`.  
- Detection is **advisory**: a not-detected preset may still be selected, but fire fails fast (or falls through) if the binary is absent at fire time (see E).  
- Settings renders: 🟢 `detected · v1.2.3` / ⚪ `not found on PATH`.

### D. Turn routing semantics ("send the round's prompt")

When a turn is **routed** to preset `P`:

1. At `runTurn` start, instead of building the normal Marble agent prompt, the harness captures the **user's message** (the "round's prompt").  
2. It wraps it minimally: a short harness preamble (workspace path, `workdir` = workspace or a dedicated subdir, "you are operating in Marble session `<id>`; reply with your result") + the user message verbatim.  
3. It hands the wrapped prompt to `agentproc.Manager` using preset `P` (reusing `prepare`/`BuildArgv`/`StartBackground`), with `cwd` = workspace root (or `workdir`).  
4. The turn **awaits completion**, polling `task_id` (progress → `cwd_mtime_changed` / `stuck_hint` as today), and **emits the collected `Result`** as the turn's assistant message.  
5. Turn **Stop** kills the process group (ADR-0014 Q8); the turn's busy flag holds for the duration.

**Result shape folded into the transcript:**

```text
[subprocess: grok-fast · ok · 214s · cwd=…]
<summary / raw output>
```

No Marble model call occurs on a routed turn (see Q8).

### E. Effective resolution order

| Priority | Source | When applied |
|----------|--------|--------------|
| 1 | `TurnOpts.AgentPresetID` | Long-press / right-click send (that turn only) |
| 2 | `session.agent_preset_id` (set via the unified routing selector) | Session is locked to subprocess-only (mutually exclusive with `model_id`) |
| 3 | Normal Marble model turn | Default (today's behavior) |

If the selected preset is **missing, disabled, or not detected** at fire time: emit a harness advisory and **fall through** to the next priority (ultimately a normal Marble turn) — matching ADR-0018's resolver; never hard-crash.

### F. Per-turn override (long-press / right-click send)

- Long-press (touch) / right-click (desktop) on the send button opens a **preset chooser** (detected presets first, disabled/not-found greyed).  
- Selecting a preset sets a **turn-scoped** `TurnOpts.AgentPresetID` for the next send only (exactly like ADR-0018 `CronModelID`); a normal click clears it.  
- The UI shows a transient "routing → grok-fast" indicator on the composer.

### G. Session lock (subprocess-only) — reuses the model selector

The session lock does **not** add a second picker. A session can run either a **model** or a **subprocess** on a turn, never both — so the existing ADR-0018 **session model selector** becomes a single unified **routing selector**.

- **Entries:** "Process default (CLI)" + catalog models (ADR-0018) + a **"Subprocess"** group populated from **detected** presets only. Non-detected presets are omitted from the selector (they remain visible/editable in Settings, where detection runs).  
- **Semantics:** choosing a model entry = normal Marble model turns (ADR-0018 unchanged). Choosing a subprocess preset = **session lock** — every user turn routes to that preset (subprocess-only). Selecting one **clears** the other (**mutual exclusion**).  
- **Storage:** keep `sessions.model_id` (ADR-0018) and add `sessions.agent_preset_id` (preset slug or `""`). Mutually exclusive — `PATCH /api/sessions/{id}` with `{ "agent_preset_id": "grok-fast" }` clears `model_id`, and vice versa. `route_agents` is **dropped**: selecting a preset *is* the lock.  
- **Agent tools** mirror the unified selector (`session_set_model` ↔ `session_set_agent_preset`).  
- **Session badge:** "↳ grok-fast" when a preset is locked; the normal model badge (ADR-0018) when a model is selected.

### H. New drivers & extensibility

- Add an **`opencode`** driver (`opencode run "<prompt>"` headless; `--format json` — **verify flags at implement**, same caveat as ADR-0014 for claude).  
- Refactor `driverFor()` into an **extensible driver registry** keyed by `driver` string, so adding a harness is data + a small adapter, not a core edit.  
- `grok` + `claude` drivers are unchanged in behavior.

### I. Security & concurrency (reuse)

| Control | Source |
|---------|--------|
| Concurrency cap | `max_per_session` (ADR-0014 Q7) |
| Timeouts | `default_timeout_sec` / `max_timeout_sec` (clamped), per-preset optional override |
| Output cap | `max_output_bytes` |
| Stuck detection | `stuck_after_sec` → `stuck_hint`; optional `stuck_kill` |
| Kill | Turn Stop → process-group SIGTERM/SIGKILL (ADR-0014 Q8) |
| Auto-approve | Per-driver `auto_approve` (non-interactive flags) — unchanged |
| Workspace jail | `cwd`/`workdir` resolve under workspace (ADR-0014 Q9) |
| System agents | `system_agents_enabled` gate (default off) |

No new sandbox or privilege model in v1.

## Open questions (Q1–Q13)

Decisions to lock via `0030-review.html` → `0030-answers.json`. Recommendations below.

| ID | Question | Rec |
|----|----------|-----|
| Q1 | Preset shape: generalize `drivers` → named `presets` (driver + command + model + args + timeout)? | Yes — `presets`, one preset per driver by default |
| Q2 | Storage: SQLite `agent_presets` (schema v4) vs keep `agent_process.json`? | SQLite table; `agent_process.json` = global caps + seed |
| Q3 | Detection: resolve `command` + `--version` probe, cached in row? | Yes — startup warm + on-demand, cached |
| Q4 | Prompt wrap: verbatim user message vs harness preamble + cwd + user message? | Wrapped (preamble + cwd + verbatim message) |
| Q5 | Collection: reuse BG task + poll + progress/stuck; result folded as assistant message? | Yes — reuse `agentproc.Manager` |
| Q6 | Per-turn override UX: long-press/right-click send → preset chooser? | Yes — sets `TurnOpts.AgentPresetID` |
| Q7 | Session lock: reuse the model selector as a single routing selector (models + detected presets, mutually exclusive)? | Yes — one selector, `agent_preset_id` ↔ `model_id`, detected-only |
| Q8 | Marble model role on routed turn: verbatim emit vs model re-summarizes? | Verbatim (no model call — keep cost savings real) |
| Q9 | Caps: reuse `max_per_session` / timeouts / stuck / kill? | Yes — reuse, no new caps |
| Q10 | Add `opencode` driver + make driver registry extensible? | Yes — verify flags at implement |
| Q11 | Fallback when preset missing/disabled/not-detected: fall through + advisory? | Yes — fall through to normal turn |
| Q12 | System agents: user sessions yes, system default off? | Yes — same as ADR-0014 Q12 |
| Q13 | Agent tools: list/select preset + set session lock; create/edit operator-only? | Yes — mirror ADR-0018 KD8 |

## Implementation sketch (non-normative)

```
internal/agentproc/
  preset.go      — Preset struct, load/save, detection probe
  registry.go    — driver registry (grok|claude|opencode) keyed by driver
  config.go      — split global caps (agent_process.json) vs presets (DB)

internal/db/
  agent_presets.go — schema v4 table + CRUD
  sessions.go      — add agent_preset_id (mutually exclusive with model_id)

internal/session/
  turn.go / loop.go — TurnOpts.AgentPresetID; resolveEffectiveAgent(s, opts)
                      → if routed: capture user msg → wrap → agentproc.StartBackground
                      → poll (progress/stuck) → emit Result verbatim → endTurn

internal/tools/
  agentproc.go    — unchanged tool (model-initiated)
  agentpresets.go — agent_preset_list / agent_preset_get / agent_preset_set (mirror model tools)

internal/api/
  settings.go     — agents section (list/detect/save)
  sessions.go     — PATCH agent_preset_id (clears model_id; unified routing selector)

internal/web/static/
  settings.js     — Agents/Subprocesses section + detect
  app.js          — composer: long-press/right-click preset chooser + routing chip
```

**Tests:** preset CRUD; detection (found + not-found); resolver fall-through (turn override → session lock → normal); routed turn emits verbatim result; missing/disabled preset → advisory + normal turn; session lock routes every turn; Stop kills process group.

## Consequences

### Positive

- Turns `call_agent_process` from "ask the model nicely" into a deterministic, one-gesture operation.  
- Operators can lock cheap/fast sessions to a local harness without prompt gymnastics.  
- Host detection removes the "is it even installed?" guesswork.  
- Extensible drivers make "grok, opencode, claude, …" operator data, not a recompile.  
- Strong symmetry with ADR-0018 keeps the mental model small (presets ≈ models, local CLI ≈ cloud endpoint).

### Negative / risks

- Two config surfaces for agents (`agent_process.json` global caps vs DB presets) — needs clear precedence and docs.  
- Turn-routing holds the session busy for the subprocess duration (long runs block the session, same as a normal long turn).  
- Verbatim emit means no Marble-model sanitization of subprocess output — scope `cwd`/`workdir` and prompts carefully.  
- CLI flag drift for `opencode`/future drivers (same risk as ADR-0014; drivers need upkeep).  
- Detection can go stale (binary moved/removed after startup) — mitigated by fire-time `LookPath` + fall-through.

### Neutral

- `call_agent_process` tool semantics unchanged for model-initiated calls.  
- No new sandbox/privilege model.

## References

- ADR-0014 `call_agent_process` (drivers, caps, auto-approve, BG, kill)  
- ADR-0018 Selectable Models (catalog, `TurnOpts`, resolver, session preference, agent-tool symmetry)  
- ADR-0007 Settings UI (modal, sections, editable durable config)  
- ADR-0005 tools/loop, ADR-0010 turn cancel, ADR-0022 long-turn efficiency  
- Code: `internal/agentproc/{config,driver,manager}.go`, `internal/tools/agentproc.go`, `internal/session/{session,turn,loop}.go`

## Changelog

| Date | Change |
|------|--------|
| 2026-09-14 | Proposed — presets + detection + turn send-routing (per-turn override + session lock) |
| 2026-09-14 | Refined — session lock reuses the ADR-0018 model selector as a single unified routing selector (models + detected presets, mutually exclusive); drop `route_agents` |
| 2026-09-15 | **Accepted** — locked Q1–Q13 to rec; schema **v8** (not v4) |
| 2026-09-15 | **Implemented** — presets + detect + opencode driver registry + session lock + per-turn send routing + Settings → Agents + `agent_preset_*` tools |
