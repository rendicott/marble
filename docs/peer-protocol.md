# Marble Peer Protocol v1 (ADR-0020 / ADR-0021)

`peer_protocol_version`: **1**

Transport: WebSocket (peer dials harness) + HTTP for mutual pairing.

## Pairing (HTTP)

### Start (operator / Settings)

`POST /api/computers/pair/start`  
Auth: same as Settings (admin session).

Response:
```json
{
  "pairing_id": "uuid",
  "h_code": "ABCD23",
  "expires_in_sec": 600,
  "harness_url_hint": "http://host:8080"
}
```

### Join (peer — public)

`POST /api/computers/pair/join`  
Body:
```json
{
  "h_code": "ABCD23",
  "device_id": "uuid",
  "os": "linux",
  "caps": {"browser": true, "desktop": true, "confirm": true}
}
```

Response:
```json
{
  "pairing_id": "uuid",
  "p_code": "WXYZ89",
  "status": "joined"
}
```

Peer then polls `GET /api/computers/pair/status?pairing_id=&device_id=` until `status=sealed` and receives `device_token` + `computer_id` once.

### Confirm (operator)

`POST /api/computers/pair/confirm`  
```json
{ "pairing_id": "uuid", "p_code": "WXYZ89", "display_name": "home-laptop", "id": "home-laptop" }
```

Creates `computers` row; status becomes sealed for peer poll.

## WebSocket (peer → harness)

`GET /api/computers/ws?device_id=…&token=…`  
Upgrades to WebSocket. First message may be `hello`; harness replies `hello_ack`.

### Peer → harness

| type | fields |
|------|--------|
| `hello` | device_id, token, caps, os, peer_version, protocol_version |
| `pong` | |
| `result` | id, ok, screenshot_b64?, text?, meta?, error? |
| `confirm_result` | id, ok (accepted) |

### Harness → peer

| type | fields |
|------|--------|
| `hello_ack` | computer_id, protocol_version |
| `action` | id, kind, deadline_ms, payload |
| `cancel` | |
| `ping` | |

### Action kinds

| kind | payload | result |
|------|---------|--------|
| `screenshot` | `{}` | screenshot_b64 (JPEG max edge 1280), meta `{w,h,scale,screen_w,screen_h,locked?}` — `w/h` are **image** pixels (click space); `scale = screen_w/w` |
| `desktop_click` | `{x,y,button?}` — **image-space** coords | ok + screenshot_b64 + meta (atomic post-click shot in same queue slot); `last_click` in meta |
| `desktop_type` | `{text}` | ok |
| `desktop_key` | `{key, mods?}` | ok |
| `browser_ensure` | `{force?}` | text JSON ensure result; attaches or launches user Chrome with CDP |
| `browser_tabs` | `{}` | text JSON list |
| `browser_open` | `{url, new_tab?}` | ok |
| `browser_snapshot` | `{}` | text |
| `browser_act` | `{action, target?, text?, x?, y?}` — actions include open, click, click_text, **click_button**, type, press, eval, wait (x=timeout_ms), set_input_files (text=paths). No jQuery `:contains` selectors. | ok/text |
| `confirm` | `{prompt, risk}` | confirm_result ok |

Busy: concurrent actions while queue depth 1 → error `peer busy (action queue depth 1)` (harness retries briefly).

Deadlines: default 120s, max 300s (peer clamp).
