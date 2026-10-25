# ADR-0039: Harness State Portability, Export/Import, and Warm Standby

| Field | Value |
|-------|-------|
| **Status** | **Proposed** |
| **Date** | 2026-10-24 |
| **Author** | — |
| **Deciders** | Project owner |
| **Tags** | portability, backup, standby, sqlite, memory, secrets, ops, failover |
| **Extends** | ADR-0002 (session memory), ADR-0003 (SQLite + lock + limp), ADR-0009 (mpub), ADR-0013 (soul), ADR-0015 (cron), ADR-0016 (secrets by env name), ADR-0017 (OAuth), ADR-0018 (model catalog), ADR-0019 (attachments), ADR-0020/0021 (computers + peer), ADR-0027 (TTS), ADR-0028 (sinks), ADR-0030 (agent presets), ADR-0036 (grants) |
| **Evidence** | Host `rinux` (this Ubuntu box) lost its onboard Wi-Fi adapter and was unreachable for days. It is back on a USB dongle (`enx00e04c68039e`, `192.168.86.81`) as the only NIC. All harness state lives on one disk on one machine. The next hardware fault costs the same days unless state can move. |

## Context

The live rinux harness (systemd user unit `marble-harness.service`, recipe also in `~/restart-harness-qwen38.sh`):

```text
/home/russell/projects/marble/bin/marble-harness
  --workspace  /home/russell
  --memory     /home/russell/.marble
  --base-url   http://spark-37a9-280.tail6f1a62.ts.net:8000/v1
  --model      qwen3.8-27b
  --addr       :8080
  --public-url http://rinux.tail6f1a62.ts.net:8080
```

`EnvironmentFile=-/home/russell/.marble/env`. Tailscale identity: hostname `rinux`, MagicDNS `rinux.tail6f1a62.ts.net`, `100.96.73.27`. The model itself is already off-box (`spark-37a9-280`); a rinux death does not take the weights with it. A peer (`marble-peer.service` → `~/.marble-peer`) and the operator workspace (`/home/russell`, including `projects/`, skills, launch scripts) share the same disk — and that disk is tight: `/dev/nvme0n1p5` is **108 G with 92 G used (90%, ~11 G free)**. There is no `crontab`, no restic/borg/rclone, and no backup job of any kind; `rsync` is the only copy tool installed. So a bundle destination on the same filesystem is not a backup, and the first full bundle has to leave the box (tailnet host or USB disk). Long-session `session` Markdown plus `attachments/`, `blobs/`, and `mpub/` assets are the likely heavy parts; `--stats` exists to answer that before anything is scheduled.

`db.Open` (`internal/db/db.go`) takes `marble.lock` (PID + `flock`, `internal/db/lock.go`) and opens `$MEMORY/marble.db` with `modernc.org/sqlite`, `PRAGMA journal_mode=WAL`, `busy_timeout=5000`, `SetMaxOpenConns(1)`. Schema is v14 (`CurrentSchemaVersion`). A newer schema on an older binary goes **limp** (files still work, DB writes stop). There is no export, no backup API, no replica role. `overage-rescue` and `repair-sessions` are the only existing subcommands; both refuse `-apply` while the lock is held.

`$MEMORY` contents were not listed from this session: the shell policy blocks `/home/russell/.marble`. Layout below is from code (`memory.Store`, `db.Open`, `cmd/marble-harness/main.go`, README “Memory layout”). Sizes are unknown until Phase 1 grows a `--stats` dry run.

### Inventory

