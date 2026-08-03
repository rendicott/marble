# ADR-0022: Long-Turn Efficiency — Anti-Thrash, Escalation, Tool Discipline, Checklist / DoD

| Field | Value |
|-------|--------|
| **Status** | **Accepted** (ready to implement) |
| **Date** | 2026-08-01 |
| **Accepted** | 2026-08-02 |
| **Author** | — |
| **Deciders** | Project owner |
| **Tags** | agent-loop, tools, computer-use, anti-thrash, continuation, context, peer |
| **Extends** | ADR-0005 (tools & agent loop), ADR-0001 (inner loop), ADR-0010 (turn transparency), ADR-0013 (soul/system), ADR-0019 (attachments), ADR-0020 / **0021** (peer computer-use) |
| **Evidence** | Session `0wc5p9dwyv` (Princess Dance / Play Console): ~639 tool rounds, 165/179 `browser_act` were raw `eval`, ~115 sleep-only shells, zero `computer_confirm`, zero `wait` / `set_input_files`, repeated identical desktop coords, turn deaths at 15m wall / 80 iters before later limit raises |
| **Answers** | `adr/0022-answers.json` (`2026-08-02T01:23:00.354Z`) — all Q1–Q12 locked |
| **Review UI** | [0022-review.html](0022-review.html) |

## Overview

Long multi-tool turns (computer-use, research, multi-system ops) currently **survive** better after soft/hard wall and auto-continuation work, but still **waste** large fractions of rounds on:

1. **Identical retries** (same tool args / same screen coordinates) with no progress  
2. **Low-level reinvention** of higher tools (`eval` + `sleep` instead of `wait` / `click_text` / `set_input_files`)  
3. **No escalation** when a UI will not yield (never `computer_confirm`, never a one-step human ask)  
4. **No durable plan / definition of done**, so the agent re-tours the same pages and re-celebrates partial API success  
5. **Context bloat** (screenshots + eval dumps) that soft-warns but rarely changes structure  
6. **Continuation that only says “keep going”**, replaying the same thrash  

This ADR defines **general-purpose** harness + peer + prompt policies for **all** long tasks—not Play Console specifically—in four priority bands:

| Band | Theme | Intent |
|------|--------|--------|
| **P0** | Hard anti-thrash + stuck→escalate | Stop pure loops; force a different move or a human |
| **P1** | Tool discipline + rich auto-continue | Prefer right primitives; continue with **state**, not slogans |
| **P2** | Peer host-chrome + eval demotion | Cheap environmental fixes; make bad defaults harder |
| **P3** | Checklist / DoD / compact / skills | Plan, verify, remember; teach patterns once |

**Architecture principle:** efficiency is a **loop + tool contract** problem first. Raising `--max-tool-iters` and `--hard-wall` alone only buys more thrash. Policies must be **fail-closed on repetition** and **fail-open toward confirmation and structured tools**.

## Background & Motivation

### Observed failure modes (session `0wc5p9dwyv`, generalizable)

| Mode | What happened | General form |
|------|----------------|--------------|
| Eval-as-browser | 165× `action=eval` for click/read/dismiss | Model treats CDP as a JS REPL |
| Sleep-as-wait | ~115× `shell_execute` = `sleep N` | Polling without a condition |
| Coord thrash | Same `(x,y)` desktop click 7–9× | Blind GUI retry |
| Overlay thrash | Escape + “Restore pages?” forever | Host chrome steals agency |
| No confirm | `computer_confirm` never used | Stuck SPA never escalates |
| Wrong layer | Hours of UI after API already uploaded AAB | GUI before API/scripts |
| Weak DoD | “Release completed” ≠ user can install | Provider truth ≠ user truth |
| Empty continue | User typed Continue; agent replayed Family checkbox | No transferred state |

### What already shipped (context; not this ADR’s scope to re-decide)

| Item | State |
|------|--------|
| Soft wall default 20m, throttled | Live |
| Hard wall configurable (e.g. 2h) | Live |
| Max tool iters higher (e.g. 200) + soft 150 | Live |
| `--auto-continue-reserve` near max → schedule continuation | Live |
| Browser `wait`, `set_input_files`, post-click mini snapshot | Peer + specs (underused by models) |
| Screenshot-gated desktop click + post-click auto-shot | Live |
| CDP-fail auto-screenshot | Live |

**Gap:** survival ≠ efficiency. This ADR closes the efficiency gap.

## Goals & Non-Goals

