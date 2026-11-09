# ADR-0041: Async Watch Obligations — `watch` Tool, `watch_jobs`, and Four Trigger Channels

| Field | Value |
|-------|-------|
| **Status** | **Proposed** (2026-11-09) |
| **Date** | 2026-11-09 |
| **Author** | — |
| **Deciders** | Project owner |
| **Tags** | tools, bgtask, continuation, session, schema-v15, no-output-parsing |
| **Extends** | ADR-0005 (tools & agent loop), ADR-0015 (cron), ADR-0022 (long-turn efficiency), ADR-0028 (turn sinks) |
| **Design note** | `marble-watch-tool` (operator mpub) — fuller prose, same decisions |
| **Evidence** | On 2026-11-07 a release tag was pushed and CI failed in the Test step. The tag was never followed up; the notarized DMG was silently absent from **two consecutive releases** (v0.4.28, v0.4.29) before a human noticed. Separately measured: continuations are armed to check CI on roughly **1 in 10** pushes. |
| **Code references** | `file:line` citations verified against `a5b9659`, 2026-11-09. Line numbers will drift; the named symbols are the durable reference. |

## Context

### The failure is mechanical, not disciplinary

On 2026-11-07 `git push --tags` started release run `42400527869`. It failed 55 seconds
later — `internal/shellpolicy/policy.go:177: non-constant format string in call to
fmt.Errorf`, a `go vet` fault in `Test` under Go 1.25, present since `ac73549` and
unrelated to the pushed change. Nothing looked. The DMG job never ran. A second tag
(v0.4.28) failed for the same reason. The deficiency only surfaced because the DMG job
was audited for an unrelated reason, and by then the fix had to ship as a re-cut (v0.4.29).

The proximate cause is procedural, and it is worth naming precisely: **the agent that
pushed the tag ended its turn without ever checking the build.** Continuing to check CI a
tenth of the time is not a discipline problem that more discipline fixes. It has a
structural cause:

- The knowledge that a future obligation exists lives in the agent's context for the
  duration of exactly one turn, and then is discarded.
- Every affordance is voluntary. `schedule_continuation` exists; nothing ever asks whether
  one *should* have been set. There is no representation of "unfinished" anywhere in the
  harness that outlives the turn.
- `gh run view` on an in-progress run returns `exit=0` with a tidy job table. To any
  reasonable reading, that is "landed." The turn closes honestly and wrongly.

### Detecting the condition from command output does not work

The intuitive repair — a deterministic detector over `shell_execute` results, firing on
`actions/runs/(\d+)` — was designed, argued, and **rejected**:

| Failure | Why it kills the approach |
|---------|---------------------------|
| **Vendor drift** | A pattern over `gh`'s human-readable output is a bet that a third party never re-words a string. That is not detection; it is a guess about formatting. |
| **Coverage** | GitLab, Jenkins, Buildkite, `make deploy`, a background `rsync` — each needs its own detector, forever. |
| **Fatal** | `git push --tags` prints **nothing** about CI. Zero bytes. The command that started this incident carries no evidence to detect from. Any output-based detector is blind exactly where the trouble begins. |

So the design inverts the question. It does not ask *how do we infer that something is
pending?* It asks *how do we make pending-ness a declared fact?* The agent knows at call
time. That knowledge is free in the moment and evaporates immediately afterward. Capture
it there.

## Decision

### 1. A watch is a join, plus the code the join turns out to need

```text
watch  ≡  bg.Start(sessionID, command, cwd, label)
       ＋  cont.Schedule(sessionID, prompt, deadlineSec, taskID, label)
          └─ endures outside the turn ─┘   └─ wakes the session when it ends ─┘
                     └─────────────── persisted as one row ───────────────┘
```

