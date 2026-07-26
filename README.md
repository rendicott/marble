# Marble

**Marble** is a small, ownable **agent harness** written in Go. It talks to **OpenAI-compatible** model endpoints (local and/or keyed cloud), runs a tool-using agent loop, and exposes a multi-session web UI.

> **MVP status.** Marble is intentionally minimal. A process-wide CLI model is always available as fallback; additional models live in a **Settings catalog** (per-session + optional cron pin). Optional **Google OAuth** allowlist (shared full-admin sessions) and a **single writer** per memory directory. Expect sharp edges; design decisions live in [`adr/`](adr/).

## What's new in v0.4.0

Highlights since **v0.3.0**:

### Selectable models (ADR-0018) — schema v3
- **Model catalog** in SQLite (max 32 entries): display name, provider `model` string, optional `base_url`, per-entry `api_key_env`, context limits, capability flags (`tools` / `reasoning` / `images` / `voice`), cost metadata (stored for a future spend ADR — Marble does not bill)
- **Process default** remains the CLI `--model` / `--base-url` / `--api-key-env` / context flags — always selectable, **read-only** in Settings (restart to change)
- **Effective model** per turn: cron pin → session `model_id` → process fallback; budgets, trim, compact, and client construction use the active row
- **Per-session picker** in the chat header; tools `model_list` / `session_set_model` (safe mid-turn; takes effect next turn)
- **Cron optional `model_id`** — pin a catalog model for scheduled fires
- **Settings → Models** — list / add / edit / delete / health-test; **Copy to new** prefills the add form from process or any catalog row (mobile-friendly)
- Secrets stay in the environment only (never stored in the catalog or argv)

### Multimodal attachments (ADR-0019) — schema v4
- **Stage then send** — paste, drag-drop, or paperclip images and basic text documents; durable store under `$MEMORY/attachments/`
- **OpenAI multimodal content parts** on the wire (`text` + `image_url` data URLs) for models with `cap_images`
- **Capability strip / re-include** — images always accepted into chat history (`marble-att://` sentinels); stripped from the **outbound** prompt when the active model lacks image support, re-materialized when a vision model is active again
- **Composer UX** — staging chips, non-blocking warning when staging images under a non-vision model, **Send locked while uploads are in flight**
- **Transcript** chips + modal preview (images inline; text/markdown sanitized)
- Agent tool **`message_attach`** for durable chat attachments; legacy **`attach_file`** remains workspace/UI-oriented

### Loop / UI reliability
- Final assistant messages after tool loops reliably surface over SSE (buffer + resync)
- Attachment upload race fixed by disabling Send until stage POSTs complete

### v0.3.0

- **Public hardening** — mpub **Content-Security-Policy** (no scripts), global `X-Frame-Options` / `nosniff` / `Referrer-Policy`
- **Private mpub** — anonymous viewers get **uniform 404** (no existence oracle / login redirect)
- **Minimal public health** — unauthenticated `GET /api/health` in Google mode returns only `ok` / `auth_mode` / `tls_enabled` (full detail after login)
- **OAuth login limits** — rate-limit login starts per client IP; cap + GC pending PKCE states

### v0.2.0

- **Google OAuth + multi-user (ADR-0017)** — Sign in with Google, email allowlist (all full admins), identity on chat history (not sent to the model)
- **Optional in-process TLS** — `--tls-cert-file` / `--tls-key-file`
- **mpub visibility** — new pages default **private**; `mpub_set_visibility` to promote/demote
- **GitHub Actions releases** — multi-arch binaries on tags

v0.1.x included durable **cron** (ADR-0015) and model **`--api-key-env`** (ADR-0016).

## Features

### Agent runtime
- **Multi-turn agent loop** — user message → model ↔ tools → final reply
- **Tool rounds** — soft advisory (default 65) / hard stop (default 80)
- **Context budget** — trim history to the **active** model’s limits; soft warn, auto-compact via LLM system agent
- **Soft wall-clock** advisory for long continuous tool runs
- **Turn progress UI** — phase, iteration, tool rounds, context %, live steps, **Stop** to cancel
- **System agents** — e.g. compaction sessions in a separate sidebar section
- **Shell safety** — process-group kill on timeout/stop so backgrounded children cannot hang a turn

