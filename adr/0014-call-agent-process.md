# ADR-0014: `call_agent_process` — Drive External Agent Harnesses

| Field | Value |
|-------|--------|
| **Status** | Proposed |
| **Date** | 2026-07-18 |
| **Deciders** | Project owner |
| **Tags** | tools, subprocess, agents, claude-code, grok-build, orchestration |
| **Extends** | ADR-0005 (tools / shell / BG), ADR-0010 (turn cancel / timeouts), ADR-0001 (inner loop) |

## Context

Marble is strong at **ergonomics**: multi-session UI, memory, soul, MCP, turn progress, workspace jail, mpub. Other harnesses (e.g. **Claude Code**, **Grok Build**) are strong at **deep coding loops**, specialized tools, models, and permission UIs.

Operators want **both**: stay in Marble’s chat UX while occasionally **delegating hard work** to an external headless agent process, then fold the result back into the Marble session.

Both ecosystems already expose **print / single-shot** style CLIs suitable for subprocess driving:

| Harness | Typical headless entry | Notes |
|---------|------------------------|--------|
| **Claude Code** | `claude -p "…"` (print / non-interactive) | Common pattern for scripted one-shot agent runs; flags evolve — driver must version-check |
| **Grok Build** | `grok -p "…" --output-format json` (also `--single`) | Confirmed locally: `-p, --single` “prints the response to stdout and exits”; `--output-format` ∈ `plain`, `json`, `streaming-json`; also `grok agent`, `--cwd`, `--max-turns`, `--permission-mode`, `--always-approve`, etc. |

**Similarities:** prompt in → agent work → text/JSON out; cwd matters; long runtime; need timeouts/cancel.  
**Differences:** flag names, permission models, output schemas, auth, streaming, resume/session ids.

A **generic tool** with a **driver** argument keeps Marble from hard-coding one vendor forever.

## Goals

1. Add a first-class tool **`call_agent_process`** that spawns a configured external agent CLI.  
2. **`format` / driver** argument selects an adapter (`claude`, `grok`, later others).  
3. Capture **stdout/stderr**, exit code, duration; return a **normalized result** to the Marble model.  
4. Prefer **structured output** when the driver supports it (`json` / streaming later).  
5. Honor **workspace cwd**, **timeouts**, **turn cancel** (ADR-0010), and shell-like **safety** (allowlist of formats, no arbitrary shell).  
6. Preserve Marble UX: tool bubble + optional progress while the child runs.

## Non-goals (v1)

- Embedding Claude/Grok SDKs as libraries (subprocess CLI only).  
- Fully interactive TUI attach from the browser.  
- Bidirectional multi-turn “remote control” of a live TUI session (resume/continue is open Q).  
- Replacing Marble’s own agent loop.  
- Arbitrary `exec` of unregistered binaries.

## Decision (proposed)

### 1. Tool name & schema

```json
{
  "name": "call_agent_process",
  "description": "Run an external coding agent harness (headless) and return its result. Use for deep multi-file coding, refactors, or tasks better handled by Claude Code or Grok Build. Prefer Marble tools for simple FS/shell. Requires format (driver) and prompt.",
  "parameters": {
    "type": "object",
    "properties": {
      "format": {
        "type": "string",
        "enum": ["grok", "claude"],
        "description": "Driver / harness family"
      },
      "prompt": {
        "type": "string",
        "description": "Task prompt for the external agent"
      },
      "cwd": {
        "type": "string",
        "description": "Working directory relative to Marble workspace (default: workspace root)"
      },
      "output_format": {
        "type": "string",
        "enum": ["plain", "json"],
        "description": "Preferred child output (driver maps to CLI flags). Default json when supported, else plain"
      },
      "timeout_sec": {
        "type": "integer",
        "description": "Wall timeout (clamped by harness max)"
      },
      "extra_args": {
        "type": "array",
        "items": { "type": "string" },
        "description": "Optional allowlisted extra CLI args (driver-validated)"
      }
    },
    "required": ["format", "prompt"]
  }
}
```

