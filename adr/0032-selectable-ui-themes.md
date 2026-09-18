# ADR-0032: Selectable UI themes (light + tan) from Settings, remembered in a cookie

| Field | Value |
|-------|--------|
| **Status** | **Accepted** (2026-09-18) — implemented |
| **Date** | 2026-09-18 |
| **Accepted** | 2026-09-18 |
| **Author** | — |
| **Deciders** | Project owner |
| **Tags** | ui, settings, theme, css, cookie, web |
| **Extends** | ADR-0007 (Settings UI / UI prefs), ADR-0001 (web SPA) |
| **Review UI** | [0032-review.html](0032-review.html) |
| **Answers** | [0032-answers.json](0032-answers.json) (`2026-09-18T16:15:22.306Z`) — Q1–Q12 locked (rec) |

## Summary

Give the embedded web UI **named, selectable themes**. v1 ships three:

| Id | Role |
|----|------|
| `dark` | Today's look. Default when no cookie is set. |
| `light` | Cool light chrome. |
| `tan` | Warm paper / kraft chrome. |

The operator picks a theme in **Settings ⚙ → UI**. The choice is remembered in a **cookie** (`marble_theme`) so the next load paints the right palette **before first paint** (inline boot script in `<head>`). No SQLite key, no `localStorage` for this pref — cookie is the source of truth.

## Context & pain

### Current state (verified)

| Area | Today |
|------|-------|
| Palette | Single dark theme. CSS custom properties on `:root` in `internal/web/static/style.css` (`--bg`, `--panel`, `--text`, `--accent`, …) |
| Hardcoded hex | Dozens of chrome colors still baked in (`#12151c` sidebar, `#0c0e13` inputs, `#0a0c11` code, status pills, limp banner, …). Switching `:root` tokens alone would leave a mottled dark-on-light UI. |
| Settings ⚙ | Modal with an **UI** pane (`settings.js`). Stores `show_closed` / `show_dotfiles` in **`localStorage`** key `marble.ui.prefs`. Save is explicit; limp does not block the UI pane. |
| Persistence split (ADR-0007 Q15) | UI chrome prefs are **browser-local**, not SQLite. Explorer/session toggles remain independent. |
| Boot | `index.html` is `<link rel="stylesheet" href="/style.css" />` then `<body>` — no theme attribute, no blocking script. |
| Other surfaces | ADR pages (`adr/*.html`) have their own hardcoded dark kit. mpub, Wonderstand, desktop peer are separate. |

### Observed pain

| Pain | Why it matters |
|------|----------------|
| Dark-only chrome | Long sessions in a bright room are harsh; some operators want paper/light. |
| Token layer is incomplete | `:root` vars exist but much of the UI ignores them. A theme switch today would be a half-theme. |
| `localStorage` is too late for paint | Settings JS loads at the bottom of `index.html`. A theme stored only there flashes dark, then swaps. |
| No operator control | Gear → UI has toggles for closed sessions and dotfiles, nothing for look. |

## Goals

1. **Selectable themes** from Settings ⚙ → UI: `dark`, `light`, `tan`.
2. **Remember with a cookie** so a reload (and a new tab on this origin) keeps the choice.
3. **No flash of the wrong theme** — apply `html[data-theme]` in a blocking `<head>` script before CSS.
4. **One token layer** — chrome, transcript, modals, inputs, and status colors go through CSS variables. Hardcoded hex in `style.css` is a bug after this ADR.
5. **Keep today's look as `dark`.** No cookie ⇒ identical to current UI.
6. **Per-browser, not per-harness.** Theme is chrome, not a SQLite setting. Other devices / browsers keep their own cookie.

## Non-goals (v1)

| Non-goal | Why |
|----------|-----|
| Auto `prefers-color-scheme` | Explicit pick only. OS light mode must not surprise a dark-default operator. |
| Custom / uploaded palettes | Named catalog only. |
| Per-session theme | One chrome for the SPA. |
| Theming ADR pages, mpub, Wonderstand, desktop peer | Separate surfaces, separate kits. |
| Server-side theme (Set-Cookie from Go, SQLite key, account sync) | Cookie written by the browser is enough. |
| Stuffing theme into `marble.ui.prefs` localStorage | Boot script needs a trivial parse; cookie is that. |
| Runtime theme editor / contrast playground | Palettes are code in `style.css`. |
| Dark-dim / high-contrast / "system" extra ids | Three ids in v1. |

