# ADR-0033: Image-generation models — `kind=image` catalog rows + the `generate_image` tool

| Field | Value |
|-------|--------|
| **Status** | **Accepted** (2026-09-22) — implemented (schema v10) |
| **Date** | 2026-09-22 |
| **Author** | Marble + project owner |
| **Deciders** | Project owner |
| **Tags** | models, images, gpt-image, openai, logo, svg, vectorize, vtracer, catalog, tools |
| **Extends** | ADR-0018 (Selectable Models / `model_catalog`), ADR-0019 (multimodal attachments), ADR-0029 (reliable image attachments), ADR-0016 (API-key auth) |
| **Supersedes** | — |

## Summary

ADR-0018 assumed every `model_catalog` row is a **chat** model served on
`/chat/completions`. OpenAI's image models (`gpt-image-*`) are not: they only exist on
`/v1/images/generations`, `/v1/images/edits`, or as an `image_generation` tool inside
`/v1/responses`. Registering one as a catalog row therefore made Marble call
`/chat/completions` and hard-fail:

```
model HTTP 404: {"error":{"message":
  "This model is only supported in v1/responses and not in v1/chat/completions."}}
```

This ADR adds a **`kind`** discriminator (`chat` | `image`) to the model catalog and a
first-class **`generate_image`** tool. `kind=image` rows become **tool backends**, not
session models: they are unselectable as the session model, and the agent calls them
through `generate_image`, which writes into the workspace and (optionally) traces the
raster result to a **real SVG** with `vtracer`.

## Context & pain

### Current state (verified)

| Area | Today |
|------|-------|
| Catalog | `model_catalog` (schema v3) has no notion of model *kind*; every row is assumed chat-capable |
| Client | `model.Client.ChatWithOpts` always POSTs `{base}/chat/completions` (`internal/model/client.go`) |
| Resolution | `resolveEffective` → `resolveCatalogID` → `processEffective`; any enabled row is accepted as the session model (`internal/session/model_resolve.go`) |
| Attachments | `StageChatAttachment` + `OnChatAttachment` already stage durable image chips (`internal/tools/attach_url.go`, ADR-0029) |
| Secrets | `api_key_env` names resolve live from `$MEMORY/env` (`internal/config`), never stored in the catalog (ADR-0016) |

### What is awkward today

| Situation | Failure mode |
|-----------|--------------|
| Operator adds `gpt-image-1` as a model | Every turn 404s with `only supported in v1/responses`; the session looks broken |
| Agent wants a logo | No tool exists; nothing in the catalog can be *called* for pixels |
| Operator wants an SVG | Image models emit **raster only** — no SVG path existed anywhere |
| Cost control | No accounting for image token spend |

The core gap: **the catalog models one axis (chat), but some providers are a different axis
(image generation)**, and there was no tool bridging Marble to the Images API.

## Goals

1. **Make the failure impossible** — a `kind=image` row can never be sent to `/chat/completions`.
2. **Make image models callable** — a `generate_image` tool that hits `{base}/images/generations`.
3. **Artifacts land in the workspace** — files under the workspace jail, not just base64 in a transcript.
4. **Visible results** — generated images become durable chat attachments, like peer screenshots.
5. **Real SVG** — an opt-in `vectorize` that traces the raster to genuine vector paths.
6. **Cost visibility** — report token usage and an estimated USD cost per call.
7. **Backwards compatible** — existing rows migrate to `kind='chat'`; nothing else changes.

## Non-goals (v1)

| Non-goal | Why |
|----------|-----|
| Responses API (`/v1/responses`) as a transport | The Images API covers generation/editing; Responses adds multi-turn image tool-use we don't need yet. The 404 hint now *explains* this instead of crashing |
| Image **editing** / inpainting / masks | `images/edits` is a follow-up; generation is the ask (logos, SVG) |
| Streaming / partial images | Adds transport complexity for no agent-loop benefit |
| Auto-vectorizing every image | Tracing is lossy and slow; opt-in per call |
| Using an image model *as* the agent brain | It has no chat surface. It is a tool backend, full stop |

## Decision

### D1. `kind` on the model catalog (schema v10)

`model_catalog.kind TEXT NOT NULL DEFAULT 'chat'`, normalised to `chat` | `image` by
`db.NormalizeModelKind`; `ValidateModelCatalog` rejects anything else. Migration
`migrateV9toV10` adds the column and tolerates fresh databases that already have it
(guarded by `pragma_table_info`, so the `ALTER` is conditional).

### D2. `kind=image` rows are tool backends, never session models

Enforced at three layers so a stale selection can never 404 a session:

| Layer | Behaviour |
|-------|-----------|
| `setSessionModel` | Rejects with `… is an image-generation model; pick a chat model and use the generate_image tool` |
| `resolveCatalogID` | Returns `ok=false` + advisory and falls back to the process default |
| `runTurn` (pre-flight) | Belt-and-braces guard that ends the turn with the same explanation |

### D3. `generate_image` tool