### Goals

1. **Detect no-progress loops** at the harness (tool name + args fingerprint, optional desktop coords) and **hard-fail** the tool with a directive to change approach or confirm.  
2. **Escalate stuck GUI** after a small fixed number of failures of the same class → prefer `computer_confirm` / user one-step, not more coords.  
3. **Discourage sleep-only shell** and **default eval-for-click/read** via tool results + specs + optional soft blocks.  
4. **Enrich auto-continuation** with last checklist, URL, failure mode, and a ban list of recently failed fingerprints.  
5. **Peer:** dismiss common host overlays once per navigation; return **before/after** success signals on actions.  
6. **Checklist / DoD** for multi-step work: durable artifact + verification against user-visible outcome.  
7. **Context hygiene:** compact or strip bulky tool payloads on long computer-use turns without losing the plan.  
8. Policies work for **filesystem/shell/research** tasks as well as computer-use (fingerprints are tool-agnostic).

### Non-goals (v1 of this ADR)

| Non-goal | Rationale |
|----------|-----------|
| Perfect intent classification / ML thrash detector | Rule-based fingerprints first |
| Replacing model judgment entirely | Harness only blocks *obvious* no-progress |
| Auto-clicking money/OTP without confirm | Safety unchanged (ADR-0020 KD12) |
| Guaranteeing Play Console automation | Product UIs remain hard; escalation is the exit |
| Changing max iters / hard wall defaults again | Orthogonal; already operator-tunable |
| Multi-peer / multi-monitor | Still ADR-0020 scope |

## Priority bands (implementation contract)

### P0 — Must ship first (stop bleeding)

| ID | Change | Owner |
|----|--------|--------|
| **P0.1** | **Anti-repeat:** if same tool fingerprint succeeds or fails **N** times in a sliding window (or consecutive) with no state delta, return hard tool error | Harness `tools.Registry` / turn context |
| **P0.2** | **Stuck→escalate:** after **K** computer-use failures of the same class (or anti-repeat trip), inject mandatory advisory: call `computer_confirm` or ask user one step; optionally **block** further identical desktop/browser clicks until confirm or different action class | Harness loop + tools |
| **P0.3** | **Strategy break at auto-continue:** when near max-iters or hard-wall auto-continue fires, require summary + forbidden-retry list in continuation prompt (not only “resume”) | Harness `autoStopAndContinue` |

### P1 — High ROI next

| ID | Change | Owner |
|----|--------|--------|
| **P1.1** | **Sleep-only shell:** detect `command` that is only `sleep` / `timeout`; return error or strong rewrite hint → use `browser_act wait` / structured poll | Harness shell tool |
| **P1.2** | **Promote wait / set_input_files / click_text** in specs + ephemeral advisories when model uses eval/sleep for those jobs | Specs + loop advisories |
| **P1.3** | **Rich auto-continue payload:** last N tool names, last URL (if any), last error, fingerprint ban list, optional checklist blob from turn context | Harness |
| **P1.4** | **Action success criteria** on browser/desktop results: URL before/after, primary button disabled?, mini snapshot already partial—standardize | Peer + harness pass-through |

### P2 — Environmental & defaults

| ID | Change | Owner |
|----|--------|--------|
| **P2.1** | **Dismiss host overlays once** (Chrome restore, “Not now”, peer confirm noise) on browser open / ensure | Peer |
| **P2.2** | **Eval demotion:** `eval` allowed; if used for click/type when `click_text`/`type` would do, return soft warning + count toward thrash class | Harness and/or peer |
| **P2.3** | **Screenshot/wait budgets:** cap consecutive pure screenshots or pure waits per turn; force decision/confirm/user update | Harness |

### P3 — Structure & learning

| ID | Change | Owner |
|----|--------|--------|
| **P3.1** | **Checklist artifact** for multi-step turns: create/update `$MEMORY` or session-scoped checklist; inject into advisories / auto-continue | Harness tools or convention + soul |
| **P3.2** | **Definition of done = user-visible** when user stated one; verification step before “done” celebration | Prompt / soul + optional tool |
| **P3.3** | **Tool-blob compact:** auto or aggressive compact of old tool results / images on long computer-use turns | Loop (extends ADR-0005 auto-compact) |
| **P3.4** | **Skill pack** “enterprise SPA / form wizards / file inputs via CDP” | Skills tree + docs |

## Key Decisions (locked — `0022-answers.json` 2026-08-02T01:23:00.354Z)

