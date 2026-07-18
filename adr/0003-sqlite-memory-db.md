# ADR-0003: SQLite Database in Memory Directory

| Field | Value |
|-------|--------|
| **Status** | Accepted / Implemented |
| **Date** | 2026-07-17 |
| **Deciders** | Project owner |
| **Tags** | database, sqlite, sessions, diagnostics, daemon, versioning, limp-mode |
| **Extends** | ADR-0002 (file-based session/daily memory) |

## Context

ADR-0002 established a durable **memory leaf** (`--memory`) with Markdown transcripts (`session/*.md`) and daily digests (`daily/*.md`). Structured session state, diagnostics, and daemon health need a **persistent, queryable store** co-located with that leaf.

**Files remain first-class.** The database is **supplemental** dual-write.

## Decision summary

| Area | Decision |
|------|----------|
| DB path | `$MEMORY/marble.db` — create if missing |
| Dual-write | MD primary for transcripts; DB index + diagnostics + bodies until purge |
| Large payloads | Truncate inline; spill to `$MEMORY/blobs/`; purge per settings |
| Tokens | Both `*_reported` and `*_est` |
| Schema mismatch | Auto-migrate only with coded steps; else **limp mode** |
| Close | `status=closed`; keep DB rows until prune |
| Prune closed | Default **4 days** after `closed_at` (DB setting); **keep** `.md` files |
| Blobs | Default max age **4 days**; on session prune delete that session’s blobs immediately; otherwise ref-aware or age cleanup |
| Lock | One harness per memory/DB; fail if locked |
| Config | CLI for model/base-url; DB does not store base-url |
| Timestamps | UTC RFC3339 `Z` only in DB |
| Inline spill threshold | Default **32 KiB** (`db_inline_max_bytes`) |
| Prune cadence | Every daemon sweep |
| UI closed sessions | Hidden by default; “show closed” toggle |
| Retention settings | Seeded DB defaults for v1; API/UI later (no extra CLI flags) |
| Limp | Chat + tools + MD sessions + list from MD; **no DB writes**; keep MD flush daemon |

## Layout

```
$MEMORY/                          # --memory leaf
├── marble.db                     # SQLite (create if missing)
├── marble.lock                   # optional PID lock file (plus SQLite lock)
├── session/<id>.md               # first-class human transcripts
├── daily/YYYY-MM-DD.md
└── blobs/                        # spilled large payloads
    └── <blob_id>
```

## Roles: files vs DB

| Concern | First-class home | DB role |
|---------|------------------|---------|
| Transcript text | `session/*.md` | Full message bodies in `session_events` until **purge** |
| Daily digests | `daily/*.md` | Not required in DB for v1 |
| Session list / status | MD front matter fallback (limp) | `sessions` table |
| Tool/debug stats | — | `session_events` |
| Daemon health | — | `daemon_state` |
| Tunables | — | `settings` |
| Model base URL | **CLI only** | **Never** |
| Model id | CLI / runtime | Optional on session/events for diagnostics |

If DB is unavailable or limp: Markdown persistence and chat continue.

## Payload policy

| Rule | Detail |
|------|--------|
| Inline cap | `settings.db_inline_max_bytes` default **32768** (32 KiB) |
| Over cap | Write full content to `$MEMORY/blobs/<blob_id>`; DB stores truncated preview + `blob_id`, `content_truncated=1` |
| Blob max age | `settings.blob_max_age_days` default **4** |
| Blob cleanup | Prefer **unreferenced** blobs; also purge by age when unreferenced. Never delete a blob still referenced by a non-purged session’s events |
| On session **prune** | Delete that session’s DB events **and** its blobs **immediately** (even if &lt; 4 days old) |
| Markdown | Unaffected by inline cap; ADR-0002 rules apply |

## Token accounting

| Column | Meaning |
|--------|---------|
| `tokens_in_reported` | Provider usage input (NULL if absent) |
| `tokens_out_reported` | Provider usage output (NULL if absent) |
| `tokens_in_est` | Harness estimate |
| `tokens_out_est` | Harness estimate |

## Schema (v1)

### `schema_meta` (singleton `id=1`)

| Column | Type | Notes |
|--------|------|--------|
| `schema_version` | INTEGER | `1` at create |
| `created_at` / `updated_at` | TEXT | UTC |
| `created_by_harness` | TEXT | optional |

### `settings`

