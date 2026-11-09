# Modern harness techniques — survey and Marble gap analysis

Research date: 2026-11-07. Sources: Claude Code sourcemap leak (2026-03-31) analyses, `sst/opencode`
source (cloned `main`, 2026-11), Cloudflare/Anthropic Code Mode write-ups, Playwright-MCP field reports.
Written after fixing a live MCP bug (below) — that bug is itself the strongest finding.

---

## 0. The bug found on the way in: every stdio MCP server has been dead since boot

`internal/mcp/stdio.go` started children with `exec.CommandContext(ctx, …)` where `ctx` is the
**dial** context. Both callers cancel that context the instant dialing returns:

- `cmd/marble-harness/main.go:129` — `cancelMCP()` right after `mcpMgr.Start(...)`
- `internal/api/settings.go:319-321` — `cancel()` right after `s.MCP.Reload(...)`

`CommandContext` kills the child on cancellation, so every stdio server died milliseconds after
logging `mcp: connected … tools=5`. The manager kept the session object, so later tool calls wrote to
a dead pipe → `write |1: broken pipe`. Cached `tools/list` metadata still answered `list_prompts`/
`list_resources`, so **health looked green while nothing was live**. This is a silent capability
loss, not a crash.

Repair (applied):
1. `exec.Command` — child lifetime owned by `Close()`, which already SIGTERM/SIGKILLs the process group.
2. Self-heal in `Manager.Execute` — dead-pipe detection (`EPIPE`/`EOF`/`connection reset`) triggers one
   respawn from the last known config (`m.cfg`) and a retry. OOM/crash of a child no longer fails a turn.
3. `internal/mcp/lifetime_test.go` — regression tests for both (`TestStdioSurvivesDialContextCancel`,
   `TestManagerRespawnsDeadChild`).

Still missing (recommended): a periodic `ping` per session, `ok:false` on the health endpoint when a
child is gone, and a UI note. Health that cannot go red is not health.

---

## 1. What the Claude Code leak actually teaches

The team's harness, per the leaked source, is not clever prompts — it is **bandwidth discipline**.

| Technique | Detail | Marble status |
|---|---|---|
| Stable/dynamic prompt boundary | `SYSTEM_PROMPT_DYNAMIC_BOUNDARY` splits the system prompt so the static half is byte-identical every turn and stays in the prompt cache; anything tagged `DANGEROUS_uncachedSystemPromptSection` is known cache-breaking | Static prompt already immutable (`SystemPrompt()`, ADR-0013) — good. Never inject per-turn context (git, memory) *ahead* of it |
| 3-layer memory | Index (~150 chars/line, always loaded) → topic files (on demand) → transcripts (grep only, never loaded) | Close: `memory_search` + `knowledge/`. Keep the index cheap; never dump content into it |
| Dedicated Grep/Glob/LSP tools | Structured results beat shelling out to `grep`/`rg` | Have them (native + gopls MCP) |
| 5 compaction strategies | Context overflow treated as the central engineering problem | One compact + auto-continue reserve |
| 25+ event hooks | Intercept/modify behavior at every stage | None — sinks are output-only |
| Subagent topology | fork / teammate / worktree; forked agents inherit parent context **byte-identically** so N forks cost ≈ 1 in cache terms | `call_agent_process` spawns fresh context each time |
| Permission by side-query ("critic") | Ask a model "is this command safe in this cwd for this intent?" instead of brittle allowlists | `jev_gate` exists but unused by the shell policy |
| Magic Docs | Idle subagent re-reads a `MAGIC DOC` file and updates *only that file* | Absent; cheap to add |
| Read-before-edit | Edits are exact-string matches; read first or the match fails | Enforced (`edit_file` requires prior `file_read`) |

---

## 2. opencode source: the concrete numbers worth copying