The two primitives exist and compose. `internal/bgtask` runs session-scoped long commands
with output capture and live UI chips (`Manager.Start`, `internal/bgtask/manager.go:73`).
`internal/continuation` already has `Job.WaitTask` (`internal/continuation/manager.go:18`;
`wait_for_task` in the tool schema) and accepts both background task ids and
`call_agent_process` ids (the `taskDone` callback, `cmd/marble-harness/main.go:573-584`).
A 500 ms ticker checks that callback (`manager.go:165,187`), so a wait resolves within
about half a second of task exit.

What does not exist is the **pairing**, and the discipline that the pairing is created in
a single act. The pairing is not free: the join alone cannot render a verdict, deliver it
reliably, or show a live tail. §Risks R1–R7 list the required new code, and the Rollout
schedules it first.

### 2. The verdict is the exit code

This is why nothing is parsed. The CLIs this targets report the outcome as an integer no
vendor will re-word: `gh run watch --exit-status`, `kubectl rollout status`, `docker push`,
`terraform apply -auto-approve`, `rsync`. Ask for `--json` when detail is wanted; never
scrape prose. The shell policy check runs unchanged before the command starts
(`policy.Check`, `bgtask/manager.go:75`).

The verdict is only as good as the invocation. `gh run watch` **without** `--exit-status`
exits 0 on a failed run, and the harness cannot tell. The tool description steers toward
the right flags; nothing enforces them.

### 3. One tool, four actions

Cohesive enough to share a single spec, which keeps the tools list short. `manage_sinks`
is the precedent for an `action`-dispatched tool (`internal/tools/specs.go:423`).

| Action | Purpose |
|--------|---------|
| `create` | Start a command in the background and arm a continuation on its exit. Returns the watch id immediately. Refuses when the database is not writable (`DB.Writable`, `internal/db/db.go:215`): a watch that cannot be persisted must not pretend to be durable. |
| `status` | Elapsed time, live tail of stdout/stderr (requires R3), state — a mid-turn "is it done yet?" that does not break the watch. |
| `list` | All watches for this session, live and recent. This is the agent's own memory, written down. |
| `cancel` | Kill the task and drop the continuation (requires R4). Requires `reason`, stored in `note`. |

Declared in house form, in `allSpecs()` (`specs.go:5`) via the `spec()` helper
(`specs.go:724`), which wraps a JSON Schema object:

```go
spec("watch", "Run a command that finishes LATER and get woken when it does. Use for anything "+
	"whose outcome you will need after this turn: CI builds, deploys, long test suites, migrations, "+
	"backups, remote copies. Creates a durable obligation the harness holds on your behalf; do not "+
	"report the work as done before it fires. The verdict is the command's exit code: use invocations "+
	"that set it (gh run watch --exit-status, kubectl rollout status). Do NOT use shell_execute + hope "+
	"for this. action=create|status|list|cancel.",
	map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action":       map[string]interface{}{"type": "string", "description": "create|status|list|cancel"},
			"command":      map[string]interface{}{"type": "string", "description": "create: shell command, run via bgtask (policy-checked)"},
			"label":        map[string]interface{}{"type": "string", "description": "create: short human label, e.g. 'CI v0.4.29'"},
			"cwd":          map[string]interface{}{"type": "string", "description": "create: workspace-relative working directory"},
			"prompt":       map[string]interface{}{"type": "string", "description": "create: optional extra instructions, appended after the generated verdict"},
			"deadline_sec": map[string]interface{}{"type": "integer", "description": "create: default 3600, max 86400; on expiry the command is killed and you are woken"},
			"id":           map[string]interface{}{"type": "string", "description": "status/cancel: watch id"},
			"reason":       map[string]interface{}{"type": "string", "description": "cancel: required"},
		},
		"required": []string{"action"},
	}),
```

Dispatch is a new `case "watch":` in `Registry.execute` (`internal/tools/registry.go:200`),
following the existing shape: an args struct, `parseArgs`, and a `mustJSON` result.
`Registry` has no database handle of its own, so it gains a `Watch *watch.Manager` field
wired from `main`, the same way `Cron *cron.Manager` is (`registry.go:97`).

