# ADR-0015: Cron Jobs — Durable Schedules, UI Modal, Agent CRUD

| Field | Value |
|-------|--------|
| **Status** | Accepted / Implemented |
| **Date** | 2026-07-19 |
| **Deciders** | Project owner |
| **Tags** | cron, scheduler, ui, tools, daemon, sqlite |
| **Extends** | ADR-0002/0003 (memory/DB daemon), ADR-0005 (`schedule_continuation` / BG), ADR-0007 (Settings modal chrome), ADR-0010 (turn busy / stop) |
| **Answers** | `adr/0015-answers.json` (`2026-07-20T00:06:38.005Z`) |

## Context

Marble already has **one-shot delayed resume** via `schedule_continuation` (in-memory timers; max ~24h; cancel on session close). That is the right primitive for “finish this after the build ends” or “nudge me in 10 minutes.”

Operators also want **durable, recurring, inspectable schedules**:

- “Every morning, summarize overnight logs in session X.”
- “Every Monday 09:00, open a hygiene checklist turn.”
- “Every 15 minutes, poll a status endpoint and report if unhealthy.”

Agents should be able to **create/list/update/delete** those jobs during a turn. Humans should manage the same jobs from a **clock-icon modal** without leaving the chat UI.

### Gap vs `schedule_continuation`

| | `schedule_continuation` | **Cron (this ADR)** |
|--|-------------------------|---------------------|
| Lifetime | One fire | Recurring (or optional finite) |
| Persist | In-memory (restart loses) | SQLite (survive restart) |
| Scope | Bound to one session turn flow | Global catalog; targets a session (create if missing) |
| UI | Optional chip (limited) | Full modal CRUD + history |
| Agent | `schedule_continuation` only | CRUD tool suite |
| Busy / overlap | Retry briefly then drop | Skip if busy; advance schedule |

Keep both. Cron is not a replacement for wait-for-task one-shots (**Q15**).

## Goals

1. **Durable cron jobs** stored in SQLite under `$MEMORY` (**Q9**).  
2. **Clock icon** in the session sidebar opens a **modal** for list/create/edit/enable/disable/delete + recent runs (**Q1**, **Q2**).  
3. **Agent tools** for full **CRUD** (and list/get/run) on the same store (**Q10**, **Q13**, **Q18**).  
4. On fire: inject a prompt into a **target session** and start a turn; if the session is missing, **create a new session** and bind the job (**Q3**, **Q4**, **Q19**).  
5. Survive harness restart; limp-mode pauses fires (**Q14**).  
6. Predictable caps so a runaway agent cannot schedule infinite load (**Q11**).

## Non-goals (v1)

- Distributed multi-host scheduler / leader election  
- OS `crontab` integration or systemd timers  
- Arbitrary shell-only cron without a session turn (defer: shell action type)  
- Calendar UI / RRULE / complex holiday calendars  
- Per-user multi-tenant ACL (single-operator harness)  
- Replacing `schedule_continuation` or BG tasks  

## Decision

### 1. Job model

A **cron job** is a durable row:

```text
id              short id (same family as session ids)
name            human label (required)
enabled         bool
schedule_kind   "cron" | "interval"          # Q5
cron_expr       5-field cron when kind=cron
interval_sec    ≥ 60 when kind=interval      # Q11
timezone        IANA; default "Local"        # Q6
session_id      preferred target (may be empty or stale)
prompt          text injected at fire (required)
created_by      "ui" | "agent" | "system"
created_at / updated_at
next_run_at     computed, indexed
last_run_at / last_status / last_error
run_count
max_runs        optional finite cap (null = forever)
```

**v1 fire action (**Q4**):** `session_prompt` only — inject `prompt` with prefix `[cron:<id> <name>]` (**Q20**) via the continuation inject path, then start a turn if the session is not busy.

**Session targeting (**Q3**, **Q17**, **Q19**):**

1. If `session_id` refers to an **existing** session → use it.  
2. If missing / unknown / deleted → **create a new user session**, set the job’s `session_id` to the new id (persist), inject prompt, start turn.  
3. Title for auto-created sessions: e.g. `cron: <job name>` (implementer detail).  
4. No auto-disable for missing session (creation replaces that path).

### 2. Schedule formats (**Q5**, **Q6**)

| Kind | Spec | Notes |
|------|------|--------|
| `cron` | Standard **5-field** (`min hour dom mon dow`) | Well-tested parser (e.g. robfig/cron v3). No seconds field in v1. |
| `interval` | `interval_sec` ≥ **60** | Advance from last scheduled tick; no catch-up storm. |

**Timezone (**Q6**):** store IANA string; default **`Local`** (host). UI shows next run in that zone.

### 3. Scheduler daemon

- In-process ticker (same family as continuation / memory daemon), e.g. every **1–5s**.  
- On tick: claim due jobs (`next_run_at <= now` AND `enabled`), compute next, write run row, invoke fire.  
- Single-writer harness (`marble.lock`) — no multi-writer v1.  
- On harness start: reload enabled jobs; recompute stale `next_run_at`.

