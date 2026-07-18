# ADR-0005: Expanded Tool Suite & Robust Agent Loop

| Field | Value |
|-------|--------|
| **Status** | Accepted |
| **Date** | 2026-07-17 |
| **Deciders** | Project owner |
| **Tags** | tools, agent-loop, shell, memory, skills, compaction, background-tasks, system-agents |
| **Extends** | ADR-0001 (inner loop + v1 tools), ADR-0002/0003 (memory + DB settings), ADR-0004 (workspace jail) |

## Context

ADR-0001 shipped a minimal tool set (`file_read`, `file_write`, `list_files`) and a simple agent loop (serial tools, hard stop at `--max-tool-iters`, FIFO context trim only). That is enough for toy edits; it is not enough for real sysadmin / coding / long-running work.

Marble needs:

1. A **richer tool suite** for navigation, surgical edits, shell, background processes, memory, skills, and UI attachments.
2. A **policy-aware tool loop**: soft warnings, hard caps, auto-compaction nudges, continuations when work outlives a turn.
3. **Configurable shell power** — from “almost nothing” to “full operator shell” — with defaults that fail closed, prohibited-command lists in DB settings, and permission inheritance from the harness process (no privilege escalation).

This ADR defines tool contracts, loop policies, shell safety model, background tasks, scheduled continuations, memory/skill tools, and open decisions for review.

## Decision summary

| Area | Decision |
|------|----------|
| v1 tools | Keep `file_read`, `file_write`, `list_files`; add the suite below |
| Tool registry | Full suite always in v1 (**Q29**); tool profiles later if schema tax hurts |
| Workspace jail | All path tools use shared `workspacefs` resolve (ADR-0004) |
| Memory tools | Read/write intentional knowledge + search; writes only under `$MEMORY/knowledge/` (**Q25**) |
| Skills | `$MEMORY/skills` + `$WORKSPACE/.marble/skills` (**Q26**) |
| Shell | `deny_list` default; DB settings; `/bin/bash -lc` or `/bin/sh -c`; inherit harness UID |
| BG tasks | Session-scoped; max **8**; no restart survival; SIGTERM then SIGKILL |
| Continuations | Max **24h**; fire delay **or** task exit (first); cancel on close; DB persist when normal |
| Context | Soft warn ≥60%; LLM compact via system agents; hard fail-closed as today |
| Tool rounds | Soft **65** / hard **80** (`--max-tool-iters` hard default 80) |
| Shell timeout | Default **60s**; agent max **5m**; longer → background task |
| Round wall time | Soft **3m** without user reply |
| Compaction | **LLM summary** via **system agents** |
| Advisories | UI chips + **DB logs**; omit from session MD |
| Config surface | Loop/shell defaults: CLI + DB `settings` (DB wins for tunable security lists) |

## Decisions locked (Q1–Q30)

Source: `adr/0005-answers.json` (`2026-07-17T17:55:38.280Z`).

### Loop & context

| ID | Decision |
|----|----------|
| **Q1** | Hard tool-round stop **80**; soft advisory **65** |
| **Q2** | Soft wall-clock **3 minutes** of continuous tool rounds → advisory |
| **Q3** | Auto-compact only if usage ≥**85%** for **3** consecutive rounds; else tool-driven (`session_compact`) |
| **Q4** | Compaction uses **LLM summary**. Summarization (and similar harness jobs) run as **system agents** scheduled by the same inner daemon as memory flush/prune. System agents appear in the UI under a **separate session selector** at the **bottom of the left pane** (collapsible/expandable), each with a normal short **session id**, openable transcript, closeable like user sessions |
| **Q5** | `usage_ratio = est_prompt / (context_limit − max_output − reserve)` |
| **Q6** | Harness advisories: collapsible UI chips; **omit from session Markdown**; **retain in DB** `session_events` |

### Shell

