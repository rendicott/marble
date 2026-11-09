# ADR-0040: Tool Result Continuation and Edit Normalization

| Field | Value |
|-------|-------|
| **Status** | **Proposed** |
| **Date** | 2026-11-07 |
| **Author** | grok |
| **Deciders** | Project owner |
| **Tags** | tools, truncation, edit, normalization, continuation, prompt-cache |
| **Extends** | ADR-0005 (tools and agent loop — Q22/Q23 prior-read gate, `edit_file`/`apply_patch` semantics), ADR-0006 (MCP bridge) |
| **Evidence** | docs/modern-harness-techniques.md section 4 P1 items 4 and 5; opencode read/edit tools |

## Context

Current tool results are clamped at --max-tool-result (default 32000 chars, internal/config/config.go:145). internal/tools/registry.go:350-355 implements clamp(s, max) which returns s unchanged when len(s) <= max, else s[:max] + "\n\n…[truncated %d of %d chars]" (max, len). The cut may land mid-rune and mid-line. No continuation or spill mechanism exists.

internal/tools/registry.go Execute() applies clamp() to every tool path, including the MCP branch (r.MCP.Execute(...) result is clamped).

file_read (internal/tools/fs_basic.go:13-14) already accepts Offset and Limit int args and applies them
(fs_basic.go:40-51): a line-range read is possible today. What is missing is the other half — when
clamp() cuts the *returned text*, the model is never told where the cut landed, so it cannot compute the
next Offset and must re-read from line 1. The spill + `next_offset` marker supplies that missing half
without changing the Offset/Limit contract.

editFile (internal/tools/edit.go:17) enforces a strict read-before-edit gate at :24: if !wasRead(tc, a.Path) → "edit_file requires a successful file_read on %q earlier in this turn". Patch application at :141 requires prior read and uses strings.Count(text, a.OldString) for matching: count 0 → "old_string not found in %s"; count > 1 with replace_all=false also errors. Matching is byte-exact.

internal/db/events.go:30 AppendEvent already spills oversize content to blobs with marker "…[truncated; full content in blob]" at :135. This provides precedent for spilling to storage.

The reference pattern from opencode (read-filesystem.ts) uses MAX_READ_LINES 2000, MAX_READ_BYTES 50 KiB, per-line cap of 2000 chars with explicit "... (line truncated to 2000 chars)" marker, and returns {content, offset, truncated, next}.

These limitations cause prompt bloat on large outputs, broken UTF-8, incomplete model context on truncation, and fragile edit matching on files containing smart quotes, dashes, or varied whitespace. Prompt-cache invalidation occurs on any result change. No unified diff is produced on edits.

This ADR defines continuation for large tool results via spills and normalization for edit matching that preserves exactness on hits while improving robustness.

## Decision D1 — Tool result continuation via spill files

### Interface (D1)

Every tool result that exceeds the configured clamp limit is split into a head (the clamped portion)
plus a marker block. The marker is a model-readable block, one key per line:

```
…[truncated; see spill]
spilled_path: .tool-results/abc123-spill-001.txt
spilled_bytes: 45231
next_offset: 31987
total_bytes: 77218
```

- Offset units are byte offsets. Because clamp() truncates on len() (bytes in Go), resuming by bytes is exact and cheap. Offsets are line-aligned: scan forward from the clamp point to the next newline so the model never receives a half-line. Never split a UTF-8 rune: scan backward from the clamp point to a valid rune boundary before cutting.
- The resume mechanism reuses the existing file_read Offset and Limit arguments on the spill path. For non-file tools the marker names the spill_path so file_read can be called on it. A new continue_result token is explicitly rejected because it would require new client-side plumbing, break MCP compatibility, and duplicate the already-supported Offset/Limit on file_read.
- Spill location is workspace .tool-results/ (directly visible to the jail, readable by file_read with no new jail rule). $MEMORY is rejected because it lives outside the workspace, would require new permission rules, and would not be visible to normal file_read.
- Retention/GC: at most 50 files or 100 MiB total, age-based sweep (delete files >24h old). Sweep runs at harness startup and after every spill. Spill files never enter the transcript or the prompt; only the head + marker appear in events.
- Cache safety: results under the cap remain byte-identical to today, so the prompt prefix and cache key are unchanged.
- MCP bridge (mcp_*) and peer computer_exec: their results already pass through the same `clamp()` in
  `Registry.Execute`, so they inherit the marker with no protocol change. Both are server-side spills —
  the child's stdout and the peer's stdout are captured whole before clamping, so the spill is written
  by the harness, not by the remote end.