| Class | Where | Change rate | Machine-coupled | Sensitivity |
|-------|-------|-------------|-----------------|-------------|
| SQLite | `$MEMORY/marble.db` (+ `-wal`/`-shm`) | Every event, persist, sweep | Paths in `sessions.workspace`, `sessions.md_path`; `model_catalog.base_url`; `computers.endpoint_hint` | High — transcripts, events, token hashes, pairing plaintext while a handshake is open (`computer_pairings.device_token_plain`) |
| Markdown memory | `$MEMORY/session/<id>.md`, `daily/YYYY-MM-DD.md` | Persist interval 5m + end of turn; daily compact on activity | Absolute workspace paths inside transcripts | Medium — the transcript of record (ADR-0002/0003 dual-write) |
| Knowledge / soul / skills | `$MEMORY/knowledge/*.md`, `soul.md`, `$MEMORY/skills/`; also `$WORKSPACE/.marble/skills` | Rare, operator/agent authored | Skill bodies may hard-code `/home/russell/...` | Medium; soul is every-turn context (64 KiB cap) |
| mpub | `$MEMORY/mpub/<slug>/` (`meta.json` + content + assets) | Per publish | Public pages inherit `--public-url`; visibility public\|private | Medium (private pages assume OAuth allowlist) |
| Attachments / blobs | `$MEMORY/attachments/`, `$MEMORY/blobs/`; `session_attachments.path` and `blobs.path` are **relative** | Uploads; blobs age out (`blob_max_age_days` default 4) | Workspace copies at `$WORKSPACE/.attachments/<session>/…` (`session/workspace_attach.go`) | Medium; bytes live under memory, workspace copies are a convenience |
| Config JSON | `$MEMORY/mcp.json`, `tts.json`, `sinks.json`, `agent_process.json` (0600 for the last three writers) | Rare | `mcp.json` `command`/`cwd`; `sinks.json` `deep_link_base`; agent driver paths | Medium; `secret_env` / `api_key_env` names only |
| Secrets | `$MEMORY/env` mode **0600** (`config.RelMemoryEnv`); never in SQLite | Rare; Settings → Secrets live-rereads ~2s | systemd `EnvironmentFile=` path | **Critical** — model keys, OAuth client secret, sink tokens, TTS keys |
| TTS cache | `$MEMORY/tts-cache/` | Rebuildable | None | Low; omit from bundles |
| Backups | `$MEMORY/backups/overage/`, `backups/session-pre-repair-*` | On rescue/repair | None | Medium; pre-images of transcripts |
| Lock / WAL sidecars | `marble.lock`, `marble.db-wal`, `marble.db-shm` | Live | PID is this host | Do not copy; lock is local-FS only |
| Settings / catalog / cron / clerk / search | tables `settings`, `model_catalog`, `cron_jobs`/`cron_runs`, `clerk_session_state`, `session_search(_terms)`, `agent_presets`, `daemon_state` | Mixed | `agent_presets.detected_path`; cron timezone `Local`; `default_model_id` | Medium; cron **fires turns** if a replica is writable |
| Computers / grants | `computers`, `computer_pairings`, `computer_grants` (hash only) | Pairing rare | Device tokens + `endpoint_hint`; peer reconnects by harness URL | High (token_hash; plaintext token during pairing) |
| Launch / host identity | systemd unit, `--public-url`, `--base-url`, Tailscale hostname + node key, USB NIC | At restart | **Yes** — this is the coupling the outage demonstrated | Public URL is not secret; Tailscale node key is |
| OAuth / login cookies | `auth.SessionStore` **in-memory** (ADR-0017 Q11); 7-day cookie | Lost on restart already | `--oauth-redirect-url` must match the reachable origin | Re-login after promote |
| Continuations / BG tasks | `continuation.Manager`, `bgtask.Manager` in-memory | Live turns | PIDs, cwd | Dropped on any failover |
| Workspace | `--workspace /home/russell` | Continuous | Home dir = harness FS root | Mixed; **not** harness state. Git covers `projects/`; uncommitted work and `~/skills` do not. |
| Peer identity | `~/.marble-peer/` (`config.json`, `credentials.json` 0600, `device_id`, `chrome-profile/`, `state.json`, `marble-peer.lock`) | Pairing rare; Chrome profile large | Bound to that desktop and to a harness URL | High (device tokens). Lives **outside** `$MEMORY`. |

A second harness already exists on `rinis-mac-mini` (`~/rini-marble-harness.plist`, `--memory /Users/rini/.marble`, `--public-url http://rinis-mac-mini.tail6f1a62.ts.net:8080`). That is a **different** operator memory leaf. It is not a merge target and not a standby for rinux.

Other tailnet Linux: `claw-world-new` (online), `spark-37a9-280` (the model host). Putting the only replica on spark couples “no model” with “no memory”. Prefer a third machine (or a new mini PC) as standby.