`POST {base_url}/images/generations`, decoding `data[].b64_json` (GPT Image models *always*
return base64, never URLs).

| Arg | Default | Notes |
|-----|---------|-------|
| `prompt` | — | required |
| `path` | `generated/image-<ts>.png` | workspace-relative, jail-enforced; `-2`, `-3` suffixes for `n > 1` |
| `model_id` | first enabled `kind=image` row | must be a `kind=image` row |
| `size` | `1024x1024` | `1024x1536`, `1536x1024`, up to 3840×2160 |
| `quality` | `medium` | `low` · `medium` · `high` · `xhigh` · `max` · `auto` |
| `background` | `transparent` | `opaque` · `auto` — transparency is why logos work |
| `output_format` | `png` | `jpeg` · `webp` |
| `n` | `1` | max 4 |
| `vectorize` | `false` | also write a sibling `.svg` via `vtracer` |
| `attach` | `true` | stage durable chat attachment(s) |

Returns `{ok, files:[{path,bytes,mime,vectorized_path}], attachment_ids, model, model_id,
size, quality, background, vectorized, usage, est_cost_usd, latency_ms, notes}`.
Turn-scoped cap: 6 calls per turn (mirrors the `attach_from_url` cap).

### D4. SVG is traced, not generated

GPT Image models **cannot emit SVG** — they emit raster pixels. Two honest paths to vector
output, and the tool takes the second:

1. **Text model authors SVG markup directly** — works today with any competent chat model for
   hand-drawn logos/wordmarks (no new machinery).
2. **`vectorize: true`** — the generated PNG is traced to real `<path>` geometry by
   `vtracer` (`colormode="color"`, `mode="spline"`, `filter_speckle=4`) via `python3`.
   Missing `python3`/`vtracer` yields a `notes` hint, never a hard failure.

### D5. Cost accounting

Per-call estimate from reported usage at GPT Image rates — text in $5/M, image in $8/M,
image out $30/M — rounded to 4 dp. Best-effort: `quality` dominates cost (a 1024×1024 square
ranges ~$0.006 low → ~$0.21 high), and `quality=auto` lets the model pick the tier, so the
figure is an estimate by construction.

### D6. Auth unchanged

`api_key_env` on the image row (e.g. `OPENAI_API_KEY`) resolves through the existing
ADR-0016 path. `ImageClientFor` builds a normal `model.Client`, so inherited/forced/none
modes and live env overlay reload all keep working.

## Consequences

**Positive**

- The reported 404 is now structurally impossible, and the *error message itself* is
  rewritten into an actionable hint (`Register it as kind=image and call generate_image`).
- Marble gains its first image *production* capability (logos, icons, illustrations) with
  artifacts on disk and chips in chat.
- Real SVG output via tracing — no other path in the harness produced vector art before.
- Cost and token usage are visible per call.

**Negative / accepted**

- One more catalog axis; the settings editor grows a `kind` select and an `image` badge.
- `vtracer` is an optional host dependency (`pip install vtracer`); absent, `vectorize`
  degrades to a note.
- Tracing a photographic image produces a large, ugly SVG. `vectorize` is intended for
  flat/logo art, which is what the tool is for.

**Neutral**

- Schema v10 is additive; older binaries refuse the DB (standard forward-compat guard).

## Verification

| Check | How |
|-------|-----|
| v9 → v10 migration | `TestMigrateV9toV10AddsKind` (downgrades a real DB, reopens, asserts `kind='chat'` on legacy rows) |
| Idempotent on fresh DB | `TestMigrateV9toV10Idempotent` |
| Kind validation | `TestValidateModelCatalogKind`, `TestNormalizeModelKind`, `TestModelCatalogKindRoundTrip` |
| Images API contract | `TestGenerateImageHappyPath` (path, auth, body, b64 decode, usage), `…HTTPError`, `…NoImageReturned`, `…RejectsBadBase64` |
| Tool behaviour | `TestGenerateImageWritesFileAndAttaches`, `…DefaultPathAndMultiple`, `…RejectsNonImageModel`, `…NoModelConfigured`, `…RequiresPromptAndJail`, `…TurnCallCap`, `…Vectorize`, `…Dispatch`, `…SpecRegistered` |
| Session guard | `TestImageModelCannotBeSessionModel` |
| Live endpoint | Manual: `gpt-image-2.5-flare`, `1024x1024`, `quality=low`, `background=transparent` → RGBA PNG, 6.6 s, `usage.output_tokens=196` |

`go build ./...`, `go vet ./...`, and `go test ./...` (17 packages) all pass.

## Open questions

1. Should `images/edits` (mask-based inpainting) get its own tool, or an `edit` mode on
   `generate_image`? *(leaning: separate tool, different arg shape)*
2. Should the first `kind=image` row be explicitly pinned as "the default image model"
   rather than relying on `sort_order`? *(leaning: pin it if operators run more than two)*
3. Should `vectorize` get quality knobs (`filter_speckle`, `mode`, colour precision)
   surfaced as args? *(leaning: only if requesters hit the defaults' limits)*
