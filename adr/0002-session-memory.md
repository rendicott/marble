# ADR-0002: Session Management, History, and Memory

| Field | Value |
|-------|--------|
| **Status** | Accepted / Implemented |
| **Date** | 2026-07-16 |
| **Deciders** | Project owner |
| **Tags** | sessions, memory, persistence, daemon, learning |
| **Supersedes / extends** | ADR-0001 (in-memory-only sessions) |

## Context

ADR-0001 delivered the agent **inner loop** and a multi-session web UI, but session state is **process-local**: restart clears everything. Marble’s purpose is broader than a disposable coding chat:

> A **general-purpose harness** for sysadmin work, personal automation, and **learning over time**.

That requires:

1. **Stable session identity** that is short enough to type, cite, and put in filenames.
2. **Durable transcripts** so work survives restarts and can be audited later.
3. A **memory root** separate from the tool workspace (ops secrets, notes, and history should not be mixed into the project the agent is editing).
4. A **background persistence loop** so operators do not have to think about save.
5. **Daily compaction** so long-term recall stays cheap and human-readable, without rewriting empty days.

This ADR defines session lifecycle, on-disk layout, the daemon, close semantics, and daily compaction. It does **not** yet define vector RAG, skill distillation, or cross-session tool memory graphs (future ADRs).

## Decision

1. Add a required-or-default **`--memory`** directory (may live **outside** `--workspace`).
2. Under that root, maintain:
   - `session/<session_id>.md` — full (or checkpointed) session transcripts
   - `daily/YYYY-MM-DD.md` — compacted rollup for days with activity
3. Assign each session a **short unique id** (not a full GUID).
4. Run an in-process **background daemon** that flushes dirty sessions to disk every **5 minutes**.
5. On **session close** (UI: right-click → Close), flush immediately, mark closed, and free the in-memory session.
6. Once per calendar day (and on shutdown if needed), **compact** that day’s session activity into `daily/…` **only if** new activity occurred that day.

## Product framing

| Mode | How memory helps |
|------|------------------|
| Sysadmin | Past runbooks, command outcomes, host notes survive across reboots of the harness |
| Personal automation | Recurring tasks can reference yesterday’s decisions and file paths |
| Learn over time | Daily digests become the substrate for later “what did we learn?” tooling |

Workspace (`--workspace`) remains the **agent’s tool jail** for `file_read` / `file_write` / `list_files`.  
Memory (`--memory`) is the **harness’s private store**; tools do not write there unless a future tool deliberately exposes it.

## Session IDs

### Requirements

- Unique within a memory root (and ideally collision-resistant across machines).
- Short enough for UI, logs, and filenames (not 32-char hex GUIDs).
- Filesystem-safe (`[a-z0-9_-]` preferred).

### Format (v1)

```
<base32-crockford-time><base32-random>
```

Concrete choice:

| Part | Size | Notes |
|------|------|--------|
| Time prefix | 6 chars | Crockford base32 of unix minute or similar → roughly sortable |
| Random suffix | 4 chars | ~20 bits of entropy |

**Example:** `k7m2q9-a3f2` or compact `k7m2q9a3f2` (10–11 chars).

Implementation may use: 4 bytes time + 2–3 bytes crypto/rand, encoded Crockford base32, lowercase, **no padding**.

**Not used:** UUID/GUID, 16-byte hex.

On collision (extremely rare): regenerate random suffix.

### Lifecycle states

| State | Meaning |
|-------|---------|
| `active` | Loaded in registry; accepting messages |
| `dirty` | Has unpersisted transcript changes |
| `closed` | User closed; flushed; removed from live registry |
| `archived` | Optional later; closed + compacted into daily |

## On-disk layout

`--memory` points at a user-chosen root. Marble owns a `memory/` subtree inside it:

```
$MEMORY/                          # --memory argument
├── session/
    │   ├── k7m2q9a3f2.md         # one file per session
    │   └── p1x4c8b2n0.md
    ├── daily/
    │   ├── 2026-07-15.md         # only days with activity
    │   └── 2026-07-16.md
    └── state.json                # optional: daemon watermarks, last compact day
```

### Why leaf layout?