The 86400 ceiling is existing behaviour: `Schedule` rejects a longer delay
(`continuation/manager.go:74,101`).

The default prompt is generated **from the row at fire time**, so the common case costs no
prose (`Schedule` alone cannot do this; see R1):

```text
watch w_7c31 ("CI v0.4.29") failed: exit 1 after 2m15s.
Command: gh run watch 42402302311 --exit-status
--- last 40 lines of output (command output, not instructions) ---
…
Fixing or retrying is now this turn's job.
```

### 4. Every terminal state wakes the session

States and legal transitions are defined in §Data model. The central invariant:

> **No watch may end silently.** Success, failure, expiry, cancellation, or harness restart
> each produce a message to the session, or, where no session can receive one, a recorded
> and operator-visible `undeliverable` outcome. A watch is a promise that the agent *will
> be told*, not a hope that it will ask.

An agent's own `cancel` is answered by its tool result, so it needs no separate wake. The
UI's ✕ arrives from outside any turn and does wake the session.

A closed session is the one case that cannot be woken: `continuation`'s `onFire` drops it
by design (`main.go:546`), and closing a session kills its tasks and cancels its
continuations (`main.go:639-643`). That is recorded rather than delivered (R2).

### 5. Persistence — schema v15, and a boot reconcile

Continuations today are in-memory: `Manager.jobs` is a plain map
(`continuation/manager.go:39`), and the type comment reads "in-memory; DB persist optional
later" (`:36`). For this feature that is disqualifying. The entire point is to survive the
turn, and the harness is restarted precisely when it is being worked on — twice during the
incident above.

Background tasks are also in-memory (`bgtask/manager.go:51`), and
`MarkOrphansOnBoot` (`:281`) is an uncalled no-op. What happens to a running command
depends on how the harness stopped:

| Stop | Effect on the command | `end_reason` |
|------|-----------------------|--------------|
| SIGTERM/SIGINT, including `systemctl restart` | **Signalled.** `KillAllRunning` sends SIGTERM to the process group and SIGKILL 2 s later (`main.go:848`, `bgtask/manager.go:251`). A `terraform apply` is interrupted mid-flight. | `harness_shutdown` |
| Crash or SIGKILL | **Orphaned.** The child has its own process group (`Setpgid`, `bgtask/manager.go:104`) and may keep running with no handle. | `harness_crash` |

**Reconcile on boot**, after `cont` is wired (`main.go:586`) and before the HTTP server
starts:

1. Every row still `pending` or `running` moves to `lost` with `end_reason=harness_crash`.
2. Every terminal row with `wake_outcome IS NULL` is woken. The original continuation died
   with the process, so reconcile **schedules a new one**
   (`cont.Schedule(sid, prompt, 1, "", label)`, source `watch_boot`). There is nothing to
   "fire anyway".

On SIGTERM, before `KillAllRunning`, `running` rows move to `lost` with
`end_reason=harness_shutdown`. Otherwise the ticker can observe the killed task during the
shutdown window (`cont.Stop` runs only after an HTTP drain of up to 8 s) and start a turn
reporting a bogus failure.

The prompt names which case applied: *the command was interrupted and may be half-done*
versus *the command may still be running unsupervised*. A restart therefore converts
silence into a message — the thesis of this ADR applied to the harness's own mortality.

### 6. Four trigger channels

A perfect async primitive that is never called is decoration. The channels are not equals.