| ID | Decision |
|----|----------|
| **Q7** | `/bin/bash -lc` if present, else `/bin/sh -c` |
| **Q8** | Default mode: **`deny_list`** with strong seed patterns (powerful but foot-gun resistant) |
| **Q9** | `sudo` only with explicit `shell_allow_sudo=true` + remove from deny — still no privilege beyond harness |
| **Q10** | Network unrestricted in v1 (same as host user); document trust model; sandbox later ADR |
| **Q11** | No cwd outside workspace when `shell_cwd_strict=true` (default) |
| **Q12** | Filtered parent env inherit + limited tool `env` overlay; drop dangerous bash hooks |
| **Q13** | Best-effort path heuristics **on** by default (block shell targeting `--memory`) |
| **Q14** | `--disable-shell` CLI plus DB `shell_enabled` |
| **Q15** | **512 KiB** combined stdout/stderr; spill rest to blob; always include exit + timing |
| **Q15b** | Deny seed: destructive disk + privilege + power control; expand after first week of use. **Normal file deletion allowed**; block catastrophic patterns like `rm -rf /` |

### Background & continuations

| ID | Decision |
|----|----------|
| **Q16** | BG tasks do **not** survive harness restart; boot marks orphans dead |
| **Q17** | Max **8** concurrent BG tasks per session |
| **Q18** | Continuation max delay **24 hours** |
| **Q19** | Fire on whichever comes first (`delay_sec` or `wait_for_task`); require at least one |
| **Q20** | **Cancel** continuations on session close |
| **Q21** | Persist schedules to SQLite in normal mode; in-memory only when limp |

### Edit / patch

