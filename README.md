# Marble

**Marble** is a small, ownable **agent harness** written in Go. It talks to a single **OpenAI-compatible** model endpoint (local or keyed cloud), runs a tool-using agent loop, and exposes a multi-session web UI.

> **MVP status.** Marble is intentionally minimal. It supports **one model** (one `--base-url` + `--model` pair per process), a local-operator trust model (no multi-user auth), and a single writer per memory directory. Expect sharp edges; design decisions live in [`adr/`](adr/).

## Features

### Agent runtime
- **Multi-turn agent loop** — user message → model ↔ tools → final reply
- **Tool rounds** — soft advisory (default 65) / hard stop (default 80)
- **Context budget** — trim history to fit; soft warn, auto-compact via LLM system agent
- **Soft wall-clock** advisory for long continuous tool runs
- **Turn progress UI** — phase, iteration, tool rounds, context %, live steps, **Stop** to cancel
- **System agents** — e.g. compaction sessions in a separate sidebar section
- **Shell safety** — process-group kill on timeout/stop so backgrounded children cannot hang a turn

### Model providers
- **OpenAI-compatible** Chat Completions (`/v1/chat/completions`, `/v1/models`)
- **Local / open endpoints** — no API key by default (no `Authorization` header)
- **Optional API key auth (ADR-0016)** — `--api-key-env=NAME[,NAME2…]` reads the key from the environment (never from argv); first non-empty env wins
- **Health / Settings** show auth mode + env name + configured yes/no — never the secret

### Tools
- **Filesystem** — `file_read` / `file_write`, `list_files`, `grep`, `glob`, `codebase_summary`
- **Edits** — `edit_file` (prior-read), `apply_patch` (atomic multi-hunk)
- **Shell** — `shell_execute` with deny-list policy, timeouts, output caps; optional `--disable-shell`
- **Background tasks** — start / check / kill long-running shell jobs
- **Continuations** — one-shot delayed resume (`schedule_continuation`: delay and/or wait for BG task)
- **Cron (ADR-0015)** — durable recurring schedules: `cron_list` / `cron_get` / `cron_create` / `cron_update` / `cron_delete` / `cron_run`
- **Web** — `web_fetch` (HTTP(S) → markdown/JSON); prefer after MCP search when available
- **External agents (ADR-0014)** — `call_agent_process` (`format=grok|claude`) with optional `workdir`, high timeouts, `background` mode
- **Memory & skills** — `memory_*` under `$MEMORY/knowledge/`, `skill_*` from skill roots; prompt nudges memory when unsure
- **Context** — `get_context_usage`, `session_compact`
- **Attachments** — `attach_file` (UI chips, not re-injected into the model)
- **MCP** — optional stdio/HTTP servers from `$MEMORY/mcp.json` (e.g. Tavily web search)
- **mpub** — publish HTML/markdown pages served at `/mpub/{slug}`

### Sessions & memory
- **Multi-session** web UI with short session ids
- **Deep links** — `/s/{session_id}` restores a session in the UI
- **Markdown-first** transcripts under `$MEMORY/session/<id>.md`
- **SQLite dual-write** (`marble.db`) for index, events, settings, cron, daemon state
- **Limp mode** if the DB schema is unreadable/mismatched (chat + MD still work)
- **Daemon** — periodic flush, prune closed sessions, blob GC, daily compaction
- **Session info** panel — tokens, tool histogram, recent events
- **Every-turn soul (ADR-0013)** — optional `$MEMORY/soul.md` injected as a second system message
- **Cron session badges** — 🕐 next to sessions bound to durable cron jobs

### Web UI
- Chat with **markdown** rendering (user + assistant)
- Left session list + system agents, **Close**, **Session info**
- **Workspace explorer** modal (browse/edit/upload under the tool jail)
- **System prompt & soul** modal (👁) — immutable system prompt + editable soul
- **Cron jobs** modal (🕐) — list/create/edit/enable/run-now/history + next-fire preview
- **Settings** modal (⚙) — runtime read-only (incl. model auth), DB settings, MCP, UI prefs
- SSE live updates for messages, tools, turn progress