| Channel | Implemented at | Exists today? | Load-bearing? | Reaches |
|---------|----------------|---------------|---------------|---------|
| (a) Tool description | New `spec("watch", …)` in `allSpecs()` (`specs.go:5`); sent via `r.Tools.Specs()` (`internal/session/loop.go:262`) | Seam yes, spec no | **Yes** — the only channel that can create a watch | Model |
| (b) Standing advisory | New `append` to the `advisories` slice (`loop.go:407`, between `:440` and `:451`); prepended to the outbound prompt at `:457` | Seam yes | **Yes**, once a watch exists | Model |
| (c) Turn-end note | Final-answer branch (`loop.go:745-790`), before the `return` at `:790`, via `r.advisory` (`:1087`) | Seam yes | No | Operator only |
| (d) Sink | New dispatch into `sink.Manager.dispatch` (`internal/sink/manager.go:390`); new kind in `KnownKinds` (`internal/sink/event.go:28`) | **No** — sinks are turn-scoped | No — backstop | Operator only |

The channels only exist on turns that reach the tool loop. Turns routed to a subprocess
agent return early (`loop.go:243-247`), and `cap_tools=false` models receive no tool specs
(`loop.go:263`). See R7.

**(a) The tool description — creates the habit. Load-bearing.**
Sent with every model call in a turn (`loop.go:262`). It names the situations by example —
CI builds, deploys, long suites, remote copies — and names the competing default
(`"Do NOT use shell_execute + hope for this"`), because an unopposed competitor wins. This
channel is free, never drifts from vendor output, and is the only one that can conjure a
watch out of nothing.

The competitor is also in the system prompt, which today instructs the agent to use
`start_background_task` + `schedule_continuation` with `wait_for_task`
(`internal/session/session.go:295`; also `:280`, `:282`). Those lines must name `watch` in
the same change or the two instructions contradict each other. Steering by guidance has
precedent (`specs.go:98`, `session.go:280`, the timeout hints at
`internal/shellpolicy/policy.go:230,233`), but its effect on `watch` adoption is unmeasured.

**(b) The standing advisory — keeps an existing watch in view. Load-bearing once one exists.**
While a watch is `pending` or `running`, the loop appends one line to the `advisories`
slice. That slice carries the context-usage warning (`loop.go:408`) and the repeat-awareness
note (`:435`), and is prepended as a system message to a per-call copy of the prompt
(`:457`), so it never accumulates in history.

This is **not** `Runner.advisory` (`:1087`). That function publishes a UI event, a turn
step and a `harness_advisory` DB event, and the model never sees it. The
`[harness] image attachments omitted…` line at `:401` is one of those; a watch line sent
that way would be invisible to the agent.

```text
[harness] watch w_7c31 ("CI v0.4.29") still running.
Do not report this work as finished. You will be woken when it exits.
```

The line deliberately carries no elapsed time: it sits at the head of the prompt, and a
value that changes every round would defeat any provider-side prefix caching.

Coverage is "every model call, except an auto-compact round", because `:488-491` rebuild
`prompt` without re-prepending. That one-round gap is accepted. Repetition is the
mechanism here; a one-shot note would be useless. But this channel acts only after a watch
exists, so it **cannot** move the arm-rate off ten percent. Only (a) can.

**(c) The turn-end note — informational. Not load-bearing.**
When the model returns a final answer with no tool calls while a watch is live, the loop
emits an `r.advisory` and writes a `watch_open_at_turn_end` session event. Two limits:

- It reaches the operator, not the model, which has already finished answering. Making
  the model react would mean spending another round, which this ADR does not do.
- Ending a turn with a live watch is the **correct** use of the tool, so this fires on
  every well-behaved turn as well. Distinguishing "told the user it is pending" from
  "claimed it landed" would mean parsing prose, which §Context rejects. It is therefore a
  status note, not an alarm, and is not sent to sinks. It does not cover the other turn
  exits (`loop.go:349`, `:345`, `:370`, `:606`, `:802`).

Advisory only — **never a hard block**, because a harness that refuses to end a turn is a
harness an operator has to fight.

**(d) The sink — covers undelivered wakes. Backstop.**
A wake that could not be delivered goes to the operator's eyes as a `watch_undelivered`
sink delivery (ADR-0028). Sinks today are built per finished turn (`finishTurn`,
`internal/sink/manager.go:342`; kinds at `event.go:28`), so this needs a new non-turn
entry point. Successful wakes need nothing new: the woken turn is already classified
`continuation` (`manager.go:381`), subject to the existing filter (`filters.go:134`).