### Goals

1. **Export** one harness’s portable state and **import** it onto another, with a manifest and checksums.
2. Survive a rinux-class outage in **minutes of operator work**, not days of reconstruction.
3. Leave room for a **warm standby** that is read-only until promoted.
4. Keep secrets off the wire in cleartext.

Non-goals for this ADR: no-downtime active-active (two writers), merging two operators’ memory dirs, replicating the workspace home directory, replicating `~/.marble-peer` Chrome profiles, changing Tailscale itself.

## Options

### A. Cold bundle — `marble-harness state export|import`

A subcommand in the same style as `overage-rescue` (`cmd/marble-harness/main.go`). Default dry-run; `-apply` on import refuses while `marble.lock` is held.

**Mechanics.** `VACUUM INTO` (or `sqlite3_backup`) writes a consistent `marble.db` without copying `-wal`/`-shm`. Files under `$MEMORY` are copied into a directory tree (or a `.tar.zst`) next to `manifest.json` with per-file SHA-256. `$MEMORY/env` is omitted unless encrypted. Import stops the dest harness, verifies checksums, refuses a dest binary whose `CurrentSchemaVersion` is **older** than the bundle, copies files, optionally rewrites `sessions.workspace` / `md_path` prefixes, writes `$MEMORY/env` from `secrets.age`, then the operator starts the dest unit with new `--public-url` / `--workspace`.

**Gets right.** Small surface; works onto any disk the operator can `scp` to; is the only thing that helps when rinux is already dark. Matches dual-write (MD + DB) and limp-mode rules.

**Breaks.** Restart required. In-flight turns, continuations, BG shells, OAuth cookies, and `peerhub.InstanceID` are lost (they are already lost on a crash). Workspace files are not in the bundle. Peers still point at the old URL until edited. First full copy size is unknown.

### B. Git-backed state repo

Cron or a hook commits `session/`, `daily/`, `knowledge/`, `skills/`, `soul.md`, `mpub/`, and the JSON configs to a private git remote. `marble.db` is excluded or dumped to `marble.sql` on a schedule.

**Gets right.** Knowledge/skills/soul are already Markdown and diff well. An off-box git remote survives a dead disk. Operator already lives in git for `projects/`.

**Breaks.** Session markdown is large, rewritten whole (`memory.Store.WriteSession` atomic rewrite), and will bloat git. SQLite dumps race WAL. Secrets must never be committed. Cron on the **primary** is itself state that dies with the box. Git is a complement for the slow-changing Markdown, not a failover of `model_catalog`, `computers`, or `cron_jobs`.

### C. Live streaming replication / warm standby

Primary periodically snapshots SQLite with the backup API / `VACUUM INTO` to a staging file and `rsync`s that plus the Markdown/config/attachment trees to a standby’s `--memory` over Tailscale. Standby process runs `--role=standby`: HTTP UI may be up read-only; cron, sinks, computer actions, and new turns are fenced. Promote = stop primary, one final pull, take the lease, start without `--role=standby`.

**Gets right.** Background sync is what the operator asked for. WAL is never copied. `marble.lock` stays local (one process per tree). Failover is a runbook, not a rebuild.

**Breaks.** Replication lag (seconds to a few minutes). Split-brain if both run as primary — `marble.lock` does **not** fence two machines. Secrets still need a separate encrypted path. First full sync can be large. The standby must not share a filesystem with the primary (NFS, option E).

### D. Hot standby with client redirect

C, plus on promotion either (1) move the Tailscale hostname `rinux` onto the new node so `http://rinux.tail6f1a62.ts.net:8080` keeps working, or (2) leave hostnames stable and rewrite `--public-url` + `sinks.json` `deep_link_base` + peer `config.json` harness URLs. Session resume from the replicated DB + `session/*.md`.

**Gets right.** Bookmarks, confirm links (`--public-url` is already the origin for computer-confirm), and sink deep links survive. Closest to “no downtime” this architecture can honestly offer: drain the in-flight turn, cut, promote, one re-login.