| Tool | Spills? | Notes |
|------|---------|-------|
| file_read | Yes | Uses Offset/Limit directly; returns next_offset when truncated |
| grep | Yes | Large matches spill to one file per match set |
| glob | No | Always small |
| list_files | No | Always small |
| shell | Yes | Stdout/stderr spills independently |
| shell background tail | Yes | Only the tail portion; full log in spill |
| web_fetch | Yes | Body spills; headers stay in head |
| MCP bridge (mcp_*) | Yes | Backend result is clamped then spilled identically |
| peer computer_exec | Yes | Remote output spilled locally |

This design reuses existing file_read, follows the db/events.go spill precedent, and keeps prompt-cache behavior identical for sub-cap results.

## Decision D2 — Edit normalization on miss only

### Interface (D2)

Normalization applies ONLY on a miss (`strings.Count(text, a.OldString) == 0`). A hit remains
byte-exact as today; no rewriting of the on-disk file occurs.

Today `editFile` returns `"edited %s (%d replacement(s))"` (internal/tools/edit.go:53) — a claim, not
evidence, so the model usually re-reads the file to check. D2 replaces that string with the unified diff
below.

Glyph substitution table (applied only to the search key and to candidate lines for matching):

- U+2018, U+2019 → '
- U+201C, U+201D → "
- U+2013, U+2014 → -
- U+00A0, U+2009, U+202F → space
- U+2026 → ...

Matching first tries exact byte match. On miss it tries glyph-normalized forms. If still no match it falls back to trimmed-line matching: ignore leading/trailing whitespace per line, require equal line counts between old_string and candidate. The error message reports which fallback tier fired (e.g. "normalized glyph match failed, trying trimmed-line fallback").

Per-region line-ending preservation: withRegionEndings() (inspired by opencode) keeps CRLF in regions that originally used CRLF and LF elsewhere. The file is written exactly with the line endings the model supplied in its edit; no silent normalization of file content.

When more than one candidate matches (after normalization and fallback), error with line numbers and a 40-char excerpt for each; never guess. The message suggests using replace_all=true.

Ordering: the wasRead gate (internal/tools/edit.go:24) is evaluated FIRST and unchanged. Normalization never bypasses the read-before-edit requirement.

Both `edit_file` and `apply_patch` will return a unified diff with 3 lines of context and ---/+++
headers carrying byte counts. The diff itself is capped at 4 KiB; past the cap it is truncated with a
note ("…[diff truncated at 4 KiB]") but the edit still proceeds. `apply_patch` keeps its existing
`reverseOp` rollback (internal/tools/edit.go:79-105): normalization is attempted per file, and a single
failure in any hunk aborts the whole patch with full rollback — no partial writes.

This makes edit matching robust to common typography while preserving exact semantics on successful matches and providing clear diagnostics.

## Alternatives Considered

- New continue_result tool: rejected because it duplicates file_read Offset/Limit, adds client complexity, and breaks MCP uniformity.
- Spill to $MEMORY: rejected; lives outside workspace, requires new jail rules, invisible to file_read.
- Always normalize file content on write: rejected; would mutate source files silently and break exact-match expectations.
- Line-based truncation only: rejected; byte offsets are cheaper and exact given Go's len().
- Raise default max-tool-result: rejected; large prompts still hurt latency and cache hit rate.

## Compatibility & Migration

Results under the clamp remain byte-identical; prompt-cache keys unchanged. Existing agents using Offset/Limit on file_read continue to work. Edits that already succeeded stay successful. New error messages for multi-match and fallback tiers are the only observable change for failing edits. No migration required.

## Test Plan

- Unit tests for clamp with rune boundaries, line alignment, and marker format.
- Integration tests: large file_read, shell ls /dev/urandom, web_fetch of big page; verify marker, spilled file, resume via Offset.
- Edit tests: smart quotes, em-dashes, mixed line endings, multi-candidate cases, trimmed-line fallback.
- wasRead gate remains enforced before any normalization.
- Diff cap and truncation behavior.
- GC sweep at startup and after spills; verify .tool-results/ stays under limits.
- Prompt-cache hit rate before/after on sub-cap results.
- MCP round-trip of spill marker.

## Risks

- Spill directory growth if GC fails (mitigated by dual limits and startup sweep).
- Model confusion from marker block (mitigated by clear one-line key format and precedent from opencode).
- False positives in normalized matching (mitigated by exact-first policy and explicit tier reporting).
- Diff truncation hiding important context (4 KiB chosen to cover typical edits).

## Rollout

1. Implement spill path and marker in registry.go + fs_basic.go.
2. Add normalization helpers and update edit.go.
3. Update tests and render_docs.py if needed.
4. Deploy via harness rebuild and user systemd restart.
5. Monitor first 100 turns for spill frequency and edit success rate.

## Open Questions

- Should spill files be automatically cleaned on turn end if referenced only once?
- Exact default for .tool-results/ GC limits (50 files / 100 MiB proposed).
- Should web_fetch JSON bodies receive structural truncation instead of raw spill?