If the agent never called `watch` there is no row, and **no channel, including this one,
has anything to report**. That residue remains uncovered.

**Deliberately not built:** a pre-flight hook that sniffs `git push`, `gh workflow run`, or
`docker push` and nags. That is the rejected detector of §Context in a different costume — a
list of magic strings that rots and never covers the command nobody anticipated. Those
strings belong in the tool description as guidance to a reasoner, not in Go as a switch
statement.

### 7. Relationship to what already exists

| Subsystem | Relationship |
|-----------|--------------|
| `bgtask` | `watch` calls `Start`, with the same shell policy and the same session scope. **Not unchanged:** needs a live tail, a usable tail on noisy commands, and a cap that counts only running tasks (R3, R5). |
| `continuation` | Gains `Source` (`agent` \| `watch` \| `watch_boot`) and `WatchID`, a sibling `ScheduleWatch` so the two existing `Schedule` callers (`tools/shell.go:300`, `loop.go:865`) stay as they are, and single-job `Cancel` (R1, R4). `agent`-source jobs behave as before. |
| `main` `onFire` | Gains a `watch` branch that finalizes the row, renders the prompt, and does not drop on busy (R1, R2). Put the logic in `internal/watch` so it is testable without `main`. |
| `shellpolicy` | Identical checks before the command starts. No new bypass. |
| `cron` | Orthogonal. Cron is recurrence; watch is one-shot completion. A cron job that wants to check a build calls `watch` from inside its fire. |
| `sinks` | `watch_undelivered` kind and a non-turn dispatch; nothing on the happy path. |
| Events | New kinds `watch_started`, `watch_fired`, `watch_open_at_turn_end`, `watch_undelivered`. `session_events.kind` is free text (`db.go:289`), so no migration is needed. |

### 8. UI

A chip beside the existing continuation and bg-task chips, on the same `Notify` plumbing
(`main.go:528-536`) — no new SSE channel. `/api/sessions/{id}/pending`
(`internal/api/pending.go`) gains a `watches` list, and `taskRefsFromResult`
(`internal/session/pending.go:20`) gains a `watch` case:

```text
⏱ watching  CI v0.4.29 · 1m32s  ✕
```

Amber while `running`, green for `done`, red for `failed` with the code, grey struck-through
when `end_reason=cancelled`. Clicking opens the same tail view `watch status` returns, so
operator and agent read identical facts. `undeliverable` renders as a small warning row —
visible, not accusatory.

## Data model

`CurrentSchemaVersion` moves 14 → 15 (`internal/db/db.go:30`). `upgradeSchema` errors on a
version with no case (`db.go:139`), so this needs `case 14:` and a `migrateV14toV15`, plus
the version list in the comment above the constant.

```sql
CREATE TABLE watch_jobs (
    id              TEXT PRIMARY KEY,
    session_id      TEXT NOT NULL,
    label           TEXT,
    command         TEXT NOT NULL,
    cwd             TEXT,
    prompt          TEXT,           -- optional agent text, appended to the verdict
    task_id         TEXT,           -- bgtask handle; void after restart
    continuation_id TEXT,           -- continuation handle; void after restart
    status          TEXT NOT NULL,  -- pending|running|done|failed|lost|expired
    end_reason      TEXT,           -- see transition table
    exit_code       INTEGER,        -- NULL when the process never produced one
    output_tail     TEXT,           -- scrubbed, capped; survives restart for redelivery
    created_at      TEXT NOT NULL,
    deadline_at     TEXT NOT NULL,
    ended_at        TEXT,
    wake_outcome    TEXT,           -- NULL = unsettled | started | undeliverable:<why>
    note            TEXT            -- e.g. the cancel reason
);
CREATE INDEX idx_watch_jobs_session ON watch_jobs(session_id, status);
```