Name is intentionally **generic** (`call_agent_process` + `format`), not `call_claude` / `call_grok`.

### 2. Driver interface (in-process adapters)

```text
Driver {
  Name() string                    // "grok" | "claude"
  ResolveBinary() (path, error)    // PATH or config override
  BuildCmd(req) (argv, env, cwd)   // argv only — no shell
  ParseResult(stdout, stderr, code) NormalizedResult
  SupportsJSON() bool
}
```

**Normalized result (tool return text / JSON):**

```json
{
  "format": "grok",
  "ok": true,
  "exit_code": 0,
  "duration_ms": 12345,
  "cwd": "/abs/path",
  "summary": "…primary assistant text…",
  "raw": { },
  "stderr_tail": "…"
}
```

Drivers map vendor JSON → `summary` + optional `raw`.

### 3. Example argv (illustrative; exact flags owned by driver + version)

**Grok Build (confirmed shape):**

```bash
grok -p "$PROMPT" --output-format json --cwd "$CWD" [--max-turns N] …
# equivalent: grok --single "$PROMPT" --output-format json
```

**Claude Code (ecosystem convention — verify at implement time):**

```bash
claude -p "$PROMPT" [--output-format json] …
# or: claude --print "$PROMPT"
```

Drivers may probe `--help` / version once and cache capability bits.

### 4. Configuration

**Rec:** `$MEMORY/agent_process.json` (or Settings subsection later):

```json
{
  "drivers": {
    "grok": {
      "enabled": true,
      "command": "grok",
      "default_output_format": "json",
      "default_args": ["--permission-mode", "acceptEdits"],
      "env": {}
    },
    "claude": {
      "enabled": true,
      "command": "claude",
      "default_output_format": "plain",
      "default_args": [],
      "env": {}
    }
  },
  "max_timeout_sec": 900,
  "default_timeout_sec": 300,
  "max_output_bytes": 1048576
}
```

- Missing binary ⇒ clear tool error (“install claude / grok or set command path”).  
- Secrets stay in process env / operator login for those CLIs — Marble does not store API keys for them in v1.

### 5. Safety

| Concern | Proposal |
|---------|----------|
| Binary | Only registered drivers; resolve via config + PATH |
| Args | Built by driver; `extra_args` filtered against allowlist |
| cwd | Must resolve **inside** Marble workspace (same as shell jail) |
| Timeout | Default 5m, max e.g. 15m; turn cancel kills process group |
| Concurrency | Soft cap (e.g. 1–2 concurrent external agents per session) — open Q |
| Permissions | Document that child may edit files under cwd with its own policy; Marble does not sandbox the child beyond cwd + process kill |
| Output | Truncate to max tool result; spill large stdout to blob if needed |

**Not** routed through `shell_execute` string — dedicated tool avoids shell injection and policy noise.

### 6. UX / progress

- While running: turn progress shows tool `call_agent_process` / phase `running_tool` (existing ADR-0010).  
- Optional later: stream child `streaming-json` into progress steps (v2).  
- v1: wait for process exit; return aggregated result.

### 7. System prompt guidance

Short policy:

```text
External agents: use call_agent_process(format=grok|claude, prompt=…) for large multi-file coding or harness-specific strengths. Prefer Marble tools for simple reads/edits/shell. Pass a clear self-contained prompt and cwd under the workspace. Summarize the external result for the user; do not re-run blindly on failure — inspect stderr_tail.
```

### 8. Relationship to existing tools

| Tool | Use when |
|------|----------|
| Marble FS / edit / shell | Small, controlled changes |
| MCP / web_fetch | Research |
| **`call_agent_process`** | “Bring a bigger agent” for a scoped task |
| `start_background_task` | Long shell jobs **without** agent loop |

## Architecture

