# ADR-0001: Marble Harness Inner Loop

| Field | Value |
|-------|--------|
| **Status** | Proposed |
| **Date** | 2026-07-16 |
| **Deciders** | Project owner |
| **Tags** | architecture, harness, llm, golang, web |

## Context

Existing agent harnesses (Hermes, OpenClaw, and similar) provide useful primitives but ship heavy UX, multi-provider auth surfaces, and complexity that get in the way of a clean coding loop. We want a **small, ownable harness** — written in Go — whose center is:

1. A **local OpenAI-compatible model** (no API keys).
2. A **tool-calling agent loop** with a minimal first toolset.
3. A **web portal** for multi-session chat, not a terminal-in-browser.

This ADR defines the **inner loop** only: model I/O, tools, session isolation, context budget, CLI launch surface, and the first web UI shell. Outer concerns (auth, remote multi-user, plugins, MCP, messaging gateways) are explicitly out of scope.

### First model (same as OpenCode)

| Setting | Value |
|---------|--------|
| Protocol | OpenAI-compatible Chat Completions (`/v1/chat/completions`) |
| Base URL (default) | `http://127.0.0.1:8000/v1` |
| Model ID (default) | `Qwen/Qwen3.5-122B-A10B-FP8` |
| Context window (default) | `262144` tokens |
| Max output (default) | `32768` tokens |
| Auth | None required (empty or dummy `Authorization` if the client library insists) |

All of the above are **CLI-configurable** so we can retarget another OpenAI-compatible endpoint without code changes.

## Decision

Build **Marble** as a single Go binary (`marble-harness`) that:

1. Starts an HTTP server hosting a web portal.
2. Owns an in-process **session registry**; each session is an independent goroutine + message history + tool runtime.
3. Implements an **agent loop**: user message → model (with tools) → tool calls → tool results → model → … until a final assistant message (or limit hit).
4. Ships three filesystem tools first: `file_read`, `file_write`, `list_files`.
5. Enforces **context budget** on every model call using configurable token limits and a simple trim policy.

## Architecture (center-out)

```
                    ┌─────────────────────────────────────┐
                    │           Web Portal (UI)            │
                    │  left: sessions   right: chat + box  │
                    └───────────────┬─────────────────────┘
                                    │ HTTP + SSE/WebSocket
                    ┌───────────────▼─────────────────────┐
                    │         marble-harness (Go)          │
                    │  HTTP API · Session Registry · FS    │
                    └───────────────┬─────────────────────┘
               ┌────────────────────┼────────────────────┐
               ▼                    ▼                    ▼
        ┌────────────┐      ┌────────────┐      ┌────────────┐
        │ Session A  │      │ Session B  │      │ Session N  │
        │ history    │      │ history    │      │ history    │
        │ agent loop │      │ agent loop │      │ agent loop │
        └─────┬──────┘      └─────┬──────┘      └─────┬──────┘
              │                   │                   │
              └───────────────────┼───────────────────┘
                                  ▼
                    ┌─────────────────────────────┐
                    │  Model Client (OpenAI wire) │
                    │  base-url · model · limits  │
                    └──────────────┬──────────────┘
                                   ▼
                    ┌─────────────────────────────┐
                    │  Local vLLM (Qwen, no key)  │
                    └─────────────────────────────┘
```

### Package sketch (implementation later)

```
marble/
  adr/                          # this document family
  cmd/marble-harness/           # main, CLI flags
  internal/
    config/                     # flag → Config struct
    model/                      # OpenAI-compatible client
    session/                    # registry, history, agent loop
    tools/                      # file_read, file_write, list_files
    token/                      # budget + trim (approx tokenizer first)
    api/                        # HTTP handlers, SSE stream
    web/                        # embedded static UI (embed.FS)
```

## Agent loop (inner loop)

For a single user turn in one session:

1. Append `user` message to session history.
2. **Budget check / trim** history + system + tool schemas so estimated tokens ≤ `context_limit - max_output - reserve`.
3. Call `POST {base_url}/chat/completions` with:
   - `model`
   - `messages` (full remaining history)
   - `tools` (OpenAI function/tool schema for the three FS tools)
   - `tool_choice: "auto"`
   - `max_tokens` = configured max output
4. Parse response:
   - If `tool_calls` present: execute each tool (in order, same turn), append `tool` role results, go to step 2 (loop).
   - If final `content` (and no tool calls): append `assistant` message, stream/emit to UI, stop turn.
5. Safety caps (configurable later; sensible defaults now):
   - max tool iterations per user turn (e.g. 25)
   - max concurrent tool calls (serial first version is fine)
   - per-tool timeouts

Sessions never share history. They share only:

- process-level config (model endpoint, limits, workspace root)
- tool *implementations* (same code paths)
- the HTTP server / UI shell

## Tools (v1)

All tools operate under a single **workspace root** (CLI flag, default: process CWD). Paths are resolved relative to that root; path escape (`..` outside root) is rejected.

| Tool | Purpose | Inputs | Output |
|------|---------|--------|--------|
| `file_read` | Read a text file | `path`, optional `offset`/`limit` (lines) | file contents (truncated if huge) |
| `file_write` | Create/overwrite a text file | `path`, `content` | ack + bytes written |
| `list_files` | List directory entries | `path` (default `.`), optional recursive bool | name, type, size |

Tool results are returned as strings to the model. Large reads are truncated with a clear marker so the model can re-request slices.

## Context limits

Limits are first-class and **CLI-configurable** (not hardcoded to Qwen):