| ID | Decision |
|----|----------|
| **Q22** | Prior-read required for `apply_patch` **update**; not for **add**/**delete** |
| **Q23** | Fail if `old_string` matches more than once unless `replace_all` |
| **Q24** | Temp files + rename; reverse ops for deletes; test multi-file failure mid-batch |

### Memory / skills / attach

| ID | Decision |
|----|----------|
| **Q25** | `memory_write` only under `$MEMORY/knowledge/` |
| **Q26** | Skill roots: `$MEMORY/skills` + `$WORKSPACE/.marble/skills` |
| **Q27** | `attach_file` is **UI event only**; does not inject bytes into model context |
| **Q28** | Keep both `list_files` and `glob` |

### Schema / config

| ID | Decision |
|----|----------|
| **Q29** | Register full tool suite always in v1; tool profiles later if schema tax hurts |
| **Q30** | CLI `--max-tool-iters` hard default **80**; soft **65** via setting/constant |

## Product framing

| Mode | Tools that matter |
|------|-------------------|
| Coding | `codebase_summary`, `grep`, `glob`, `edit_file`, `apply_patch`, `attach_file` |
| Sysadmin | `shell_execute`, background tasks, continuations |
| Learn over time | `memory_*`, `skill_*`, `session_compact`, `get_context_usage` |

## Relationship to existing tools

| Existing | Fate |
|----------|------|
| `file_read` | Keep — required for `edit_file` prior-read rule |
| `file_write` | Keep — full overwrite; prefer `edit_file` / `apply_patch` for surgery |
| `list_files` | Keep — shallow listing; `codebase_summary` / `glob` for project maps |

---

## Tool suite

### Conventions (all tools)

- Results are **strings** to the model (JSON text OK for structured tools).
- Large results truncated to `--max-tool-result` (or DB setting) with a clear marker + how to re-fetch.
- Errors are tool results (not silent failures): `ERROR: …` prefix or structured `{ "ok": false, "error": "…" }`.
- Path args: workspace-relative preferred; absolute only if under workspace after jail resolve.
- Tools that mutate disk dual-write diagnostics to SQLite when not limp (ADR-0003).

### A. Filesystem & code navigation

#### `codebase_summary`

Presents a **project layout tree** with file sizes (and later language-specific metadata).

| Input | Notes |
|-------|--------|
| `path` | Root relative to workspace (default `.`) |
| `max_depth` | Default 4; hard cap e.g. 12 |
| `max_entries` | Default 2000; abort/truncate beyond |
| `ignore` | Optional globs; always ignore common junk (`.git`, `node_modules`, …) |

**Output:** ASCII/Unicode tree + sizes (human + bytes), directory totals.  
**v1:** no language parsers. Later: language hints (Go module, pyproject, etc.).

#### `grep`

Regex search across workspace file contents.

| Input | Notes |
|-------|--------|
| `pattern` | RE2/Go regex |
| `path` | Subtree (default `.`) |
| `glob` | Optional file filter |
| `case_insensitive` | bool |
| `max_matches` | Default 50; hard cap 500 |
| `context_lines` | Default 2 (before/after) |

**Output:** path, line number, line text, optional ± context. Skip binaries (same heuristics as explorer).

#### `glob`

Recursive glob (`**` supported) within workspace.

| Input | Notes |
|-------|--------|
| `pattern` | e.g. `**/*_test.go` |
| `path` | Subtree root (default `.`) |
| `max_results` | Default 500 |

#### `edit_file`

Targeted text replacement in an **existing** file.

| Input | Notes |
|-------|--------|
| `path` | File path |
| `old_string` | Exact match to find |
| `new_string` | Replacement |
| `replace_all` | Default false |

**Hard rule:** requires a successful **`file_read` on the same path earlier in the same agent turn** (same user-turn tool loop). If not, return error instructing the model to read first.

**Also:** if `old_string` not found or not unique (and `replace_all` false) → error, no write.

#### `apply_patch`

Atomic multi-file edit set.

| Input | Notes |
|-------|--------|
| `edits` | Array of typed ops |

**Ops (v1):**

| `op` | Fields | Behavior |
|------|--------|----------|
| `add` | `path`, `content` | Create; fail if exists |
| `update` | `path`, `old_string`?, `new_string`? or full `content` | Update existing |
| `delete` | `path` | Delete file |

**Atomicity:** apply to temp/side copies or record reverse ops; on **any** failure → **rollback all** successful ops in the batch; return which op failed.  
**Do not** leave a half-applied multi-file change.

#### `attach_file`

Attach a workspace file to the **current assistant reply** so the UI can render it inline (or as a downloadable chip).

| Input | Notes |
|-------|--------|
| `path` | Workspace file |
| `as` | Optional display name |
| `inline` | Prefer inline for text/images if size allows |

**Harness behavior:** does not necessarily dump full file into model context again if already read; emits a UI event `attachment` with path, mime, size, optional preview. Caps: reuse explorer edit/download limits (e.g. inline text ≤ 1 MiB).

---

### B. Shell & process control

#### `shell_execute` ⚠ highest scrutiny

Run a shell command **scoped to the workspace** (default `cwd` = workspace root).

| Input | Notes |
|-------|--------|
| `command` | String passed to shell (`/bin/bash -lc` proposed) |
| `cwd` | Optional relative subdir of workspace |
| `timeout_sec` | Default **60**; agent may set up to **300** (5m) |
| `env` | Optional map; cannot inject privileged vars that escalate (open Q) |

**Permission model:**

1. Process runs as the **same UID/GID** as `marble-harness` (inherit parent). No setuid, no capability add.
2. **Cannot exceed** harness privileges (OS-enforced).
3. Additional **policy layer** (Marble):
   - Prohibited command patterns from DB `settings` (and optional CLI seed on first create).
   - Optional allowlist mode for locked-down deployments.
   - Network/filesystem outside workspace: **not** OS-sandbox by default in v1 — document trust model; optional future bubblewrap/firejail ADR.
4. User configures weak→powerful via settings (deny lists, allow lists, timeout ceilings, enable/disable tool entirely).

**Default prohibited examples (seeded, editable in DB):**

- Destructive disk: `rm -rf /`, `mkfs`, `dd of=/dev/`
- Privilege: `sudo`, `su`, `pkexec`, `doas` (unless explicitly allowed)
- System control: `shutdown`, `reboot`, `init `, `systemctl poweroff`
- Marble self-harm optional: killing the harness PID, writing under `--memory` via shell (open Q)

Matching is **pattern-based** (regex or token prefix); not a perfect security boundary — defense in depth with OS user isolation.

**Timeouts:**

| Tier | Value | Behavior |
|------|--------|----------|
| Default | 60s | If agent omits timeout |
| Agent max | 300s (5m) | Clamp; error text suggests `start_background_task` |
| Soft suggest | >60s requested | Result note: “consider background task for long jobs” |
| On timeout | kill process group | SIGTERM → wait → SIGKILL; return partial stdout/stderr |

**Output:** exit code, duration, stdout/stderr (truncated), killed-by-timeout flag.

#### `start_background_task`

Start long-running command; returns **task id**.

| Input | Notes |
|-------|--------|
| `command` | Same shell policy as `shell_execute` |
| `cwd` | Optional |
| `label` | Optional human label |

**Scope:** per **session**. Survives across turns until exit/kill/session close (open Q: survive harness restart?).

**Output:** `task_id`, pid (optional), start time, log path if streamed to memory/blobs.

#### `kill_background_task`

| Input | Notes |
|-------|--------|
| `task_id` | Required |
| `signal` | `term` (default) or `kill` |
| Escalation | If term and still alive after grace (e.g. 5s), auto SIGKILL optional |

#### `check_background_task`

| Input | Notes |
|-------|--------|
| `task_id` | Optional; omit → list all for session |
| `tail_lines` | Optional stdout/stderr tail |

**Output:** status (`running`/`exited`/`killed`/`failed`), exit code, timestamps, resource hints if cheap.

---

### C. Continuations & context

#### `schedule_continuation`

Schedule a **delayed auto-resume** of the session: at fire time, inject `prompt` as a **system/user synthetic message** and start an agent turn (if session not busy).

| Input | Notes |
|-------|--------|
| `prompt` | Required — re-injected text |
| `delay_sec` | Wait N seconds (min 1; max open Q, e.g. 24h) |
| `wait_for_task` | Optional `task_id` — fire when task exits **or** delay, whichever policy (open Q) |
| `label` | Optional |

**Harness:** timer in-process; persist scheduled jobs to DB so limp/restart behavior is defined (open Q).  
**UI:** show pending continuation chip on session.

#### `get_context_usage`

Return current context estimate and traced token totals.

**Output (sketch):**

```json
{
  "context_limit": 262144,
  "budget": 221184,
  "estimated_prompt_tokens": 120000,
  "usage_ratio": 0.54,
  "reported_in": 118000,
  "reported_out": 4000,
  "est_in": 120000,
  "est_out": 4100,
  "message_count": 80,
  "soft_warn": false,
  "recommend_compact": false
}
```

#### `session_compact`

Summarize / compact conversation history when context is high.

| Input | Notes |
|-------|--------|
| `style` | `auto` / `aggressive` / `preserve_code` |
| `keep_last_n` | Messages to retain verbatim (default e.g. 12) |

**Behavior (locked — Q3/Q4):**

1. Request an **LLM summary** of older messages (not a deterministic-only extract).
2. Compaction work is executed by a **system agent** (see below), not as a silent side-effect with no session identity.
3. When complete, replace middle history of the **target user session** with a single summary block; keep last *N* messages verbatim.
4. Persist: summary applied to target session MD/history; **DB** events for compact request + result; system-agent transcript retained per system-session rules.
5. Return new usage estimate to the caller (user session tool result).

---

### System agents (Q4)

Harness-owned sessions used for internal LLM work (first consumer: **session compaction**). Same short-id and storage substrate as user sessions where practical; **distinct kind** for UI and policy.

#### Scheduling

- Created and resumed by the **inner scheduling daemon** — same process loop family as memory flush, prune, daily compact, continuation timers (ADR-0002/0003).
- Daemon may enqueue: “compact session X”, future: daily LLM digest, skill distill, etc.
- System agents use the same model client/config unless a later setting overrides.
- Tool access for system agents is **restricted by kind** (e.g. compaction agent: read target transcript + write summary artifact; no free `shell_execute` unless a future kind needs it). Open detail for implementers; default: minimal tools.

#### Identity & lifecycle

| Concern | Behavior |
|---------|----------|
| ID | Short session id (same generator as user sessions) |
| Kind | `system` (DB `sessions` column or equivalent; MD front matter `kind: system`) |
| Purpose / title | e.g. `compact · k7m2q9a3f2`, `system · daily-digest` |
| Parent | Optional `parent_session_id` → user session being served |
| Close | Operator can **Close** like a normal session (flush, mark closed, prune rules apply) |
| Spawn | Daemon or harness action; user does not “New” a system agent from the main + control (unless future debug affordance) |

#### UI (left pane)

```
┌ Left pane ─────────────────────┐
│  [Sessions ▾]  [📁]  [+]       │
│  ○ user session A              │
│  ● user session B              │
│  ○ user session C              │
│                                │
│  ▾ System agents          (2)  │  ← collapsible section, bottom
│    ○ c0mp4ct1a1  compact · B   │
│    ○ sysd4ily01  daily digest  │
└────────────────────────────────┘
```

- **Separate selector** from the main user session list, anchored at the **bottom** of the left pane.
- **Collapsible / expandable** (remember collapsed state in `sessionStorage` or similar).
- Each row: session id, title/purpose, activity affordances consistent with user rows (select → transcript in main pane; right-click **Close**, etc.).
- Selecting a system agent shows its transcript (prompts, model replies, tool use) for audit/debug.
- Badge/count of active or recent system agents optional.

#### Persistence

- Dual-write like user sessions when not limp: `session/<id>.md` + DB rows.
- List APIs: `GET /api/sessions` may include `kind` filter or return both; UI splits panes client-side.
- Advisories and compact machinery that touch system agents still follow Q6 for *user* MD omission of harness chips.

---

### D. Memory

Memory root remains `--memory` (ADR-0002/0003). Extend layout:

```
$MEMORY/
├── marble.db
├── session/<id>.md
├── daily/YYYY-MM-DD.md
├── blobs/
├── knowledge/                 # intentional long-term notes (new)
│   └── <topic-slug>.md
└── skills/                    # optional local skills (new)
    └── <skill-name>/
        └── SKILL.md
