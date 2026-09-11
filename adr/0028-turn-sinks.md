# ADR-0028: Generic turn sinks — mirror every session response to external channels

| Field | Value |
|-------|--------|
| **Status** | **Proposed** (2026-09-11) — open for review |
| **Date** | 2026-09-11 |
| **Author** | — |
| **Deciders** | Project owner |
| **Tags** | notifications, mirror, sink, webhook, orb, slack, ntfy, discord, events, config |
| **Extends** | ADR-0010 (SSE / `Session.Subscribe`), ADR-0016 (API-key env pattern), ADR-0017 (OAuth / session auth), ADR-0018 (config style), ADR-0019 (attachments / blobs) |
| **Review UI** | [0028-review.html](0028-review.html) |
| **Answers** | *(to be written after review)* |

## Summary

Add an optional, transport-agnostic **turn-sink** layer to the harness. When a session finishes a turn, the harness hands a small, normalized **turn event** to zero or more configured **sinks**. A sink is a named adapter (Orb, Slack, ntfy, Discord, or a generic HTTP webhook) that receives the final assistant response plus a **deep link back to the session** and projects it into that channel's native notification mechanism.

Design principles:

1. **Not an "Orb mirror".** Orb is one built-in sink over a generic webhook base; Slack / ntfy / Discord / "any endpoint" are the same interface with a different payload template.  
2. **Trigger is the existing event stream.** The sink manager subscribes to `Session.Subscribe()` and fires on turn-complete (`status: idle`) — no changes to the agent turn loop.  
3. **Deep link is a concept, not a field.** The event carries a transport-neutral `DeepLink`; each sink maps it to its native "click" affordance (Orb `X-Orb-Return-Url`, Slack link/button, ntfy `Click:` header, Discord embed `url`).  
4. **Secrets by env-var name** (ADR-0016 style) — config stores `secret_env`, never the value.  
5. **Fire-and-forget, additive, no recursion** — sinks never block the turn and never start a new agent turn.  
6. **Declarative filters per sink** — kinds, skip-empty, skip-cron, session regex, min chars, optional coalescing.

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
7. **No regression** — with zero sinks configured (or the feature off), behavior is identical to today.

## Non-goals

| Non-goal | Rationale |
|----------|-----------|
| Agent-driven publishing (a skill/MCP "remember to post") | Already possible today; this ADR is the *reliable, every-turn* path |
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
| **orb** | `X-Orb-Return-Url: {{.DeepLink}}` header (ADR-orb-0011 `return_url`) |
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

## Decisions (open, Q1–Q12)

All **open questions recommend "use rec"**; see `0028-review.html` (interactive kit).

- **Q1** Trigger → subscribe to the session Event stream (not a named hook).  
- **Q2** Abstraction → generic webhook sink + templates; Orb/Slack/ntfy/Discord as built-ins over it.  
- **Q3** Deep-link auth → login-first (reuse `/s/{id}` under ADR-0017); no expiring share token in v1.  
- **Q4** `deep_link_base` → per-sink with a global default.  
- **Q5** Secrets → env-var name only (ADR-0016 `api_key_env`).  
- **Q6** First sinks → `orb` + `webhook`; `slack`/`ntfy`/`discord` next.  
- **Q7** Payload → full final message (not preview-only).  
- **Q8** Tool-only turns → skip by default (only turns with a final assistant message).  
- **Q9** Filters → kinds + skip-empty + skip-cron + session regex + min-chars.  
- **Q10** Coalescing → optional `min_interval_sec` per sink, off by default.  
- **Q11** Delivery → fire-and-forget + bounded queue + idempotency key + bounded retry.  
- **Q12** Config surface → `$MEMORY` JSON file + env secrets; Settings UI later.

## Locked context (L1–L6)

- **L1** Session event pub/sub already exists (`Subscribe`/`publish`, ADR-0010); reuse as the trigger.  
- **L2** Persistent settings + secrets-by-name already exist (ADR-0016 `api_key_env`).  
- **L3** SPA deep link `/s/{id}` already exists.  
- **L4** Sinks are harness code, not agent turns — never recurse into `/api/prompt`.  
- **L5** Additive + fire-and-forget; never block the turn.  
- **L6** Deterministic idempotency key; raw secrets never in config/logs/ADR.

## Implementation plan

| Milestone | Scope |
|-----------|--------|
| **M0** | This ADR + review HTML |
| **M1** | `internal/sink` — `TurnEvent`, `Sink`, `Manager`, filters, `webhook` sink, `orb` sink; config; subscribe to the session stream |
| **M2** | `slack`, `ntfy`, `discord` templates + a `stdout` test sink; Settings UI for sinks |
| **M3** | Coalescing polish, per-sink delivery history/log, custom `text/template` in settings |

### Suggested PR slice (post-accept)

| PR | Scope |
|----|--------|
| S0 | `TurnEvent` + `Sink` + `Manager` + fake sink tests |
| S1 | `webhook` sink + template rendering + config load |
| S2 | `orb` sink (return_url header + idempotency) |
| S3 | Wire manager into the session stream subscription |
| S4 | Filters + coalescing |
| S5 | Slack/ntfy/Discord templates + stdout sink |

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
