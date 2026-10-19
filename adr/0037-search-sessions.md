# ADR-0037: Search Sessions

| Field | Value |
|-------|--------|
| **Status** | **Accepted** (2026-10-18, operator accepted every recommendation) |
| **Date** | 2026-10-18 |
| **Author** | — |
| **Deciders** | Project owner |
| **Tags** | ui, search, sessions, sqlite, privacy |
| **Extends** | ADR-0002 (session memory), ADR-0007 (settings UI), ADR-0013 (system prompt & soul), ADR-0023 (clerk) |
| **Evidence** | Field request: the operator wants to type "android" and land on the sessions where that work happened, instead of scrolling the sidebar. The eye icon that opens the system prompt is in the way of the button that should do it. |

## Context

The sidebar lists sessions by title and recency. Finding old work means remembering which session it was, or opening them one by one. Titles are a weak signal: `deriveAutoTitle` copies the latest user message, so a long session about an Android app may be titled with whatever was asked last.

The button the operator wants for search is already occupied. `internal/web/static/index.html` line 249:

```html
<button id="btn-prompt" class="icon-btn" type="button"
        title="System prompt &amp; every-turn context" …>👁</button>
```

`prompt.js` binds it and opens `#prompt-modal` (`index.html` lines 68–96), which shows the immutable system prompt and edits `soul.md` through `/api/prompt`. That viewer is used rarely. Search would be used constantly. The eye has to move, not share the spot.

Sessions live in two places. The canonical transcript is `$MEMORY/session/<id>.md`, written by `Registry.PersistSession` (`internal/session/registry.go`). A live session sits in the registry until then: `Session.endTurn` (`internal/session/session.go`) marks it dirty, and the daemon flushes dirty sessions on its sweep, so a session can be minutes old and still absent from disk. Any index built only from disk misses the session the operator is in right now.

`marble.db` is at schema v13 (`CurrentSchemaVersion` in `internal/db/db.go`), migrated stepwise by `upgradeSchema`. FTS5 is compiled into the `modernc.org/sqlite` build already in `go.mod`, so a full-text table needs no new dependency. `config.ScrubSecrets` (`internal/config/env_file.go`) already strips known secret values from tool output before it is shown.

## Decision

A magnifying glass replaces the eye. It opens a small modal that searches sessions as the operator types, and lists each hit as a session id plus a short description. Clicking a hit calls `selectSession(id)` and closes the modal.

### Locked

1. **The eye does not remain.** `#btn-prompt` becomes the search button (magnifying glass, `aria-label="Search sessions"`). `#prompt-modal` is removed from the top bar.
2. **The system prompt moves into Settings as its own tab.** Settings tabs are not registered by a function: `index.html` lines 188–200 are `<button data-sec="…">` elements, and `settings.js` `render()` (line 2248) branches on the `section` value set by the click handler at line 2801. Add `<button data-sec="prompt">` and a branch. The branch reuses the `/api/prompt` load and save that `prompt.js` already performs, and it must set the settings `dirty` flag so a half-edited soul survives switching tabs.
3. **Search covers this harness only.** One memory directory, both user and system sessions, including closed ones. No cross-harness search, no peer search.
4. **No embeddings, no third-party search engine, no new dependency.** "Basic semantic" is defined below, and that definition is the whole of it.

### What "basic semantic" means

Tokenize on non-alphanumeric boundaries, lowercase, drop tokens shorter than three characters, strip one trailing `s`. Then expand through a small alias table compiled into the binary, applied at query time:

```
android → apk, gradle, adb, aapt
ios     → xcode, swift, ipa
dmg     → hdiutil, notarize, codesign
```

So the query "android" matches a session whose transcript never says "android" but does say "apk" and "gradle". The table is bidirectional and hand-maintained; growing it is a code change, not a model update.

It does **not** mean embeddings, a vector index, an external service, or LLM query rewriting. A query for "the mobile build" will not find an Android session unless a token or an alias overlaps. That gap is accepted for v1.

### Index

Schema v14, in the existing `marble.db`, through `upgradeSchema` like every prior bump. Two tables:

- `session_search(session_id PRIMARY KEY, title, description, updated_at, source_hash)` — what the modal renders, plus a hash so a sweep can skip unchanged transcripts.
- `session_search_terms(session_id, term, count, PRIMARY KEY(session_id, term))` with an index on `term`.

A per-session term list with counts, not positional postings and not an FTS5 copy of the transcript. The markdown file stays the only full copy. Counts are enough to score "does this session mention apk, and how much"; phrase search is out of scope for a modal that returns sessions rather than lines. FTS5 remains available later without a dependency change if phrase search is ever wanted.

Updates:

- **On message append**, the live session's term set is updated in memory. This is what makes a not-yet-flushed session searchable.
- **On `endTurn`**, that term set merges into SQLite, even though the markdown flush may still be minutes away.
- **On the daemon sweep**, sessions whose `source_hash` differs from the file on disk are reindexed from `memory.DecodeSession`. This also catches transcripts written out of band.

A query unions the SQLite rows with the in-memory term sets, so the session open right now is always included. If `marble.db` is not writable, the endpoint returns 503 and the modal says search is unavailable. No second index file is built for limp mode.

### Privacy

Indexed: user and assistant message text, and tool results after `config.ScrubSecrets`, clipped (see Q5).

Not indexed: `$MEMORY/env` and soul text, attachment bytes, the system prompt, `tool_args_json` from `session_events` (it can carry secrets the model passed and is not the transcript), thinking and harness messages (see Q5).

`GET /api/sessions/search?q=` is operator-auth only. It must not be added to `isPublicPath`. Device tokens, enroll, and the peer websocket cannot call it. The response is `{id, description, updated_at}` — no matching snippet, because a snippet of tool output is exactly where a secret would leak. The description itself is operator-authored or clerk-authored text, which the operator already sees in the sidebar.

## Resolved questions

The operator accepted every recommendation on 2026-10-18 (`0037-answers.json`). These are now decisions.

| Question | Decision |
|----------|----------|
| Q1 Short description | Clerk summary when one exists, else the session title. No generated blurb. |
| Q2 Postings vs term list | Per-session term list with counts, as designed above. |
| Q3 Live sessions | Incremental in-memory term set updated on message append, not a transcript scan per keystroke. |
| Q4 Cap and ranking | 20 results. Match strength first, recency as the tiebreak. |
| Q5 What gets indexed | User and assistant in full, capped at 8 KiB. Tool results only after `ScrubSecrets`, clipped to 2 KiB. Thinking and harness messages excluded. |
| Q6 Debounce and minimum length | 150 ms debounce, 2 character minimum. |

## Consequences

- One schema bump, v13 → v14, following `upgradeSchema`. An older harness opening the bumped database goes read-only, as it does today for any newer schema.
- The first run after upgrade indexes every `$MEMORY/session/*.md`. That is a background sweep, not a blocker for startup.
- The alias table will be wrong sometimes and silently. A session about Apache Groovy matches "gradle" only if the table says so. This is the cost of refusing an embeddings service, and it is the right cost for v1.
- Removing `#prompt-modal` deletes the only UI for `soul.md` unless the Settings tab lands in the same change. They ship together.