| Flag | Default | Meaning |
|------|---------|---------|
| `--base-url` | `http://127.0.0.1:8000/v1` | OpenAI-compatible API root |
| `--model` | `Qwen/Qwen3.5-122B-A10B-FP8` | Model id |
| `--context-limit` | `262144` | Total context window (input+output budget frame) |
| `--max-output` | `32768` | Max generation tokens per model call |
| `--context-reserve` | `8192` | Headroom reserved for tool schemas + formatting |
| `--workspace` | `.` | Filesystem root for tools |
| `--addr` | `:8080` | Web + API listen address |

### Budget policy (v1, simple)

Let `budget = context_limit - max_output - context_reserve`.

Before each model call:

1. Estimate tokens for system prompt + tool JSON schemas + messages (approximation OK in v1: e.g. `chars/4` or a lightweight tokenizer).
2. If over budget, drop **oldest non-system messages** first (FIFO trim), never drop the latest user message.
3. Optionally compress oversized single tool results (truncate middle).

No automatic summarization in v1 (can be a later ADR). The loop must **fail closed** with a clear UI error if even the latest turn alone exceeds budget.

## Concurrency model

- **Session registry**: mutex-protected map `sessionID → *Session`.
- **Session**: owns `[]Message`, a `sync.Mutex` for history, and at most one **in-flight agent turn** (channel or `atomic` busy flag). Concurrent user posts while busy return `409` or queue (v1: reject with busy).
- **Agent turn**: runs in a goroutine; streams events to subscribers (SSE).
- Sessions are independent: no shared conversation memory, no cross-session tool state.

## Web portal (primary UI)

Single-page app (static files embedded in the binary):

```
┌──────────────────┬────────────────────────────────────────┐
│  Marble          │  Session title / id                     │
│  [ + New ]       ├────────────────────────────────────────┤
│                  │                                         │
│  ○ Session A     │   assistant / user / tool bubbles       │
│  ● Session B     │   (scrollable transcript)               │
│  ○ Session C     │                                         │
│                  │                                         │
│                  ├────────────────────────────────────────┤
│                  │  [ message input .................... ] │
│                  │  [ Send ]                               │
└──────────────────┴────────────────────────────────────────┘
```

### UX rules

- Left pane: list sessions + **New session** control; click selects active session.
- Right pane: transcript for active session; chat input at bottom.
- Sending a message includes **full session history** as model context (after budget trim).
- Stream assistant tokens and tool call progress into the transcript (SSE).
- No terminal emulator; native chat UI.

### HTTP API (sketch)

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/` | SPA |
| `GET` | `/api/sessions` | List sessions |
| `POST` | `/api/sessions` | Create session |
| `GET` | `/api/sessions/{id}` | Session metadata + history |
| `POST` | `/api/sessions/{id}/messages` | User message → start agent turn |
| `GET` | `/api/sessions/{id}/events` | SSE stream for that session |
| `GET` | `/api/health` | Liveness + model config echo |

Persistence: **in-memory only** for v1 (restart clears sessions). Disk persistence is a later ADR.

## CLI

```bash
marble-harness \
  --base-url http://127.0.0.1:8000/v1 \
  --model Qwen/Qwen3.5-122B-A10B-FP8 \
  --context-limit 262144 \
  --max-output 32768 \
  --context-reserve 8192 \
  --workspace /path/to/project \
  --addr :8080
```

Open `http://127.0.0.1:8080/`.

## Consequences

### Positive

- Centered design: model + tools + sessions are small and testable in Go.
- No API key plumbing for the first model.
- Context limits are explicit and retargetable when the model changes.
- Multi-session isolation is a natural fit for goroutines.
- Web UI is a real chat surface, not a PTY clone.

### Negative / trade-offs

- Token estimation without a true tokenizer will be approximate (risk of over/under-trimming).
- In-memory sessions only until a persistence ADR.
- Single-host, no auth (fine for local/Tailscale trust network; not public-internet ready).
- Serial tool execution and simple trim policy may feel crude on huge histories.
- Qwen “reasoning” fields (content vs reasoning) may need client-side normalization — validate during implementation.

### Risks

| Risk | Mitigation |
|------|------------|
| Model returns `content: null` with reasoning-only | Normalize response; request non-thinking or map reasoning→content when needed |
| Tailscale endpoint offline | Health endpoint + clear UI error |
| Path escape via tools | Workspace root jail + clean path checks |
| Context blow-up from tool dumps | Truncate tool results; line-windowed `file_read` |

## Out of scope (later ADRs)

- Auth / multi-user
- Session persistence / resume across restarts
- Summarization / memory
- Extra tools (shell, git, web search, browser)
- MCP / plugin system
- Desktop/TUI clients
- Evaluation harness / replay

## Implementation order (after ADR acceptance)

1. `config` + `model` client smoke test against local Qwen.
2. `tools` unit tests with temp workspace.
3. `session` agent loop (no UI) via a tiny CLI subcommand or test.
4. HTTP API + in-memory registry.
5. Embedded web portal (sessions list + chat + SSE).
6. Context budget integration tests with synthetic long histories.

## Acceptance criteria (for this ADR)

- [ ] Stakeholders agree center-out scope and package boundaries.
- [ ] Defaults match OpenCode local Qwen configuration.
- [ ] Tool set limited to the three FS tools for v1.
- [ ] Multi-session concurrency model accepted.
- [ ] Web layout (left sessions / right chat) accepted.
- [ ] CLI flag set accepted as the sole config surface for v1.

## References

- OpenCode local provider: `~/.config/opencode/opencode.jsonc` (`local-llama` → Qwen3.5-122B-A10B-FP8).
- OpenAI Chat Completions tool calling (wire format for local vLLM).
- Go `embed` for shipping UI with the binary.