`output_tail` is required because bgtask output is in-memory: a wake redelivered after
restart would otherwise have no evidence. It must pass through `config.ScrubSecrets`
before being written. Tool
output is scrubbed at `Registry.Execute` (`registry.go:165`), but the wake path
(`main.go:550` → `PostContinuation`) does not go through it.

### State machine

```text
              ┌────────────► failed
              │                ▲
 pending ─────┼─► running ─────┼──► done
              │      │         │
              │      ├─────────┴──► expired
              ▼      ▼
             lost ◄──┘
```

| From | To | Trigger | `end_reason` |
|------|----|---------|--------------|
| — | `pending` | `create`: row inserted **before** `bg.Start`, so a crash here is detectable | — |
| `pending` | `running` | `bg.Start` and `ScheduleWatch` both succeeded; handles written | — |
| `pending` | `failed` | `bg.Start` returned an error (policy, cap, spawn), or `ScheduleWatch` failed (task is killed) | `start_error` |
| `pending` | `lost` | Boot reconcile | `harness_crash` |
| `running` | `done` | `Reason=task_done`, task `exited`, exit 0 | `exit_zero` |
| `running` | `failed` | `Reason=task_done`, non-zero exit | `exit_nonzero` |
| `running` | `failed` | Task `failed` with no exit code (`bgtask/manager.go:138`) | `wait_error` |
| `running` | `failed` | `cancel`, or the UI ✕ | `cancelled` |
| `running` | `failed` | Session closed (`main.go:639`) | `session_closed` |
| `running` | `expired` | `Reason=delay` with the task still running; task is then killed | `deadline` |
| `running` | `lost` | SIGTERM/SIGINT, before `KillAllRunning` | `harness_shutdown` |
| `running` | `lost` | Boot reconcile | `harness_crash` |

`done`, `failed`, `lost` and `expired` are terminal; there is no transition out of them.
Cancellation is `failed` with `end_reason=cancelled` rather than a seventh state, so the
agent sees a single non-success class and the UI differentiates on `end_reason`.

Every transition is a compare-and-set
(`UPDATE … WHERE id=? AND status=?`; zero rows affected means another path won). This is
how `cancel`, expiry and task exit are arbitrated, and it is required rather than tidy:
`Kill` does not cancel the task context (`bgtask/manager.go:183`), so a killed task reports
`exited` with exit code -1 instead of `killed` (the `ctx.Done` check at `:144-148` only
matches `KillAllRunning`). The row must already be terminal before the kill signal is sent,
or the kill would be misread as `exit_nonzero`.

Delivery is orthogonal to state. `wake_outcome` is written after the wake settles, and the
terminal transition is committed first, so a crash between the two causes a duplicate wake
and never a missing one.

## Consequences

**Bounded claim.** This turns a ten-percent habit into a durable record with a guaranteed
wake-up *conditional on the tool being chosen once*, and adds guidance toward that choice.
It does not claim to read the agent's intentions, and it does nothing at all when the
tool is not called.

| Scenario | Outcome |
|----------|---------|
| Agent calls `watch`, build fails | Woken with exit code and output tail. **Solved.** |
| Agent calls `watch`, then claims the work landed | Advisory every round warns against it; nothing prevents it. The wake still arrives and contradicts the claim. **Mitigated.** |
| Agent calls `watch`, harness restarts mid-build | Row survives; told the watch was `lost`. Silence is **solved**; the command is **not** resumed. |
| Agent calls `watch`, session busy at exit | Wake retried until the session is free (R2). **Solved.** |
| Agent calls `watch`, session later closed | No wake is possible. Recorded as `undeliverable`, sink delivery. **Recorded, not woken.** |
| Routed turn or `cap_tools=false` model | No `watch` tool, no advisory (R7). **Not covered.** |
| Agent never calls `watch` | No row exists; no channel has anything to report. **Not solved.** |