```

#### `memory_search`

Search **auto** (session/daily) + **intentional** (`knowledge/`) memory.

| Input | Notes |
|-------|--------|
| `query` | Keywords |
| `since` / `until` | Optional dates |
| `tags` | Optional tag filter (front matter) |
| `scope` | `all` / `session` / `daily` / `knowledge` |
| `max_results` | Default 20 |

**v1:** keyword / substring / simple scoring (no vectors yet).

#### `memory_fetch`

| Input | One of |
|-------|--------|
| `path` | Relative under memory (jailed to memory root) |
| `topic` | knowledge topic slug |
| `session_id` | Load session md |

#### `memory_write`

Persist **intentional** knowledge.

| Input | Notes |
|-------|--------|
| `topic` | Slug / title |
| `content` | Markdown body |
| `tags` | string[] |
| `mode` | `append` / `overwrite` |

Writes under `$MEMORY/knowledge/…` with YAML front matter (topic, tags, updated_at).  
**Not** for session transcripts (those remain daemon dual-write).

---

### E. Skills

Skills are reusable procedure packs (inspired by agent skill files): directory with `SKILL.md` + optional references.

#### `skill_search`

| Input | `query`, `max_results` |
| Output | name, description, path, tags — **no full body** |

#### `skill_load`

| Input | `name`, optional `ref` (relative file inside skill dir) |
| Output | Full markdown content of `SKILL.md` or ref |

**Discovery roots (proposed):**

1. `$MEMORY/skills/`
2. Optional `$WORKSPACE/.marble/skills/` (open Q)
3. Optional bundled skills in binary (open Q)

---

## Agent loop (detailed)

### Turn lifecycle (unchanged spine)

```
user message accepted
  → begin turn (busy)
  → loop:
       budget trim / inject loop advisories
       model call (tools enabled)
       if tool_calls → execute (policy) → append results → continue
       if final content → emit assistant → end turn
  → end turn