The flag is the **data home** (may already contain other app data later). The nested `memory/` package name keeps session/daily/state namespaced and matches the mental model “this folder is Marble memory.”

If the path feels redundant in practice, a later ADR may flatten to `$MEMORY/session` — for now nest under `memory/`.

**Decision:** `--memory` is the leaf. Paths are `$MEMORY/session/…` and `$MEMORY/daily/…` with no extra nesting.

### Session file format (`session/<id>.md`)

Human-readable Markdown with a YAML front matter block for machine fields:

```markdown
---
id: k7m2q9a3f2
title: "nginx renew certs"
created_at: 2026-07-16T20:01:02-04:00
updated_at: 2026-07-16T21:15:44-04:00
closed_at: null
status: active
message_count: 12
workspace: ~/src/marble
model: Qwen/Qwen3.5-122B-A10B-FP8
---

# Session k7m2q9a3f2 — nginx renew certs

## 2026-07-16 20:01:02 · user
Renew the cert on edge-01 and reload nginx.

## 2026-07-16 20:01:08 · tool · list_files
path=.
(result excerpt…)

## 2026-07-16 20:01:40 · assistant
Here’s what I did…
```

Rules:

- Append or rewrite whole file on flush (v1: **rewrite full transcript** for simplicity).
- Tool results may be truncated in the persisted form if huge (note original length).
- Binary/secrets: do not special-case yet; treat as plain text (future redaction ADR).

### Daily file format (`daily/YYYY-MM-DD.md`)

One file per **local calendar day** (timezone: host local unless `--memory-timezone` added later).

```markdown
---
date: 2026-07-16
generated_at: 2026-07-16T23:59:01-04:00
sessions:
  - id: k7m2q9a3f2
    title: nginx renew certs
    messages: 12
  - id: p1x4c8b2n0
    title: backup restic
    messages: 4
---

# Daily log — 2026-07-16

## k7m2q9a3f2 — nginx renew certs
- Goal / opener: Renew the cert on edge-01…
- Outcome: certificate renewed; nginx reloaded
- Paths / hosts touched: /etc/nginx/…, edge-01
- Open threads: none

## p1x4c8b2n0 — backup restic
…
```

Compaction is a **summary**, not a full replay. Full fidelity stays in `session/*.md`.

## Background daemon

Runs inside `marble-harness` (same process), started at boot:

| Concern | v1 decision |
|---------|-------------|
| Interval | **5 minutes** (configurable later: `--persist-interval`) |
| Scope | All sessions with `dirty == true` |
| Action | Write/update `session/<id>.md` |
| Atomicity | Write temp file + rename |
| Errors | Log; retry next tick; surface count on `/api/health` |
| Shutdown | Flush all dirty sessions before exit |

### Dirty tracking

A session becomes dirty when:

- User message accepted
- Assistant message appended
- Tool result appended
- Title changes

Clear dirty after successful flush. Periodic tick is a no-op if nothing dirty.

### Startup hydration (v1 scope)

| Behavior | v1 |
|----------|----|
| List sessions from disk on boot | **Yes** — index `session/*.md` front matter |
| Auto-resume full history into agent context | **Optional load on open** — opening a session in UI loads transcript into memory registry |
| Closed sessions | Appear in UI history list (later filter); not auto-active |

Minimum for this ADR:

1. Persist live sessions on interval + close.
2. On boot, **discover** session files for the left-pane list (title, ids, updated_at).
3. Selecting a session **loads** messages into the live registry if not already loaded.

## Session close (UI + API)

### UI

- **Right-click** (long-press on mobile — follow-up UX) on a session row → menu:
  - **Close session** — flush, mark `status: closed`, set `closed_at`, remove from live agent registry (or mark inactive)
  - (Future) Rename, Export, Delete

### API

| Method | Path | Behavior |
|--------|------|----------|
| `POST` | `/api/sessions/{id}/close` | Flush transcript; mark closed; drop live turn state |
| `DELETE` | `/api/sessions/{id}` | Optional later — delete disk file (not in v1 required set) |

Close always triggers an **immediate persist**, not waiting for the 5-minute tick.

## Daily compaction

### When

- Background job: after session flush tick, or a dedicated daily ticker (e.g. every hour), check:
  - `today` has activity (`dirty` was true today, or any session `updated_at` on this local date), **and**
  - daily file missing or `generated_at` older than latest session activity for that day → regenerate.
