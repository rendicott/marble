# ADR-0035: Subprocess Liveness Signals — Multi-Signal Stuck Detection

| Field | Value |
|-------|--------|
| **Status** | **Accepted** (implementation in progress) |
| **Date** | 2026-10-06 |
| **Author** | — |
| **Deciders** | Project owner |
| **Tags** | tools, subprocess, agents, call_agent_process, anti-thrash |
| **Extends** | ADR-0014 (`call_agent_process`), ADR-0030 (agent-process presets), ADR-0022 (long-turn efficiency / anti-thrash) |
| **Evidence** | Field report 2026-10-06: two healthy `grok` agents killed at ~12m and ~7.5m on `stuck_hint`; both were streaming tokens the whole time. Follow-up measurements of process-tree and session-state signals on a live `grok` run (below). |

## Context

`call_agent_process` runs headless external coding agents (`grok`, `claude`, `opencode`) and
returns a `progress` block on every poll. `progress.stuck_hint` is meant to say "this child looks
hung — consider killing it", and both the tool description and the system prompt licensed a caller
to kill a background agent "unless it is under ~5–8m" when `stuck_hint` is true.

The hint was computed from the **child's working directory** only:

```
CWDMtimeChanged = newest mtime under Task.CWD (depth 4, 400 files) is newer than StartedAt + 3s
WriteSignals    = count of write-ish tokens in captured stdout+stderr
StuckHint       = running && elapsed* >= stuck_after_sec && !CWDMtimeChanged && WriteSignals == 0
                  (*elapsed = time since PROCESS START, not since the last sign of life)
```

Two structural defects:

1. **`elapsed` measures the wrong interval.** The predicate is effectively "has this agent ever
   written a file", not "is it still working". An agent 12 minutes into a long reasoning phase is
   indistinguishable from one wedged at t=0.
2. **One signal, and the wrong one.** Coding agents are I/O-bound on a model API and write nothing
   to cwd during planning/reasoning. With `--output-format json`, stdout is buffered until exit,
   so `WriteSignals` is structurally 0 for the whole run and can never rescue the decision.

A first draft of this ADR replaced cwd with each vendor's private session-state files
(`grok` `events.jsonl`, `claude` transcript JSONL), parsed per vendor. That fixes today's failure
but couples correctness to undocumented, fast-moving file formats. **Harnesses will change over
time**, and a detector that silently loses its only signal when a vendor renames a file regresses
straight back to the false-positive kill.

## Evidence (2026-10-06)

The two killed agents:

| Session | Effort | Messages | Last phase | Runtime | Outcome |
|---|---|---|---|---|---|
| `01a11029-7be9-7582-b288-cb10e19f572c` | high | 292 msg / 145 chat | `streaming_text` | 07:41→07:49 | killed at ~12m |
| `01a11034-f8bf-7b11-9d0f-d37c211f953674` | medium | 170 msg / 90 chat | `streaming_reasoning` | 07:54→08:02 | killed at ~7.5m |

Both were emitting tokens continuously; neither had touched cwd, correctly, since neither had
reached its first edit.

`grok` session-state event cadence across four real sessions:

| Session | events | span | median gap | p95 gap | **max gap** |
|---|---|---|---|---|---|
| `…fix-w2-android-ui` | 1538 | 286 s | 0.08 s | 1.2 s | 6.1 s |
| `…fix-w3-web` | 1551 | 288 s | 0.02 s | 1.0 s | 40.5 s |
| `…fix-w1-sync` | 3697 | 713 s | 0.04 s | 0.8 s | **77.7 s** |
| `…orb` | 3305 | 661 s | 0.07 s | 1.0 s | 29.6 s |

A live `grok` run (reason, then run a `sleep 25` tool, then answer), sampled every 2 s across
the child's process tree via `/proc`:

| Phase | Δ read bytes / 2 s | Δ write bytes / 2 s | Δ CPU ticks / 2 s | session-state age |
|---|---|---|---|---|
| startup / streaming | 36 KB – 164 MB | 42 KB – 1.3 MB | 6 – 142 | < 2 s |
| tool running (`sleep 25`) | 64 – 560 | 168 – 285 | 0 – 1 | grew to 18.7 s |

The same probe against `claude` (2.1.289, headless `-p`, one tool call running
`python3 -c "import time; time.sleep(45)"`), same sampling:

| Phase | Δ read bytes / 2 s | Δ write bytes / 2 s | Δ CPU ticks / 2 s | transcript age |
|---|---|---|---|---|
| startup / streaming | 12 KB – 1.9 MB | 5 KB – 305 KB | 13 – 175 | < 2.2 s |
| tool running (45 s) | 628 – 1198 | 112 – 192 | 2 – 17 | grew to 44.5 s |

During the tool call the transcript was silent for the full 44 s; only the process tree (`bash`,
`python3` descendants) showed the agent was mid-tool.

Observations that shape the decision:

