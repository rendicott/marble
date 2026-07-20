# ADR-0016: Optional Model Provider API Key Auth

| Field | Value |
|-------|--------|
| **Status** | Accepted / Implemented |
| **Date** | 2026-07-20 |
| **Deciders** | Project owner |
| **Tags** | model, auth, api-key, config, cli, openai |
| **Extends** | ADR-0001 (model client / OpenAI wire), ADR-0007 (Settings runtime RO) |
| **Answers** | `adr/0016-answers.json` (`2026-07-20T00:42:21.580Z`) |

## Context

Marble’s model client talks **OpenAI-compatible** Chat Completions (`--base-url` + `--model`). Historically it always sent a dummy header:

```http
Authorization: Bearer marble
```

That works for **local / open** endpoints that ignore auth. It does **not** work for hosted providers that require a real API key.

Operators need optional keyed auth without putting secrets on the CLI or in Settings DB, while keeping the **default keyless** local workflow.

## Goals

1. Optional API key from an **environment variable named at launch** (**Q1**, **Q2**).  
2. If configuration is absent or empty → **no Authorization header** (**Q4**).  
3. Same credential on **Chat** and **Health** (**Q6**).  
4. Never expose the key in logs, health, or Settings — only mode + env name + set/unset (**Q5**, **Q7**).  
5. Document systemd `EnvironmentFile` + `0600` (**Q9**).

## Non-goals (v1)

- OAuth / device flow  
- Multi-provider key map (**Q8** deferred)  
- Storing keys in SQLite or Settings UI  
- Literal `--api-key=sk-…` (**Q10**)  
- Custom header names e.g. `x-api-key` (**Q12** deferred)  

## Decision

### 1. Launch surface (**Q1**, **Q2**, **Q10**, **Q11**)

```text
--api-key-env=OPENAI_API_KEY
--api-key-env=OPENAI_API_KEY,OPENROUTER_API_KEY
```

| Case | Behavior |
|------|----------|
| Flag **omitted** | Unauthenticated (local default) |
| Flag set, env **empty/unset** | Unauthenticated + single **WARNING** at startup (**Q11**) |
| Flag set, env **non-empty** | Use first non-empty env value as key |

- Value is the **env var name(s)**, never the secret.  
- Comma-separated list; **first non-empty** wins (**Q2**).  
- **No** literal `--api-key` in v1 (**Q10**).

### 2. HTTP auth (**Q3**, **Q4**, **Q6**)

When a key is resolved:

```http
Authorization: Bearer <key>
```

When no key:

- **Omit** `Authorization` entirely (drop dummy `Bearer marble`).

Apply to `POST …/chat/completions` and `GET …/models` (and any future model HTTP calls) via one client helper.

### 3. Config + client

```go
// config
APIKeyEnv string // flag value, e.g. "OPENAI_API_KEY" or "A,B"

// resolved once at launch (not logged)
// APIKey string  — empty means no auth

// model.Client
APIKey string // empty → no Authorization header
```

Resolve only at process start (restart to change — same as other CLI flags).

### 4. Observability (**Q5**, **Q7**)

| Surface | Content |
|---------|---------|
| Startup | `model auth: none` **or** `model auth: bearer from env OPENAI_API_KEY (set)` / `(unset)` — **never** the key |
| `/api/health` | `model_auth`, `model_auth_env` (name only), `model_auth_configured` (bool) |
| Settings → Runtime | Same read-only fields |
| Logs | Never print key or Authorization header |

### 5. Systemd (**Q9**)

```ini
[Service]
EnvironmentFile=-%h/.config/marble/model.env
# model.env mode 0600 contains: OPENAI_API_KEY=sk-...
ExecStart=... --api-key-env=OPENAI_API_KEY ...
```

Document in README.

### 6. Settings

Model / base URL / auth remain **CLI-only** (ADR-0007). Runtime section shows auth mode only — no secret input field.

## Decisions locked (Q1–Q12)

| ID | Decision |
|----|----------|
| **Q1** | Flag: **`--api-key-env`** (env var name, not the key) |
| **Q2** | **Comma-separated** list; first non-empty env wins |
| **Q3** | **`Authorization: Bearer <key>`** only in v1 |
| **Q4** | **Omit** Authorization when no key (drop dummy `Bearer marble`) |
| **Q5** | Health + Settings: mode + env name + set/unset; **never** the secret |
| **Q6** | Chat and Health share the same auth |
| **Q7** | Startup one-liner; never print key; redact Authorization |
| **Q8** | Defer multi-key map; one process = one base URL = one key |
| **Q9** | Document systemd EnvironmentFile + 0600 in README |
| **Q10** | No literal `--api-key` in v1 |
| **Q11** | WARNING if flag set but env empty |
| **Q12** | Defer custom header names |

## Implementation sketch

1. `config`: `--api-key-env`; resolve first non-empty env.  
2. `model.Client`: optional `APIKey`; `setAuth(req)` on Chat + Health.  
3. Empty key → no Authorization header.  
4. Health JSON + Settings Runtime fields.  
5. README: local vs cloud + systemd example.  
6. Tests: no header when empty; Bearer when set; multi-env first-wins; health omits secret.

## Consequences

### Positive

- Hosted OpenAI-compatible providers without redesigning the wire path.  
- Local default stays keyless.  
- Secrets stay in the environment, not argv / git / Settings DB.

### Trade-offs

- Restart to change key or env binding.  
- Bearer-only may miss exotic gateways (deferred).  
- Dropping dummy `Bearer marble` is a small behavior change (local stacks that ignore auth unaffected).

### Risks

| Risk | Mitigation |
|------|------------|
| Key in logs | Never log Authorization; truncate error bodies |
| World-readable env file | Document 0600 |
| Empty env + cloud base URL | Startup WARNING + clear 401 |

## Acceptance criteria

- [x] Open questions answered (`0016-answers.json`)  
- [x] ADR locked + review HTML updated  
- [x] `--api-key-env` + Bearer when set; omit auth when not  
- [x] Health / Settings never expose secret  
- [x] Tests + README examples  

## Implementation order

1. Config flag + env resolve  
2. Model client auth helper  
3. Health + Settings Runtime  
4. README / unit examples  
5. Tests  

## Changelog

| Date | Change |
|------|--------|
| 2026-07-20 | Proposed |
| 2026-07-20 | Locked Q1–Q12 from `0016-answers.json` |
| 2026-07-20 | Implemented: `--api-key-env`, model client auth, health/Settings, README |

## References

- `adr/0016-answers.json`  
- ADR-0001 model client  
- ADR-0007 Settings Runtime  
- `internal/model/client.go`  