### 4. Busy / missed / limp policies (**Q7**, **Q8**, **Q14**)

| Situation | Behavior |
|-----------|----------|
| Target session **busy** | **Skip**; log `skipped_busy`; advance `next_run_at` |
| Target session **missing** | **Create new session**, rebind job `session_id`, fire into it |
| Harness **down** over a fire time | **No catch-up**; next future slot from now |
| **Limp / model down** | **Pause fires**; log `skipped_limp`; keep jobs |
| Turn **Stop** | Cancels in-flight turn only; jobs unchanged |

### 5. Persistence (**Q9**, **Q12**)

SQLite in `marble.db` (schema bump):

```sql
CREATE TABLE cron_jobs (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  schedule_kind TEXT NOT NULL,        -- cron | interval
  cron_expr TEXT,
  interval_sec INTEGER,
  timezone TEXT NOT NULL DEFAULT 'Local',
  session_id TEXT,                    -- may be empty; filled/rebound on fire
  prompt TEXT NOT NULL,
  created_by TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  next_run_at TEXT,
  last_run_at TEXT,
  last_status TEXT,
  last_error TEXT,
  run_count INTEGER NOT NULL DEFAULT 0,
  max_runs INTEGER
);

CREATE TABLE cron_runs (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL,
  scheduled_at TEXT NOT NULL,
  started_at TEXT,
  finished_at TEXT,
  status TEXT NOT NULL,  -- ok | skipped_busy | skipped_limp | error | created_session | …
  error TEXT,
  session_id TEXT,
  FOREIGN KEY(job_id) REFERENCES cron_jobs(id) ON DELETE CASCADE
);

CREATE INDEX idx_cron_jobs_next ON cron_jobs(enabled, next_run_at);
CREATE INDEX idx_cron_runs_job ON cron_runs(job_id, scheduled_at DESC);
```

**Prune (**Q12**):** keep last **50** runs per job and/or drop older than **30 days**.

### 6. Caps (**Q11**)

| Cap | Value |
|-----|--------|
| Max jobs (enabled + disabled) | **50** |
| Max concurrent cron-started turns | **3** (global) |
| Min interval | **60s** |
| Max prompt chars | **16 KiB** |
| Max name length | **80** |

### 7. UI — clock modal (**Q1**, **Q2**, **Q16**, **Q18**)

**Entry:** sidebar **🕐** icon only; tooltip “Cron jobs”.

**Order:**  
`Sessions · 📁 · 🚀 · 👁 · 🕐 · ⚙ · +`

**Modal title:** “Cron jobs”

```
┌ Cron jobs ────────────────────────── [+ New] [↻] [×] ┐
│ Durable schedules inject a prompt + start a turn.    │
│ Missing session → create new session and rebind.     │
│                                                      │
│ ● Morning summary     cron 0 8 * * *   → 0wbkaz…    │
│   next in 3h · last ok · [Edit] [Disable] [Run now]  │
│ ○ Health poll         every 15m        disabled      │
│                                                      │
│ ── Recent runs ──────────────────────────────────── │
│ 08:00 morning-summary  ok / created_session …        │
└──────────────────────────────────────────────────────┘
```

**Create / edit:** name, enabled, session target (optional / picker), schedule tabs Cron | Interval, timezone, prompt, optional max_runs, **next 5 fire times** preview, Save. Confirm delete / dirty discard.

**Run now (**Q18**):** fire immediately; schedule phase unchanged.

### 8. HTTP API

| Method | Path | Notes |
|--------|------|--------|
| `GET` | `/api/cron/jobs` | List (+ `?enabled=1`) |
| `GET` | `/api/cron/jobs/{id}` | Job + recent runs |
| `POST` | `/api/cron/jobs` | Create |
| `PUT` | `/api/cron/jobs/{id}` | Update (partial OK) |
| `DELETE` | `/api/cron/jobs/{id}` | Hard delete |
| `POST` | `/api/cron/jobs/{id}/run` | Run now |
| `GET` | `/api/cron/runs` | Global recent runs |

Validate cron expr / interval on write; return `next_run_at` and preview times.

### 9. Agent tools (**Q10**, **Q13**, **Q18**)

| Tool | Purpose |
|------|---------|
| `cron_list` | List jobs (filters: enabled, session_id) |
| `cron_get` | Job detail + last runs |
| `cron_create` | Create |
| `cron_update` | Patch by id |
| `cron_delete` | Delete by id |
| `cron_run` | Run now |

```json
{
  "name": "morning-summary",
  "enabled": true,
  "schedule_kind": "cron",
  "cron_expr": "0 8 * * *",
  "interval_sec": null,
  "timezone": "America/New_York",
  "session_id": "0wbkazh96k",
  "prompt": "Summarize overnight activity and open issues.",
  "max_runs": null
}
```

