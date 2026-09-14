# Image attach-from-url — payload sketch (ADR-0029)

Non-normative examples for implementers.

## Tool call (agent) — single

```json
{
  "name": "attach_from_url",
  "arguments": {
    "url": "https://images.example.com/siberian-tiger.jpg",
    "name": "siberian-tiger.jpg",
    "alt": "Siberian tiger walking in snow",
    "source_page": "https://en.wikipedia.org/wiki/Siberian_tiger",
    "credit": "Example Photographer",
    "license": "CC BY-SA 4.0"
  }
}
```

## Tool call — batch (Q6 locked, max 4)

```json
{
  "name": "attach_from_url",
  "arguments": {
    "urls": [
      "https://images.example.com/tiger-1.jpg",
      "https://images.example.com/tiger-2.jpg"
    ],
    "source_page": "https://en.wikipedia.org/wiki/Siberian_tiger"
  }
}
```

## Tool result (success)

```json
{
  "ok": true,
  "attachment_id": "att_0wd4example",
  "mime": "image/jpeg",
  "kind": "image",
  "bytes": 245760,
  "name": "siberian-tiger.jpg",
  "source_url": "https://images.example.com/siberian-tiger.jpg",
  "final_url": "https://images.example.com/siberian-tiger.jpg",
  "note": "chat attachment (durable); cite attachment_id when referring to this image"
}
```

## Tool result (blocked)

```json
{
  "ok": false,
  "error": "url_blocked",
  "url": "http://169.254.169.254/latest/meta-data/"
}
```

## HTTP API (M1 — Q9 locked)

```http
POST /api/sessions/0wd3247f02/attachments/from_url
Content-Type: application/json

{
  "url": "https://images.example.com/siberian-tiger.jpg",
  "name": "siberian-tiger.jpg",
  "alt": "Siberian tiger walking in snow"
}
```

```http
HTTP/1.1 201 Created
Content-Type: application/json

{
  "attachment_id": "att_0wd4example",
  "mime": "image/jpeg",
  "kind": "image",
  "bytes": 245760,
  "source_url": "https://images.example.com/siberian-tiger.jpg"
}
```

## Provenance meta (Q5 locked: meta_json)

```json
{
  "source_url": "https://images.example.com/siberian-tiger.jpg",
  "final_url": "https://images.example.com/siberian-tiger.jpg",
  "fetched_at": "2026-08-22T20:00:00Z",
  "alt": "Siberian tiger walking in snow",
  "credit": "Example Photographer",
  "license": "CC BY-SA 4.0",
  "source_page": "https://en.wikipedia.org/wiki/Siberian_tiger"
}
```

## Assistant citation patterns (any client)

Markdown (if client resolves attachments — optional future):

```markdown
![Siberian tiger](marble-att://att_0wd4example)
```

Or prose + chip only (today’s durable UI path):

```text
See the attached photo (att_0wd4example).
```

Wonderstand presentation (future consumer, not required by this ADR):

```json
{
  "protocol_version": 1,
  "phases": [
    {
      "id": "p1",
      "speech_text": "Here is a Siberian tiger in winter coat.",
      "visual": {
        "kind": "image_attachment_id",
        "image_attachment_id": "att_0wd4example"
      }
    }
  ]
}
```