- **Every single signal has gaps.** Session state went quiet for ~19 s while a tool ran;
  process I/O and CPU are near-idle during the same window; cwd is quiet for the whole reasoning
  phase. Only the *combination* is continuous.
- **Raw counters never stop ticking.** An idle CLI still accrues I/O (~125 B/s for `grok`,
  up to ~675 B/s for `claude`) and CPU. "Counter changed" is meaningless; a **rate threshold** is
  required. Active streaming never dropped below ~8.8 KB/s.
- **Agent CPU does not discriminate.** Idle `claude` spikes to ~8.5 % CPU (event loop, GC), the
  same as while streaming. CPU therefore only counts *real compute* (a tool building or testing),
  with a high threshold; model streaming is left to I/O and session state.
- **Tools escape the process group.** The `sleep 25` the agent ran was *not* in the child's
  process group, so descendant discovery must follow the `ppid` chain, not `pgid`.

## Decision

**`stuck_hint` fires only when every available liveness signal has been quiet for
`stuck_after_sec`. Signals are generic by default; vendor-specific knowledge is configuration,
not code.**

### 1. Signals

Each running background task carries a tracker that measures independent signals. Each signal
reports *available* (could it be measured at all) and *last advance* (when it last showed
activity). A signal that never advanced is aged from `StartedAt`.