## Design

### A. Theme ids and default

Canonical ids (cookie value, `data-theme` value, Settings control value):

```
dark | light | tan
```

- Unknown / missing / empty → `dark`.
- Default is **`dark`** so existing operators see no change until they pick.

`html` carries the active id:

```html
<html lang="en" data-theme="tan">
```

Also set `color-scheme` (`dark` for `dark`, `light` for `light` and `tan`) so native scrollbars and form controls match.

### B. Token layer

Keep the existing names; add the tokens that are currently hardcoded. All three themes define the full set.

| Token | Purpose | Dark (today) |
|-------|---------|--------------|
| `--bg` | App canvas | `#0f1115` |
| `--panel` | Cards, bubbles, dialogs | `#151922` |
| `--panel-2` | Raised / nested | `#1b2130` |
| `--sidebar` | `#sidebar`, modal nav, list wells | `#12151c` |
| `--border` | Hairlines | `#2a3142` |
| `--text` | Primary copy | `#e8ecf4` |
| `--muted` | Secondary copy | `#8b93a7` |
| `--accent` | Links, focus, brand | `#7c9cff` |
| `--accent-2` | Secondary accent (teal today) | `#5eead4` |
| `--user` | User bubble fill | `#243056` |
| `--assistant` | Assistant bubble fill | `#1a1f2a` |
| `--tool` | Tool bubble fill | `#14241f` |
| `--danger` | Errors, close | `#f87171` |
| `--ok` | Success | `#4ade80` |
| `--warn` | Warnings, running | `#fbbf24` |
| `--input-bg` | Inputs, textarea, code wells | `#0c0e13` |
| `--chip` | Chips, pills, code-inline bg | `#252b3a` |
| `--mark-glow` | Brand mark halo | `rgba(124, 156, 255, 0.35)` |
| `--overlay` | Hover / selected fills | `rgba(124, 156, 255, 0.08)` |
| `--code-bg` | Markdown `pre` | `#0a0c11` |
| `--shadow` | Modal / menu shadow | `rgba(0, 0, 0, 0.45)` |

Fonts (`--mono`, `--sans`) stay shared; not themed.

CSS shape:

```css
:root, html[data-theme="dark"] { /* dark tokens */ }
html[data-theme="light"] { /* light tokens */ }
html[data-theme="tan"] { /* tan tokens */ }
```

`:root` **is** dark so a missing `data-theme` still matches today.

Implementation rule: after this ADR, `style.css` must not introduce new raw chrome hexes. Status colors (`--ok` / `--warn` / `--danger`) stay recognizable across themes (green / amber / red), retinted for contrast on the new grounds. `color-mix(...)` against tokens is allowed; `rgba(124, 156, 255, …)` hardcoded to the dark accent is not.

### C. Proposed palettes (starting point; hex may tweak for contrast)

**Light** — cool paper, not stark white:

| Token | Hex |
|-------|-----|
| `--bg` | `#f3f5f9` |
| `--panel` | `#ffffff` |
| `--panel-2` | `#e8ecf4` |
| `--sidebar` | `#eceff5` |
| `--border` | `#cfd6e4` |
| `--text` | `#1b2230` |
| `--muted` | `#5c6578` |
| `--accent` | `#3b6cff` |
| `--accent-2` | `#0f766e` |
| `--user` | `#dce6ff` |
| `--assistant` | `#eef1f7` |
| `--tool` | `#d8f3e8` |
| `--danger` | `#dc2626` |
| `--ok` | `#15803d` |
| `--warn` | `#b45309` |
| `--input-bg` | `#ffffff` |
| `--chip` | `#e2e8f4` |
| `--code-bg` | `#e8ecf4` |
| `--mark-glow` | `rgba(59, 108, 255, 0.28)` |
| `--overlay` | `rgba(59, 108, 255, 0.08)` |
| `--shadow` | `rgba(27, 34, 48, 0.18)` |

**Tan** — warm kraft / paper:

