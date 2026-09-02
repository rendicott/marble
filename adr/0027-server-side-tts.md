# ADR-0027: Server-side TTS — neural narration for Wonderstand (and clients)

| Field | Value |
|-------|--------|
| **Status** | **Accepted** (implemented — harness + Wonderstand client) |
| **Date** | 2026-08-18 |
| **Accepted** | 2026-08-20 |
| **Author** | — |
| **Deciders** | Project owner |
| **Tags** | tts, audio, elevenlabs, openai, wonderstand, immersion, attachments, presentation, config |
| **Extends** | ADR-0025 (Wonderstand protocol / `speech_text`), ADR-0024 (Wonderstand client), ADR-0019 (session attachments / blobs), ADR-0016 (API key env pattern), ADR-0018 (catalog / secrets style), ADR-0010 (SSE) |
| **Supersedes (partial)** | ADR-0019 non-goal “no audio/voice wire” — **outbound synthesized narration only**. Inbound mic/STT and `cap_voice` model wire remain out of scope. |
| **Review UI** | [0027-review.html](0027-review.html) |
| **Sketch** | [docs/tts-server-sketch.md](../docs/tts-server-sketch.md) |
| **Answers** | [0027-answers.json](0027-answers.json) (`2026-08-20T20:30:32.189Z`) — all Q1–Q10 locked |

## Summary

Add **optional server-side text-to-speech** to the Marble harness so immersion clients (Wonderstand first) can play **neural narration** instead of awkward on-device `TextToSpeech`.

Design principles:

1. **Pluggable providers** — config chooses `elevenlabs` | `openai` | `none` (more later). Not hard-wired to one vendor forever.  
2. **Secrets like models** — only **env var names** in config (`ELEVENLABS_API_KEY`); never keys in git, ADR answers, or tool args.  
3. **Audio as session attachments** — reuse ADR-0019 blob store + GET; MIME `audio/mpeg` (or `audio/wav` / `audio/ogg` as provider requires).  
4. **Protocol additive** — optional `phase.audio` (and/or message-level audio refs) on ADR-0025 presentation; plain `speech_text` remains required for fallback and web.  
5. **Client fallback** — if TTS disabled, key missing, synthesize fails, or offline → Wonderstand keeps platform TTS.  
6. **Cache** — hash(`provider` + `model` + `voice` + `speech_text`) → reuse blob; immersion replay is free.  
7. **Phased delivery** — config + interface → on-demand API → Wonderstand player → optional eager synthesize on turn complete.

## Context & pain

### Today

| Layer | Behavior |
|-------|----------|
| **Wonderstand** | `TtsPlayer` → Android `TextToSpeech`; mute default; `SpeechText.clean()`; segments from presentation or `ArtifactParser` |
| **Harness** | ADR-0025 `speech_text` plain per phase; **no** audio generation; attachments are image/doc-oriented (ADR-0019 v1 explicitly deferred audio) |
| **Operator** | Family internal testing; immersion diagrams work; **voice still robotic** |

### Observed pain

| Pain | Why it matters |
|------|----------------|
| On-device TTS awkward | OEM engines, flat prosody, choppy multi-utterance segments |
| No shared voice identity | Each phone sounds different; no “Wonderstand narrator” brand |
| Client-held API keys | Bad for Family APK; billing and rotation live on device |
| Replay cost / latency if naive | Re-synthesize every unmute without cache |
| ADR-0019 “no audio” | Blocks first-class audio attachments until this ADR opens **outbound** audio |

Research (2026): ElevenLabs / OpenAI gpt-4o-mini-tts / Google Chirp / Cartesia all viable; **server-side + cache + fallback** is the product fit for Marble + Wonderstand.

## Goals