| Signal | Source | Advance means | Generic? |
|---|---|---|---|
| `proc_io` | `/proc/<pid>/io` `rchar+wchar`, summed over the child's descendant tree | rate ≥ `min_io_bytes_per_sec` over the sample interval | yes (Linux) |
| `proc_cpu` | `/proc/<pid>/stat` `utime+stime+cutime+cstime` over the tree (reaped children land in their reaper's `cutime`) | rate ≥ `min_cpu_percent` (real compute, e.g. a build) | yes (Linux) |
| `proc_tree` | descendant set, discovered via the `ppid` chain | a descendant appeared or exited (tool start/finish) | yes (Linux) |
| `output` | bytes written to the child's stdout+stderr | byte count grew | yes |
| `cwd` | newest mtime under `Task.CWD` (existing walk) | newest mtime moved past `StartedAt + 3s` | yes |
| `session_state` | files matched by the driver's `state_globs` for the pinned session id | newest mtime, or newest timestamp in the tail of the newest JSONL, moved | config-driven |

Process signals are sampled by a lightweight background loop per task (default every 5 s), so the
verdict does not depend on how often a caller polls. A poll also samples, throttled. Where `/proc`
is unavailable the process signals report unavailable and the rest still decide.

### 2. The detection rule

```
alive_age_sec = now - max(last advance of every available signal)
stuck_hint    = status == running
                && elapsed >= stuck_after_sec
                && alive_age_sec >= stuck_after_sec
alive         = alive_age_sec <= alive_window_sec
```

`elapsed` is only a floor (nothing is stuck before it has existed for the window). Losing any one
signal (a vendor renames its state files, `/proc` is absent, output is buffered) makes the
detector **less sensitive, never more trigger-happy**. Every failure path degrades toward
"alive", never toward a kill.

Tasks with no tracker (synchronous runs, which are never polled, and tasks that failed to start)
keep the previous cwd/write-signal rule, labelled as such in `stuck_reason`.

### 3. Session-state signal is configuration

Many agent CLIs keep a per-session state directory and accept a session-id flag. The harness
generates a UUID v4 per task, passes it via the driver's configured flag, and globs for the
state by that id. The id is ours and fresh, so a match needs no cwd cross-check.

`agent_process.json`, per driver (built-in defaults shown; an older config file that sets
neither key inherits them):

```json
"grok":   { "session_id_flag": "-s",
            "state_globs": ["$GROK_HOME/sessions/*/{session_id}",
                            "~/.grok/sessions/*/{session_id}"] },
"claude": { "session_id_flag": "--session-id",
            "state_globs": ["$CLAUDE_CONFIG_DIR/projects/*/{session_id}.jsonl",
                            "~/.claude/projects/*/{session_id}.jsonl"] },
"opencode": { }
```

- `{session_id}` is substituted; `~/` and `$VARS` expand from the child's environment; a pattern
  naming an unset variable is skipped.
- A glob matching a directory contributes its direct files; a file contributes itself.
- `session_id_flag: "none"` disables pinning for a driver.
- The session flag is appended once, after flag de-duplication; any earlier occurrence is removed.

Parsing stays deliberately shallow and format-agnostic: `stat` every matched file (mtime, size);
tail-read at most 16 KB of the newest `*.jsonl` and scan backwards for the first line carrying a
timestamp key (`ts` / `timestamp` / `time`) and a `phase` key. Truncated or unparseable lines are
skipped. **Never read a whole state file per poll**; real transcripts reach tens of MB (39.7 MB
observed).

When a harness changes its layout, the fix is a config edit. The detector meanwhile still has
five generic signals.

## API surface (additive JSON on `progress`)

| Field | Type | Meaning |
|---|---|---|
| `alive` | bool | some available signal advanced within `alive_window_sec` |
| `alive_age_sec` | int | seconds since the freshest advance across all signals |
| `alive_signal` | string | name of the freshest signal (`proc_io`, `session_state`, …) |
| `last_signal_at` | RFC3339 | when that advance happened |
| `phase` | string | last `phase` value from session state, if any; else `""` |
| `signals` | object | per signal: `{available, age_sec, detail}` |
| `stuck_reason` | string | lists every signal with its age, and every unavailable one with why |

The task also carries `agent_session_id` when a session id was pinned.

Unchanged: `elapsed_sec`, `cwd_mtime_changed`, `newest_mtime`, `stuck_hint`, `stuck_after_sec`,
`write_signals`. Field names are additive only, since pollers are LLM callers reading prose.

`stuck_reason` example:

```
no liveness advance for 512s on any signal: proc_io 512s (<4096 B/s), proc_cpu 512s (<25%),
proc_tree 600s (1 descendant: sleep), output 600s, cwd 600s (unchanged since start),
session_state 515s (phase=streaming_text); unavailable: none
```

## Config (`$MEMORY/agent_process.json`)

| Field | Default | Meaning |
|---|---|---|
| `stuck_after_sec` | 480 | **restated**: seconds with *no advance on any signal* before `stuck_hint` |
| `alive_window_sec` | 120 | an advance younger than this reports `alive: true` (advisory) |
| `liveness.min_io_bytes_per_sec` | 4096 | tree I/O rate counted as activity (~6× worst idle noise, ~½ slowest observed streaming) |
| `liveness.min_cpu_percent` | 25 | tree CPU rate counted as activity: real compute only (~3× idle `claude` spikes) |
| `liveness.sample_sec` | 5 | background sample interval |
| `drivers.<name>.session_id_flag` | per driver | flag used to pin the session id; `"none"` disables |
| `drivers.<name>.state_globs` | per driver | where that session's state lives |
| `stuck_kill` | false | unchanged; never auto-kill by default |

## Caller-facing text

The tool description (`internal/tools/specs.go`), the background-start note and `stuck_hint` note
(`internal/tools/agentproc.go`), the system prompt (`internal/session/session.go`), and the routed
subprocess status line (`internal/session/agent_route.go`) said *"Do not kill under ~5–8m unless
stuck_hint."* That guidance turned a bad hint into a dead agent. Replace it with: judge by
`progress.alive` / `alive_age_sec` / `phase` / `signals`; `stuck_hint` means *every* measured
signal has been quiet for the window, and `stuck_reason` lists what was measured.
`internal/api/agents.go` exposes `alive_window_sec` alongside `stuck_after_sec`.

## Non-goals

- Streaming child stdout into the turn (still wait-for-exit).
- Auto-kill (`stuck_kill` unchanged, default off).
- Deep per-vendor parsing (message counts, turn numbers). The `signals` detail strings carry
  what generic parsing can recover; anything richer belongs in config-driven extensions later.
- Non-Linux process signals. `/proc` is Linux-only; elsewhere the process signals report
  unavailable.

## Verification

- Unit tests with on-disk fixtures, no network, no real agent runs:
  - **regression for the field failure:** fresh session-state file, stale cwd, no output,
    `stuck_after_sec` forced to 5 s ⇒ `stuck_hint` false, `alive` true, `phase` non-empty;
  - process-tree sampling of a real short-lived child (e.g. `sleep`) finds the descendant via
    `ppid` even when it is in its own process group;
  - every signal stale beyond the window ⇒ `stuck_hint` true, `stuck_reason` lists each signal;
  - no state configured (`opencode`) ⇒ `session_state` reported unavailable with the reason, and
    the other signals still decide;
  - a JSONL whose last line is truncated does not panic and does not lose liveness (mtime holds);
  - glob expansion: `{session_id}`, `~/`, `$VAR`, unset-var skip;
  - `BuildArgv` emits the configured session flag exactly once after de-duplication;
  - an older config without `session_id_flag` / `state_globs` inherits the driver defaults;
  - the existing `TestBuildProgressStuck` keeps passing on the tracker-less path.
- `go build ./... && go vet ./... && go test ./...` green.

## Consequences

- Long-reasoning agents are no longer killable by a heuristic that cannot see them think.
- Robust to harness drift: a renamed or removed vendor state file removes one signal out of six,
  and is fixed by editing `agent_process.json`.
- A tool that legitimately runs silent for longer than `stuck_after_sec` (no output, no compute,
  no new processes, e.g. a long `sleep`) is reported stuck. That is honest: nothing observable is
  happening. `stuck_hint` stays advisory.
- Biased toward "alive". Background noise above the thresholds (e.g. a CLI that polls a server
  while wedged) can keep a hung agent looking alive. That is the intended failure direction; the
  hard `timeout_sec` still bounds every run.
- The harness reads `/proc` and, where configured, vendor-private state files. Both are read-only,
  bounded (tail reads, capped directory listings), and every parse failure degrades a signal to
  unavailable rather than to a kill.
