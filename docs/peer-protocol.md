# Marble Peer Protocol v1 (ADR-0020 / ADR-0021)

`peer_protocol_version`: **2** (v2 adds the peer lock; v1 peers/harnesses still interoperate — see [Peer lock](#peer-lock-v2))

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
| `lock_state` (v2) | meta `{held, mine, holder, holder_name, since}` — sent on connect and whenever the lock changes |

### Harness → peer

| type | fields |
|------|--------|
| `hello_ack` | computer_id, protocol_version, instance_id (v2), harness_name (v2) |
| `action` | id, kind, deadline_ms, payload |
| `lock` (v2) | id, kind = `acquire` \| `release` |
| `cancel` | |
| `ping` | |

### Action kinds

| kind | payload | result |
|------|---------|--------|
| `screenshot` | `{}` or, with `caps.region`, `{region?:{x,y,w,h}, space?:"image"\|"screen", scale?, max_edge?}` | screenshot_b64 (JPEG, max edge 1280 by default), meta `{w,h,scale,screen_w,screen_h,region,zoom,downscaled,locked?}` — `w/h` are **image** pixels (click space); `scale` = display px per image px; `region` = captured display area (full screen when not cropped); `zoom` = image px per display px; `downscaled` = fewer image px than display px. See [Region capture](#region-capture). |
| `desktop_click` | `{x,y,button?,zoom?}` — **image-space** coords of the last screenshot | ok + screenshot_b64 + meta (atomic post-click shot in same queue slot, same view as the last screenshot); `last_click` in meta. `zoom:true` (caps.region) makes the post-click shot a 400×400 display-px native crop centred on the click. |
| `desktop_type` | `{text}` | ok |
| `desktop_key` | `{key, mods?}` | ok |
| `browser_ensure` | `{force?}` | text JSON ensure result; attaches or launches user Chrome with CDP |
| `browser_tabs` | `{}` | text JSON list |
| `browser_open` | `{url, new_tab?}` | ok |
| `browser_snapshot` | `{}` | text |
| `browser_act` | `{action, target?, text?, x?, y?}` — actions include open, click, click_text, **click_button**, type, press, eval, wait (x=timeout_ms), set_input_files (text=paths). No jQuery `:contains` selectors. | ok/text |
| `confirm` | `{prompt, risk}` | confirm_result ok |

## Peer lock (v2)

A peer can be paired with several harnesses (`marble-peer pair` once per harness; `marble-peer harnesses` lists them). It keeps one WebSocket per harness, but **only the harness holding the peer lock may send actions**.

- Peer advertises `caps.lock: true`. Harness sends `{"type":"lock","id":…,"kind":"acquire"}`; peer replies `result` with `ok` (or `ok:false`, `error: "peer is locked by harness …"`).
- An `action` from a harness without the lock gets `result ok:false` with `meta.lock_required: true`. `cancel` from a non-holder is ignored.
- Marble acquires on a session's first peer call and releases when the last session using that peer finishes its turn.
- The lock survives a reconnect from the same harness process. A `hello_ack` with a **new `instance_id`** (harness restarted) drops a lock that harness held.
- **Stuck lock:** the peer tray / mini UI shows the holder and has **Clear lock** (`POST /lock/clear` on the mini UI). Clearing also stops the in-flight action; the holder is notified via `lock_state` (`mine:false`).
- **v1 harness ↔ v2 peer:** the v1 harness's first action takes an implicit lock, released on disconnect or after 2 min without an action.
- **v2 harness ↔ v1 peer:** no `caps.lock`, so the harness skips locking.

Busy: concurrent actions while queue depth 1 → error `peer busy (action queue depth 1)` (harness retries briefly).

Deadlines: default 120s, max 300s (peer clamp).

## Region capture

Peers advertising `caps.region` accept options on `screenshot`. Added from field report `marble-peer-region-capture` (2026-10-08): driving a phone mirror ~140 px wide inside a 1.5×-downscaled full shot put 44 pt tap targets at ~5 image px, and about one tap in six landed.

| Field | Meaning |
|---|---|
| `region` | `{x,y,w,h}` to capture; omitted = full screen. Clamped to the display; entirely off-screen is an error. |
| `space` | `image` (default): pixels of the **last screenshot**, the same space clicks use, so a caller outlines what it sees. `screen`: display (click-space) coordinates. |
| `scale` | Image px per display px. Default: everything captured, e.g. 2 per display px on Retina. Never upscales beyond the capture. |
| `max_edge` | Longest-edge cap. Omitted = 1280; `0` = uncapped (bounded at 4096). Applies to regions too. |

- The peer crops the captured frame in software, so behaviour is identical on Linux, macOS and Windows.
- **Clicks follow the view.** After a region shot, click coordinates are pixels of that crop; the peer maps them back through `meta.region`. No extra arithmetic for the caller.
- **The view is sticky.** Post-click and post-type shots repeat the last explicit capture request (a zoomed view stays zoomed), so the post-action image verifies the hit at the same detail. A `screenshot` with no options returns to the full screen.
- The last-click crosshair is stored in display coordinates and drawn wherever it falls in the current image.
- With no options, `screenshot` behaves exactly as before, so older harnesses are unaffected. A harness must not send options to a peer without `caps.region`; the Marble harness refuses with an upgrade message instead.
