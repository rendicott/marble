# ADR-0004: Workspace File Explorer Modal

| Field | Value |
|-------|--------|
| **Status** | Accepted / Implemented |
| **Date** | 2026-07-17 |
| **Deciders** | Project owner |
| **Tags** | ui, workspace, files, modal, upload, download |
| **Extends** | ADR-0001 (workspace tool jail), ADR-0002/0003 (memory is separate) |

## Context

Marble’s agent tools can already read/write/list under `--workspace`, but the **operator** has no first-class UI to browse that tree. Today they must use an external file manager or ask the model.

We want an in-app **file explorer modal** scoped to the launch workspace so operators can:

- Navigate the same tree the agent sees  
- View/edit non-binary text files  
- Create, rename, delete, download, upload  
- Copy paths for pasting into chat  

**Out of scope for this ADR:** browsing `--memory`, multi-root workspaces, rich IDE features (diff, git, LSP), binary hex editors.

## Decision

1. Add a **folder icon** control in the sidebar head, next to **New session (+)**, that opens a **modal** file explorer.
2. Explorer is rooted at the harness **`--workspace`** (absolute path known to the server; never escapes it).
3. **Browse** directories; select a **non-binary** file → modal becomes a **split view**: tree (or list) + **viewer/basic editor**.
4. **Context menus** as specified (file/folder vs empty space).
5. **Download:** single file as-is; directory as **`.tar.gz`**.
6. **Drag-and-drop:** into modal = upload; out of modal = download (browser-supported best effort).
7. **Refresh** reloads the current directory listing from the server.
8. Implement via **HTTP API** under `/api/workspace/...` plus SPA modal UI (no separate binary).

## UI placement & chrome

```
┌ Sidebar head ─────────────────────────┐
│ [mark] Marble     [Sessions▾] [📁] [+] │
└───────────────────────────────────────┘
```

| Control | Role |
|---------|------|
| 📁 (folder) | Open workspace explorer modal |
| + | New session (unchanged) |

### Modal layout

**Browse-only (no file selected, or directory selected):**

```
┌ Workspace explorer ──────────────────────── [↻] [×] ┐
│ path: /abs/workspace/subdir                         │
│ ┌ listing ────────────────────────────────────────┐ │
│ │ 📁 src/                                         │ │
│ │ 📄 README.md                                    │ │
│ └─────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────┘
```

**File selected (text):**

```
┌ Workspace explorer ─────────────────── [Save] [↻] [×] ┐
│ path crumbs…                                           │
│ ┌ tree/list ─────┬── editor ─────────────────────────┐ │
│ │ …              │  (textarea / CodeMirror-lite)     │ │
│ │ README.md ●    │                                   │ │
│ └────────────────┴───────────────────────────────────┘ │
└────────────────────────────────────────────────────────┘
```

- **↻ Refresh** — reload listing (+ re-fetch open file if any).  
- **Save** — only when a dirty text buffer exists.  
- **×** — close modal (prompt if dirty — open Q).

## Path safety (hard requirements)

All paths from the client are treated as **relative to workspace** (or absolute only if under workspace after clean).

| Rule | Behavior |
|------|----------|
| Jail | Resolve with `filepath.Abs` + require prefix of workspace root |
| Reject | `..` escape, symlinks that leave workspace (follow carefully or reject external links) |
| Same as tools | Align with `internal/tools` resolve semantics |

## Binary vs text

| Classification | Behavior on select |
|----------------|-------------------|
| **Text** | Split view: load content into editor (size-capped — open Q) |
| **Binary** | No editor; show metadata panel (size, mime, “binary — download only”) |

**v1 heuristics (proposed):**

- Extension allowlist for text (`.go`, `.md`, `.txt`, `.json`, `.yaml`, `.yml`, `.toml`, `.css`, `.js`, `.ts`, `.html`, `.sh`, `.env`, `.mod`, `.sum`, no extension small files, …)  
- AND/OR content sniff: no NUL in first 8 KiB, valid UTF-8  
- Over size limit → treat as “too large to edit”; offer download only  

## Context menus

### On file

| Action | Behavior |
|--------|----------|
| **Copy path** | Copy workspace-relative path (open Q: also absolute?) to clipboard |
| **Delete** | Confirm → delete file |
| **Rename** | Inline prompt → rename (same directory) |
| **Download** | Browser download of file bytes |

### On folder

| Action | Behavior |
|--------|----------|
| **Copy path** | Same as file |
| **Delete** | Confirm → recursive delete (open Q: require empty only?) |
| **Rename** | Rename directory |
| **Download** | Stream **`dirname.tar.gz`** of folder tree (jailed walk) |

### On empty listing space (background)

| Action | Behavior |
|--------|----------|
| **New folder** | Prompt name → `mkdir` |
| **New file** | Prompt name → create empty file (optional open in editor) |

## Drag and drop

| Direction | Behavior |
|-----------|----------|
| **Into modal** (from OS / browser) | Upload into **current directory** (overwrite policy — open Q) |
| **Out of modal** (from row) | Browser download (file) or tar.gz (folder) via drag — **best effort**; HTML5 DnD download support varies by browser |

**Note:** Drag-*out* as true file download is inconsistent across browsers. Minimum bar: drag-out triggers same as Download when possible; otherwise Download menu remains authoritative.