### 2.1 Read tool is *resumable* (`packages/core/src/tool/read-filesystem.ts`)
- `MAX_READ_LINES 2000`, `MAX_READ_BYTES 50 KiB`, first chunk 256 KiB.
- Per-line cap `MAX_LINE_LENGTH 2000` with an explicit marker
  `... (line truncated to 2000 chars)`.
- Returns `{content, offset, truncated, next}` — a truncated read tells the model **where to resume**.
- Media ingest capped 20 MiB; binary files rejected with a typed error rather than mojibake.

**Marble gap:** `internal/tools/registry.go` `clamp()` cuts at `--max-tool-result` (default 32 000
chars) with `…[truncated N of M chars]` and **no continuation**. The model must re-read from the top,
and each retry is a fresh 32k. This is the single cheapest performance fix available.

Recommended: on overflow, spill the full body to `.tool-results/<ts>-<tool>.txt`, return the head plus
`next_offset` and the path. Marble already has this pattern for event blobs (`db/events.go`).

### 2.2 Search reports truncation in one round trip (`tool/search.ts`)
Fetches `limit + 1` results so `truncated: true` is known without a second query. Trivial, copy it.

### 2.3 Edit tool (`tool/plugin/edit.ts`) — normalization is the win
Exact-string replace, but with layered fallbacks:
- `normalizeForMatch()` maps curly quotes → ASCII, en/em dashes → `-`, NBSP/thin spaces → space.
- `findLineOccurrences()` falls back to trimmed-line matching, tracking CRLF vs LF per line.
- CRLF/LF preserved per matched region (`withRegionEndings`) — no whole-file ending churn.
- `replaceAll` semantics; `oldString` must match exactly once when false.
- Returns a `FileDiff` so the model *sees* the patch, not the whole file.

**Marble gap:** `edit_file` is byte-exact. Every copy-paste of typographic punctuation into a prompt
turns into a failed match and a re-read. Normalizing on miss (never on hit) would remove a whole class
of tool-call waste. Returning a unified diff instead of "edited N lines" also saves the verify re-read.

### 2.4 Code Mode (`packages/core/src/codemode/`)
Tools exposed **as sandboxed JavaScript** inside one `execute` tool:
`const tab = await tools.browser.tabs.open({url}); return await tools.browser.snapshot({tabID: tab.id})`.
- Catalog is *partial*; a global `search({namespace})` finds the rest. No guessed tool names.
- Catalog updates are sent as **diffs of instruction lines** (`Instructions.diffByKey`) so the system
  prompt prefix stays cache-stable when tools appear/disappear.
- Reported effect (Cloudflare 2025-09, Anthropic): a Drive→Salesforce flow went 150 000 → 2 000 tokens.

