# Wonderstand Protocol — payload sketch (ADR-0025)

**Status:** sketch for protocol **1** (**Accepted**). Normative decisions: [adr/0025-wonderstand-protocol.md](../adr/0025-wonderstand-protocol.md); locked answers: [adr/0025-answers.json](../adr/0025-answers.json) (`2026-08-08T16:07:31.856Z`).

This document shows **example multi-beat + SVG** payloads on real harness shapes (`Message`, create/post bodies, SSE). It is not an OpenAPI spec.

## 1. Client advertise

### Create session

`POST /api/sessions`

```json
{
  "title": "ws: package status",
  "client": {
    "name": "wonderstand",
    "protocol": 1
  }
}
```

### Post user message

`POST /api/sessions/{id}/messages`

```json
{
  "content": "What's the status of the package and what does tracking look like?",
  "attachment_ids": [],
  "client": {
    "name": "wonderstand",
    "protocol": 1
  }
}
```

Optional header (metrics only; body is normative for protocol):

```http
X-Marble-Client: wonderstand
```

## 2. Assistant message with presentation (hydrate + SSE)

`GET /api/sessions/{id}` → `messages[]` and SSE `{"type":"message","message":{…}}` share the same object shape.

### Full multi-beat example

Canonical immersion story: **table → screenshot → SVG route sketch**.