| Key | Default | Meaning |
|-----|---------|---------|
| `blob_max_age_days` | `4` | Age-based blob eligibility for purge |
| `closed_session_max_age_days` | `4` | Prune closed sessions after this many days |
| `db_inline_max_bytes` | `32768` | Spill threshold |

Columns: `key` TEXT PK, `value` TEXT, `updated_at` TEXT UTC.

### `sessions`

| Column | Type | Notes |
|--------|------|--------|
| `id` | TEXT PK | short id |
| `title` | TEXT | |
| `status` | TEXT | `active` / `closed` |
| `created_at` / `updated_at` | TEXT | UTC |
| `closed_at` | TEXT NULL | UTC |
| `message_count` | INTEGER | |
| `dirty` | INTEGER | MD flush dirty flag |
| `workspace` | TEXT | path only |
| `model` | TEXT | model **id** only, not base-url |
| `md_path` | TEXT | e.g. `session/<id>.md` |

### `session_events`

Append-only until session prune.

| Column | Type | Notes |
|--------|------|--------|
| `id` | INTEGER PK AUTOINCREMENT | |
| `session_id` | TEXT | |
| `seq` | INTEGER | |
| `ts` | TEXT | UTC |
| `kind` | TEXT | message / tool / model_call / error / … |
| `role` | TEXT NULL | |
| `content` | TEXT NULL | full if ≤ inline cap; else truncated |
| `content_truncated` | INTEGER | 0/1 |
| `blob_id` | TEXT NULL | |
| `tool_name` / `tool_call_id` / `tool_args_json` | | args may truncate + blob |
| `model` | TEXT NULL | id only |
| `tokens_in_reported` / `tokens_out_reported` | INTEGER NULL | |
| `tokens_in_est` / `tokens_out_est` | INTEGER NULL | |
| `latency_ms` / `finish_reason` / `error` / `meta_json` | | |

Indexes: `(session_id, seq)`, `(session_id, ts)`, `(kind)`, `(blob_id)`.

### `blobs`

| Column | Type | Notes |
|--------|------|--------|
| `id` | TEXT PK | |
| `path` | TEXT | relative under `blobs/` |
| `byte_size` | INTEGER | |
| `created_at` | TEXT | UTC |
| `session_id` | TEXT NULL | |
| `content_sha256` | TEXT NULL | optional |

### `daemon_state` (singleton)

| Column | Notes |
|--------|--------|
| `last_sweep_at` / `next_sweep_at` | UTC |
| `last_sweep_duration_ms` | |
| `sessions_seen` / `dirty` / `flushed` / `failed` | last sweep |
| `blobs_purged` / `sessions_pruned` | last sweep |
| `last_error` / `last_daily_compact_at` / `updated_at` | |

## Session lifecycle

| Event | Behavior |
|-------|----------|
| **Create** | Short id; write MD; upsert `sessions`; dual-write events as turns happen |
| **Close** | Flush MD; `status=closed`, `closed_at=now()`; **retain** all events/bodies in DB |
| **Prune** | Each daemon sweep: delete sessions with `status=closed` AND `closed_at` older than `closed_session_max_age_days`; delete events; delete session blobs; **do not delete** `session/<id>.md` |
| **UI** | Active sessions by default; toggle **Show closed** for `status=closed` not yet pruned |

## Versioning & limp mode

### Version policy

- `CurrentSchemaVersion` in binary; `schema_meta.schema_version` in DB.
- **Auto-migrate only** when an explicit step exists in the binary.
- Breaking changes later may require new memory dirs or a future migrator (deferred).

| Situation | Mode |
|-----------|------|
| No DB file | Create v1 → **normal** |
| version == binary | **normal** |
| version &lt; binary, migration exists | migrate → **normal** |
| version &lt; binary, no migration | **limp** |
| version &gt; binary | **limp** (do not open/write DB; ignore DB entirely) |
| Lock held by another process | **Fatal exit** (not limp) |
| DB corrupt | **limp** with reason (treat as unusable DB) |

### Limp mode (locked)

| Capability | Limp |
|------------|------|
| UI banner + `health.mode=limp` | Yes |
| Chat with CLI model | Yes |
| Workspace tools | Yes |
| New sessions → Markdown on disk | Yes |
| List history from `session/*.md` front matter | Yes |
| 5m Markdown flush daemon | Yes (MD only) |
| DB open/write/dual-write events | **No** — ignore DB entirely when version &gt; binary or unusable; no writes when mismatch |
| Coded migrations without steps | No |