| Token | Hex |
|-------|-----|
| `--bg` | `#e8dcc8` |
| `--panel` | `#f4ead8` |
| `--panel-2` | `#ddcfb6` |
| `--sidebar` | `#dccdb3` |
| `--border` | `#c4b396` |
| `--text` | `#3b2f1e` |
| `--muted` | `#7a6a52` |
| `--accent` | `#8b5e3c` |
| `--accent-2` | `#2f6f4e` |
| `--user` | `#e6d3b0` |
| `--assistant` | `#efe6d4` |
| `--tool` | `#d5e4c4` |
| `--danger` | `#b91c1c` |
| `--ok` | `#3f7d4e` |
| `--warn` | `#b45309` |
| `--input-bg` | `#f7f1e4` |
| `--chip` | `#e4d6be` |
| `--code-bg` | `#efe4d0` |
| `--mark-glow` | `rgba(139, 94, 60, 0.30)` |
| `--overlay` | `rgba(139, 94, 60, 0.10)` |
| `--shadow` | `rgba(59, 47, 30, 0.18)` |

Hex values may shift during implementation if contrast on bubbles / buttons fails a quick WCAG-AA check on `--text` vs `--bg` / `--panel`. **Do not add a fourth id** without a follow-on ADR.

### D. Cookie (source of truth)

| Field | Value |
|-------|--------|
| Name | `marble_theme` |
| Value | `dark` \| `light` \| `tan` |
| Path | `/` |
| Max-Age | `31536000` (1 year) |
| SameSite | `Lax` |
| Secure | set **only** when `location.protocol === "https:"` (local UI is often `http://127.0.0.1`) |
| HttpOnly | **no** — JS must read and write it |
| Domain | omit (host-only) |

Not a secret. Unknown values ignored. Cookie is **not** mixed into `marble.ui.prefs`.

Write helper (settings + any future picker):

```js
function setThemeCookie(id) {
  const v = (id === "light" || id === "tan") ? id : "dark";
  let c = "marble_theme=" + encodeURIComponent(v)
    + "; Path=/; Max-Age=31536000; SameSite=Lax";
  if (location.protocol === "https:") c += "; Secure";
  document.cookie = c;
}
```

Read helper is duplicated in the boot script (no module load before paint).

### E. Boot script (no FOUC)

In `index.html` `<head>`, **before** the stylesheet:

```html
<script>
(function () {
  try {
    var m = document.cookie.match(/(?:^|; )marble_theme=([^;]*)/);
    var t = m ? decodeURIComponent(m[1]) : "dark";
    if (t !== "dark" && t !== "light" && t !== "tan") t = "dark";
    document.documentElement.setAttribute("data-theme", t);
    document.documentElement.style.colorScheme = t === "dark" ? "dark" : "light";
  } catch (e) {}
})();
</script>
<link rel="stylesheet" href="/style.css" />
```

Tiny, synchronous, no network. `settings.js` reapplies the same pair (`data-theme` + `color-scheme` + cookie) when the operator picks a swatch.

### F. Settings ⚙ → UI

Add a **Theme** control at the top of the existing UI pane, above the closed-sessions / dotfiles checkboxes.

**Control:** three **swatch cards**, not a `<select>`. Each card shows a mini strip of `--bg` / `--panel` / `--accent` and the id label. Selected card gets a border/check.

**Apply:** live. Changing the swatch:

1. Sets `html[data-theme]` and `color-scheme` immediately (the open Settings modal restyles in place — that *is* the preview).
2. Writes the cookie immediately.
3. Does **not** require the pane's **Save** button (Save stays for `show_closed` / `show_dotfiles`).

Hint copy: `Stored in a cookie on this browser. Applies immediately.`

**Reset** (the existing UI-section Reset): does **not** change theme. Picking `dark` is how you go back. Theme is not a draft field in `marble.ui.prefs`.

Limp mode: theme picker stays enabled (browser-local, like the rest of the UI pane).

### G. Scope of files

| File | Change |
|------|--------|
| `internal/web/static/index.html` | Boot script in `<head>` before CSS |
| `internal/web/static/style.css` | Token blocks for three themes; replace hardcoded chrome hex with vars |
| `internal/web/static/settings.js` | Swatch UI, cookie read/write, live apply |

No Go, no schema bump, no `/api/settings` key. Auth cookies (ADR-0017) are unrelated; do not reuse those names.

Login / OAuth interstitial: if it is a separate HTML document that does not load `style.css`, leave it. If it is the SPA (`index.html` fallback), it inherits the cookie automatically.

### H. Security / privacy

- Cookie value is a 4–5 char enum. No PII.
- Not HttpOnly by design.
- `SameSite=Lax` is enough for a same-origin SPA.
- Do not echo the cookie into SQLite, transcripts, or agent context.
- Theme ids are allow-listed on read; anything else → `dark`.

## Alternatives considered

