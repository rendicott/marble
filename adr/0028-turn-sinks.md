# ADR-0028: Generic turn sinks — mirror every session response to external channels

| Field | Value |
|-------|--------|
| **Status** | **Accepted** (2026-09-11) — implemented (M1+M2) |
| **Date** | 2026-09-11 |
| **Accepted** | 2026-09-11 |
| **Author** | — |
| **Deciders** | Project owner |
| **Tags** | notifications, mirror, sink, webhook, orb, slack, ntfy, discord, events, config |
| **Extends** | ADR-0010 (SSE / `Session.Subscribe`), ADR-0016 (API-key env pattern), ADR-0017 (OAuth / session auth), ADR-0018 (config style), ADR-0019 (attachments / blobs) |
| **Review UI** | [0028-review.html](0028-review.html) |
| **Answers** | [0028-answers.json](0028-answers.json) (`2026-09-11T22:40:54.873Z`) — Q1–Q13 locked |

## Summary

Add an optional, transport-agnostic **turn-sink** layer to the harness. When a session finishes a turn, the harness hands a small, normalized **turn event** to zero or more configured **sinks**. A sink is a named adapter (Orb, Slack, ntfy, Discord, or a generic HTTP webhook) that receives the final assistant response plus a **deep link back to the session** and projects it into that channel's native notification mechanism.

Design principles:

1. **Not an "Orb mirror".** Orb is one built-in sink over a generic webhook base; Slack / ntfy / Discord / "any endpoint" are the same interface with a different payload template.  
2. **Trigger is the existing event stream.** The sink manager subscribes to `Session.Subscribe()` and fires on turn-complete (`status: idle`) — no changes to the agent turn loop.  
3. **Deep link is a concept, not a field.** The event carries a transport-neutral `DeepLink`; each sink maps it to its native "click" affordance (Orb `X-Orb-Return-Url`, Slack link/button, ntfy `Click:` header, Discord embed `url`).  
4. **Secrets by env-var name** (ADR-0016 style) — config stores `secret_env`, never the value.  
5. **Fire-and-forget, additive, no recursion** — sinks never block the turn and never start a new agent turn.  
6. **Declarative filters per sink** — kinds, skip-empty, skip-cron, session regex, min chars, optional coalescing.  
7. **Per-session override** — a sink's global `enabled` is the *default*; any session can force a sink on/off (or inherit) just for itself via a session-level UI toggle.

## Context & pain

### Today

| Layer | Behavior |
|-------|----------|
| **Turn end** | `Session.endTurn()` publishes `Event{Type:"status", Status:"idle"}`; the final assistant message is appended just before (`loop.go` `appendUI(am)`, plus `forceEndAssistant` for cut-off turns) |
| **Stream** | `Session.Subscribe() chan Event` + `publish(Event)` already feed the SPA UI (ADR-0010) |
| **Session deep link** | SPA routes `/s/{session_id}` (also `?session=…`, `#/s/…`) |
| **Config/secrets** | Persistent settings via `/api/settings`; secrets resolved from `~/.marble/env` by name (ADR-0016 `api_key_env`) |

### Observed pain

| Pain | Why it matters |
|------|----------------|
| No "turn finished" notification | Operator must watch the UI or poll; nothing pings the tray/phone when a long agent run completes |
| No easy jump back | Wanting to reopen a finished session requires navigating the session list |
| Point solutions sprawl | "Publish to Orb" is a skill; "ping Slack" would be another; each is agent-driven and best-effort |
| Agent-driven = unreliable | A skill/MCP publish can be forgotten, interrupted, or hit the context limit; it also costs tokens and risks recursion |

The fix is a **harness-level, transport-agnostic** hook: one signal ("a turn just ended"), fanned out to N destinations, with the destination format the only thing that varies.

## Goals

