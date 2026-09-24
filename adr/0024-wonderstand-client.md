# ADR-0024: Wonderstand — Native Android client (pointer)

| Field | Value |
|-------|--------|
| **Status** | **Accepted** — implemented (Wonderstand client, APK 0.1.10) |
| **Date** | 2026-08-07 |
| **Tags** | wonderstand, android, client, pointer |
| **Canonical design** | **`marble-wonderstand`** repo / folder: [`adr/0001-wonderstand-client.md`](../../marble-wonderstand/adr/0001-wonderstand-client.md) (path relative when trees are siblings under `projects/`) |
| **Extends** | ADR-0010, ADR-0017, ADR-0019; optional Clerk ADR-0023 later |

## Summary

**Wonderstand** is a separate Android application that uses **this harness** as the agent engine over HTTP/SSE (typically Tailscale). It optimizes for:

- multi-session bounce on a phone  
- camera / share / upload image intake  
- voice in (STT) and spoken replies (TTS)  
- **immersion mode**: visual-first artifacts full-screen, prose as audio, swipe between visual beats  

The web UI remains the operator cockpit (settings, models, peer, cron). Wonderstand does **not** replace it.

## Harness impact (MVP)

**None required** for a first client: existing sessions/messages/attachments/SSE APIs suffice. Optional later:

- `X-Marble-Client: wonderstand` for metrics  
- Wonderstand-oriented soul snippet  
- Structured `artifacts[]` metadata on assistant messages  

## Pointer only

Do not duplicate the full design here. Lock decisions and implement in:

```
projects/marble-wonderstand/
  README.md
  adr/0001-wonderstand-client.md
  adr/0001-answers.json
  docs/immersion-timeline.md
```

## See also

- [ADR-0025 Wonderstand Protocol](0025-wonderstand-protocol.md) — structured `presentation` / client advertise (**Accepted**, Q1–Q8 locked)
- [docs/wonderstand-protocol-sketch.md](../docs/wonderstand-protocol-sketch.md)