| # | Decision | Source |
|---|----------|--------|
| **KD1** | Fingerprint anti-repeat **opt-in** (`--anti-repeat-n=0` default). Q1 originally preferred on; post-ship experience (polls of agent/URL/file size) showed false positives — **default off** (2026-08-02 amendment). Escalate/sleep/eval rails remain on. | **Q1** (amended) |
| **KD2** | Threshold **N = 3 consecutive** identical fingerprints (windowed 3-in-8 optional later) | **Q2** |
| **KD3** | On trip: **hard tool error** (not silent skip); message names next allowed moves | Fixed |
| **KD4** | **K = 3** consecutive `computer_*` failures (timeout, not_found, anti-repeat, requires screenshot) → escalate lock; anti-repeat trip on computer_* may escalate immediately | **Q3** |
| **KD5** | While `EscalateLock`: **hard-block** further desktop click / browser click\*; allow confirm, snapshot, screenshot, open, shell/API, different class | **Q4** |
| **KD6** | Sleep-only shell: **hard reject**; hint `browser_act wait`. Escape: `--block-sleep-shell=false` | **Q5** |
| **KD7** | Eval mutations: **warn**, then **hard error after M=5** mutate-evals in last 20 tools; suggest `click_text` / `type` / `set_input_files` | **Q6** |
| **KD8** | Auto-continue always carries **state packet** (checklist, URL, tools, ban list) when Cont available | Fixed |
| **KD9** | Peer overlay dismiss: **on by default**, best-effort **once per `browser_open`**, `overlays_dismissed` meta, no loop | **Q7** |
| **KD10** | Checklist v1: **convention + soft advisory** (file/memory); no new tool/schema yet | **Q8** |
| **KD11** | Anti-repeat applies to **all tools**; escalate lock is **computer\*-centric** | **Q9** |
| **KD12** | v1 CLI + settings read-only: `--anti-repeat-n`, `--block-sleep-shell`, `--stuck-escalate-k`, `--eval-mutate-max` | **Q10** |
| **KD13** | Extra auto-compact when toolRounds high **and ≥50% of last 20 tools are computer\***; keep checklist + last L | **Q11** |
| **KD14** | Anti-repeat counts **identical successful calls** (e.g. re-open same URL 3×); different args / real state change OK | **Q12** |

## Proposed Design

### 1. Turn-scoped thrash state (`tools.TurnContext`)

```text
TurnContext {
  ...
  Fingerprints []FingerprintEvent  // ring buffer, e.g. last 32
  ThrashScore  int
  EscalateLock bool   // when true, only confirm / non-repeat / different class
  Checklist    string // optional markdown/json blob
  LastURL      string
  LastFailure  string
  BanList      []string // fingerprints forbidden until approach change
}
```

**Fingerprint** (canonical string):

```text
tool_name + "\0" + normalized_args_json
```

Normalization:

- Drop volatile keys (`computer_id` optional keep for multi-peer)  
- For `shell_execute`: collapse whitespace; treat pure sleep specially  
- For `computer_desktop_act` click: `click|x|y|button`  
- For `computer_browser_act`: `action|target|text_prefix(80)|x|y`  
- Cap JSON length (e.g. 512) with stable hash of overflow  

**v1 rule (KD2, KD14):** **consecutive identical fingerprints only**, including **successful** identical calls. Different args that imply real state change do not match. Optional later: clear consecutive counter on explicit state-delta fields (`url` / `title` / exit code) when those differ.

### 2. Anti-repeat algorithm (P0.1) — locked N=3, all tools, successes count

On each `Registry.Execute` **before** running the tool:

1. Compute fingerprint F.  
2. If F ∈ BanList → error with ban reason.  
3. Count **consecutive trailing** events with fingerprint == F (success or failure).  
4. If count ≥ **N=3** (`--anti-repeat-n`, 0 disables) → do **not** call peer/shell; return:

```text
error: anti-repeat: tool X with same args used N times without progress.
NEXT (pick one): (1) different action class (2) computer_confirm / ask user one step
(3) API/script path (4) session_compact + re-plan. Forbidden: retry identical args.
```

5. Else run tool; append FingerprintEvent{F, ok, summary}.

**UI:** harness advisory chip once per trip (not every model call).  
**Events:** `session_events.kind = harness_advisory` or `tool_result` with error (prefer tool_result so the model sees it as tool output).

### 3. Stuck → escalate (P0.2) — locked K=3, hard-block clicks

