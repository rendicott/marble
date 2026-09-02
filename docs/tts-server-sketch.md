# Server-side TTS — payload sketch (ADR-0027)

Companion to [adr/0027-server-side-tts.md](../adr/0027-server-side-tts.md).

## Status probe

```http
GET /api/tts/status
```

```json
{
  "enabled": true,
  "provider": "elevenlabs",
  "configured": true,
  "default_voice": "21m00Tcm4TlvDq8ikWAM",
  "default_model": "eleven_flash_v2_5",
  "cache": true,
  "format": "mp3",
  "max_chars_per_request": 4000
}
```

## On-demand synthesize

```http
POST /api/sessions/0wcgcwedgn/tts
Content-Type: application/json
```

```json
{
  "text": "Your package is in transit with USPS. Estimated delivery Thursday.",
  "message_id": "m-0wcgcwed-9",
  "phase_id": "p1"
}
```

```json
{
  "attachment_id": "att_tts_01HZX…",
  "mime": "audio/mpeg",
  "bytes": 48210,
  "duration_ms": 4200,
  "voice": "21m00Tcm4TlvDq8ikWAM",
  "provider": "elevenlabs",
  "model": "eleven_flash_v2_5",
  "cached": false,
  "url": "/api/sessions/0wcgcwedgn/attachments/att_tts_01HZX…"
}
```

Cache hit (same text+voice+model):

```json
{
  "attachment_id": "att_tts_01HZX…",
  "mime": "audio/mpeg",
  "cached": true,
  "url": "/api/sessions/0wcgcwedgn/attachments/att_tts_01HZX…"
}
```

## Phase with audio (ADR-0025 additive)

```json
{
  "id": "msg_assistant_9",
  "role": "assistant",
  "content": "# Status\n\nPackage **in transit**…",
  "presentation": {
    "protocol_version": 1,
    "phases": [
      {
        "id": "p1",
        "speech_text": "Your package is in transit with USPS.",
        "prose_markdown": "Package **in transit** with USPS.",
        "visual": {
          "kind": "table",
          "table": {
            "headers": ["Field", "Value"],
            "rows": [["Carrier", "USPS"], ["ETA", "Thu"]]
          }
        },
        "audio": {
          "attachment_id": "att_tts_01HZX…",
          "mime": "audio/mpeg",
          "duration_ms": 3200,
          "voice": "21m00Tcm4TlvDq8ikWAM",
          "provider": "elevenlabs"
        }
      },
      {
        "id": "p2",
        "speech_text": "Here is the route sketch.",
        "visual": {
          "kind": "svg",
          "svg": "<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 100 40\">…</svg>"
        }
      }
    ]
  }
}
```

`p2` has no `audio` yet → client calls `POST …/tts` on unmute/prefetch or uses platform TTS.

## Client play algorithm

```text
play(phase):
  stop current audio/tts
  if phase.audio?.attachment_id:
     stream GET attachment
  else if tts_status.enabled && configured:
     r = POST tts { text: phase.speech_text, phase_id, message_id }
     stream r.url
     optionally remember r on phase locally for scrubber
  else:
     platform TtsPlayer.speak(phase.speech_text)

prefetch(phase_next):
  if no audio and tts configured: POST tts in background (ignore errors)
```

## Config example (conceptual)

```json
{
  "tts": {
    "enabled": true,
    "provider": "elevenlabs",
    "api_key_env": "ELEVENLABS_API_KEY",
    "default_voice": "21m00Tcm4TlvDq8ikWAM",
    "default_model": "eleven_flash_v2_5",
    "format": "mp3",
    "cache": true,
    "max_chars_per_request": 4000,
    "max_concurrent": 2,
    "eager_on_wonderstand_turn": false
  }
}
```

```bash
# env (never commit)
ELEVENLABS_API_KEY=...
```

## Error shapes

| HTTP | Body (sketch) |
|------|----------------|
| 503 | `{"error":"tts_disabled"}` |
| 503 | `{"error":"tts_not_configured"}` |
| 400 | `{"error":"text_too_long","max":4000}` |
| 502 | `{"error":"provider_failed","provider":"elevenlabs"}` |