1. **Config-gated TTS** on the harness: enable/disable, provider, voice, model, key env name.  
2. **Provider interface** `Synthesize(ctx, req) → (audio []byte, mime string, meta)` with **ElevenLabs** as first implementation (**Q1** locked).  
3. **On-demand HTTP API** so clients can request audio for text (or phase id) without blocking the agent turn (**Q2** locked).  
4. **Durable audio attachments** under the session; safe GET like other attachments (inline playable where appropriate).  
5. **Wire into presentation** — optional `audio` on phase pointing at `attachment_id` (+ duration/mime/voice meta).  
6. **Wonderstand consume path** — prefer phase audio / attachment URL; else platform TTS.  
7. **Cache + caps** — avoid duplicate spend; bound text length and concurrent synth jobs.  
8. **No regression** when TTS off — web UI and non-Wonderstand clients unchanged.

## Non-goals

| Non-goal | Rationale |
|----------|-----------|
| Inbound speech-to-text / mic pipeline on harness | Wonderstand already uses on-device STT; separate ADR |
| Model `cap_voice` / multimodal audio **to** the LLM | ADR-0019 / 0018 territory; not immersion playback |
| Real-time duplex voice agent | Different product (Cartesia/LiveKit-class); not phase read-aloud |
| Shipping vendor SDKs inside the Android APK | Keys and synthesis stay on rinux |
| Perfect lip-sync / word timestamps in p1 | Optional later (`alignment`); scrubber stays phase-level in p1 |
| Replacing ADR-0025 `speech_text` | Audio is enrichment; speech_text remains canonical for fallback and a11y |
| Multi-tenant billing UI | Single-operator Family harness; env key is enough |
| Guaranteed offline neural voice | Offline = platform TTS only |

## Design

### A. Config (process / settings)

Conceptual shape (p1: flags and/or JSON under `$MEMORY`; Settings UI later — **Q3** locked):

```json
{
  "tts": {
    "enabled": false,
    "provider": "none",
    "api_key_env": "ELEVENLABS_API_KEY",
    "default_voice": "",
    "default_model": "",
    "format": "mp3",
    "cache": true,
    "max_chars_per_request": 4000,
    "max_concurrent": 2,
    "eager_on_wonderstand_turn": false
  }
}
```

| Field | Notes |
|-------|--------|
| `enabled` | Master switch; false ⇒ API returns 503/disabled; clients use platform TTS |
| `provider` | `none` \| `elevenlabs` \| `openai` (extensible) |
| `api_key_env` | **Name** of env var only (ADR-0016 style) |
| `default_voice` / `default_model` | Provider-specific ids |
| `cache` | Content-addressed blob reuse |
| `eager_on_wonderstand_turn` | If true, after final assistant + presentation, synth all phase `speech_text` (**Q2** / **Q4**) |

Operator puts secrets in **`$MEMORY/env`** (Settings → Secrets) or process environment:

```bash
ELEVENLABS_API_KEY=sk_...
# or OPENAI_API_KEY=... when provider=openai
```

### B. Package layout (harness)

```text
internal/tts/
  tts.go           // Provider interface, Request/Result, cache key
  config.go        // load/validate
  cache.go         // hash → attachment id or blob id
  elevenlabs.go    // first provider
  openai.go        // optional second (same milestone or M2)
```

```go
type Request struct {
    Text     string
    Voice    string // empty → default
    Model    string
    Format   string // mp3|wav|…
    SessionID string // for attachment ownership
    PhaseID   string // optional correlation
    MessageID string
}

type Result struct {
    MIME     string
    Bytes    []byte
    Duration time.Duration // if known
    Provider string
    Voice    string
    Model    string
    Cached   bool
}

type Provider interface {
    Name() string
    Synthesize(ctx context.Context, req Request) (Result, error)
}
```

### C. HTTP API

#### C1. On-demand synthesize (normative for p1 — **Q2** locked)

```http
POST /api/sessions/{id}/tts
Content-Type: application/json

{
  "text": "Your package is in transit with USPS.",
  "voice": optional,
  "model": optional,
  "message_id": optional,
  "phase_id": optional
}
```

**Response 200:**

```json
{
  "attachment_id": "att_…",
  "mime": "audio/mpeg",
  "bytes": 48210,
  "duration_ms": 4200,
  "voice": "…",
  "provider": "elevenlabs",
  "cached": false,
  "url": "/api/sessions/{id}/attachments/att_…"
}
```

