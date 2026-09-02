# Marble ADRs

Architecture Decision Records live here as Markdown plus an optional HTML review page.

## Layout

| File | Role |
|------|------|
| `NNNN-short-title.md` | Canonical ADR text |
| `NNNN-doc.html` | **Readable HTML** of the ADR (generated; browse via the ADR server) |
| `NNNN-review.html` | Human review UI (goals, mocks, open questions) |
| `NNNN-answers.json` | **Structured answers** from the review UI (agent reads this) |
| `index.html` | Catalog of all ADRs (generated) |
| `doc-kit.css` | Shared styles for generated doc pages |
| `render_docs.py` | Regenerates `*-doc.html` + `index.html` from markdown |
| `review-kit.js` / `review-kit.css` | Shared inline Q&A controls for review pages |

## Browse on the web

**Recommended: user systemd unit** (stays up without an agent/shell bg task):

```bash
mkdir -p ~/.config/systemd/user
cp adr/marble-adr.service.example ~/.config/systemd/user/marble-adr.service
# edit WorkingDirectory / ExecStart paths if your clone isn’t ~/projects/marble
systemctl --user daemon-reload
systemctl --user enable --now marble-adr
systemctl --user status marble-adr
```

Ad-hoc (no systemd):

```bash
python3 adr/serve.py          # http://127.0.0.1:8791/adr/
```

Then open:

- [http://127.0.0.1:8791/adr/](http://127.0.0.1:8791/adr/) — catalog (`index.html`)
- `http://127.0.0.1:8791/adr/NNNN-doc.html` — full ADR text
- `http://127.0.0.1:8791/adr/NNNN-review.html` — Q&A review UI

After editing any `NNNN-….md` (or adding a new ADR), refresh HTML:

```bash
python3 adr/render_docs.py
```

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
2. Run `python3 adr/render_docs.py` so `NNNN-doc.html` + `index.html` include it.
3. Copy an existing `*-review.html` or start from the kit pattern above.
4. Set `data-adr` / `data-adr-title` on `<body>`; link the doc page (`NNNN-doc.html`) in the review nav.
5. Mark every open question with `class="q"` + `data-qid` + `data-rec`.
6. Link `review-kit.css` + `review-kit.js` (relative paths).
7. After review: commit `NNNN-answers.json` with the ADR when decisions land; re-run `render_docs.py` if the markdown status/body changed.

## Agent instructions (short)

When the user says **collect answers** for an ADR:

1. Read `adr/NNNN-answers.json` (fail clearly if missing).
2. Merge into the ADR markdown: decisions-locked table, body sections, open-questions status, changelog.
3. Update `NNNN-review.html` locked styling / `data-status="locked"` + `data-decision` for newly decided ids.
4. Leave still-open questions untouched.