```

### Definitions

| Term | Meaning |
|------|---------|
| **Tool round** | One model response that includes ≥1 tool call, plus execution of those tools |
| **Tool call** | Single function invocation inside a round (may be parallel later; v1 serial) |
| **Turn** | One user message (or continuation injection) through final assistant reply or hard stop |
| **Usage ratio** | `estimated_prompt_tokens / (context_limit - context_reserve)` or vs full window — pick one and document |

### Soft vs hard controls

| Control | Soft (advise) | Hard (enforce) |
|---------|---------------|----------------|
| Context ≥ **60%** | Inject advisory: call `get_context_usage` / `session_compact` | — |
| Context ≥ **90%** (proposed) | Strong advisory; prefer compact before tools | Optional block new large tool results |
| Over budget | — | FIFO trim; fail closed if latest turn alone too big |
| Tool rounds ≥ **65** | Advisory: schedule continuation, compact, or final reply | — |
| Tool rounds ≥ **80** (default hard) | — | Stop turn with error + partial state |
| Shell timeout | Suggest BG task | Clamp to 5m; kill on expiry |
| Round wall clock ≥ **3m** (**Q2**) | Advisory: continuation or final reply | Optional hard turn ceiling (keep 15m total for now) |
| Max tool result size | Truncate marker | Always |

### Loop advisories (implementation) — **Q6 locked**

Before each model call (or after threshold crossings), the harness may append a **transient system note** for the model:

```
[harness] context_usage=72% rounds=40/65 soft. Prefer session_compact if history is large.
[harness] rounds=65 soft cap. Consider schedule_continuation or final user update.
```

| Surface | Policy |
|---------|--------|
| Model input | Ephemeral advisory for that call (strip/replace each round; do not stack forever) |
| UI | Collapsible **harness** chips / system bubbles |
| Session Markdown | **Omit** harness advisories |
| SQLite `session_events` | **Keep** — kind e.g. `harness_advisory` with content + thresholds |

### Auto-compaction policy — **Q3/Q4 locked**

| Trigger | Action |
|---------|--------|
| usage ≥ 60% | Soft warn (throttle: on crossing + every N rounds) |
| usage ≥ 75% | Stronger warn |
| Model calls `session_compact` | Enqueue/run **system agent** LLM summary; apply result to user session; continue turn when ready (or return “compacting…” + continuation — implementer detail) |
| Auto path | If usage ≥ **85%** for **3 consecutive** model rounds without successful compact → harness triggers compact system agent (same LLM path) |
| Post-compact still ≥ 90% | Soft fail: advise new session or drop large tool payloads |

### Max tool rounds — **Q1 locked**

| Setting | Default | Storage |
|---------|---------|---------|
| `tool_round_soft` | **65** | DB settings / CLI |
| `tool_round_hard` | **80** | DB settings / CLI |

At soft: advisory to use `schedule_continuation`, background tasks, or reply to user.  
At hard: stop like today’s max-iters error (message includes how to continue).

### Shell timeout policy (loop-aware)

1. Default timeout 60s when unspecified.  
2. If model requests >300s → clamp + tool error text: use `start_background_task`.  
3. If model requests 61–300s → allow, attach hint in result.  
4. Hung processes: killed with process group; never leave orphans if possible (`Setpgid`).

### Single-round / turn time — **Q2 locked**

| Cap | Default | Notes |
|-----|---------|-------|
| Per `shell_execute` | 60s–5m | As above |
| Soft wall for “no user-visible progress” | **3 minutes** of continuous tool rounds | Advisory |
| Hard turn context | **15 minutes** (existing) | Keep; document |

### Context usage ratio — **Q5 locked**

```
usage_ratio = est_prompt / (context_limit - max_output - context_reserve)
```

Same denominator as the budget frame used for history trim. Soft warn when `usage_ratio ≥ 0.60`.

### Parallel tool calls

**v1:** execute tool_calls **serially** in array order (deterministic, safer for edit_file read rules).  
**Later:** parallel for pure read tools only.

### `edit_file` / `apply_patch` prior-read — **Q22/Q23 locked**

Harness tracks `map[path]bool` of successful `file_read` in current turn.

- `edit_file`: requires prior read; fails if `old_string` not unique unless `replace_all` (**Q23**).
- `apply_patch`: prior-read required for **`update`** ops only; **`add`** / **`delete`** exempt (**Q22**).
- Rollback: temp files + rename; reverse ops for deletes (**Q24**).

### Background tasks + session close — **Q16/Q17/Q20 locked**

- Max **8** concurrent BG tasks per session.
- Do **not** survive harness restart; boot marks orphans dead.
- On session **close**: SIGTERM all session BG tasks, wait grace, SIGKILL; **cancel** pending continuations.

### Continuations firing — **Q18–Q21 locked**

When timer fires:

1. If session busy → requeue shortly or mark deferred.  
2. If closed → **cancelled** (no fire).  
3. Else inject prompt (role: `user` with prefix `[scheduled continuation]` or dedicated role) and `PostUserMessage`-equivalent.

Semantics: fire on **whichever comes first** of `delay_sec` and `wait_for_task` (require at least one). Max delay **24h**. Persist schedules to DB in normal mode; in-memory only when limp.

---

## Shell security (deep dive)

### Goals

- Configurable from **weak** (deny-by-default allowlist) to **powerful** (broad shell, minimal deny list).  
- No privilege escalation beyond parent process.  
- Operator-visible policy; stored in DB so it survives restarts and is inspectable.  
- Fail closed when disabled or deny matched.

### Settings keys (seed in `settings`) — **Q7–Q15 locked**

| Key | Default | Meaning |
|-----|---------|---------|
| `shell_enabled` | `true` | Master switch (also CLI `--disable-shell`) |
| `shell_mode` | `deny_list` | `deny_list` \| `allow_list` |
| `shell_deny_patterns` | JSON array of regexes | Matched against full command line |
| `shell_allow_patterns` | `[]` | Used when mode=allow_list |
| `shell_allow_sudo` | `false` | Must be true **and** sudo removed from deny to allow |
| `shell_default_timeout_sec` | `60` | |
| `shell_max_timeout_sec` | `300` | Hard ceiling |
| `shell_max_output_bytes` | `524288` | Capture cap (512 KiB); spill remainder to blob |
| `shell_cwd_strict` | `true` | cwd must stay in workspace |
| `shell_block_memory_paths` | `true` | Reject commands that clearly target `--memory` path (heuristic) |

CLI: `--disable-shell` (and optional future overrides). Shell binary: `/bin/bash -lc` if present, else `/bin/sh -c`.

**Deny seed (Q15b):** destructive disk, privilege escalation, power control. **Normal file deletion is allowed**; block catastrophic patterns such as `rm -rf /` (not ordinary `rm` of workspace files). Expand seed after first week of use.

### Permission inheritance

```
OS user of marble-harness
  └── child: bash -lc <command>   # or sh -c
        cwd = workspace (or subdir)
        env = filtered parent env + limited tool env overlay
        no new capabilities