| Option | Why not |
|--------|---------|
| `localStorage` (`marble.ui.prefs.theme`) | Matches other UI prefs, but the boot path is worse: `settings.js` is deferred. A second blocking script that parsed JSON from `localStorage` works, but the operator asked for a cookie, and a cookie is the natural "read in `<head>`" primitive. |
| SQLite `settings.theme` | Wrong layer (per-harness, not per-browser). Would require an API round-trip before paint or a flash. Conflicts with ADR-0007's UI-prefs split. |
| `document.documentElement.classList` (`theme-tan`) | Equivalent to `data-theme`; attribute is easier to target and inspect. |
| CSS `@media (prefers-color-scheme)` as default | Surprises operators; v1 is explicit. Can be a later `system` id. |
| Separate `themes.css` file | Extra request; v1 catalog is small. Keep tokens in `style.css`. |
| Server `Set-Cookie` from `Accept` / query | Overkill; the picker writes the cookie. |

## Decisions locked (Q1–Q12)

All rec. Source: `adr/0032-answers.json` (`2026-09-18T16:15:22.306Z`).

| ID | Decision |
|----|----------|
| **Q1** | Settings ⚙ → UI pane (existing). Theme swatches at the top, above closed-sessions / dotfiles. |
| **Q2** | `dark` (current look, default) + `light` + `tan`. Three ids in v1. No system / custom / high-contrast yet. |
| **Q3** | Cookie `marble_theme`. Not localStorage, not SQLite, not mixed into `marble.ui.prefs`. |
| **Q4** | `Path=/; Max-Age=31536000` (1 year); `SameSite=Lax`; `Secure` only when `location.protocol` is https; not HttpOnly; host-only (no Domain). |
| **Q5** | Live apply + write cookie immediately on swatch click. Settings Save is not required for theme. |
| **Q6** | Inline boot script in `index.html` `<head>` before the stylesheet. Sets `html[data-theme]` and `color-scheme` from the cookie. |
| **Q7** | `html[data-theme='dark\|light\|tan']` CSS variables. `:root` stays dark. Retokenize hardcoded chrome hex in `style.css` — leftover hex is a bug. |
| **Q8** | No in v1. Explicit pick only. Default `dark`. A later `system` id can follow `prefers-color-scheme`. |
| **Q9** | Three swatch cards (mini bg/panel/accent strip + id). Not a `<select>`. |
| **Q10** | Embedded web SPA only (`index.html` + `style.css` + `settings.js`). Not ADR pages, mpub, Wonderstand, or desktop peer. |
| **Q11** | UI-section Reset does not change theme. Picking `dark` is how you go back. Theme is not a `marble.ui.prefs` draft field. |
| **Q12** | Lock token names + proposed hex tables in this ADR. Contrast tweaks at implement are allowed. No fourth theme id without a follow-on ADR. |

## Open questions

None. Q1–Q12 locked.

## Implementation order

1. **Boot + dark attribute** — `index.html` script; `:root, html[data-theme="dark"]` (behavior-identical). Cookie helpers.
2. **Retokenize `style.css`** — replace hardcoded chrome hex with the token table. Dark still looks like today.
3. **`light` + `tan` blocks** — fill the token tables; fix leftover contrast bugs (markdown `strong`, code, pills).
4. **Settings swatches** — UI pane control; live apply; cookie write.
5. **Browser check** — dark (default, no cookie), light, tan, reload persistence, unknown cookie, http (no Secure), Settings Reset leaves theme, limp still allows picker.

Suggested PRs (post-accept): one is fine (all UI, no schema). Split 1–2 vs 3–4 if the retokenize diff is large.

## Acceptance criteria

- [x] Q1–Q12 answered and merged into this ADR
- [x] Three themes selectable from Settings ⚙ → UI
- [x] Choice remembered in `marble_theme` cookie and restored on reload with no wrong-theme flash
- [x] No cookie ⇒ today's dark UI
- [x] Hardcoded chrome hex in `style.css` replaced by tokens
- [x] No Go / SQLite / API change

## Changelog

- 2026-09-18 — proposed (light + tan from Settings; cookie remember)
- 2026-09-18 — accepted; Q1–Q12 locked (rec) from `0032-answers.json`; implemented (derived surface tokens `--warn-fill` / `--danger-fill` / `--ok-fill` + edges/text, `--scrim`, `--on-accent` — contrast tweaks allowed by Q12)