`session_id` optional on create: empty → first fire creates a session and binds it.

**System prompt nudge:**

```text
Cron: use cron_* tools for durable recurring schedules (survive restarts).
Use schedule_continuation for one-shot delayed resume or wait-for-background-task.
Prefer interval ≥ 60s; target a session_id when you want a known thread, or omit to let the harness create one on first fire.
```

UI and agent share the same store (**Q13**).

### 10. Package layout

```
internal/cron/
  manager.go      # load, tick, claim, fire callback
  schedule.go     # parse cron/interval, next times
  store.go        # SQLite via internal/db
internal/db/cron.go
internal/api/cron.go
internal/tools/cron.go + specs
internal/web/static/cron.js + modal in index.html
```

Fire callback: resolve-or-create session → inject → start turn (reuse continuation inject path).

## Decisions locked (Q1–Q20)

| ID | Decision |
|----|----------|
| **Q1** | **🕐** icon only; order `Sessions · 📁 · 🚀 · 👁 · 🕐 · ⚙ · +` |
| **Q2** | **Modal** (settings / explorer / soul chrome) |
| **Q3** | **Global** catalog; target existing `session_id`, or **create session** if none |
| **Q4** | Inject prompt + start turn on target session only (v1) |
| **Q5** | Both **5-field cron** and **interval_sec** (min 60s) |
| **Q6** | Default **Local** (host); per-job IANA override |
| **Q7** | Busy → **skip** + `skipped_busy`; advance `next_run_at` |
| **Q8** | **No catch-up**; next future slot from now after downtime |
| **Q9** | SQLite **`cron_jobs` + `cron_runs`** in `marble.db` |
| **Q10** | `cron_list`, `cron_get`, `cron_create`, `cron_update`, `cron_delete`, `cron_run` |
| **Q11** | **50** jobs · **3** concurrent cron turns · min interval **60s** |
| **Q12** | Last **50** runs/job and/or **30 days** |
| **Q13** | UI + agent full CRUD on the same store |
| **Q14** | Limp → **pause fires**, log `skipped_limp`; keep jobs |
| **Q15** | Keep **`schedule_continuation`** for one-shot / wait-for-task |
| **Q16** | Show next **5** fire times on create/edit |
| **Q17** | Missing target → **create session** (not auto-disable); optional `max_runs` still OK |
| **Q18** | **Run now** from UI and `cron_run` |
| **Q19** | Target missing at fire → **create new session**, rebind, fire |
| **Q20** | Prefix: **`[cron:<id> <name>]`** |

## Consequences

### Positive

- Operators control long-lived automation in one modal.  
- Agents set up recurring ops without shell crontab.  
- Missing session no longer silently drops work — a new thread is created.  
- Clear split: continuation = ephemeral one-shot; cron = durable.

### Trade-offs

- Auto-creating sessions can accumulate orphan threads if jobs keep losing targets — mitigate with clear `cron: <name>` titles and UI visibility.  
- No catch-up means downtime gaps are silent except run log.  
- Global concurrent fire cap may delay some jobs under load.

### Risks

| Risk | Mitigation |
|------|------------|
| Agent creates noisy every-minute jobs | Min 60s; max 50 jobs; UI visible |
| Runaway turns from cron | Concurrent fire cap 3; tool-round hard stops |
| Session spam from missing targets | Rebind job to new session; show in modal |
| Timezone confusion | Store IANA; preview next fires |
| DB growth from runs | Prune 50/job + 30d |

## Acceptance criteria

- [x] Open questions answered (`0015-answers.json`)  
- [x] ADR locked table + review HTML updated  
- [x] Schema + manager + API + tools + clock modal implemented  
- [x] Restart preserves jobs; fire injects turn; busy/skip/limp + create-session policies work  
- [x] Tests: schedule next, CRUD, manager fire + schema v2  

## Implementation order

1. DB schema + store + schedule next-time helpers  
2. `internal/cron` manager + fire callback (resolve-or-create session)  
3. REST `/api/cron/*`  
4. Agent `cron_*` tools + system prompt  
5. UI: 🕐 + modal + form + run history + next-5 preview + run now  
6. Tests + manual smoke (interval job, busy skip, missing session create, restart)  

## Changelog

| Date | Change |
|------|--------|
| 2026-07-19 | Proposed |
| 2026-07-20 | Locked Q1–Q20 from `0015-answers.json`; missing session → create + rebind (Q3/Q17/Q19) |
| 2026-07-20 | Implemented: schema v2, `internal/cron`, API, tools, 🕐 modal |

## References

- `adr/0015-answers.json`  
- ADR-0005 `schedule_continuation` / BG tasks  
- ADR-0003 SQLite memory  
- ADR-0007 / 0013 modal chrome  
- ADR-0010 turn busy / stop  
- Common cron parsers (e.g. robfig/cron v3)  