1. **Normalized turn event** — session id/title, workspace, model, turn id, kind, final message, preview, deep link, timestamp.  
2. **`Sink` interface** — `Name()` + `Deliver(ctx, TurnEvent) error`; a registry fans one event out to N sinks.  
3. **Generic webhook sink** — URL + method + headers + a Go `text/template` body; Orb/Slack/ntfy/Discord are pre-baked templates over it.  
4. **Deep link as a concept** — `deep_link_base + /s/{session_id}`, projected per sink.  
5. **Filters per sink** — kind allow/deny, skip-empty, skip-cron, session regex, min chars, optional coalescing.  
6. **Reliability semantics** — non-blocking, bounded queue, deterministic idempotency key, bounded retry, no recursion.  
7. **Per-session override** — global default + a per-session inherit/on/off toggle per sink.  
8. **No regression** — with zero sinks configured (or the feature off), behavior is identical to today.

## Non-goals

| Non-goal | Rationale |
|----------|-----------|
| Agent-driven publishing (a skill/MCP "remember to post") | Already possible today; this ADR is the *reliable, every-turn* path. `manage_sinks` mutates sink **config**, it does not publish a turn |
| Replacing the SSE/UI stream | Sinks are additive observers, not a new client protocol |
| Guaranteed delivery (exactly-once) | At-least-once within a process via idempotency key + bounded retry; cross-restart delivery is out of scope |
| Changing the model wire / system prompt | Purely a post-turn side effect |
| Two-way sync back from a channel | A notification with a return link, not a bot that accepts replies |
| Multi-tenant/billing | Single-operator harness |

## Design

### A. Normalized turn event

```go
type TurnEvent struct {
    SessionID    string
    SessionTitle string
    Workspace    string
    ModelID      string
    TurnID       string    // stable per (session, turn)
    Kind         string    // complete | error | stop | cron | continuation
    Message      string    // final assistant text (no thinking/tool chatter)
    Preview      string    // first line, for banners
    DeepLink     string    // deep_link_base + "/s/" + SessionID
    Timestamp    time.Time
}
```

### B. Sink interface + manager

```go
type Sink interface {
    Name() string
    Deliver(ctx context.Context, ev TurnEvent) error
}

type Manager struct { sinks []Sink }
func (m *Manager) OnEvent(ev session.Event) // subscribed to each open session
```

The manager buffers the latest `assistant` message and, on `status: idle`, emits a `TurnEvent` to every enabled sink whose filters pass. It is a **peer subscriber** on the existing stream — `session` never learns what a sink is.

### C. Generic webhook sink (the flexibility core)

```go
type WebhookSink struct {
    ID       string
    URL      string
    Method   string
    Headers  map[string]string
    Template string            // Go text/template over TurnEvent
    SecretEnv string           // env var name whose value becomes an auth header (optional)
    Filters  Filters
}
```

Orb/Slack/ntfy/Discord are thin constructors that set `URL`, `Method`, `Headers`, and `Template`. A `custom` type lets the operator point `Template` at any HTTP endpoint.

### D. Deep link projection per sink

| Sink | Deep link mapping |
|------|-------------------|
| **orb** | `X-Orb-Return-Url: {{.DeepLink}}` header (ADR-orb-0011 `return_url`); `X-Orb-Run-Id: {{.SessionID}}` so Orb clients can group turns from the same Marble session |
| **slack** | a link/button in the webhook JSON payload |
| **ntfy** | `Click: {{.DeepLink}}` header |
| **discord** | embed `url` |
| **webhook** | `{{.DeepLink}}` wherever the template places it |

### E. Config (proposed shape)

```json
{
  "sinks": [
    { "id": "orb", "type": "orb", "enabled": true,
      "topic_id": "t_…", "secret_env": "ORB_AK",
      "deep_link_base": "https://rinux.tail…",
      "filters": { "kinds": ["complete","error"], "skip_cron": true } },

    { "id": "slack", "type": "slack", "enabled": false,
      "secret_env": "SLACK_WEBHOOK_URL", "channel": "#ops",
      "deep_link_base": "https://rinux.tail…" },

    { "id": "generic", "type": "webhook", "enabled": false,
      "url": "https://hooks.example.com/x", "method": "POST",
      "headers": { "Content-Type": "application/json" },
      "template": "{\"text\":\"{{.Preview}}\",\"link\":\"{{.DeepLink}}\"}" }
  ]
}
```

Settings live with the other persistent settings; secrets stay in `~/.marble/env` and are referenced by name.

### F. Filters