### Cron jobs (ADR-0015)
- **Durable** SQLite schedules (survive restart); **not** a replacement for one-shot `schedule_continuation`
- Schedule kinds: **5-field cron** and **interval** (min 60 seconds)
- On fire: inject `[cron:id name]` + prompt into a target session and start a turn
- Missing session → **create** a new session and rebind the job; busy → **skip**; limp/model-down → pause fires
- Caps: 50 jobs, 3 concurrent cron turns; run history pruned (last 50/job and/or 30 days)
- Same store for UI and agent tools

## Launch

### 1. Prerequisites
- Go 1.22+ (module and release builds target Go 1.22)
- A running **OpenAI-compatible** chat API (`/v1/chat/completions`, `/v1/models`) — local (no key) or hosted (API key via env)
- Optional: Node/`npx` if you use stdio MCP servers (e.g. Tavily)
- Optional: `grok` / `claude` CLIs on `PATH` for `call_agent_process`

### 2. Build

```bash
git clone https://github.com/rendicott/marble.git
cd marble
go build -o bin/marble-harness ./cmd/marble-harness
```

### 3. Run (foreground)

**Local / open model (no API key):**

```bash
./bin/marble-harness \
  --workspace /path/to/workspace \
  --memory ~/.marble \
  --base-url http://127.0.0.1:8000/v1 \
  --model YourLocalModelId \
  --addr :8080
```

**Hosted provider (API key from environment — ADR-0016):**

```bash
# Never put the secret on the CLI. Name the env var instead:
export OPENAI_API_KEY=sk-...   # placeholder — use your real key only in the environment
./bin/marble-harness \
  --workspace /path/to/workspace \
  --memory ~/.marble \
  --base-url https://api.openai.com/v1 \
  --model gpt-4.1-mini \
  --api-key-env=OPENAI_API_KEY \
  --addr :8080
```

**xAI Grok example:**

```bash
export GROK_API_KEY=...        # or XAI_API_KEY — match --api-key-env
./bin/marble-harness \
  --workspace /path/to/workspace \
  --memory ~/.marble \
  --base-url https://api.x.ai/v1 \
  --model grok-4.5 \
  --api-key-env=GROK_API_KEY \
  --context-limit 500000 \
  --max-output 32768 \
  --context-reserve 8192 \
  --addr :8080
```

- `--api-key-env` accepts a **comma-separated** list; the first non-empty env wins (e.g. `OPENAI_API_KEY,OPENROUTER_API_KEY`).
- If the flag is omitted or the env is empty, Marble sends **no** `Authorization` header (local default).
- There is no `--api-key=sk-…` flag (avoids argv / process-list leaks).

Open **http://127.0.0.1:8080/** (or `http://host:8080/s/{session_id}` for a deep link).

| Flag | Meaning | Typical default |
|------|---------|-----------------|
| `--workspace` | Tool jail (files + shell cwd root) | `.` |
| `--memory` | Session MD, DB, MCP config, mpub, soul, cron | `~/.marble` |
| `--base-url` | OpenAI-compatible API root | `http://127.0.0.1:8000/v1` |
| `--model` | Single model id for this process | set explicitly for your stack |
| `--api-key-env` | Env var name(s) for model API key (optional) | empty = no auth |
| `--context-limit` | Model context window (tokens) | `262144` |
| `--max-output` | Max generation tokens per model call | `32768` |
| `--context-reserve` | Reserved for tools/formatting | `8192` |
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
# Optional secrets (mode 0600). Example line: OPENAI_API_KEY=sk-...
EnvironmentFile=-%h/.config/marble/env
Environment=PATH=%h/.local/bin:/usr/local/bin:/usr/bin:/bin
ExecStart=%h/src/marble/bin/marble-harness \
  --workspace %h \
  --memory %h/.marble \
  --base-url http://127.0.0.1:8000/v1 \
  --model YourLocalModelId \
  --addr :8080
# For a keyed cloud provider, also pass e.g.:
#   --api-key-env=OPENAI_API_KEY
#   --base-url https://api.openai.com/v1 --model ...
Restart=on-failure
RestartSec=3
KillMode=control-group

[Install]
WantedBy=default.target
```

```bash
# If using a cloud key:
umask 077
printf 'OPENAI_API_KEY=sk-...\n' > ~/.config/marble/env
chmod 600 ~/.config/marble/env
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

Put secrets (e.g. `TAVILY_API_KEY`, `GROK_API_KEY`) in `~/.config/marble/env` or the process environment — **not** in the unit `ExecStart` line, and **never** in git.

### 5. Optional MCP