```json
{
  "id": "msg_asst_01",
  "role": "assistant",
  "content": "# Package status\n\nYour package is **in transit** with USPS.\n\n| Field | Value |\n| --- | --- |\n| Carrier | USPS |\n| ETA | Thu |\n| Last scan | Regional hub |\n\nTracking page:\n\n![tracking](attachment:att_abc)\n\n```svg\n<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 200 80\" role=\"img\">\n  <rect width=\"200\" height=\"80\" fill=\"#0f1115\"/>\n  <circle cx=\"30\" cy=\"40\" r=\"8\" fill=\"#5eead4\"/>\n  <circle cx=\"100\" cy=\"40\" r=\"8\" fill=\"#7c9cff\"/>\n  <circle cx=\"170\" cy=\"40\" r=\"8\" fill=\"#fbbf24\"/>\n  <path d=\"M38 40 H92 M108 40 H162\" stroke=\"#9aa3b5\" stroke-width=\"2\"/>\n  <text x=\"20\" y=\"68\" fill=\"#9aa3b5\" font-size=\"10\">Origin</text>\n  <text x=\"88\" y=\"68\" fill=\"#9aa3b5\" font-size=\"10\">Hub</text>\n  <text x=\"155\" y=\"68\" fill=\"#9aa3b5\" font-size=\"10\">Local</text>\n</svg>\n```\n",
  "created_at": "2026-08-08T12:00:00Z",
  "attachments": [
    {
      "id": "att_abc",
      "name": "tracking.png",
      "mime": "image/png",
      "kind": "image",
      "size": 184320
    }
  ],
  "presentation": {
    "protocol_version": 1,
    "phases": [
      {
        "id": "p1",
        "speech_text": "Your package is in transit with USPS. Estimated arrival Thursday. Last scan was the regional hub.",
        "prose_markdown": "Your package is **in transit** with USPS. ETA **Thu**.",
        "visual": {
          "kind": "table",
          "table": {
            "headers": ["Field", "Value"],
            "rows": [
              ["Carrier", "USPS"],
              ["ETA", "Thu"],
              ["Last scan", "Regional hub"]
            ]
          }
        }
      },
      {
        "id": "p2",
        "speech_text": "Here is the tracking page screenshot.",
        "prose_markdown": "Tracking page:",
        "visual": {
          "kind": "image_attachment_id",
          "image_attachment_id": "att_abc"
        }
      },
      {
        "id": "p3",
        "speech_text": "Route sketch from origin through the hub to local delivery.",
        "visual": {
          "kind": "svg",
          "svg": "<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 200 80\" role=\"img\"><rect width=\"200\" height=\"80\" fill=\"#0f1115\"/><circle cx=\"30\" cy=\"40\" r=\"8\" fill=\"#5eead4\"/><circle cx=\"100\" cy=\"40\" r=\"8\" fill=\"#7c9cff\"/><circle cx=\"170\" cy=\"40\" r=\"8\" fill=\"#fbbf24\"/><path d=\"M38 40 H92 M108 40 H162\" stroke=\"#9aa3b5\" stroke-width=\"2\"/><text x=\"20\" y=\"68\" fill=\"#9aa3b5\" font-size=\"10\">Origin</text><text x=\"88\" y=\"68\" fill=\"#9aa3b5\" font-size=\"10\">Hub</text><text x=\"155\" y=\"68\" fill=\"#9aa3b5\" font-size=\"10\">Local</text></svg>"
        }
      }
    ]
  }
}
```

### How the player maps phases

| Phase `id` | Audio (`speech_text`) | Screen (`visual`) |
|------------|----------------------|-------------------|
| `p1` | In transit / ETA / last scan | Structured table |
| `p2` | Tracking screenshot line | `GET …/attachments/att_abc?inline=1` |
| `p3` | Route sketch | Inline SVG (no fence parse) |

Scrubber jumps by **`phase.id`**, not by re-tokenizing markdown.

### Speech-only follow-up phase (hold visual)

If Q3 locks “hold previous”:

```json
{
  "id": "p4",
  "speech_text": "I can set a reminder when the status changes. Want that?",
  "prose_markdown": "I can set a reminder when the status changes. Want that?"
}
```

Screen stays on `p3` SVG until a later phase supplies a new `visual`.

## 3. HTML card visual

```json
{
  "id": "p1",
  "speech_text": "Here is a one-screen status card.",
  "visual": {
    "kind": "html",
    "html": "<!DOCTYPE html><html><head><meta charset=\"utf-8\"><style>body{font-family:system-ui;background:#0f1115;color:#e8ecf4;padding:1rem} .ok{color:#4ade80}</style></head><body><h1 class=\"ok\">In transit</h1><p>USPS · ETA Thu</p></body></html>"
  }
}
```

Client: WebView with scripts disabled (Wonderstand policy).

## 4. Minimal presentation (single SVG beat)

When the model only needs one visual:

```json
{
  "protocol_version": 1,
  "phases": [
    {
      "id": "p1",
      "speech_text": "Architecture sketch of harness and Wonderstand.",
      "visual": {
        "kind": "svg",
        "svg": "<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 240 100\"><rect x=\"10\" y=\"30\" width=\"90\" height=\"40\" rx=\"6\" fill=\"#1e2330\" stroke=\"#7c9cff\"/><text x=\"28\" y=\"55\" fill=\"#e8ecf4\" font-size=\"12\">harness</text><rect x=\"140\" y=\"30\" width=\"90\" height=\"40\" rx=\"6\" fill=\"#1e2330\" stroke=\"#5eead4\"/><text x=\"150\" y=\"55\" fill=\"#e8ecf4\" font-size=\"12\">wonderstand</text><path d=\"M100 50 H140\" stroke=\"#9aa3b5\" stroke-width=\"2\" marker-end=\"url(#a)\"/></svg>"
      }
    }
  ]
}
```

`content` on the parent message should still include an equivalent markdown/SVG fence for the web UI.

## 5. SSE envelope (unchanged type)

```json
{
  "type": "message",
  "session_id": "0wc…",
  "message": {
    "id": "msg_asst_01",
    "role": "assistant",
    "content": "…markdown…",
    "attachments": [],
    "presentation": {
      "protocol_version": 1,
      "phases": []
    },
    "created_at": "2026-08-08T12:00:00Z"
  },
  "at": "2026-08-08T12:00:00Z"
}
```

Empty `phases` or omitted `presentation` ⇒ client uses `ArtifactParser` as today.

## 6. Optional M3 fence in content (not required for M1)

If review locks Q2 toward a debuggable fence, models may append:

````markdown
```presentation
{
  "protocol_version": 1,
  "phases": [ … ]
}
```
````

Harness extracts into `message.presentation` and may strip the fence from TTS paths. **Web still sees markdown** either in `content` or after strip policy is defined at implement time.

## 7. Client preference algorithm (reminder)

```text
if message.presentation?.protocol_version is supported:
  play(phases)   // speech_text + visual by phase.id
else:
  play(ArtifactParser.parseAssistant(message))
```

## 8. Related

- ADR: [0025-wonderstand-protocol.md](../adr/0025-wonderstand-protocol.md)  
- Pointer: [0024-wonderstand-client.md](../adr/0024-wonderstand-client.md)  
- Client immersion: marble-wonderstand `docs/immersion-timeline.md`  
- API map: marble-wonderstand `docs/api-client.md`