```

Network: unrestricted in v1 (same as host user) — document trust model; true sandbox is a later ADR.

Document: if you launch Marble as root, the agent is root. **Don't.**

### What v1 is not

- Not a container/seccomp sandbox  
- Not multi-tenant isolation  
- Deny lists are **best-effort** (obfuscation can bypass)  

Trust model remains local/Tailscale operator (ADR-0001).

### Audit

Dual-write every shell invocation to `session_events` (command, cwd, timeout, exit, truncated out).  
Optional redaction later.

---

## System prompt / tool descriptions

Tool schemas must encode policies the model should follow:

- Prefer `edit_file` / `apply_patch` over full `file_write` for existing files.  
- Read before edit.  
- Long jobs → background + `schedule_continuation` or poll `check_background_task`.  
- Context high → `session_compact`.  
- Use `memory_write` for durable facts; don't rely on chat alone.

---

## API / UI surface (supporting)

| Addition | Purpose |
|----------|---------|
| SSE `attachment` | `attach_file` rendering |
| SSE `task` / `continuation` | BG + schedule status |
| Health / session detail | context usage %, open tasks |
| Settings UI (later) | shell deny list editor — not required for ADR accept |

---

## Package sketch

```
internal/
  tools/
    registry.go          # dispatch + specs
    fs_*.go              # read/write/list/grep/glob/summary/edit/patch/attach
    shell.go             # shell_execute + policy
    bgtask.go            # start/kill/check
    memory_tools.go
    skill_tools.go
    context_tools.go     # get_context_usage, session_compact helpers
  shellpolicy/           # load settings, match deny/allow
  bgtask/                # process groups, logs
  continuation/          # timers + DB persistence
  skills/                # discover/load