### Model providers
- **OpenAI-compatible** Chat Completions (`/v1/chat/completions`, `/v1/models`)
- **Process CLI model** — `--base-url`, `--model`, context flags, optional `--api-key-env` (always available as fallback)
- **Catalog models (ADR-0018)** — additional endpoints/models from Settings; per-entry base URL and `api_key_env`; session picker + cron pin
- **Local / open endpoints** — no API key by default (no `Authorization` header)
- **Optional API key auth (ADR-0016)** — `--api-key-env=NAME[,NAME2…]` (and catalog `api_key_env`) reads keys from the environment only; first non-empty env wins
- **Health / Settings** show auth mode + env name + configured yes/no — never the secret
- **Multimodal (ADR-0019)** — image (+ basic document) parts when catalog `cap_images` is set; process default stays text-only on the wire

### Auth & access (ADR-0017)
- **open** (default) — no login; local-operator trust model
- **google** — Sign in with Google when OAuth flags + allowlist are complete
- Allowlisted users are **full admins**; chat sessions are **shared**
- User identity on messages in UI/MD/events — **never** forwarded to the model
- Optional **HTTPS** via cert/key files, or TLS termination at a reverse proxy
- Login **rate limits** + capped pending OAuth state (DoS hardening)

### Tools
- **Filesystem** — `file_read` / `file_write`, `list_files`, `grep`, `glob`, `codebase_summary`
- **Edits** — `edit_file` (prior-read), `apply_patch` (atomic multi-hunk)
- **Shell** — `shell_execute` with deny-list policy, timeouts, output caps; optional `--disable-shell`
- **Background tasks** — start / check / kill long-running shell jobs
- **Continuations** — one-shot delayed resume (`schedule_continuation`: delay and/or wait for BG task)
- **Cron (ADR-0015)** — durable recurring schedules: `cron_list` / `cron_get` / `cron_create` / `cron_update` / `cron_delete` / `cron_run` (optional `model_id`)
- **Models (ADR-0018)** — `model_list`, `session_set_model`
- **Web** — `web_fetch` (HTTP(S) → markdown/JSON); prefer after MCP search when available
- **External agents (ADR-0014)** — `call_agent_process` (`format=grok|claude`) with optional `workdir`, high timeouts, `background` mode
- **Memory & skills** — `memory_*` under `$MEMORY/knowledge/`, `skill_*` from skill roots; prompt nudges memory when unsure
- **Context** — `get_context_usage`, `session_compact`
- **Attachments** — `message_attach` (durable chat chips + model-visible images when `cap_images`); `attach_file` (workspace path / UI-oriented)
- **MCP** — optional stdio/HTTP servers from `$MEMORY/mcp.json` (e.g. Tavily web search)
- **mpub** — publish HTML/markdown at `/mpub/{slug}`; tools: `mpub_publish` / `list` / `get` / `unpublish` / `mpub_set_visibility`

### Sessions & memory
- **Multi-session** web UI with short session ids
- **Deep links** — `/s/{session_id}` restores a session in the UI
- **Markdown-first** transcripts under `$MEMORY/session/<id>.md` (including attachment sentinels)
- **SQLite dual-write** (`marble.db`) for index, events, settings, cron, **model catalog**, **attachments**, daemon state
- **Schema** — binary supports **v4** (v3 = catalog, v4 = attachments); stepwise migrate on open
- **Limp mode** if the DB schema is unreadable/mismatched (chat + MD still work)
- **Daemon** — periodic flush, prune closed sessions, blob/attachment GC, daily compaction
- **Session info** panel — tokens, tool histogram, recent events
- **Every-turn soul (ADR-0013)** — optional `$MEMORY/soul.md` injected as a second system message
- **Cron session badges** — 🕐 next to sessions bound to durable cron jobs
- **Per-session model_id** — catalog override until cleared back to process default

### Web UI
- Chat with **markdown** rendering (user + assistant)
- Left session list + system agents, **Close**, **Session info**
- **Session model picker** — switch catalog model for the next turn
- **Composer attachments** — paste / drop / paperclip; stage chips; Send waits for uploads
- **Workspace explorer** modal (browse/edit/upload under the tool jail)
- **System prompt & soul** modal (👁) — immutable system prompt + editable soul
- **Cron jobs** modal (🕐) — list/create/edit/enable/run-now/history + next-fire preview + optional model pin
- **Settings** modal (⚙) — runtime (CLI model + OAuth), **Models** catalog, DB settings, MCP, UI prefs
- SSE live updates for messages, tools, turn progress, attachments
- Sign-in flow when Google mode is enabled

