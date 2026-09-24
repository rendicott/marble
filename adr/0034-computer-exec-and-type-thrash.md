# ADR-0034: computer_exec, Peer State Legibility, and Type-Class Thrash Guard

| Field | Value |
|-------|--------|
| **Status** | **Accepted** (implemented) |
| **Date** | 2026-09-23 |
| **Author** | — |
| **Deciders** | Project owner |
| **Tags** | computer-use, peer, anti-thrash, tools |
| **Extends** | ADR-0020 / 0021 (peer computer-use), ADR-0022 (long-turn efficiency, anti-thrash / escalate lock) |
| **Evidence** | Field report `peer-gui-loop-report` (Marble session `0wefnnqmpq`, peer `rwin`, marble-peer v0.1.2): ~150 tool iterations, 0 command outputs actually read, retyping the same PowerShell command into a window whose output could not be confirmed as landed or not |

## Overview

An agent task ("install the Orb Windows tray client on peer `rwin`, verify it, build a CQA gate")
degenerated into a ~150-iteration loop of `computer_screenshot` → `computer_desktop_act
action=type` → `computer_screenshot`, because the only way to read peer state (config, logs,
installed software) was to open a PowerShell window and read a screenshot of it. A terminal window
is ~40 lines tall; any command whose output exceeded that, or produced none, was indistinguishable
from a command that never ran. Separately, `shell_execute`'s tool description did not say it runs on
the harness host — ~10 calls were spent discovering that a `curl -o "C:\Users\..."` against it wrote
a file literally named `C:UsersPublic...` into the harness workspace instead of downloading anything
on the peer. See the full report for the complete root-cause breakdown.

This ADR records the fixes made in response, split across `marble` (this repo) and
`marble-desktop-peer`.

## Decisions

### marble-desktop-peer

1. **New `computer_exec` peer action kind (P0).** Runs a command via `internal/shellexec`
   (`powershell.exe` on Windows, `bash`/`sh` elsewhere) and returns `stdout`/`stderr`/`exit_code` as
   text. This is the actual fix — the three diagnostic commands the report says should have run first
   (`Get-Content config.json`, `Get-Content orb.log -Tail 40`, `Get-ChildItem`) become three
   `computer_exec` calls instead of an unbounded pixel loop. Advertised via `caps.exec` (new field on
   `protocol.Caps`, mirrored in `StatusJSON` and the pairing `caps` map).
2. **`screenshot` / `desktop_click` now include best-effort `window_title` / `focused_app` in `meta`**
   (P2) — answers "did my keystrokes land where I think?" without a second call. Linux via `xdotool
   getactivewindow`, macOS via System Events, Windows via `GetForegroundWindow` +
   `QueryFullProcessImageNameW`.
3. **`desktop_type` now returns an atomic post-type screenshot**, the same pattern `desktop_click`
   already had (P2) — a stuck/unfocused field is visible immediately instead of retyping into it
   silently.

Deferred: `computer_read_text` (OCR / accessibility-tree text for a screen region) — the report's P1.
`computer_exec` already closes the specific failure class this session hit; a general OCR primitive
is a larger, separately-scoped effort (platform accessibility APIs differ enough per-OS that it
deserves its own design pass, not a rider on this fix).

### marble (this repo)

4. **`computer_exec` tool** — spec, handler (`computerExec` in `internal/tools/computer.go`), registry
   dispatch. Mirrors `shell_execute`'s shape (`command`/`cwd`/`timeout_sec` → `stdout`/`stderr`/
   `exit_code`) so the model can reuse what it already knows about that tool.
5. **`computer_bind` now returns `os`/`caps`** (P2) — previously only `computer_id`/`session_id`;
   `computer_list` already had these per-computer, `bind` just didn't echo them back. Resolved via the
   existing `ListComputers()` callback, no new peer round trip.
6. **`shell_execute`'s tool description now states it runs on the harness host, not any bound peer**
   (P0 — the report calls this the single highest-ROI-per-effort fix: "one sentence... would have
   saved ~10 calls"). `shell_execute` also now appends a warning line to its output when the command
   contains an obvious Windows-style path (`C:\...`) while the harness host is not Windows — this is
   exactly the failure mode from the report (backslashes silently consumed by the POSIX shell).
7. **ADR-0022 escalate lock (`isComputerClickClass`) now covers `type`, not just `click`.** The lock
   existed and was engaging (via the generic `ComputerFailStreak` path) but its hard-block gate only
   matched `action=="click"` for `computer_desktop_act` — so a locked turn still let `type` through
   without limit. This was the actual mechanism of the 150-iteration loop: the guard existed, it just
   didn't cover the action class that thrashed.
8. **New near-duplicate-type detector**, the `type` analog of the existing near-duplicate-click check:
   the same typed text repeated (2+ prior identical fingerprints) trips `EscalateLock` immediately,
   independent of `AntiRepeatN` (which defaults to off). Click already had this; type did not, which is
   why repeated identical `type` calls with `OK:true` responses never tripped the generic fail-streak
   path either (a "successful" keystroke that lands nowhere is not, by any local signal, a failure).
9. **`computerDesktopAct`'s post-click screenshot/`ui_unchanged` handling is generalized** into
   `attachPostActionScreenshot`, shared by `click` and `type`, so `type` gets the same "see the result
   immediately, and `ui_unchanged` means it silently didn't land" treatment `click` already had.
10. System prompt and tool descriptions updated to steer the model at `computer_exec` for reading peer
    state instead of typing into a terminal and screenshotting it.

## Deferred / explicitly out of scope

- **`computer_read_text` (OCR/accessibility text)** — see above; needs its own design pass.
- **Cross-tool loop detection generalized beyond computer_desktop_act** (the report's harness P1: "the
  iteration cap caught this at 190, but only after ~150 wasted calls") — the type-class fixes above
  (items 7–9) close the specific loop this session hit. A more general early-intervention mechanism
  across arbitrary tool pairs is a bigger change to `internal/tools/thrash.go` and is left for a
  follow-up ADR if repeated in another failure class.
- **The CQA release-verification gate** (report §5: fetch `SHA256SUMS`, download clients, verify hash,
  run `orb-mcp -version`, publish/read back a scratch topic) — this is `orb`-repo/product work, not a
  `marble`/`marble-desktop-peer` harness or peer change, and is out of scope for this ADR.

## Verification

- `marble-desktop-peer`: `go build` (linux), `go vet` cross-compiled for `GOOS=windows` and
  `GOOS=darwin`, `go test ./...` (all packages green, including new `internal/shellexec` tests:
  echo/non-zero-exit/empty-command/timeout).
- `marble`: `go build ./...`, `go vet ./...`, `go test ./...` (all packages green), plus new tests
  `TestEscalateLockBlocksType`, `TestNearDupDesktopType`, `TestShellExecuteWarnsOnWindowsPath` in
  `internal/tools`.
- Not yet done: an end-to-end run against a real bound peer (this dev box has neither a live peer nor
  Windows/macOS to test against — see `marble-peer-windows-ci-verification` precedent: real
  verification for peer-facing changes happens via a draft PR's `windows-latest` CI job, read
  verbosely, not locally).