**Breaks.** Tailscale hostname move needs the old node down (or renamed) first — MagicDNS lag of seconds to a minute. OAuth redirect URLs in Google Console must list the new origin if the hostname changes. Peers hold the lock against `InstanceID`; a new primary always gets a new UUID (`peerhub/hub.go`) and re-acquires. Not zero-downtime for a turn that is mid-tool-loop.

### E. Rejected or complementary

| Idea | Verdict |
|------|---------|
| NFS/SMB shared `$MEMORY` | **Reject.** `marble.lock` is flock+PID on one kernel. WAL over NFS is a known corruption path. Two harnesses on one leaf is already forbidden. |
| Cloud-only memory (S3 as the live leaf) | **Reject as live store.** Fine as a **destination** for cold bundles. Object storage is not POSIX, not WAL-safe, and secrets would sit next to transcripts. |
| Full VM snapshot | Complementary disaster option. Hours, couples the OS, not portable onto `claw-world-new` or a Mac. Does not teach the harness about its own state. |
| Docker volume | This harness is a systemd user unit on a home dir, not a container. A volume does not solve USB-NIC death. |
| Git (option B) for knowledge/skills/soul only | **Complement.** Cheap, do it; do not pretend it moves `marble.db`. |
| Nightly `scp` of a Phase-1 bundle to another tailnet host | **Complement / Phase 1 ops.** This is the “next outage does not cost days” bar. |

## Decision

**Primary: option A now, option C next, option D as the promotion story.** Option B only for the slow Markdown (knowledge/skills/soul). Option E’s shared-disk and cloud-leaf ideas stay rejected.

This is a three-phase path. Phase 1 alone would have turned the Wi-Fi outage into “boot another box, import last night’s bundle, re-login.” Phases 2–3 add a warm standby so the last few minutes are not lost.

### Phase 1 — export/import CLI (days; **this is the outage stopper**)

New subcommands, same binary, same flag style as `overage-rescue`:

```text
marble-harness state export  [-memory DIR] [-out PATH] [--stats] [--secrets=omit|age]
marble-harness state import  [-memory DIR] [-in PATH]  [-apply] [--rewrite-workspace FROM=TO]
```

Default export is dry-run/`--stats` (counts, bytes, largest prefixes). Write requires an explicit `-out`. Import is dry-run until `-apply`, and `-apply` refuses a live `marble.lock`.

#### Bundle layout (`marble-state/v1`)

```text
marble-state-v1/
  manifest.json
  marble.db                 # VACUUM INTO snapshot (no -wal/-shm)
  files/
    session/  daily/  knowledge/  skills/  mpub/
    attachments/  blobs/  backups/
    soul.md  mcp.json  tts.json  sinks.json  agent_process.json
  secrets.age               # only with --secrets=age
```

Omitted always: `marble.lock`, `marble.db-wal`, `marble.db-shm`, `tts-cache/`. Omitted by default: `env`.

`manifest.json`:

```json
{
  "schema": "marble-state/v1",
  "created_at": "2026-10-23T18:00:00Z",
  "source": {
    "hostname": "rinux",
    "public_url": "http://rinux.tail6f1a62.ts.net:8080",
    "workspace": "/home/russell",
    "memory": "/home/russell/.marble",
    "harness_version": "…",
    "db_schema_version": 14,
    "model": "qwen3.8-27b",
    "base_url": "http://spark-37a9-280.tail6f1a62.ts.net:8000/v1"
  },
  "files": [{"path": "files/session/….md", "sha256": "…", "bytes": 0, "mode": "0644"}],
  "db": {"path": "marble.db", "sha256": "…", "bytes": 0, "schema_version": 14},
  "secrets": "omitted",
  "excludes": ["env", "tts-cache", "marble.lock", "marble.db-wal", "marble.db-shm"]
}
```

#### SQLite snapshot

Do not copy `marble.db` while WAL is live. Export opens the DB (or uses the running process via a future admin endpoint) and runs `VACUUM INTO '<staging>/marble.db'`, which is a consistent checkpointed copy. If export is a separate process and the harness holds the lock, export either (1) talks to a `POST /api/admin/state-snapshot` on the live harness, or (2) the operator stops the unit first. Prefer (1) so a nightly bundle does not require downtime. The snapshot file is what goes in the bundle; `-wal`/`-shm` never do.