### Cron jobs (ADR-0015)
- **Durable** SQLite schedules (survive restart); **not** a replacement for one-shot `schedule_continuation`
- Schedule kinds: **5-field cron** and **interval** (min 60 seconds)
- On fire: inject `[cron:id name]` + prompt into a target session and start a turn
- Optional **model_id** pin (ADR-0018) for the cron turn’s effective model
- Missing session → **create** a new session and rebind the job; busy → **skip**; limp/model-down → pause fires
- Caps: 50 jobs, 3 concurrent cron turns; run history pruned (last 50/job and/or 30 days)
- Same store for UI and agent tools

### mpub visibility
| Visibility | Who can read `/mpub/{slug}` |
|------------|----------------------------|
| **private** (default for new publishes) | Allowlisted admins when Google auth is on; everyone in open mode |
| **public** | Anyone (no login) |
| Legacy docs (no `visibility` field) | Treated as **public** (old links keep working) |

Agents should leave new pages **private** unless the user **explicitly** asks to make a page public. Use `mpub_set_visibility` to promote/demote without rewriting the body.

Anonymous requests for private or missing slugs both return **404** (no existence leak). All mpub responses send a strict **CSP** that disables scripts (mitigates same-origin XSS if public HTML is ever published).

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
| `--model` | Process default model id (catalog can add more) | set explicitly for your stack |
| `--api-key-env` | Env var name(s) for process model API key (optional) | empty = no auth |
| `--oauth-client-id` | Google OAuth client ID | empty = open mode |
| `--oauth-client-secret-env` | Env var name for OAuth client secret | — |
| `--oauth-redirect-url` | Public OAuth callback URL | — |
| `--oauth-allow-emails` | Comma-separated allowlist | — |
| `--oauth-allow-file` | Allowlist file path | — |
| `--tls-cert-file` / `--tls-key-file` | Optional HTTPS PEM paths | empty = HTTP |
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

### 5. Google OAuth + multi-user (ADR-0017)

Default remains **open** (no login). Google mode turns on only when all of these are set:

```bash
export GOOGLE_OAUTH_CLIENT_SECRET=...   # never put secret on the CLI

./bin/marble-harness \
  ... \
  --addr :8080 \
  --oauth-client-id=YOUR_ID.apps.googleusercontent.com \
  --oauth-client-secret-env=GOOGLE_OAUTH_CLIENT_SECRET \
  --oauth-redirect-url=https://your-public-host:8080/auth/callback \
  --oauth-allow-emails=you@example.com,friend@example.com \
  --oauth-allow-file=/home/you/.config/marble/allowlist.txt
```

- All allowlisted users are **full admins**; sessions are **shared**.
- User identity is stored on chat messages (UI/MD) but **not** sent to the model.
- **`GET /api/health`** is public but **minimal** when unauthenticated in Google mode (`ok`, `auth_mode`, `tls_enabled` only). Full health after login.
- **mpub**: *public* pages stay open (with CSP); *private* pages are admin-only (anonymous → **404**).

**OAuth redirect URL:** there is no separate OAuth port. The callback is on the **same HTTP listener** as the UI (`--addr`, default `:8080`), path **`/auth/callback`**. Register that full URL in Google Cloud Console (Authorized redirect URIs), and pass the same value as `--oauth-redirect-url`.

| Setup | Example `--oauth-redirect-url` |
|-------|--------------------------------|
| Local HTTP | `http://127.0.0.1:8080/auth/callback` |
| Public host + Marble TLS | `https://your-host:443/auth/callback` (or your public port) |
| Behind Caddy / ALB / Tailscale Serve | `https://your-public-hostname/auth/callback` |

### Public VPS checklist

When exposing Marble on a public IP (e.g. **:443** + TLS + OAuth):