- **If no new activity that calendar day → do nothing** (do not touch/create empty daily files).
- On process shutdown: compact today if dirty activity occurred.

### How (v1 algorithm)

Without calling the LLM initially (cheaper, deterministic):

1. Collect all sessions with `updated_at` on that date (from front matter or live registry).
2. For each, extract: title, first user message, last assistant message, tool names used, file paths mentioned (simple regex).
3. Write structured Markdown sections.

**Later (not blocking this ADR):** optional LLM summarization using the same local model, rate-limited.

### Idempotency

Daily files are regenerated from session files for that date, not append-only, so re-running compaction is safe.

## CLI

| Flag | Default | Meaning |
|------|---------|---------|
| `--memory` | `~/.marble` (proposed) | Data home; may be outside workspace |
| `--persist-interval` | `5m` | Daemon flush interval |
| `--workspace` | `.` | Unchanged from ADR-0001 — tool jail |

Example:

```bash
marble-harness \
  --workspace /srv/edge-01 \
  --memory /var/lib/marble \
  --addr :8080
```

On first run, create:

```
/var/lib/marble/session/
/var/lib/marble/daily/
```

## Concurrency & safety

- Single writer daemon + request handlers coordinate via session mutex + a small `persist` lock per session id.
- No multi-process writers assumed for v1 (one harness per memory root).
- Document: running two harnesses on the same `--memory` is undefined.

## Relationship to learning over time

This ADR lays **durable substrate**:

| Layer | Role |
|-------|------|
| `session/*.md` | Ground truth transcript |
| `daily/*.md` | Human + machine digest for a day |
| Future | Skills, facts, host inventory derived from daily/session |

Agent tools that **read memory** (e.g. `memory_search`) are a later ADR so the model can intentionally recall past days.

## Consequences

### Positive

- Restarts no longer erase work.
- Short ids fit UI and filenames.
- Memory and workspace separation matches sysadmin safety.
- 5-minute daemon + close flush is simple and robust.
- Daily compaction scales “learn over time” without rereading every raw session forever.

### Trade-offs

- Full rewrite of session Markdown is simple but can be heavy for huge transcripts (optimize later).
- Deterministic daily summaries are shallower than LLM summaries.
- Leaf layout keeps paths short and matches user mental model.
- Right-click close needs a mobile equivalent (long-press).

### Risks

| Risk | Mitigation |
|------|------------|
| Disk full / permission errors | Health flag + UI banner; keep in-memory truth |
| Partial write corruption | Temp + rename |
| Clock skew on daily boundaries | Use host local time; document |
| Secrets landing in transcripts | Future redaction ADR; careful defaults |

## Out of scope

- Multi-user auth / shared memory ACLs  
- Encrypted-at-rest memory  
- Vector index / embeddings  
- Automatic skill extraction  
- Cross-host memory sync  
- Deleting sessions from UI  
- Streaming token persistence mid-generation (flush after complete messages only)

## Implementation order (after acceptance)

1. `--memory` bootstrap + directory layout  
2. Short session id generator; migrate away from 32-char hex  
3. Session Markdown serializer/loader  
4. Dirty flags + 5-minute daemon + shutdown flush  
5. `POST /api/sessions/{id}/close` + UI right-click menu  
6. Boot-time session index + load on select  
7. Daily compaction job + skip empty days  
8. Health fields: `last_persist_ok`, `dirty_sessions`, `memory_path`

## Acceptance criteria

- [ ] `--memory` may point outside `--workspace`  
- [ ] Layout `session/*.md` and `daily/YYYY-MM-DD.md` agreed  
- [ ] Session ids short (≈10 chars), not GUIDs  
- [ ] 5-minute background persist agreed  
- [ ] Close (right-click) forces immediate flush  
- [ ] Daily compaction only when the day had activity  
- [ ] `--memory` is the leaf (`session/`, `daily/` directly under it)  
- [ ] Ready to implement after review iterations  

## References

- ADR-0001: Harness Inner Loop  
- Current in-memory registry: `internal/session`  
- Open goal: general-purpose sysadmin + personal automation + learning  