**Warning, from the field:** DeepSeek's harness shipped `run_code` running model-written TS in an
*unsandboxed* worker thread, escaping its bwrap/Landlock file sandbox (repo issue #3245) — arbitrary
host read/write from an injected page. If Marble adds Code Mode, the executor must be sandboxed for
real (bwrap + no network by default), not "trusted because it is our tool".

### 2.5 Browser sandbox pattern (`packages/plugin-browser/`)
- Browser owned by the desktop over CDP; server owns tools/permissions. Tabs carry explicit `tabID`.
- **Snapshot + refs** (`e9`-style deterministic refs from the accessibility tree), screenshots only
  when needed — cheaper and more reliable than pixel coordinates.
- **Tunnel**: bounded TCP relay so the browser (on the desktop) reaches a **server-local** dev port
  (`localhost:8000` on the server, Chromium on the desktop). That is how a remote model tests a web
  app it just built without exposing the port.
- Bounded 5 MiB transfers; captures returned as file paths, bytes kept out of model context.
- Tools are **hidden until a browser attaches** — lazy tool registration.
- Diagnostics as tools: `console`, `network.list`, `lighthouse`, `trace.*`, `heap.*`.

**Marble gap:** the peer browser is exactly the daily-Chrome mirror path, but there is no headless
local sandbox and no tunnel, so testing web code means driving the operator's real browser. The
missing piece is the cheap loop: `localhost` dev server → snapshot refs → console errors → fix.

### 2.6 Prompts and compaction
- Base prompt is **15 lines**; per-model packs (`gpt.txt`, `anthropic.txt`, `kimi.txt`, `trinity.txt`)
  carry model-specific steering instead of one blob trying to please everyone.
- Compaction settings: `auto`, `buffer` (15% / ≥16k default), `keep` recent tokens verbatim, and
  **`precompute`** — summarize in a background fiber as the context nears the threshold so the swap is
  instant. Marble's auto-continue reserve is the same idea but only fires at the wall.

---

## 3. What Marble ships every turn (measured, for the tool-budget argument)

- System prompt: one ~10 KB static string (cache-friendly).
- Tool defs: filesystem, shell/bg, cron, sinks, memory/skills, mpub, attach, peer/computer (≈10),
  subprocess, MCP bridge helpers — dozens of schemas on **every** turn regardless of task.
- Tool results: 32 000-char head clamp, no resume, no spill.

opencode hides browser and skill tools until something attaches; its catalog is searchable. That is
the same lever as Code Mode: **the cheapest tool is the one the model never sees**.

---

## 4. Recommendations, in the order I would land them

**P0 — silent failure (done, plus follow-ups)**
1. ✅ `exec.Command` + self-heal respawn + tests.
2. Periodic `ping`, health that can go red, and a session note when a server dies.
3. Audit other `CommandContext` uses for the same lifetime mistake.

**P1 — token and latency wins, small diffs**
4. Resumable truncation: spill to `.tool-results/`, return `next_offset` + path.
5. Edit normalization on miss (quotes/dashes/NBSP) + return a unified diff.
6. `search`-style `limit+1` truncation flags across grep/glob/list.
7. Live repo context (branch, last 3 commits, dirty files) appended *after* the static prompt.

**P2 — capability**
8. Lazy tool groups: peer/computer, mpub, sinks, MCP bridge loaded on demand via a `tool_search`-ish
   loader; per-model prompt packs instead of one 10 KB blob.
9. Local browser sandbox: Playwright/CDP instance + loopback tunnel to the dev server, snapshot-ref
   first, console/network tools, artifacts spilled to files.
10. Code Mode `execute` with a real bwrap sandbox and a lazy catalog (only after the sandbox exists).
11. Precomputed compaction summary in a background goroutine near the threshold.
12. Cheap hooks: pre/post tool intercepts (deny, rewrite args, inject reminders) — Marble has the
    thrash/eval-mutate logic hardcoded in `registry.go` that a hook table would generalize.

---

## 5. Sources

- Engineer's Codex, *Diving into Claude Code's Source Code Leak* (2026-04-01) — prompt boundary, memory
  layers, magic docs, subagent fork caching, critic permission pattern.
- CNBC, *Anthropic leaks part of Claude Code's internal source code* (2026-03-31).
- `github.com/sst/opencode` — `packages/core/src/tool/{read-filesystem,search,plugin/edit}.ts`,
  `packages/core/src/codemode/{instructions,catalog}.ts`, `packages/plugin-browser/README.md`,
  `packages/core/src/session/{compaction.ts,runner/prompt/system.txt}`, `plugin/system-prompt/*.txt`.
- Cloudflare, *Code Mode: the better way to use MCP* (2025-09); Anthropic code-execution-with-MCP note;
  Arize/GetMaxim Code Mode analyses (90–98% token reductions).
- `deepseek-ai/deepseek-harness` discussion #3245 — Code Mode sandbox escape.
- Playwright-MCP guides (QASkills, accessibilitychecker) + freeCodeCamp *How to make your coding agent
  check its work in the browser* — accessibility-tree refs over pixels, isolated profiles, `--caps`.