```

Agent loop changes live in `internal/session/loop.go` (+ budget helpers).

---

## Consequences

### Positive

- Real coding + ops workflows without leaving Marble.  
- Explicit context and round budgets reduce silent truncation / runaway loops.  
- Shell power is operator-tunable; defaults deny the worst foot-guns.  
- Continuations + BG tasks unlock multi-minute jobs without blocking the UX forever.  
- Memory/skills tools make “learn over time” actionable for the model.

### Trade-offs

- Large tool schema tax on context (mitigate: compact descriptions; optional tool groups later).  
- Deny-list shell is not a sandbox.  
- Atomic multi-file patch is complex to implement correctly.  
- Compaction quality depends on LLM + system-agent latency (extra model calls).  
- System agents add UI and lifecycle surface (kind, parent link, restricted tools).  
- More moving parts (timers, child processes, system sessions) → more daemon responsibilities.

### Risks

| Risk | Mitigation |
|------|------------|
| Prompt injection → shell | Deny list + never elevate; document; optional allow_list mode |
| Orphan processes | Process groups; session close cleanup; harness shutdown sweep |
| Runaway tool loops | Soft 65 / hard 80; wall clock advisories |
| Context blow-up from grep/shell | Truncation caps; max_matches |
| Half-applied patches | Rollback transaction design + tests |
| Memory path exfil via shell | Heuristic block + OS user isolation |

---

## Out of scope

- Vector/embedding RAG  
- True OS sandbox (bwrap/nsjail)  
- Multi-user auth on tools  
- MCP tool bridge  
- Language-aware `codebase_summary`  
- Interactive TTY / full PTY shell  
- Cross-session background tasks  
- GUI settings editor for shell policy (API/DB seed enough for v1)

---

## Implementation order (after acceptance)

1. Loop policy framework: soft/hard counters, advisory injection (+ DB log, no MD), raise hard default to 80.  
2. `get_context_usage` + **Q5** usage ratio plumbing (reported + est).  
3. **System agent** session kind + daemon scheduling hooks + left-pane System agents UI.  
4. `session_compact` → LLM summary via system agent; auto path at 85%×3 rounds.  
5. `grep`, `glob`, `codebase_summary` (read-only wins).  
6. `edit_file` + prior-read gate; then `apply_patch` with rollback tests.  
7. `shellpolicy` + `shell_execute` + DB seed settings.  
8. Background task registry + kill/check + session close cleanup.  
9. `schedule_continuation` (in-process first; then DB persist).  
10. Memory knowledge layout + search/fetch/write.  
11. Skills discovery + search/load.  
12. `attach_file` + UI SSE.  
13. Docs + integration tests for jail, rollback, shell deny, timeouts, compact agent.

---

## Open questions

**None blocking.** All Q1–Q30 locked — see **Decisions locked** and `adr/0005-answers.json`.

---

## Acceptance criteria

- [x] Q1–Q30 locked (loop, shell, BG/continuations, edit/patch, memory/skills, schema)  
- [x] Tool suite list accepted (names + responsibilities)  
- [x] Loop soft/hard numbers accepted (60% context, 65/80 rounds, shell 60s/5m, 3m wall)  
- [x] Shell security model accepted (inherit PID perms, DB settings, deny_list, Q15b note)  
- [x] System agents UI/daemon model accepted (Q4)  
- [x] Ready to implement in phases  

## Changelog

| Date | Change |
|------|--------|
| 2026-07-17 | Initial proposal |
| 2026-07-17 | Q1–Q6 locked; system agents + LLM compaction; advisory DB-only (no MD) |
| 2026-07-17 | Q7–Q30 locked from review answers; status → **Accepted** |

## Review ergonomics

- Interactive questions: `adr/0005-review.html` (uses `review-kit.js`)
- Structured answers file: `adr/0005-answers.json` (agent reads this on “collect answers”)
- Workflow docs: `adr/README.md`

## References

- ADR-0001 Harness inner loop  
- ADR-0002 Session memory  
- ADR-0003 SQLite + settings + limp  
- ADR-0004 Workspace explorer / `workspacefs` jail  
- Current loop: `internal/session/loop.go` (`MaxToolIters` default 25)  
- Current tools: `internal/tools/tools.go`  
