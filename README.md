# Marble

**Marble** is a small, ownable **agent harness** written in Go. It talks to a single **OpenAI-compatible** local model endpoint, runs a tool-using agent loop, and exposes a multi-session web UI.

> **MVP status.** Marble is intentionally minimal. It supports **one model** (one `--base-url` + `--model` pair per process), a local-operator trust model (no multi-user auth), and a single writer per memory directory. Expect sharp edges; design decisions live in [`adr/`](adr/).

## Features

### Agent runtime
- **Multi-turn agent loop** — user message → model ↔ tools → final reply
- **Tool rounds** — soft advisory (default 65) / hard stop (default 80)
- **Context budget** — trim history to fit; soft warn, auto-compact via LLM system agent
- **Soft wall-clock** advisory for long continuous tool runs
- **Turn progress UI** — phase, iteration, tool rounds, context %, live steps, **Stop** to cancel
- **System agents** — e.g. compaction sessions in a separate sidebar section

### Tools
- **Filesystem** — `file_read` / `file_write`, `list_files`, `grep`, `glob`, `codebase_summary`
- **Edits** — `edit_file` (prior-read), `apply_patch` (atomic multi-hunk)
- **Shell** — `shell_execute` with deny-list policy, timeouts, output caps; optional `--disable-shell`
- **Background tasks** — start / check / kill long-running shell jobs
- **Continuations** — schedule a later prompt (delay and/or wait for BG task)
- **Memory & skills** — `memory_*` under `$MEMORY/knowledge/`, `skill_*` from skill roots
- **Context** — `get_context_usage`, `session_compact`
- **Attachments** — `attach_file` (UI chips, not re-injected into the model)
- **MCP** — optional stdio/HTTP servers from `$MEMORY/mcp.json` (e.g. web search)
- **mpub** — publish HTML/markdown pages served at `/mpub/{slug}`

### Sessions & memory
- **Multi-session** web UI with short session ids
- **Markdown-first** transcripts under `$MEMORY/session/<id>.md`
- **SQLite dual-write** (`marble.db`) for index, events, settings, daemon state
- **Limp mode** if the DB schema is unreadable/mismatched (chat + MD still work)
- **Daemon** — periodic flush, prune closed sessions, blob GC, daily compaction
- **Session info** panel — tokens, tool histogram, recent events

### Web UI
- Chat with **markdown** rendering (user + assistant)
- Left session list + system agents, **Close**, **Session info**
- **Workspace explorer** modal (browse/edit/upload under the tool jail)
- **Settings** modal (runtime read-only, DB settings, MCP, UI prefs)
- SSE live updates for messages, tools, turn progress

## Launch

### 1. Prerequisites
- Go 1.18+ (module targets modern Go)
- A running **OpenAI-compatible** chat API (`/v1/chat/completions`, `/v1/models`)
- Optional: Node/`npx` if you use stdio MCP servers (e.g. Tavily)

### 2. Build

```bash
git clone https://github.com/rendicott/marble.git
cd marble
go build -o bin/marble-harness ./cmd/marble-harness
```

### 3. Run (foreground)

```bash
./bin/marble-harness \
  --workspace /path/to/workspace \
  --memory ~/.marble \
  --base-url http://127.0.0.1:8000/v1 \
  --model YourLocalModelId \
  --addr :8080
```

Open **http://127.0.0.1:8080/**

| Flag | Meaning | Typical default |
|------|---------|-----------------|
| `--workspace` | Tool jail (files + shell cwd root) | `.` |
| `--memory` | Session MD, DB, MCP config, mpub | `~/.marble` |
| `--base-url` | OpenAI-compatible API root | `http://127.0.0.1:8000/v1` |
| `--model` | Single model id for this process | set explicitly for your stack |
| `--addr` | UI / API listen address | `:8080` |

Full list: `./bin/marble-harness -h`

### 4. Recommended: user systemd service

Marble is designed so **one process owns** `$MEMORY` (file lock). Prefer a **user** unit over ad-hoc background shells.

Example unit (`~/.config/systemd/user/marble-harness.service`):

```ini
[Unit]
Description=Marble agent harness
After=network-online.target

[Service]
Type=simple
WorkingDirectory=%h
EnvironmentFile=-%h/.config/marble/env
Environment=PATH=%h/.local/bin:/usr/local/bin:/usr/bin:/bin
ExecStart=%h/src/marble/bin/marble-harness \
  --workspace %h \
  --memory %h/.marble \
  --base-url http://127.0.0.1:8000/v1 \
  --model YourLocalModelId \
  --addr :8080
Restart=on-failure
RestartSec=3
KillMode=control-group

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload
systemctl --user enable --now marble-harness
systemctl --user status marble-harness
journalctl --user -u marble-harness -f
```

After code or UI changes (static assets are **embedded** at build time):

```bash
go build -o bin/marble-harness ./cmd/marble-harness
systemctl --user restart marble-harness
```

Put secrets (e.g. `TAVILY_API_KEY=...`) in `~/.config/marble/env`, not in the unit file or git.

### 5. Optional MCP

```bash
cp adr/mcp.json.example ~/.marble/mcp.json
# edit servers; set env vars for keys, then restart Marble
```

Tools show up as `mcp_<server>_<tool>`.

## Memory layout

```
~/.marble/                 # --memory leaf (may be outside workspace)
├── marble.db              # SQLite (WAL)
├── marble.lock            # single-writer lock
├── session/<id>.md        # first-class transcripts
├── daily/YYYY-MM-DD.md
├── blobs/                 # large payload spill
├── knowledge/             # memory_write target
├── skills/                # optional skills
├── mpub/<slug>/           # published pages
└── mcp.json               # optional MCP servers
```

## MVP limitations

| Limitation | Notes |
|------------|--------|
| **One local model** | Single `--base-url` + `--model` per process; no multi-provider routing or hot model switch in v1 |
| **No multi-tenant auth** | UI is local/operator trust (bind carefully; not public-internet hardened) |
| **One harness per memory dir** | Second process fails on `marble.lock` |
| **Shell is powerful** | Deny-list policy, not a full sandbox; same OS user as the harness |
| **No token streaming** | Completions are request/response (progress UI covers loop phase, not token deltas) |
| **Stop is cooperative** | Cancels turn context; shell PG kill best-effort |

## Architecture (short)

```
cmd/marble-harness/     # process entry
internal/
  config/               # CLI flags
  model/                # OpenAI-compatible client
  session/              # registry, agent loop, turn progress, daemon
  tools/                # tool implementations + catalog
  mcp/                  # MCP client
  memory/               # session markdown store
  db/                   # SQLite dual-write
  api/                  # HTTP JSON + SSE + mpub routes
  web/static/           # embedded SPA
  workspacefs/          # explorer FS API
adr/                    # architecture decision records
```

Design docs: [`adr/`](adr/) (HTML review pages + answers JSON for locked decisions).

## Tests

```bash
go test ./...
```

## License

See repository license if/when added; until then treat as source-available for personal/ops use unless otherwise stated.