Recovery path: operator uses chat + docs to upgrade harness, restore backup, or start a fresh `--memory` dir.

## Single-process lock

- Acquire exclusive use of memory dir / `marble.db` at start (lock file with PID **and** SQLite safety).
- If lock held by a **live** process → exit with clear error.
- Stale lock (dead PID) → take over after check.

## Timestamps

All DB times: **UTC**, RFC3339 with `Z`.  
Daily MD filenames remain **local calendar day** (ADR-0002).

## Daemon sweep (normal mode)

Interval: CLI `--persist-interval` (default 5m).

1. Flush dirty sessions → Markdown.  
2. Dual-write pending session/event updates.  
3. Update `daemon_state`.  
4. Prune closed sessions past retention; delete their blobs.  
5. Blob cleanup: unreferenced (and age-eligible) orphans.  
6. Daily MD compact if activity (ADR-0002).  

**Limp:** steps 1 and 6 only (plus any non-DB maintenance); skip 2–5 DB work.

## Implementation order (post-accept)

1. `internal/db` — create/open, schema v1, settings seed, lock.  
2. Mode detection → normal | limp; health + UI banner.  
3. Dual-write from session loop (events, bodies, tokens both columns).  
4. Blob spill at 32 KiB + `blobs` table.  
5. Daemon: MD flush, prune closed+blobs, blob GC, `daemon_state`.  
6. UI: show-closed toggle; limp banner.  
7. Driver: pure-Go SQLite (`modernc.org/sqlite`) preferred.  

## Decisions log (Q1–Q10 + R2)

| ID | Decision |
|----|----------|
| Q1 | Dual-write; files first-class |
| Q2 | Truncate + `$MEMORY/blobs/`; blob max age 4d (setting) |
| Q3 | Both reported + est token columns |
| Q4 | `marble.db` |
| Q5 | Migrate only with steps; else limp + UI warning |
| Q6 | Closed persist; prune after 4d (setting) |
| Q7 | Single harness lock |
| Q8 | CLI only for base-url |
| Q9 | UTC always in DB |
| Q10 | Full bodies in DB until **purge** (not mere close) |
| R2-1 | Limp: new sessions write Markdown | yes |
| R2-2 | Limp: workspace tools | yes |
| R2-3 | Limp: list from MD front matter | yes |
| R2-4 | Newer DB than binary: ignore DB entirely | yes |
| R2-5 | Inline cap | 32 KiB |
| R2-6 | Blob GC | unreferenced (ref-aware); age as eligibility |
| R2-7 | Prune DB ≠ delete MD | keep `.md` |
| R2-8 | Session prune deletes its blobs immediately | yes |
| R2-9 | Prune every daemon sweep | yes |
| R2-10 | Hide closed by default + toggle | yes |
| R2-11 | Retention via DB settings only for v1 | yes |
| R2-12 | Limp keeps MD flush daemon | yes |

## Remaining questions (optional / fine defaults)

These do **not** block implementation if “defaults OK”:

1. **Blob id scheme:** random UUID vs sha256 of content (dedup)?  
   *Default if unset: UUID v4 filename; store optional sha256 for later dedup.*

2. **Limp banner copy / severity:** warning vs error styling only?  
   *Default: warning (amber), non-blocking.*

3. **Health field names** for API stability (`mode`, `schema_version_db`, `schema_version_binary`, `limp_reason`)?  
   *Default: use those names.*

4. **Should active sessions older than N days ever auto-close?**  
   *Default: no — only explicit close.*

5. **Multi-memory-dir:** same machine, two harnesses, different `--memory`?**  
   *Default: allowed (lock is per memory dir).*

## Acceptance criteria

- [x] Q1–Q10 decided  
- [x] Round-2 recommendations adopted  
- [x] Limp / lock / prune / blob rules specified  
- [ ] Final accept to implement (or answer optional Qs)  

## Changelog

| Date | Change |
|------|--------|
| 2026-07-17 | Initial proposal |
| 2026-07-17 | Q1–Q10 decisions + limp + round-2 questions |
| 2026-07-17 | All R2 recs accepted; optional residual Qs only |

## References

- ADR-0002 Session management & file memory  
- ADR-0001 Inner loop  