**Triggers (any):**

- Anti-repeat trip on `computer_*` (immediate stuck)  
- **K=3** consecutive `computer_*` failures (timeout, not_found, anti-repeat, requires screenshot, etc.) — **KD4 / Q3**  
- ThrashScore ≥ threshold (implementation detail; optional)

**Effects (KD5 / Q4):**

1. Set `EscalateLock` until: successful `computer_confirm`, user message arrives (new turn clears), or model uses a **different action class** (e.g. shell API script vs desktop click).  
2. Inject system advisory every model call while locked.  
3. **Hard-block** further `computer_desktop_act` **click** and `computer_browser_act` click\* (`click`, `click_text`, and mutate-`eval` if classified as click).  
4. **Still allowed:** `computer_confirm`, `computer_screenshot`, `computer_browser_snapshot`, `computer_browser_open` / tabs / ensure, shell/API, type/key when not same-class thrash, non-computer tools.

**Never auto-accept confirm.** Timeout deny remains ADR-0020.

### 4. Strategy break + rich auto-continue (P0.3 / P1.3)

When `autoStopAndContinue` runs, build prompt:

```text
[harness auto-continuation]
Why: <reason>
Progress checklist:
<Checklist or "(none — create one)">
Last URL: ...
Last tools: t1, t2, ...
Last failure: ...
Ban (do not retry): fingerprint1, ...
Rules: Do not restart completed steps. Do not retry ban list.
Prefer API/script over GUI. If UI stuck → computer_confirm one step.
When user-visible DoD met → final summary to user.
```

Also write a short **assistant** forced-end message (already partially implemented) noting continuation id.

### 5. Sleep-only shell (P1.1) — hard reject (KD6 / Q5)

If `shell_execute` command matches:

```regex
^\s*(sleep|timeout)\s+\d+(\.\d+)?\s*$
```

(or only pure-delay variants) → **hard reject**:

```text
error: sleep-only shell is blocked. Use computer_browser_act action=wait
(text=…, target=…, x=timeout_ms) or poll a real condition.
```

Escape: `--block-sleep-shell=false` (**KD12 / Q10**). Real scripts that embed sleep among other commands remain allowed.

### 6. Eval demotion (P2.2) — warn then hard at M=5 / 20 (KD7 / Q6)

If `browser_act` action is `eval` and expression matches mutation heuristics (`.click(`, `dispatchEvent`, `insertText`, setting `.value`) →  

- Always append warning + count as `eval_mutate`  
- If **≥ M=5** mutate-evals in **last 20 tools** → hard error suggesting `click_text` / `type` / `set_input_files`  
- `--eval-mutate-max=0` = warn only  

Read-only eval stays allowed; prefer `browser_snapshot` when enough.

### 7. Peer overlay dismiss (P2.1) — on by default (KD9 / Q7)

On `browser_open` (and optionally ensure):

1. Best-effort dismiss known strings: “Restore”, “Not now”, “Close” on chrome restore UI **once**.  
2. Do not loop. Return `overlays_dismissed: [...]` in meta.  
3. Desktop path: optional single Escape only if lock screen not active.  
4. Peer flag to disable if needed.

### 8. Checklist / DoD (P3.1–P3.2) — convention only in v1 (KD10 / Q8)

- Agent should `file_write` or `memory_*` a checklist when task has ≥3 independent steps.  
- Harness may inject soft advisory if toolRounds ≥ soft/2 and no checklist in TurnContext: “create a short checklist now.”  
- **No** `task_checklist_*` tool/schema in v1; add later if convention fails.

**DoD:** system/soul line:

> Prefer verifying the **user-stated success condition** (e.g. tester sees app) over provider-internal “completed” alone.

### 9. Tool-blob compact (P3.3) — computer-heavy path (KD13 / Q11)

Extend auto-compact triggers:

- Existing: usage ≥ 85% × 3 rounds  
- **New:** toolRounds high **and ≥50% of last 20 tools are `computer_*`** → compact earlier, keep **checklist + last L** messages  

### 10. Kill switches / flags (KD12 / Q10)

| Flag | Default | Meaning |
|------|---------|---------|
| `--anti-repeat-n` | **3** | 0 disables P0.1 / anti-repeat |
| `--stuck-escalate-k` | **3** | computer failures before escalate lock |
| `--block-sleep-shell` | **true** | P1.1 hard reject pure sleep |
| `--eval-mutate-max` | **5** | hard after M mutate-evals in last 20; 0 = warn only |
| `--auto-continue-reserve` | 10 | existing |