```bash
cp adr/mcp.json.example ~/.marble/mcp.json
# edit servers; set env vars for keys, then restart Marble
```

Tools show up as `mcp_<server>_<tool>`.

### 6. Optional external agents

```bash
cp adr/agent_process.json.example ~/.marble/agent_process.json
# enable grok/claude drivers; ensure binaries on PATH
```

## Memory layout

```
~/.marble/                 # --memory leaf (may be outside workspace)
├── marble.db              # SQLite (WAL) — sessions, events, settings, cron_*
├── marble.lock            # single-writer lock
├── session/<id>.md        # first-class transcripts
├── daily/YYYY-MM-DD.md
├── blobs/                 # large payload spill
├── knowledge/             # memory_write target
├── skills/                # optional skills
├── mpub/<slug>/           # published pages
├── soul.md                # optional every-turn context (ADR-0013)
├── mcp.json               # optional MCP servers
└── agent_process.json     # optional call_agent_process drivers (ADR-0014)
```

Operator secrets (API keys) live **outside** the repo, typically:

```
~/.config/marble/env       # systemd EnvironmentFile, mode 0600
```

## MVP limitations

| Limitation | Notes |
|------------|--------|
| **One model per process** | Single `--base-url` + `--model`; optional one resolved API key; no multi-provider routing or hot model switch in v1 |
| **No multi-tenant auth** | UI is local/operator trust (bind carefully; not public-internet hardened) |
| **One harness per memory dir** | Second process fails on `marble.lock` |
| **Shell is powerful** | Deny-list policy, not a full sandbox; same OS user as the harness |
| **No token streaming** | Completions are request/response (progress UI covers loop phase, not token deltas) |
| **Stop is cooperative** | Cancels turn context; shell process-group kill best-effort |
| **Cron is session-bound** | Fires inject turns into sessions (create if missing); not OS crontab / shell-only jobs |

## Architecture (short)

```
cmd/marble-harness/     # process entry
internal/
  config/               # CLI flags (incl. --api-key-env)
  model/                # OpenAI-compatible client + optional Bearer auth
  session/              # registry, agent loop, turn progress, daemon
  tools/                # tool implementations + catalog
  cron/                 # durable scheduler (ADR-0015)
  agentproc/            # external agent drivers (ADR-0014)
  mcp/                  # MCP client
  memory/               # session markdown store, soul
  db/                   # SQLite dual-write (+ cron tables)
  api/                  # HTTP JSON + SSE + mpub + cron routes
  web/static/           # embedded SPA
  workspacefs/          # explorer FS API
adr/                    # architecture decision records
```

Design docs: [`adr/`](adr/) (HTML review pages + answers JSON for locked decisions).

Notable ADRs:

| ADR | Topic |
|-----|--------|
| 0012 | `web_fetch` |
| 0013 | System prompt viewer + soul |
| 0014 | `call_agent_process` |
| 0015 | Cron jobs (UI + tools) |
| 0016 | Optional model API key (`--api-key-env`) |

## Releases

GitHub Actions builds **precompiled** binaries on version tags (`v*`), for example `v0.1.0`.

| Asset | Platform |
|-------|----------|
| `marble-harness-linux-amd64` | Linux x86_64 |
| `marble-harness-linux-arm64` | Linux aarch64 |
| `marble-harness-darwin-arm64` | macOS Apple Silicon |

Also attached: `SHA256SUMS`.

```bash
# After downloading from https://github.com/rendicott/marble/releases
chmod +x marble-harness-linux-amd64
./marble-harness-linux-amd64 --version
./marble-harness-linux-amd64 \
  --workspace "$HOME" \
  --memory "$HOME/.marble" \
  --base-url http://127.0.0.1:8000/v1 \
  --model YourLocalModelId \
  --addr :8080
```

**Publish a release** (maintainers — **GitHub Actions only**; do not upload locally built binaries):

```bash
git tag v0.1.0
git push origin v0.1.0
# Workflow "Release" builds on ubuntu-latest, tests, and attaches assets
```

If a tag already exists but the workflow failed (e.g. GitHub outage), re-run from the Actions tab:

**Actions → Release → Run workflow** → enter tag `v0.1.0`.

Workflow: [`.github/workflows/release.yml`](.github/workflows/release.yml).

## Tests

```bash
go test ./...
```

## License

See repository license if/when added; until then treat as source-available for personal/ops use unless otherwise stated.