```
Marble agent loop
    │
    ├─ tools.Execute("call_agent_process", …)
    │       │
    │       ▼
    │  DriverRegistry.Get(format)
    │       │
    │       ├─ grokDriver.BuildCmd → exec.CommandContext (Setpgid)
    │       └─ claudeDriver.BuildCmd → exec.CommandContext
    │
    ▼
NormalizedResult → tool_result → model continues in Marble
```

## Open questions

### Scope & API

1. **Tool name:** `call_agent_process` vs `run_agent` vs `delegate_agent`?  
   *Rec: `call_agent_process`.*  
2. **Driver key name:** `format` vs `driver` vs `harness`?  
   *Rec: `format` (your term) or `driver` — pick one; rec **`format`** for user wording.*  
3. **v1 drivers:** grok + claude both, or grok-first (binary present here)?  
   *Rec: both interfaces; enable whichever binary resolves.*  
4. **Default `output_format`:** json when supported else plain?  
   *Rec: yes.*  
5. **Streaming:** v1 wait-only vs stream stderr/progress?  
   *Rec: wait-only v1; design ParseResult for future streaming-json.*  

### Process & safety

6. **Auto-approve:** pass grok `--always-approve` / claude equivalent by default?  
   *Rec: configurable default_args; document risk; default conservative or acceptEdits — open.*  
7. **Max concurrent external agents per session?**  
   *Rec: 1 in v1.*  
8. **Kill on Marble turn Stop?**  
   *Rec: yes — process group SIGTERM→SIGKILL (same as shell).*  
9. **Network/FS sandbox for child?**  
   *Rec: defer; rely on child CLI sandbox flags if operator sets them in default_args.*  

### Product

10. **Resume / continue external session** (`-c`, `--resume`)?  
    *Rec: defer v1; always new headless run.*  
11. **Config location:** `$MEMORY/agent_process.json` vs Settings UI?  
    *Rec: file v1; Settings later.*  
12. **Expose in system agents?**  
    *Rec: yes if enabled (same tool registry); optional disable for system kind.*  
13. **Cost / model selection** passthrough (`--model`)?  
    *Rec: optional field or extra_args allowlist.*  
14. **Worktree isolation** (grok `--worktree`)?  
    *Rec: optional driver feature via extra_args or dedicated flag later.*  

## Implementation sketch (post-accept)

1. `internal/agentproc/` — Driver interface, config load, exec helper (PTY? no — pipes).  
2. `grok` + `claude` drivers with BuildCmd/ParseResult.  
3. Wire `call_agent_process` in tools registry + specs.  
4. Config example + README.  
5. System prompt line.  
6. Tests: mock driver; cwd jail; timeout kill; JSON parse fixture.  
7. Manual: `call_agent_process(format=grok, prompt="…")` against real CLI.

## Consequences

### Positive

- Marble remains the **operator cockpit**; deep harnesses become **power tools**.  
- Generic format/driver model extends to future CLIs (Codex, Aider, …) without new tool names.  
- Clear separation of UX vs execution horsepower.

### Trade-offs

- Child agents can change the workspace aggressively.  
- CLI flag drift requires driver maintenance.  
- Long wall times vs Marble’s soft tool-round budgets (timeouts + progress).

### Risks

| Risk | Mitigation |
|------|------------|
| Interactive auth hang | Headless flags; timeout; clear error |
| Runaway cost | max_turns / timeout; concurrency 1 |
| Prompt injection into child | Operator trust; cwd jail; no secrets in prompt by default |
| Binary missing | Actionable tool error |

## Acceptance criteria

- [ ] Open questions answered or defaulted  
- [ ] Driver interface + config shape agreed  
- [ ] Safety (cwd, kill, allowlist) agreed  
- [ ] Ready to implement  

## References

- Grok Build CLI: `grok -p/--single`, `--output-format plain|json|streaming-json`, `grok agent`, permission modes  
- Claude Code: `claude -p` / print mode (verify flags at implement)  
- ADR-0005 shell / BG process patterns  
- ADR-0010 turn cancel / process group kill  