| Status | When |
|--------|------|
| 200 | Synth or cache hit |
| 400 | Empty text / over max chars |
| 404 | Session missing |
| 409 | **Not used for busy** — TTS is allowed while the session is busy (**Q5** locked: read-only synth; does not start an agent turn) |
| 503 | TTS disabled / no key / provider error |

Auth: same as other session APIs (ADR-0017). Any authenticated session client may call TTS (**Q10**); Wonderstand is primary UX, not sole caller.

#### C2. Optional batch

```http
POST /api/sessions/{id}/tts/batch
{ "items": [ { "text":"…", "phase_id":"p1" }, … ] }
```

Returns array of results; used by eager path or client prefetch. Cap items (e.g. 32 = presentation phase cap).

#### C3. Attachment GET

Reuse `GET /api/sessions/{id}/attachments/{attId}`:

- Serve `Content-Type: audio/mpeg` (etc.)
- Prefer **inline** disposition for audio so mobile players and `ExoPlayer` can stream (**Q6** locked — extend ADR-0019 safe GET allowlist: `audio/mpeg`, `wav`, `ogg`, `mp4`, `webm`).

### D. Presentation wire (extends ADR-0025)

Additive on `Phase` (protocol 1 compatible if clients ignore unknown fields; bump guidance in Wonderstand):

```json
{
  "id": "p1",
  "speech_text": "Your package is in transit with USPS.",
  "prose_markdown": "Package **in transit**…",
  "visual": { "kind": "table", "table": { "…": "…" } },
  "audio": {
    "attachment_id": "att_…",
    "mime": "audio/mpeg",
    "duration_ms": 4200,
    "voice": "Rachel",
    "provider": "elevenlabs"
  }
}
```

| Rule | Detail |
|------|--------|
| `speech_text` | **Still required** for fallback / a11y / cache key input |
| `audio` absent | Client synthesizes via C1 or platform TTS |
| `audio.attachment_id` | Must be session-scoped; resolve via existing attachment GET |
| Eager path | Off by default; optional `eager_on_wonderstand_turn` fills `audio` later (**Q4**, M4) |
| Protocol version | **No bump** — stay on presentation protocol 1 with additive `audio`; clients that do not understand it ignore it (**Q7**). `speech_text` remains required. Only bump later if audio becomes mandatory or speech semantics change |

### E. Cache

```text
key = sha256(provider + "\0" + model + "\0" + voice + "\0" + normalize(text))
```

| Behavior | |
|----------|--|
| Hit | Return existing `attachment_id` (or duplicate ref); `cached: true` |
| Miss | Call provider; `SpillBlob` / session attachment row; store key → att id index |
| GC | Same as session blobs (ADR-0019 maintenance); optional global TTS cache dir under `$MEMORY/tts-cache/` for cross-session reuse (**Q8** locked) |

**Normalize:** Unicode NFC, collapse whitespace — avoid cache misses on trivial spacing.

### F. When to synthesize

| Mode | Trigger | Pros | Cons |
|------|---------|------|------|
| **On-demand only (p1 rec)** | Client unmute / prefetch next phase | No spend if muted; simple | First play latency |
| **Eager Wonderstand** | End of turn if `client.name=wonderstand` && presentation | Instant unmute | Cost even if never played |
| **Hybrid** | On-demand + prefetch N+1 while N plays | Best UX | Slightly more client logic |

**Locked (**Q2** / **Q4**):** p1 = **on-demand + client prefetch next phase**; eager off by default (`eager_on_wonderstand_turn: false`).

### G. Wonderstand client (consume)

```text
on play phase P:
  if P.audio?.attachment_id:
    play stream(baseUrl + attachment GET)
  else if harness tts enabled (settings or probe):
    POST …/tts { text: P.speech_text, phase_id, message_id }
    play result.url
  else:
    TtsPlayer.speak(P.speech_text)  // platform
```