```go
type Filters struct {
    Kinds         []string // allow-list; empty = all
    SkipEmpty     bool     // drop when Message is blank
    SkipCron      bool     // drop cron/continuation sessions
    OnlySessions  []string // regex list; drop non-matching
    SkipSessions  []string // regex list; drop matching
    MinChars      int
    MinIntervalSec int     // 0 = off; coalesce to one per window
}
```

### G. Reliability semantics

| Control | Behavior |
|---------|----------|
| Non-blocking | Enqueue and return; the turn/UI never waits on a sink |
| Bounded queue | Drop-with-log on overflow |
| Idempotency | Deterministic key `hash(sink, session, turn)`; pass through to sinks that support it (Orb `Idempotency-Key`); in-process delivered set otherwise |
| Timeout + retry | Per-sink timeout; a couple of jittered retries, then drop + log |
| No recursion | Manager is harness code, not an agent turn; never re-enters `/api/prompt` |
| Coalescing | Optional `min_interval_sec` to collapse a noisy session to one notification per window |

### H. Per-session sink behavior

A sink's `enabled` flag is the **global default**, not an absolute. Each session carries an optional **sink override map** that can force an individual sink on or off for *that session only*, or leave it at the default.

```go
// Per-session override. Absent key = inherit the global default.
type SinkOverride string
const (
    SinkInherit SinkOverride = ""      // follow the sink's global enabled
    SinkOn      SinkOverride = "on"    // force on, even if globally disabled
    SinkOff     SinkOverride = "off"   // force off, even if globally enabled
)

type Session struct {
    // ...
    SinkOverrides map[string]SinkOverride // key = sink id
}
```

**Resolution order** (evaluated at turn-end for each configured sink):

1. If the session has `SinkOverrides[sinkID]` set to `on` or `off`, that wins.  
2. Otherwise fall back to the sink's global `enabled`.  
3. Then the sink's filters still apply (kind allow-list, skip-empty, skip-cron, session regex, min-chars, coalescing).

Semantics:

| Sink global `enabled` | Session override | Deliver? |
|-----------------------|------------------|----------|
| on | *(inherit)* | yes |
| on | `off` | **no** |
| off | *(inherit)* | no |
| off | `on` | **yes** |

- **Persistence:** `SinkOverrides` lives in session metadata and survives reload, exactly like mute/hide prefs. It is per-session, never a global side effect.  
- **Fresh sessions inherit defaults.** A new session has an empty override map (→ follow global). A cron/continuation that opens a *new* session starts from defaults; a session that *is* the cron target carries whatever overrides are already stored on it.  
- **Deleting a sink** ignores (and may prune) its override key.  
- **The override is a filter input, not a sink mutation** — the manager reads it at delivery time; it never writes back to the global config.

### Session UI: per-session sink toggle

In the session view (not Settings), a small **Sinks** control next to the session title expands a popover listing every configured sink with a three-state toggle per sink and a quick "pause all" master:

```text
┌ Orb session · 0wdvhcj0q0 ──────────────────── [Sinks ▾] ─────────┐
│                                                                 │
│  ┌ Sinks for this session ───────────────────────────────┐      │
│  │  [ Pause all sinks ]   (sets every sink to off)       │      │
│  │                                                       │      │
│  │  orb      inherit ▾  · (global: on)                   │      │
│  │  slack    off      ▾  · (global: on)  ← overridden     │      │
│  │  debug    inherit ▾  · (global: off)                  │      │
│  │                                                       │      │
│  │  Changes apply to this session only.                  │      │
│  └───────────────────────────────────────────────────────┘      │
└─────────────────────────────────────────────────────────────────┘
```

- Each row shows the **effective** state and the global default in parentheses, so "off · (global: on)" reads as "I muted this one session."  
- The master "Pause all sinks" is a convenience that writes `off` to every sink for this session (and "Resume all" clears the overrides back to inherit).  
- The toggle is **session-scoped**; the Settings → Sinks list remains the place to change the global default.

### Agent tool: `manage_sinks`

Sessions can create and manage sinks without the Settings UI. This is **config mutation**, not agent-driven publishing — the harness still fires on `status: idle`. The tool never accepts raw secrets; `secret_env` is an env-var **name** (ADR-0016), same as Settings.