## Refresh

- Button reloads `GET` listing for current path.  
- If a file is open and not dirty, re-fetch content.  
- If dirty, refresh listing only or prompt (open Q).

## API sketch

All under workspace jail. Errors: `400` bad path, `403` escape, `404` missing, `409` conflict, `413` too large.

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/api/workspace` | Root info: absolute workspace path (display), sep |
| `GET` | `/api/workspace/list?path=` | Entries: name, type (`file`/`dir`), size, mtime, is_text |
| `GET` | `/api/workspace/read?path=` | Text content (+ encoding); reject binary/oversize |
| `PUT` | `/api/workspace/write` | Body: `{path, content}` — create/overwrite text |
| `POST` | `/api/workspace/mkdir` | `{path}` |
| `POST` | `/api/workspace/rename` | `{from, to}` |
| `DELETE` | `/api/workspace?path=` | File or dir (recursive if dir — policy Q) |
| `GET` | `/api/workspace/download?path=` | File attachment |
| `GET` | `/api/workspace/archive?path=` | `application/gzip` tar.gz of directory |
| `POST` | `/api/workspace/upload?path=` | Multipart files into directory `path` (cwd) |

`path` query/body values are **relative to workspace** (preferred) or validated absolute under root.

## Security considerations

- Explorer is as powerful as local filesystem access to the workspace — **no auth** in v1 matches current dashboard posture (local/Tailscale trust).  
- Document: exposing `:8080` on a network exposes workspace R/W + delete.  
- Future: optional auth (later ADR) gates these routes with the rest of the UI.  
- Archive/download should not follow symlinks out of jail.  
- Upload size limits and extension allow/deny (open Q).

## Relationship to agent tools

| Surface | Purpose |
|---------|---------|
| Tools `file_*` / `list_files` | Model-driven |
| Explorer API | Operator-driven |

Same jail implementation should be **shared** (extract common `workspacefs` package) to avoid drift.

## Implementation sketch (post-accept)

1. `internal/workspacefs` — resolve, list, read, write, mkdir, rename, delete, archive, classify text/binary.  
2. `internal/api` routes under `/api/workspace/*`.  
3. SPA modal component (vanilla JS consistent with current UI).  
4. Context menu + DnD + refresh/save.  
5. Tests: path escape, tar.gz round-trip, upload overwrite.  

## Decisions (partial)

| ID | Decision |
|----|----------|
| **Q1** | Context menu presents **both** “Copy relative path” and “Copy absolute path” |
| **Q2** | Dirty editor on close → prompt **Save / Discard / Cancel** |
| **Q3** | **Yes** — remember last directory (e.g. `sessionStorage`) |
| **Q4** | Navigation = **breadcrumbs** + flat list of current directory (no tree sidebar in v1) |

## Decisions (complete)

| ID | Decision |
|----|----------|
| Q1 | Both copy relative path and copy absolute path |
| Q2 | Dirty close → Save / Discard / Cancel |
| Q3 | Remember last directory (`sessionStorage`) |
| Q4 | Breadcrumbs + flat list |
| Q5 | Plain `<textarea>` editor |
| Q6 | Show dotfiles by default (+ toggle to hide) |
| Q7 | Recursive delete of non-empty folders with confirm popup |
| Q8 | New file auto-opens in editor |
| Q9 | Mobile long-press for context menu |
| Q10 | Max editable size **1 MiB** |
| Q11 | Max upload **50 MiB** per file |
| Q12 | Upload conflict → confirm; Save overwrites open file |
| Q13 | List symlinks; follow only if target stays in jail |
| Q14 | tar.gz abort ~200 MiB uncompressed / 10k files |
| Q15 | Stream large downloads; no extra cap |
| Q16 | `--workspace-readonly` deferred |
| Q17 | Audit log deferred |
| Q18 | Do not notify agent on operator edit |

## Open questions

None blocking.

## Consequences

### Positive

- Operators manage the same tree as the agent without leaving Marble.  
- Upload/download/tar.gz covers common sysadmin workflows.  
- Shared jail code hardens tools + UI together.  

### Trade-offs

- Powerful R/W surface on an unauthenticated local UI.  
- Browser DnD-out is imperfect.  
- Basic editor is not a full IDE.  

### Risks

| Risk | Mitigation |
|------|------------|
| Path escape | Shared resolve + tests |
| Accidental recursive delete | Confirm + optional empty-only |
| Huge tar/upload DoS | Caps + timeouts |
| Symlink escape | Don’t follow out of root |

## Acceptance criteria

- [x] Q1–Q4 decided (both path copies; save/discard/cancel; remember path; breadcrumbs)  
- [ ] Sidebar folder icon + modal agreed  
- [ ] Menu actions + blank-space actions agreed  
- [ ] File vs folder download behavior agreed  
- [ ] DnD semantics agreed (with browser caveats)  
- [ ] Remaining open questions (Q5–Q18) answered or defaulted  
- [ ] Ready to implement  

## Changelog

| Date | Change |
|------|--------|
| 2026-07-17 | Initial proposal |
| 2026-07-17 | Q1–Q4 decisions locked |

## References

- ADR-0001 workspace tools  
- Current UI: `internal/web/static/*`  
- Agent path jail: `internal/tools`  
