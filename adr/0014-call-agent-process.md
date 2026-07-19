# ADR-0014: `call_agent_process` — Drive External Agent Harnesses

| Field | Value |
|-------|--------|
| **Status** | Accepted / Implemented |
| **Date** | 2026-07-18 |
| **Deciders** | Project owner |
| **Tags** | tools, subprocess, agents, claude-code, grok-build, orchestration |
| **Extends** | ADR-0005 (tools / shell / BG), ADR-0010 (turn cancel / timeouts), ADR-0001 (inner loop) |
| **Answers** | `adr/0014-answers.json` (`2026-07-19T00:36:16.626Z`) |

## Context

Marble is strong at **ergonomics** (sessions, memory, soul, MCP, progress UI). External harnesses (**Claude Code**, **Grok Build**) are strong at **deep coding loops**. Operators want both: stay in Marble while delegating hard work to a headless external agent, then fold results back.

Headless entry points (illustrative):

| Harness | Headless |
|---------|----------|
| **Grok Build** | `grok -p "…" --output-format json` |
| **Claude Code** | `claude -p "…"` (verify flags at implement) |

## Goals

1. Tool **`call_agent_process`** with **`format`**: `grok` | `claude` (**Q1**, **Q2**).  
2. Both drivers in v1 if binary resolves (**Q3**).  
3. Prefer **json** output when supported (**Q4**).  
4. **Auto-approve** by default — never block on interactive permission prompts (**Q6**).  
5. **High timeouts**; tool designed for **background-task** usage; system prompt nudges `start_background_task` for long runs (**Q5**).  
6. Cap **10** concurrent/tracked external agents per session (**Q7**).  
7. Turn **Stop** kills process group (**Q8**).  
8. **No OS sandbox**; easy **dedicated directory** under workspace for the child (**Q9**).  
9. Config: `$MEMORY/agent_process.json` (**Q11**).

## Non-goals (v1)

- Resume/continue external sessions (**Q10** deferred)  
- Dedicated git worktree flag (**Q14** — use `extra_args` / grok `--worktree`)  
- Settings UI for agent_process (**Q11** later)  
- Streaming child stdout into turn steps (wait for exit)  
- Arbitrary shell exec  

## Decision

### Tool

```json
{
  "name": "call_agent_process",
  "parameters": {
    "format": { "enum": ["grok", "claude"] },
    "prompt": { "type": "string" },
    "cwd": { "type": "string", "description": "Rel path under workspace; default root" },
    "workdir": {
      "type": "string",
      "description": "Optional dedicated subdir under workspace created if missing (easy isolation — Q9)"
    },
    "output_format": { "enum": ["plain", "json"] },
    "timeout_sec": { "type": "integer" },
    "model": { "type": "string" },
    "extra_args": { "type": "array", "items": { "type": "string" } },
    "background": {
      "type": "boolean",
      "description": "If true, spawn via background-task style and return task id immediately (preferred for long runs — Q5)"
    }
  },
  "required": ["format", "prompt"]
}
```

**Rec implementation note for Q5:**  
- Tool supports **sync wait** (default for short probes) **and** **`background: true`** (or always recommend BG) that uses the existing BG task machinery / same process group tracking.  
- **Default timeout** high (e.g. **15–30 minutes**); max higher (e.g. **2 hours**) via config.  
- System prompt: *for non-trivial external agent work, call with background=true (or start_background_task wrapping) so Marble turn is not blocked for half an hour.*

### Drivers

- **grok** + **claude** adapters; enable if `command` resolves on PATH (**Q3**).  
- Default argv includes **auto-approve / non-interactive permission** flags so the child never waits on TTY (**Q6**).  
  - Grok: e.g. `--always-approve` and/or `--permission-mode bypassPermissions` (confirm best non-blocking combo at implement).  
  - Claude: equivalent print/non-interactive permission flags (verify at implement).  
- Default `output_format`: **json** if driver supports, else **plain** (**Q4**).  
- Optional **`model`** field or allowlisted **`extra_args`** (**Q13**).  
- **No resume** — always new headless run (**Q10**).

### Dedicated directory (Q9)