- **Cost per live watch is one advisory line per round.** With several watches that is
  repeated prose in every request. Mitigation: collapse to a single summary line above two
  live watches; never silently drop them.
- **A watch can outlive its usefulness.** `deadline_sec` defaults to 3600 and fails loudly;
  a cancelled watch requires a reason so the record explains itself later.
- **Boot reconcile can report `lost` for work that already finished** (the process died
  after the command exited but before the terminal row was written). The prompt says the
  outcome is unknown; a duplicate wake-up is cheaper than a silent one.
- **The metric is the judgment.** Log the watch-started-per-CI-push ratio and review it
  after a few weeks. The denominator comes from offline analysis of `session_events`; that
  is reporting, not a runtime detector. If the ratio does not move, channel (a) is not
  working, and that is the signal to revisit, not to add channels.
- **Not fixed here:** continuations remain in-memory for the `agent` source. Only `watch`
  rows are durable. Bringing agent-scheduled continuations onto the same table is the
  obvious follow-up and is deliberately out of scope, so the new table is not immediately
  load-bearing for pre-existing behaviour.

## Risks and open questions

The primitives compose, but "neither half is new" understates the work. Findings from
reading the code:

| # | Problem | Code | Required change |
|---|---------|------|-----------------|
| R1 | **The prompt is fixed at schedule time.** `Schedule` stores it and `onFire` posts it verbatim; the exit code and output do not exist yet. | `continuation/manager.go:112-120`, `main.go:550` | `Job.Source`/`WatchID` and a `watch` branch in `onFire` that finalizes the row and renders the prompt. **`continuation` cannot deliver a verdict without new code.** |
| R2 | **A fired continuation can still be dropped.** `Fired` is set before delivery; delivery then drops on load error, closed session, or 30 min of busy, and the retry goroutine is lost on restart. `CancelSession` never calls `onFire`. This breaks the §4 invariant as-is. | `manager.go:196`, `main.go:543,547,562,571,43`, `manager.go:134` | `watch` jobs retry without the 30 min cap; `wake_outcome` plus boot redelivery; `OnSessionClose` finalizes rows as `session_closed` and records `undeliverable`. |
| R3 | **There is no live tail, and the tail can be wrong.** Output is copied onto `Task` only after `Wait`, so `Get` returns empty strings while running (and `check_background_task` shows no tail until exit). `limitedWriter` keeps the **first** 512 KiB and discards the rest, so on noisy commands "last 40 lines" is not the end of the output. | `bgtask/manager.go:105-107,149-152,291-300`, `tools/shell.go:264-267` | Locked access to the live buffers, and a head+tail or ring buffer. `status` and the UI tail depend on this. |
| R4 | **No single-job cancel.** Only `CancelSession` exists. | `continuation/manager.go:134` | `Cancel(jobID)`. |
| R5 | **The per-session cap is a lifetime cap.** It counts every task id in `bySess`, and nothing deletes entries, so a session gets 8 background tasks until restart despite the "concurrent" comment and error text. Long-lived sessions are exactly where watches accumulate. Existing bug, made worse by this feature. | `bgtask/manager.go:61,94-96` | Count only `StatusRunning`. |
| R6 | **Tool output re-enters as a user-role turn.** The output tail lands in a `[scheduled continuation]` message rather than a tool result, so CI output carrying injected instructions arrives with more authority than it should, and unscrubbed unless handled. | `loop.go:62-63` | Scrub (§Data model), fence, and label as command output. Reduces the risk; does not eliminate it. |
| R7 | **Routed and tool-less turns are out of reach.** Routed turns return before the tool loop, so a subprocess agent cannot call `watch` and sees no advisory. A watch's wake always runs on the Marble model, since continuations skip routing. | `loop.go:241-247,263` | None proposed. Stated scope limit. |

Open questions:

