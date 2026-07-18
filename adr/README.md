# Marble ADRs

Architecture Decision Records live here as Markdown plus an optional HTML review page.

## Layout

| File | Role |
|------|------|
| `NNNN-short-title.md` | Canonical ADR text |
| `NNNN-review.html` | Human review UI (goals, mocks, open questions) |
| `NNNN-answers.json` | **Structured answers** from the review UI (agent reads this) |
| `review-kit.js` / `review-kit.css` | Shared inline Q&A controls for review pages |

## Review workflow

1. Open `NNNN-review.html` in a browser (`file://` or any static server).
2. Answer each question inline:
   - **Use rec** — accept the recommendation as written
   - **Custom** — type the decision in the notes field
   - **Defer** — park for later
3. Use the sticky toolbar:
   - **Use rec (all open)** — bulk-accept remaining recommendations
   - **Save answers.json** / **Download** — write `NNNN-answers.json`
   - **Copy for agent** — pasteable summary
   - **Import…** — reload a previous JSON (or drag-and-drop the file onto the page)
4. Save the JSON **next to the review HTML**:

   ```
   marble/adr/0005-answers.json
   ```

5. Tell the agent something like:

   > collect answers from ADR-0005  
   > or: apply answers from `adr/0005-answers.json`

6. The agent reads the JSON, updates the ADR markdown (locked decisions table, changelog), and refreshes the review HTML locked section.

Answers also autosave to **localStorage** in the browser (key `marble-adr-NNNN-answers`). That is convenience only — **the repo file is the source of truth for the agent.**

## Question markup (review HTML)

```html
<body data-adr="0005" data-adr-title="Expanded tools & robust agent loop">
…
<link rel="stylesheet" href="review-kit.css" />
…
<div class="q"
     data-qid="Q7"
     data-status="open"
     data-rec="/bin/bash -lc if present, else /bin/sh -c">
  <strong>Q7</strong> Shell binary / invocation?
  <div class="rec">Rec: <code>/bin/bash -lc</code> if present, else <code>/bin/sh -c</code>.</div>
</div>

<div class="q"
     data-qid="Q1"
     data-status="locked"
     data-decision="Hard 80 / soft 65"
     data-rec="Hard 80 / soft 65">
  <strong>Q1</strong> Hard stop tool rounds?
  <div class="rec">…</div>
</div>
…
<script src="review-kit.js"></script>
</body>
```

| Attribute | Meaning |
|-----------|---------|
| `data-qid` | Stable id (`Q1`, `Q15b`, …) |
| `data-status` | `open` (default) or `locked` |
| `data-rec` | Plain-text recommendation (used when choice = rec) |
| `data-decision` | Locked decision text (required when locked) |

## Answers JSON schema (`marble-adr-answers/v1`)

```json
{
  "schema": "marble-adr-answers/v1",
  "adr": "0005",
  "title": "…",
  "updated_at": "2026-07-17T12:00:00.000Z",
  "answers": {
    "Q7": {
      "status": "answered",
      "choice": "rec",
      "decision": "…",
      "notes": "",
      "question": "Q7 Shell binary…",
      "rec": "…"
    }
  }
}
```

| `status` | Meaning |
|----------|---------|
| `open` | Not decided |
| `answered` | Operator chose rec or custom |
| `locked` | Already written into the ADR; do not reopen lightly |
| `deferred` | Explicitly postponed |

## New ADR checklist

1. Write `NNNN-….md` (proposed).
2. Copy an existing `*-review.html` or start from the kit pattern above.
3. Set `data-adr` / `data-adr-title` on `<body>`.
4. Mark every open question with `class="q"` + `data-qid` + `data-rec`.
5. Link `review-kit.css` + `review-kit.js` (relative paths).
6. After review: commit `NNNN-answers.json` with the ADR when decisions land.

## Agent instructions (short)

When the user says **collect answers** for an ADR:

1. Read `adr/NNNN-answers.json` (fail clearly if missing).
2. Merge into the ADR markdown: decisions-locked table, body sections, open-questions status, changelog.
3. Update `NNNN-review.html` locked styling / `data-status="locked"` + `data-decision` for newly decided ids.
4. Leave still-open questions untouched.