Settings → runtime **read-only** display (like soft/hard wall).

## Interaction with existing loop policies

| Policy | Interaction |
|--------|-------------|
| Soft tool-round advisory | Keep; add “anti-repeat active; escalate if stuck” |
| Auto-continue near max iters | Keep; **must** use rich state packet |
| Hard wall timeout auto-continue | Same |
| Operator Stop | **No** auto-continue (existing); clear escalate lock on next user turn |
| computer_confirm 120s deny | Unchanged |
| Screenshot-gated desktop click | Unchanged; anti-repeat still applies to identical coords |

## Security & safety

- Anti-repeat must **not** auto-click through paywalls or confirms.  
- Escalate prefers **human** gates, not bypass.  
- Ban list is turn-scoped (memory of bad moves), not a global denylist of domains.  
- Sleep block must not break legitimate long `sleep` **inside** real scripts (only pure sleep commands).

## Observability

| Signal | Where |
|--------|--------|
| Anti-repeat trip | tool_result error + optional harness_advisory |
| Escalate lock on/off | harness_advisory; turn progress message |
| Sleep block | tool_result error |
| Overlay dismiss | peer meta / browser_act text |
| Auto-continue state size | log line in harness |

Metrics (optional later): trips per session, mean tools-to-done for computer-bound sessions.

## Alternatives considered

| Alternative | Why not first |
|-------------|----------------|
| Only raise max iters | Observed: more thrash, not more done |
| Only better prompts | Helps; models still sleep/eval; need hard rails |
| ML progress detector | Overkill for v1 |
| Kill computer-use for hard sites | Too absolute; confirm is the valve |
| Always force confirm every N clicks | Noisy; use stuck detection |

## Implementation plan (PR-sized)

| PR | Scope | Band |
|----|--------|------|
| **L0** | TurnContext fingerprints + anti-repeat N + tests | P0.1 |
| **L1** | Escalate lock + advisories + optional click block | P0.2 |
| **L2** | Rich auto-continue prompt + ban list | P0.3 / P1.3 |
| **L3** | Sleep-only shell block + flags + settings display | P1.1 |
| **L4** | Spec/advisory eval demotion + eval_mutate counter | P1.2 / P2.2 |
| **L5** | Peer overlay dismiss once + success meta | P1.4 / P2.1 |
| **L6** | Screenshot/wait budgets | P2.3 |
| **L7** | Checklist advisory + soul DoD line + skill stub | P3.1–P3.2, P3.4 |
| **L8** | Tool-blob compact trigger | P3.3 |

**Suggested ship order:** L0 → L1 → L2 → L3, then L5, then L4/L6/L7/L8.

**Repos:** harness + peer as noted; no monorepo merge of peer sources.

## Locked questions (Q1–Q12)

All answered 2026-08-02T01:23:00.354Z — see `adr/0022-answers.json` and KD table above. No open questions remain for v1 of this ADR.

## Success metrics (post-implement)

| Metric | Target (directional) |
|--------|----------------------|
| Sleep-only shell calls / long computer session | Near zero |
| Max consecutive identical fingerprint | ≤ N−1 |
| `computer_confirm` usage when stuck >5 min wall | Non-zero when UI stuck |
| User “Continue” after silent death | Down (auto-continue + forced end already help) |
| Tools-to-checklist-complete for multi-step ops | Down vs `0wc5p9dwyv`-class sessions |

## Changelog

| Date | Note |
|------|------|
| 2026-08-01 | Proposed from session `0wc5p9dwyv` analysis; P0–P3 bands; review UI for decisions |
| 2026-08-02 | **Accepted** — all Q1–Q12 locked from review (`0022-answers.json`); KD1–KD14 written; ready to implement L0→L8 |
| 2026-08-02 | **Amendment:** default `--anti-repeat-n=0` (off). Identical-arg thrash rails too crude for legitimate monitoring/poll loops; keep escalate K, sleep-block, eval-mutate. |

## References

- ADR-0005 tool loop soft/hard controls  
- ADR-0020 / 0021 computer-use peer  
- Session evidence: `~/.marble/session/0wc5p9dwyv.md`, `session_events` for `0wc5p9dwyv`  
- Live flags (informational): `--soft-wall`, `--hard-wall`, `--max-tool-iters`, `--auto-continue-reserve`  