1. **Should `watch` create fail or fall back in limp mode?** This ADR says fail (§3). The
   alternative is an in-memory watch with an explicit "not durable" warning.
2. **Closed sessions.** Cron reopens a closed session to deliver (`main.go:609-612`); this
   ADR does not, because closing is an operator decision and also kills the command. If
   operators expect otherwise, `session_closed` could wake via reopen.
3. **Is (c) worth building?** Given it fires on correct use and cannot reach the model, it
   may reduce to the UI chip. It is cheap, but drop it if the event proves to be noise.
4. **Expiry kills the command.** That is right for a hung watcher and wrong for a
   `gh run watch` whose underlying CI run carries on regardless. The wake prompt should
   say that only the local command was stopped.

## Rollout

1. **Prerequisites** — R3 (live, usable tail) and R5 (cap) in `bgtask`; R4 and the
   `Source`/`WatchID`/`ScheduleWatch` additions in `continuation`.
2. **Schema v15** — `watch_jobs`, migration, state machine.
3. **`watch` tool and fire path** — `create/status/list/cancel`, the `onFire` branch, boot
   reconcile, and SIGTERM handling. Update the system prompt (`session.go:280,282,295`) in
   the same change.
4. **Advisory (b)** — one `append` in `loop.go` beside the existing advisories, fed by a
   `LiveWatches(sessionID)` query.
5. **Turn-end note (c)** — advisory plus `watch_open_at_turn_end` event on the final-answer
   branch.
6. **Sink (d), UI chip** and the undeliverable warning row.
7. **Measure** the ratio.

Steps 1–4 are useful without 5–6; nothing here blocks a release.

### Testing

**Unit — state machine** (`internal/watch`, table-driven): each legal transition in
§Data model succeeds; each illegal one (any move out of a terminal state, `pending` →
`done`, and so on) affects zero rows; `cancel` racing task exit leaves exactly one
terminal state, with `end_reason=cancelled` when the cancel wins even though bgtask
reports `exited`/-1.

**Unit — reconcile:**

- Seeded `pending` and `running` rows become `lost` with `harness_crash`, and each
  schedules exactly one wake.
- A terminal row with `wake_outcome IS NULL` is redelivered; one with `wake_outcome` set
  is left alone.
- A row for a closed session becomes `undeliverable` and is not retried.
- Running reconcile twice produces no second wake.

**Unit — seams:** `create` yields one row and one continuation whose `WaitTask` matches
`task_id`; the advisory line is in the prompt copy for a live watch and absent otherwise;
`output_tail` is scrubbed; a `bgtask` test asserts a finished task frees a cap slot.

**End-to-end** — real `bgtask.Manager`, real `continuation.Manager`, temporary database, a
captured wake function in place of `PostContinuation`:

```text
watch create  command='sleep 1; echo boom >&2; exit 3'  deadline_sec=30
```

Assert that `create` returns while the command is still running with the row `running`;
then, within a few seconds, the row is `failed` / `exit_nonzero` / `exit_code=3`,
`output_tail` contains `boom`, exactly one wake was captured, its text contains
`exit 3` and `boom`, and `wake_outcome=started`.

## Alternatives rejected

| Alternative | Why not |
|-------------|---------|
| Regex/detector over `shell_execute` output | Fatal coverage hole (`git push` prints nothing); rots on vendor drift. See §Context. |
| Pre-flight command sniffer for known CI commands | Same detector, different costume. Magic strings in Go, uncovered by the command nobody anticipated. |
| Rely on `schedule_continuation` discipline | The status quo, measured at 1 in 10. Voluntary affordances with no representation of "unfinished" cannot fix this. |
| Hard-block turn end while a watch is live | Ending the turn is the correct behaviour with a live watch, and an operator fights a harness that will not let a turn close. |
| Inject (c) back into the model for one more round | Spends a model round on every correct use, and judging the final answer would require parsing prose. |
| In-memory watches only | Disqualified by the restart case, which is exactly when the harness is being worked on. |