| UX | |
|----|--|
| Mute | No network synth (unless already cached and user scrubbing — still OK to fetch cache) |
| Back / leave immersion | Stop audio player (same as TTS stop today) |
| Scrubber | Seek within clip if `duration_ms` known; else restart clip from phase start in p1 |
| Settings | Optional: “Neural narration” on/off; show provider status from `GET /api/health` or `/api/tts/status` |

### H. Health / status

```http
GET /api/tts/status
```

```json
{
  "enabled": true,
  "provider": "elevenlabs",
  "configured": true,
  "default_voice": "…",
  "cache": true
}
```

No secrets. `configured: false` if env empty.

### I. Provider notes (implementers)

#### ElevenLabs (first)

- REST text-to-speech; voice_id + model_id. **Default tier = Flash** (low latency immersion); Quality/multilingual model + voice override via config (**Q9** locked).  
- MP3 output default.  
- Errors: map 401 → configured false / 503; 429 → retry-after or 503.

#### OpenAI (second / alternative)

- `audio/speech` (`tts-1`, `tts-1-hd`, `gpt-4o-mini-tts`).  
- Reuse `OPENAI_API_KEY` or dedicated env; may share model catalog base URL patterns where applicable.

#### Future

Google Chirp, Azure, Cartesia — same `Provider` interface.

### J. Security & abuse

| Control | |
|---------|--|
| Auth | Session API auth required |
| Max chars | Default 4 KiB per request (align presentation speech cap 2 KiB soft; allow slightly more for non-phase use) |
| Max concurrent | Global semaphore (default 2) |
| MIME allowlist on GET | `audio/mpeg`, `audio/mp4`, `audio/wav`, `audio/ogg`, `audio/webm` |
| No open proxy | Only synthesize **text body**, not arbitrary URLs |
| Logging | Log provider, bytes, cached, latency — **never** full API key; truncate text in logs |

### K. Web UI

**p1 non-goal to ship full web immersion player.** Optional later: small “▶ speak” on assistant bubbles when TTS configured. Does not block Wonderstand.

## Alternatives considered

| Alternative | Why not default |
|-------------|-----------------|
| On-device only (voice pick + rate) | Cheap polish; does not meet “natural narrator” bar |
| App → ElevenLabs direct | Keys in APK / user accounts; no shared cache with sessions |
| Always-on eager synth for every session | Wastes $ on web/cron sessions |
| Replace speech_text with audio-only phases | Breaks fallback, search, a11y |
| New `/api/wonderstand/tts` only | Prefer session-scoped generic TTS; Wonderstand is primary client not sole |
| Store base64 audio inside `presentation_json` | Blows SQLite/SSE; use attachments |
| Local open-weight TTS on rinux GPU | Attractive later; ops-heavy; not p1 |

## Decisions locked (Q1–Q10)

Source: `adr/0027-answers.json` (`2026-08-20T20:30:32.189Z`). All recommendations accepted.

| ID | Decision | Locked |
|----|----------|--------|
| **Q1** | First provider **ElevenLabs**; `Provider` interface ready for OpenAI (or others) as second implementation without API redesign | 2026-08-20 |
| **Q2** | p1 = on-demand `POST /api/sessions/{id}/tts` + client prefetch of next phase; `eager_on_wonderstand_turn` defaults **false** | 2026-08-20 |
| **Q3** | p1 config via **JSON file and/or process flags** (plus env secret); Settings UI toggle/status can follow once core works | 2026-08-20 |
| **Q4** | Do **not** fill `phase.audio` on turn complete by default; optional `eager_on_wonderstand_turn` for Wonderstand sessions later (**M4**) | 2026-08-20 |
| **Q5** | **Allow** TTS while session busy — synthesize does not start an agent turn; concurrent with tools/model | 2026-08-20 |
| **Q6** | Attachment GET serves `audio/*` **inline** (playable); extend ADR-0019 safe GET allowlist for `audio/mpeg`, `wav`, `ogg`, `mp4`, `webm` | 2026-08-20 |
| **Q7** | **No** presentation `protocol_version` bump for `phase.audio` — additive optional field; old clients ignore unknown keys; `speech_text` remains required | 2026-08-20 |
| **Q8** | Cache: content hash → session attachment; optional global `$MEMORY/tts-cache` for cross-session reuse of same hash | 2026-08-20 |
| **Q9** | Default ElevenLabs model tier = **Flash** (low latency) for immersion; allow Quality/multilingual model + voice override in config | 2026-08-20 |
| **Q10** | Any authenticated session client may call `POST …/tts`; Wonderstand is primary UX but API is not wonderstand-only | 2026-08-20 |