1. Use **complete** Google OAuth flags (never leave **open** mode on the public address).
2. Keep the allowlist **tiny**; enable 2FA on those Google accounts.
3. Prefer a dedicated OS user and a **narrow** `--workspace` (not `$HOME` / `/`).
4. Consider **`--disable-shell`** if shell is not required.
5. Audit `$MEMORY/mpub` before go-live: demote secrets to **private**; legacy docs without `visibility` are treated as **public**.
6. Prefer not publishing **public HTML** with active content; CSP blocks scripts but treat public pages as hostile.
7. Firewall: only 443 (and SSH from your IP if needed).

### 6. Optional TLS (ADR-0017)

```bash
--tls-cert-file=/path/to/fullchain.pem \
--tls-key-file=/path/to/privkey.pem
```

Omit both for plain HTTP. Behind Caddy/ALB/Tailscale Serve you can leave TLS off on Marble and terminate HTTPS at the proxy (`--oauth-redirect-url` still uses the public `https://` URL). Automatic Let's Encrypt is not in-process yet.

### 7. Optional MCP

```bash
cp adr/mcp.json.example ~/.marble/mcp.json
# edit servers; set env vars for keys, then restart Marble
```

Tools show up as `mcp_<server>_<tool>`.

### 8. Optional external agents

```bash
cp adr/agent_process.json.example ~/.marble/agent_process.json
# enable grok/claude drivers; ensure binaries on PATH
```

## Memory layout

```
~/.marble/                 # --memory leaf (may be outside workspace)
├── marble.db              # SQLite (WAL) — sessions, events, settings, cron_*, model_catalog, attachments meta
├── marble.lock            # single-writer lock
├── session/<id>.md        # first-class transcripts
├── daily/YYYY-MM-DD.md
├── blobs/                 # large payload spill
├── attachments/           # staged/committed chat files (ADR-0019)
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
| **Process model is CLI-only** | Change process `--model` / `--base-url` / key via restart; catalog entries are editable live in Settings |
| **Process CapImages is false** | Vision requires a catalog entry with `cap_images` (no CLI multimodal flag yet) |
| **No audio / PDF** | Multimodal v1 is images + basic text documents only |
| **Auth is allowlist, not multi-tenant** | Optional Google OAuth; all allowlisted users full admin / shared sessions — not per-user isolation or RBAC |
| **One harness per memory dir** | Second process fails on `marble.lock` |
| **Shell is powerful** | Deny-list policy, not a full sandbox; same OS user as the harness |
| **No token streaming** | Completions are request/response (progress UI covers loop phase, not token deltas) |
| **Stop is cooperative** | Cancels turn context; shell process-group kill best-effort |
| **Cron is session-bound** | Fires inject turns into sessions (create if missing); not OS crontab / shell-only jobs |
| **Login sessions are in-memory** | OAuth cookies lost on harness restart (re-login) |

## Architecture (short)

```
cmd/marble-harness/     # process entry
internal/
  config/               # CLI flags (API key, OAuth, TLS)
  auth/                 # Google OAuth, sessions, middleware (ADR-0017)
  model/                # OpenAI-compatible client + multimodal content parts
  session/              # registry, agent loop, effective model, multimodal, daemon
  tools/                # tool implementations + catalog
  cron/                 # durable scheduler (ADR-0015)
  mpub/                 # published pages + visibility
  agentproc/            # external agent drivers (ADR-0014)
  mcp/                  # MCP client
  memory/               # session markdown store, soul
  db/                   # SQLite dual-write (cron, model_catalog, attachments)
  api/                  # HTTP JSON + SSE + models + attachments + mpub + cron
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
| 0017 | Google OAuth, multi-user identity, optional TLS |
| 0018 | Selectable models (catalog, per-session, cron pin) |
| 0019 | Multimodal attachments (images + basic documents) |

## Releases

GitHub Actions builds **precompiled** binaries on version tags (`v*`). Latest: **[v0.4.0](https://github.com/rendicott/marble/releases/tag/v0.4.0)**.

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
git tag v0.4.0
git push origin v0.4.0
# Workflow "Release" builds on ubuntu-latest, tests, and attaches assets
```

If a tag already exists but the workflow failed (e.g. GitHub outage), re-run from the Actions tab:

**Actions → Release → Run workflow** → enter tag (e.g. `v0.4.0`).

Workflow: [`.github/workflows/release.yml`](.github/workflows/release.yml).

## Tests

```bash
go test ./...
```

## License

See repository license if/when added; until then treat as source-available for personal/ops use unless otherwise stated.