#### Secrets

Never put `$MEMORY/env` in the clear in the bundle. `--secrets=omit` (default) records `"secrets":"omitted"` and prints the names present (not values), using the same parser as `config.ParseEnvFile`. `--secrets=age --age-recipient <age1…>` encrypts the file with [age](https://age-encryption.org) to `secrets.age`. There is no `--secrets=include`. The age identity lives off-box (USB, password manager); the public recipient can sit in the systemd unit as `MARBLE_STATE_AGE_RECIPIENT`. Import decrypts to dest `$MEMORY/env` and `chmod 0600`. systemd `EnvironmentFile=` is re-pointed by the operator.

`model_catalog.api_key_env`, `tts.json` `api_key_env`, and `sinks.json` `secret_env` are names. They travel with the DB/JSON. They are useless until `env` is restored.

`computer_pairings.device_token_plain` is a live-handshake leftover; export should NULL it. Durable trust is `computers.token_hash` plus the peer’s `credentials.json`, which is **not** in this bundle.

#### Path rewriting

Import `--rewrite-workspace /home/russell=/home/other` updates `sessions.workspace` and the prefix of `sessions.md_path`. `sessions.agent_cwd` is workspace-relative and stays. `session_attachments.path` / `blobs.path` are memory-relative and stay. Transcript Markdown, `mcp.json` cwd/command, and `agent_process.json` paths are **reported** as rewrite candidates, not silently mutated in v1 (false positives in prose). `sinks.json` `deep_link_base` and `--public-url` are dest launch flags, not bundle fields.

#### Version skew

- Dest binary schema **<** bundle `db_schema_version` → refuse. Upgrade the dest harness first (newer schema already limp-modes older binaries; do not invent a second rule).
- Dest binary schema **≥** bundle → copy DB, then dest `db.Open` runs `upgradeSchema`.
- Missing `marble.db` with Markdown present → dest starts limp; operator is told. That is recovery of last resort, not the happy path.

#### What Phase 1 does not move

Workspace `/home/russell`. `~/.marble-peer`. In-memory auth, continuations, BG tasks. Tailscale node key. USB NIC. The dest needs its own `--workspace` (a checkout of `projects/` plus whatever else the operator copies by hand).

**Ops on rinux, as soon as the CLI exists:** a systemd timer dumps a bundle to `claw-world-new` (or a USB disk) every night, secrets via age. That is the whole “next dongle death” mitigation.

### Phase 2 — continuous push to a standby (about a week)

A standby host with the **same** (or newer) harness binary, its own `--memory` directory, and `--role=standby`:

- Pull loop, not push: the standby calls the primary’s snapshot endpoint (or `rsync`s a directory the primary writes). Pull keeps the primary from needing write access to the standby disk.
- **SQLite:** every **60s**, primary `VACUUM INTO` (or online backup API) a staging file `state-snap/marble.db`; standby `rsync -az --checksum` that file into place **next to** the standby DB, then atomically rename onto `marble.db` only while the standby process is stopped **or** while it has the DB closed (standby should not hold SQLite open between pulls; reopen read-only after each snap). Never rsync `-wal`/`-shm`.
- **Files:** every **60s**, `rsync -az --delete` of `session/ daily/ knowledge/ skills/ mpub/ attachments/ blobs/ backups/ soul.md *.json`, excluding `env`, `tts-cache`, `marble.lock`.
- **Secrets:** a slower loop (5–15 min) rsyncs `env` over Tailscale with mode 0600 preserved, **or** drops a fresh `secrets.age` and decrypts on the standby. Prefer age so a compromised standby disk does not yield keys at rest without the identity.
- Lag target: **≤ 2 minutes** under quiet load. A busy turn’s latest `session_events` may miss the last snap; Markdown persist is 5 minutes (`--persist-interval`), so flush dirty sessions before a planned promote (`PersistSession` / daemon sweep). Add `state export --flush` that asks the live harness to persist all dirty sessions first.

Standby **must not** fire `cron_jobs`, deliver sinks, accept new turns, or acquire peer locks. Implementation: `--role=standby` short-circuits `cron.Manager` fire, `sink.Manager` deliver, `peerhub` acquire, and POST message handlers (409). GET UI can stay up as a witness. `daemon_state` sweeps that prune blobs are disabled (pruning on a replica would fight the next rsync `--delete`).

### Phase 3 — promotion runbook (days, after Phase 2)

Fencing is a **lease**, because `marble.lock` is per-filesystem.

New singleton (either `daemon_state` columns or a `settings` key pair, written only by primary):

```text
leader_id      hostname + public_url  (e.g. rinux|http://rinux.tail6f1a62.ts.net:8080)
leader_epoch   integer, monotonic
leader_until   RFC3339 UTC
```

Primary renews every **15s**, TTL **45s**. Standby refuses to leave `--role=standby` unless `promote` is invoked **and** (`leader_until` is past **or** `--force` after the operator confirms the old box is dead). `promote` increments `leader_epoch`, writes the new `leader_id`, and is the only path that starts cron/sinks/turns.

Complementary, not sufficient: a Tailscale ACL tag `tag:marble-primary` on whichever node currently serves `:8080`. DNS is not a lock.

#### Runbook (planned drain)

1. Finish or interrupt in-flight turns on the primary. `POST /api/sessions/{id}/stop` as needed.
2. `marble-harness state export --flush` (persist dirty MD + one snapshot). On standby: one extra pull. Compare `manifest.json` / DB SHA-256.
3. `systemctl --user stop marble-harness` on the primary. Confirm `marble.lock` is gone.
4. On standby: `marble-harness state promote --public-url http://<standby>.tail6f1a62.ts.net:8080` (rewrites lease; prints the `sinks.json` `deep_link_base` and systemd `--public-url` that must match).
5. Optional hostname takeover: on the dead box `tailscale down`; on the standby `tailscale set --hostname=rinux` so existing bookmarks keep working. If the hostname stays (`claw-world-new`), update Google OAuth redirect URLs and every peer `config.json` harness URL.
6. Edit dest systemd unit: `--workspace`, `--memory`, `--public-url`, `EnvironmentFile=`. Do **not** copy rinux’s `--base-url` unless that model host is still the one you want (on this fleet it is: spark is independent).
7. `systemctl --user start marble-harness`. Verify:

```bash
curl -sS http://127.0.0.1:8080/api/health
# mode=normal, schema_version_db=14, memory_path=dest
curl -sS http://127.0.0.1:8080/api/sessions | head
test -f "$MEMORY/env" && stat -c '%a' "$MEMORY/env"   # 600
```

8. Sign in again (cookies were in-memory). Send a trivial turn. Confirm cron modal shows jobs **without** having double-fired (`cron_runs` last timestamps).
9. Peers: each `~/.marble-peer/config.json` harness URL must match the new origin; tokens in `credentials.json` still hash to `computers.token_hash`. If they do not, re-pair (ADR-0020) or mint a grant (ADR-0036). The new primary’s `InstanceID` is fresh; peer locks re-acquire.
10. Leave the old disk alone until a session has been used on the new primary. Then retire or wipe `env` on the old disk.

#### Runbook (rinux already dead)

Skip 1–3. Promote `--force` on the last successful pull/bundle. Accept lag (up to the snapshot interval, plus any unsynced dirty MD). Restore `env` from `secrets.age` or the operator’s password manager. Workspace projects come from git; uncommitted home-dir work on the dead disk is gone unless the disk is later mounted.

## Risks and open questions

| Risk | Notes |
|------|-------|
| **Split-brain** | Two primaries both fire cron, both talk to peers, both append `session_events` that rsync will clobber. Lease + `--role=standby` + “stop the old unit first” is the whole mitigation. `marble.lock` does not help across hosts. `--force` is how a dead primary is declared; it must be loud. |
| **Secrets on the wire** | Default omit. Age for bundles. Tailscale for any rsync of `env`. Never git. |
| **WAL copy safety** | Copying `marble.db` + `-wal` from a live process is corrupt-on-arrival. Snapshot only via `VACUUM INTO` / backup API. |
| **`session_attachments` vs workspace** | Canonical bytes are under `$MEMORY/attachments` (relative paths). Workspace copies under `.attachments/` are **not** in the bundle; they rebuild on next view/send. Agent `attach_file` of arbitrary workspace paths is not durability. |
| **`model_catalog.base_url` / process `--base-url`** | On this fleet they point at `spark-37a9-280`, which survives rinux. A failover that also moves the model host must rewrite catalog rows and the systemd `ExecStart`. Do not bake MagicDNS names that you intend to steal for the harness into the catalog. |
| **mpub visibility** | Private pages assume the dest OAuth allowlist. Open mode (`AuthMode=open`) on a standby that is reachable on the tailnet would publish them to anyone on `:8080`. Keep dest auth ≥ primary. |
| **First full sync size** | Unknown (memory dir not measurable from this agent). `--stats` is part of Phase 1. Attachments + blobs + mpub assets are the likely heavy parts; `tts-cache` is omitted. |
| **Workspace is not separable** | `--workspace /home/russell` is the operator’s home. Harness export will not make a second rinux. Restore is “new machine, clone `projects/`, import `$MEMORY`.” Uncommitted files in home are a separate backup problem (restic, etc.). |
| **Peer state** | Exporting `computers` without `~/.marble-peer/credentials.json` means the desktop must re-pair if tokens were rotated, or if the harness URL changed. Chrome profiles stay on the desktop — correct, they are that machine’s cookies. |
| **Timezone `Local` on cron** | A standby in a different TZ will fire at the wrong wall clock after promote. Pin `timezone` to `America/New_York` (or whatever rinux uses) before relying on Phase 2. |
| **Search index** | `session_search*` can be rebuilt from Markdown on dest; include it anyway (cheap, consistent with the snapshot). |
| **Schema limp** | Shipping a v14 DB to a v13 binary is a boot into limp. Promote checklist includes harness version. |
| **OAuth redirect** | `--oauth-redirect-url` and Google Console must include the post-promote origin. In-memory sessions always drop. |

Open questions for review:

1. Snapshot endpoint on the live harness vs. stop-the-world export only? Rec: live `VACUUM INTO` endpoint, operator-auth’d, for nightly and Phase 2.
2. Standby host: `claw-world-new` vs a new mini PC vs USB-disk-only until hardware is bought? Rec: nightly bundle to `claw-world-new` immediately; dedicated mini PC as Phase 2 standby so spark and the replica stay independent.
3. Tailscale hostname steal (`rinux`) vs stable per-machine names + URL rewrite? Rec: steal for emergency promote so bookmarks and `--public-url` in the wild keep working; keep per-machine names for the planned drain test.
4. Age identity storage: USB in a drawer vs password manager vs a second tailnet node? Rec: password manager + a printed recovery key; USB as the offline copy.
5. Include `backups/` in the nightly bundle? Rec: yes (they are the overage/repair pre-images); exclude if `--stats` shows they dominate.
6. Should Phase 1 rewrite transcript Markdown paths? Rec: no in v1; report only.

## Effort

| Phase | Work | Calendar | Needed to stop the next outage costing days? |
|-------|------|----------|-----------------------------------------------|
| **1** Export/import CLI + manifest + `VACUUM INTO` + age secrets + `--stats` + nightly timer to another tailnet host | Snapshot code, tar/dir writer, import path rewrite, tests against a fixture memory dir | **A few days** | **Yes. Do this first.** |
| **1b** Git remote for `knowledge/` `skills/` `soul.md` only | Hook or timer, private repo | Hours | Nice; not sufficient |
| **2** `--role=standby`, snapshot endpoint, rsync/pull loop, disable cron/sinks/turns | New flag, fencing in cron/sink/api, a small sync unit | **About a week** | No — reduces data loss from “last night” to “last minute” |
| **3** Lease row, `state promote`, Tailscale hostname runbook, peer URL notes, OAuth redirect checklist | Small code + a written runbook exercised once | **A few days after Phase 2** | No — makes the failover repeatable |
| Dedicated standby hardware | Buy/place a mini PC on the tailnet | Lead time | Independent of code; USB dongle on rinux remains a single NIC |

Phase 1 is the decision that would have mattered last week. Phases 2 and 3 are how the fleet stops treating “the box with the dongle” as a unique snowflake.