## Implementation plan

| Milestone | Scope | Owner |
|-----------|--------|--------|
| **M0** | This ADR + review HTML + sketch | Docs |
| **M1** | `internal/tts` + ElevenLabs + config + status + `POST …/tts` + audio attachment MIME | Harness |
| **M2** | Cache (hash → blob); batch optional; health fields | Harness |
| **M3** | Wonderstand: ExoPlayer/MediaPlayer path; prefetch; stop on back; settings probe | App |
| **M4** | Optional eager `phase.audio` fill; OpenAI provider; web ▶ speak | Harness + web |
| **M5** | Metrics (chars, $, latency); voice catalog endpoint | Harness |

### Suggested PR slice (post-accept)

| PR | Scope |
|----|--------|
| **T0** | Config + interface + fake provider tests |
| **T1** | ElevenLabs client + `POST /api/sessions/{id}/tts` |
| **T2** | Audio MIME on attachment GET + spill helpers |
| **T3** | Cache layer |
| **T4** | Wonderstand playback + fallback |
| **T5** | Optional eager + `phase.audio` write-back |
| **T6** | OpenAI provider / status in Settings |

## Success metrics

- Unmute immersion on neural path sounds clearly more natural than platform TTS on a mid-range Android.  
- Second play of same phase is cache hit (`cached: true`) and starts quickly.  
- TTS disabled or no key → identical behavior to pre-ADR Wonderstand (platform TTS).  
- Web sessions with no client call incur **$0** TTS.  
- No API keys in logs, APK, or git.

## Risks

| Risk | Mitigation |
|------|------------|
| Provider outage / 429 | Fallback platform TTS; 503 + client catch |
| Cost surprise | Off by default; cache; max chars; no eager default |
| Latency on first unmute | Prefetch next phase; Flash model; show short buffering UI |
| Large MP3 in memory | Stream to blob file; cap text length |
| ADR-0019 GET treats unknown as download-only | Explicit audio inline branch (**Q6**) |
| SSML / markdown leak into provider | Only synth `speech_text` / API `text` after normalize; never raw `content` |

## See also

- [ADR-0025 Wonderstand protocol](0025-wonderstand-protocol.md) — `speech_text`, phases  
- [ADR-0024 Wonderstand client](0024-wonderstand-client.md)  
- [ADR-0019 Multimodal attachments](0019-multimodal-attachments.md) — blobs, GET, MIME  
- [ADR-0016 Model API key auth](0016-model-api-key-auth.md) — `api_key_env` pattern  
- [docs/tts-server-sketch.md](../docs/tts-server-sketch.md)  
- marble-wonderstand `TtsPlayer`, immersion UX notes  

## Changelog

| Date | Note |
|------|------|
| 2026-08-18 | **Proposed** — server-side pluggable TTS; ElevenLabs-first; on-demand API; audio attachments; phase.audio additive; Q1–Q10 open for review |
| 2026-08-20 | **Accepted** — locked Q1–Q10 (`2026-08-20T20:30:32.189Z`); ElevenLabs + Flash default; on-demand TTS; no protocol bump; busy allowed; audio GET inline |
| 2026-08-20 | **Implemented (harness p1)** — `internal/tts`, `$MEMORY/tts.json`, `GET /api/tts/status`, `POST …/tts`, audio attachments + inline GET, global `tts-cache`. |
| 2026-08-20 | **Implemented remaining** — OpenAI provider; eager `phase.audio` (config); Settings → TTS; web ▶ speak; batch POST; health TTS fields; Wonderstand ImmersionAudioPlayer (prefer attachment → synth → platform TTS). |