| action | Effect |
|--------|--------|
| `list` | All sinks (no secret values) + this session's overrides + recent delivery history |
| `get` | One sink by id |
| `create` | Add a sink (`id` + `type` required; type-specific fields; Settings-like defaults) |
| `update` | Patch fields on an existing sink |
| `delete` | Remove a sink (prunes this session's override key) |
| `test` | Synthetic turn to one sink (same as Settings test button) |
| `set_override` | This session only: `inherit` / `on` / `off` for one sink |
| `pause_all` / `resume_all` | This session only; matches the session popover |
| `set_base` | Global `deep_link_base` |
| `env_names` | Available `secret_env` names (never values) |

Writes persist to `$MEMORY/sinks.json` (same file as Settings PUT). Invalid config is saved with `validation_note` and does not deliver.

## Predefined sink types (first pass)

First pass ships **six types**. All share the same `Sink` interface and a set of **common fields**; each adds a small set of type-specific fields. `webhook` is the generic base the others are thin wrappers over — so every type below is either the base itself or a pre-baked constructor over it.

| type | purpose | auth | deep-link mapping |
|------|---------|------|-------------------|
| `orb` | publish to an Orb topic | `secret_env` → `Authorization: Bearer` | `X-Orb-Return-Url` + `X-Orb-Run-Id` (= session id) |
| `webhook` | any HTTP endpoint | arbitrary headers; optional `secret_env` | `{{.DeepLink}}` in the template |
| `slack` | Slack incoming webhook | webhook URL (no auth header) | link/button in the payload |
| `discord` | Discord webhook | webhook URL | embed `url` |
| `ntfy` | ntfy.sh or self-hosted | optional bearer/token | `Click:` header |
| `stdout` | local test/debug sink | none | printed to the log |

### Common fields (every type)

| field | required | notes |
|-------|----------|-------|
| `id` | yes | unique slug for this sink |
| `type` | yes | one of the six above |
| `enabled` | yes | **global default** for this sink (per-session override can flip it, §H) |
| `deep_link_base` | no | overrides the global default for this sink |
| `filters` | no | `{ kinds[], skip_empty, skip_cron, only_sessions[], skip_sessions[], min_chars, min_interval_sec }` |

### `orb`

Publishes the turn to an Orb topic and sets the deep link as the first-class `return_url` (ADR-orb-0011), so the tray/phone notification is one tap back to `/s/{id}`. The Marble session id is sent as `X-Orb-Run-Id` (envelope `run_id`, ADR-orb-0001 Q12) so Orb clients can group every turn from the same session. Session ids are 10-char Crockford base32 and fit Orb's run_id charset (`[A-Za-z0-9._:-]`, ≤64).

| field | required | notes |
|-------|----------|-------|
| `topic_id` | yes | `t_…` to publish to |
| `api_base` | no | default `https://api.dev.orbnet.app` |
| `secret_env` | yes | env var name holding `orb_ak_` / `orb_pk_` (e.g. `ORB_AK`) |
| `title_prefix` | no | e.g. `"marble: "` — prepended to the OS banner title |

```json
{ "id": "orb", "type": "orb", "enabled": true,
  "topic_id": "t_adcd…", "secret_env": "ORB_AK",
  "title_prefix": "marble: ", "deep_link_base": "https://rinux.tail…" }
```

### `webhook` (generic)

The escape hatch: any HTTP endpoint, any body shape, any headers. The body is a Go `text/template` over `TurnEvent`.

| field | required | notes |
|-------|----------|-------|
| `url` | yes | full endpoint |
| `method` | no | default `POST` |
| `headers` | no | static headers (e.g. `Content-Type`) |
| `template` | yes | Go `text/template` body; `{{.Message}}`, `{{.Preview}}`, `{{.DeepLink}}`, … |
| `secret_env` | no | env var whose value becomes `Authorization: Bearer …` |

```json
{ "id": "generic", "type": "webhook", "enabled": false,
  "url": "https://hooks.example.com/x", "method": "POST",
  "headers": { "Content-Type": "application/json" },
  "template": "{\"text\":\"{{.Preview}}\",\"link\":\"{{.DeepLink}}\"}" }
```

### `slack`

Slack incoming webhook (the whole URL is the credential, so it is referenced by env var name).

| field | required | notes |
|-------|----------|-------|
| `secret_env` | yes | env var name holding the full webhook URL (`SLACK_WEBHOOK_URL`) |
| `channel` | no | `#ops` override |
| `username` | no | bot display name |
| `icon_emoji` | no | e.g. `:robot_face:` |

```json
{ "id": "slack", "type": "slack", "enabled": false,
  "secret_env": "SLACK_WEBHOOK_URL", "channel": "#ops", "username": "marble" }
```

### `discord`

Discord webhook; deep link becomes the embed `url`.

| field | required | notes |
|-------|----------|-------|
| `secret_env` | yes | env var name holding the full webhook URL (`DISCORD_WEBHOOK_URL`) |
| `username` | no | bot display name |
| `avatar_url` | no | override avatar |

```json
{ "id": "discord", "type": "discord", "enabled": false,
  "secret_env": "DISCORD_WEBHOOK_URL", "username": "marble" }
```

### `ntfy`

ntfy.sh (or self-hosted); deep link becomes the `Click:` header so tapping the notification opens the session.

| field | required | notes |
|-------|----------|-------|
| `server` | no | default `https://ntfy.sh` |
| `topic` | yes | ntfy topic name |
| `secret_env` | no | env var name holding an access token/bearer (optional) |
| `priority` | no | 1–5 |

```json
{ "id": "ntfy", "type": "ntfy", "enabled": false,
  "server": "https://ntfy.sh", "topic": "rinux-marble", "priority": 3 }
```

### `stdout`

Local only; no secrets, no network. Prints the turn event to the harness log — used to debug filters/templates before pointing a real channel at the stream.

| field | required | notes |
|-------|----------|-------|
| `format` | no | `plain` (default) or `json` |

```json
{ "id": "debug", "type": "stdout", "enabled": false, "format": "plain" }
```

## Settings UI mockups

Wireframes only — the settings SPA gains a **Sinks** section. Each mock shows the list, the type picker, and the type-specific editor fields.

### Sinks list (Settings → Sinks)

```text
┌ Sinks ──────────────────────────────────────────────────────────────┐
│ Mirror finished turns to external channels.    [+ Add sink ▾]      │
│                                                                    │
│  ● orb          type: orb        [Edit] [Disable]                  │
│    topic t_adcd… · secret ORB_AK · kinds complete,error · skip cron│
│  ○ slack        type: slack      [Edit] [Enable]                   │
│    channel #ops · secret SLACK_WEBHOOK_URL                         │
│  ○ debug        type: stdout     [Edit] [Enable]                   │
│    format plain                                                   │
└────────────────────────────────────────────────────────────────────┘
```

### Add sink — type picker

```text
┌ Add sink ──────────────────────────────────────────────────────────┐
│  Type:  [orb ▾]                                                   │
│         orb — publish to an Orb topic (deep link = return_url)    │
│         webhook — any HTTP endpoint (custom template)             │
│         slack — Slack incoming webhook                            │
│         discord — Discord webhook                                 │
│         ntfy — ntfy.sh / self-hosted                              │
│         stdout — local test sink                                  │
└────────────────────────────────────────────────────────────────────┘
```

### Editor — common fields (shown for every type)

```text
┌ Edit sink · orb ──────────────────────────────────────────────────┐
│  Enabled    [x]                                                  │
│  Deep link  [https://rinux.tail…          ]   (global default)   │
│  ─ Filters ────────────────────────────────────────────────────── │
│  Kinds      [x] complete  [x] error  [ ] stop                    │
│             [ ] cron      [ ] continuation                       │
│  [x] Skip empty   [x] Skip cron   [ ] Coalesce (min_interval_sec) │
│  Only sessions  [____________________]  (regex, one per line)    │
│  Skip sessions  [____________________]                            │
└────────────────────────────────────────────────────────────────────┘
```

### Editor — type-specific fields

```text
orb:      Topic id  [t_adcd…          ]  Secret env  [ORB_AK  ▾]
          API base  [https://api.dev.orbnet.app]  Title prefix [marble: ]

webhook:  URL       [https://hooks.example.com/x]
          Method    [POST ▾]     Headers  [+ Content-Type: application/json]
          Template  [{"text":"{{.Preview}}","link":"{{.DeepLink}}"}]
          Secret env [________ (optional)]

slack:    Secret env [SLACK_WEBHOOK_URL ▾]  Channel [#ops]
          Username  [marble]                Icon [ :robot_face: ]

discord:  Secret env [DISCORD_WEBHOOK_URL ▾]  Username [marble]
          Avatar URL [________ (optional)]

ntfy:     Server    [https://ntfy.sh]   Topic [rinux-marble]
          Priority  [3 ▾]               Secret env [________ (optional)]

stdout:   Format    [plain ▾]   (plain | json)
```

Notes on the mockups:

- **Secret fields are dropdowns of env-var names** (ADR-0016) — the UI never shows a raw key; it offers existing `*_KEY`/`*_URL` names plus a free-text “add env name” option.
- **A “test” button** next to each sink sends a synthetic turn event so the operator can verify end-to-end without waiting for a real turn.
- **Invalid config is saved but marked disabled** with a validation note (e.g. `orb` missing `topic_id`), rather than silently failing at delivery.

## Alternatives considered

| Alternative | Why not default |
|-------------|-----------------|
| Agent-driven skill/MCP publish per turn | Not guaranteed on interrupt/error/context-limit; token cost; recursion risk |
| Cron polling of session files | Fragile to file format; needs dedup/order state; polling latency |
| External sidecar over SSE | Another always-on process + auth/durability; strictly more ops than a hook |
| One bespoke "Orb mirror" hard-coded | Not generic; adding Slack means a fork, not a new file |
| Named `endTurn()` hook instead of stream subscription | Works, but duplicates a signal the Event stream already carries; slightly more intrusive |

## Security & abuse

| Control | |
|---------|--|
| Secrets by name | `secret_env` only; never a raw key in config, ADR, or logs |
| Deep link is session-scoped | Points at `/s/{id}`; access still governed by session auth (ADR-0017) |
| No secret in the template output by default | Templates render `TurnEvent`, not env |
| No recursion | Sink delivery never starts a turn |
| Rate/burst | Bounded queue + coalescing prevents a noisy cron session from spamming a channel |

## Decisions locked (Q1–Q13)

Source: `adr/0028-answers.json` (`2026-09-11T22:40:54.873Z`). Q1–Q11, Q13 use rec; **Q12 is a custom decision** (Settings UI ships in the first wave).

| ID | Decision | Choice |
|----|----------|--------|
| **Q1** | Trigger = session Event stream subscriber (`status: idle`), no turn-loop change | rec |
| **Q2** | Generic webhook sink + Go `text/template`; Orb/Slack/ntfy/Discord as built-ins over it | rec |
| **Q3** | Deep-link auth = login-first (`/s/{id}` under ADR-0017); no share token in v1 | rec |
| **Q4** | `deep_link_base` per-sink with a global default | rec |
| **Q5** | Secrets by env-var name only (ADR-0016 `api_key_env`) | rec |
| **Q6** | All six predefined types ship in the first pass | rec |
| **Q7** | Full final message + first-line preview | rec |
| **Q8** | Skip tool-only turns by default; errors still mirror | rec |
| **Q9** | Filters = kinds + skip-empty + skip-cron + session regex + min-chars | rec |
| **Q10** | Coalescing optional `min_interval_sec`, off by default | rec |
| **Q11** | Fire-and-forget + bounded queue + idempotency key + bounded retry | rec |
| **Q12** | **Settings UI in first wave** (not deferred) | **custom** |
| **Q13** | Per-session three-state override (inherit/on/off) + session popover + "pause all" | rec |

## Locked context (L1–L6)

- **L1** Session event pub/sub already exists (`Subscribe`/`publish`, ADR-0010); reuse as the trigger.  
- **L2** Persistent settings + secrets-by-name already exist (ADR-0016 `api_key_env`).  
- **L3** SPA deep link `/s/{id}` already exists.  
- **L4** Sinks are harness code, not agent turns — never recurse into `/api/prompt`.  
- **L5** Additive + fire-and-forget; never block the turn.  
- **L6** Deterministic idempotency key; raw secrets never in config/logs/ADR.

> **Q12 consequence:** config canonicalizes to `$MEMORY` JSON + env secrets, but the **Settings UI lands in the first wave** (M2) — the sinks list + per-type editors + session toggle are part of the initial ship, not a follow-on.

## Implementation plan

| Milestone | Scope |
|-----------|--------|
| **M0** | This ADR + review HTML |
| **M1** | `internal/sink` — `TurnEvent`, `Sink`, `Manager`, filters, the six predefined types (`orb`, `webhook`, `slack`, `discord`, `ntfy`, `stdout`); config; subscribe to the session stream |
| **M2** | Settings UI (sinks list + per-type editors + test button); **session UI sink toggle (per-session inherit/on/off + pause-all)**; coalescing polish; per-sink delivery history/log |
| **M3** | Custom `text/template` editing in Settings; additional channel types as demand appears |

### Suggested PR slice (post-accept)

| PR | Scope |
|----|--------|
| S0 | `TurnEvent` + `Sink` + `Manager` + fake sink tests |
| S1 | `webhook` sink + template rendering + config load |
| S2 | `orb` sink (return_url header + idempotency) |
| S3 | `slack` / `discord` / `ntfy` / `stdout` constructors over the webhook base |
| S4 | Wire manager into the session stream subscription |
| S5 | Filters + coalescing + Settings UI (list + editors + test button) + session-level sink toggle |

## Success metrics

- A finished turn produces exactly one notification per enabled sink whose filters pass.  
- A retry (same session/turn) does not double-post a sink that honors the idempotency key.  
- Zero sinks configured → zero behavior change, zero token cost.  
- Adding a new channel is a new constructor file, not a fork of the manager.  
- No secrets in logs, config, or ADR.

## Risks

| Risk | Mitigation |
|------|------------|
| Sink outage / slow endpoint | Timeout + bounded retry + drop-with-log; non-blocking |
| Recursion / self-publish | Manager never re-enters `/api/prompt` |
| Notification spam | Filters + coalescing; skip-empty/skip-cron defaults |
| Deep link requires login | Login-first for v1 (Q3); share token is a documented follow-on |
| Leaking session content | Sinks are operator-configured; only the final message (already visible in the session) is mirrored |

## See also

- [ADR-0010 Agent-loop transparency](0010-agent-loop-transparency.md) — SSE / `Session.Subscribe`  
- [ADR-0016 Model API key auth](0016-model-api-key-auth.md) — `api_key_env` secret pattern  
- [ADR-0017 Google OAuth](0017-google-oauth-auth.md) — session auth / `/s/{id}` access  
- [ADR-0018 Selectable models](0018-selectable-models.md) — catalog / settings style  
- Orb ADR-0011 (`orbnet-app/orb`) — first-class `return_url` the orb sink maps onto

## Changelog

| Date | Note |
|------|------|
| 2026-09-11 | **Proposed** — generic turn-sink layer; Orb/Slack/ntfy/Discord/webhook over a template base; stream-subscription trigger; deep link as a concept; Q1–Q12 open for review |
| 2026-09-11 | **Added** predefined sink types (first pass): `orb`, `webhook`, `slack`, `discord`, `ntfy`, `stdout` — per-type config reference + Settings UI mockups |
| 2026-09-11 | **Added** per-session sink behavior: three-state override (inherit/on/off) per sink stored in session metadata, resolved at turn-end; session-level UI toggle + "pause all" |
| 2026-09-11 | **Accepted** — locked Q1–Q13 (`2026-09-11T22:40:54.873Z`); Q1–Q11, Q13 rec; Q12 custom (Settings UI in first wave) |
| 2026-09-11 | **Implemented (M1+M2)** — `internal/sink` (`TurnEvent`, manager, filters, six types), `$MEMORY/sinks.json`, stream subscription on `status:idle`, Settings → Sinks (list/editors/test), session Sinks popover (inherit/on/off + pause-all) |
| 2026-09-14 | **Orb `run_id`** — orb sink sets `X-Orb-Run-Id` to the Marble session id so related turns group in Orb clients |
| 2026-09-14 | **`manage_sinks` tool** — sessions can list/create/update/delete/test sinks and set per-session overrides; secrets by env-var name only |