- No OS sandbox.  
- First-class **`workdir`** (or `dedicated_dir`) argument: resolve under workspace, **mkdir -p**, set child **cwd** there.  
- Makes “give the agent its own folder” one field without inventing shell mkdir steps.  
- Still no dedicated git worktree flag (**Q14** deferred).

### Concurrency & cancel

- **Max 10** external agent processes per Marble session (**Q7**).  
- Marble turn **Stop** → SIGTERM/SIGKILL process group (**Q8**).  
- BG mode: `KillSession` / existing BG kill paths also terminate children.

### Config (`$MEMORY/agent_process.json`)

```json
{
  "drivers": {
    "grok": {
      "enabled": true,
      "command": "grok",
      "default_output_format": "json",
      "default_args": ["--always-approve"],
      "auto_approve": true
    },
    "claude": {
      "enabled": true,
      "command": "claude",
      "default_output_format": "json",
      "default_args": [],
      "auto_approve": true
    }
  },
  "default_timeout_sec": 1800,
  "max_timeout_sec": 7200,
  "max_per_session": 10,
  "max_output_bytes": 1048576,
  "system_agents_enabled": false
}
```

System agents: tool available on **user** sessions; **optional disable** for system agents via config (**Q12** — default off for system).

### System prompt nudge

```text
External agents: use call_agent_process(format=grok|claude, prompt=…) for large multi-file coding.
For long runs set background=true (or use start_background_task) — do not block the Marble turn on multi-minute jobs.
Use workdir for a dedicated subfolder under the workspace. Prefer Marble tools for simple edits.
Auto-approve is on for the child; scope the prompt and workdir carefully.
```

## Decisions locked (Q1–Q14)

| ID | Decision |
|----|----------|
| **Q1** | Tool name: **`call_agent_process`** |
| **Q2** | Driver key: **`format`** (`grok` \| `claude`) |
| **Q3** | Both drivers in v1 if binary resolves |
| **Q4** | Prefer **json**, else plain |
| **Q5** | Wait-capable tool; **BG is the normal path** — high timeouts + prompt nudge to use as bgtask / `background=true` |
| **Q6** | **Default auto-approve** (non-blocking) |
| **Q7** | Cap **10** per session |
| **Q8** | Stop kills process group |
| **Q9** | **No sandbox**; easy **dedicated directory** (`workdir`) |
| **Q10** | Defer resume/continue |
| **Q11** | `$MEMORY/agent_process.json`; Settings later |
| **Q12** | User sessions yes; system agents optional (default off) |
| **Q13** | Optional `model` / allowlisted `extra_args` |
| **Q14** | Defer worktree flag; `extra_args` for grok `--worktree` |

## Implementation sketch

1. `internal/agentproc/` — Driver interface, config, exec + PGroup kill.  
2. grok + claude drivers with auto-approve defaults.  
3. `call_agent_process` tool: sync + `background` path; `workdir` mkdir; session concurrency counter (max 10).  
4. Wire kill to turn Stop / session close.  
5. Example config + system prompt.  
6. Tests: mock driver, cwd jail, workdir create, timeout kill, cap enforcement.

## Consequences

### Positive

- Marble cockpit + external horsepower.  
- Non-blocking operator experience via BG + auto-approve.  
- Easy isolation via `workdir` without full sandbox complexity.

### Trade-offs

- Auto-approve is powerful — prompt/workdir discipline required.  
- High timeouts mean resource use; concurrency cap 10.  
- CLI flag drift needs driver updates.

### Risks

| Risk | Mitigation |
|------|------------|
| Child hangs on auth/TTY | Auto-approve flags; timeout; kill |
| Workspace damage | workdir isolation; clear prompts |
| Too many parallel agents | max 10 / session |

## Acceptance criteria

- [x] Open questions answered (`0014-answers.json`)  
- [x] BG/timeouts/auto-approve/workdir/cap locked  
- [x] Implemented (`call_agent_process`, drivers, agent_process.json)  

## References

- `adr/0014-answers.json`  
- Grok CLI: `-p/--single`, `--output-format`, `--always-approve`  
- Claude Code: `-p` / print mode (verify at implement)  
- ADR-0005 BG tasks; ADR-0010 Stop / PGroup kill  
