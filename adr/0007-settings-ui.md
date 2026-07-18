# ADR-0007: Settings UI

| Field | Value |
|-------|--------|
| **Status** | Accepted (ready to implement) |
| **Date** | 2026-07-17 |
| **Deciders** | Project owner |
| **Tags** | ui, settings, config, database, cli, mcp |
| **Extends** | ADR-0003 (DB `settings`), ADR-0005 (shell/loop), ADR-0006 (MCP) |
| **Answers** | `adr/0007-answers.json` (`2026-07-18T02:29:38.026Z`) |

## Context

Marble is configured from three places:

| Source | Examples |
|--------|----------|
| **CLI flags** | `--base-url`, `--model`, `--workspace`, `--memory`, `--persist-interval`, tool/shell timeouts, `--disable-shell` |
| **SQLite `settings`** | Retention, `db_inline_max_bytes`, shell policy keys |
| **Browser local state** | Explorer last path, show closed, show dotfiles |
| **MCP config** | `$MEMORY/mcp.json` (ADR-0006) |

Operators need a first-class UI to inspect effective config and edit durable tunables (and MCP servers) without SQL or hand-editing JSON alone.

## Goals

1. **Inspect** launch-time / effective harness configuration (read-only where CLI-owned).  
2. **Edit** durable DB `settings` keys (retention, shell policy).  
3. **Manage MCP** servers in Settings v1 (align with ADR-0006; not “later only”).  
4. **See system health** (mode, schema, paths, model ok, MCP chip).  
5. Honor prior constraints: base URL not in DB; limp disables DB writes; CLI shell kill-switch wins.

## Non-goals (v1)

- Hot-reload of CLI flags without restart  
- Secrets vault / OAuth  
- Settings export/import file format (copy DB is enough)  
- Changing model/base URL from UI  

## Decision

1. **⚙ gear** in sidebar head: `Sessions · 📁 · ⚙ · +` (**Q1**).  
2. **Modal** settings UI, same pattern as workspace explorer (**Q2**).  
3. **Left nav** on desktop; **top tabs** on narrow screens (**Q3**).  
4. Backend: `GET/PUT /api/settings` for DB keys; MCP via existing/extension of ADR-0006 config APIs or settings subsection that reads/writes `mcp.json` safely.  
5. **Tiers:**
   - **Runtime (read-only):** CLI/process — model, base URL, workspace, memory, addr, budgets, limp/schema; label *from launch flags — restart to change* (**Q4**, **Q5**).  
   - **Persistent (editable):** SQLite `settings`.  
   - **MCP (editable file/API):** servers from `mcp.json` (**Q14**).  
   - **UI prefs:** browser defaults; explorer/session toggles remain (**Q15**).  
6. **Agent** section: loop thresholds **read-only** from CLI/code if cheap (**Q6**).  
7. Shell lists: multi-line text → JSON arrays server-side (**Q7**).  
8. Confirm Save when any `shell_*` key changes (**Q8**).  
9. Effective shell chip (**Q9**).  
10. PUT rejects unknown keys with **400** (**Q10**).  
11. **Reset section to defaults** for Memory & Shell (**Q11**).  
12. No export/import UI (**Q12**).  
13. Shell/tool paths **read DB live** (or short TTL) after Save (**Q13**).  
14. Limp: runtime + about visible; persistent Save disabled.

## Decisions locked (Q1–Q15)

| ID | Decision |
|----|----------|
| **Q1** | Sidebar head gear next to folder (`Sessions · folder · gear · +`) |
| **Q2** | Modal v1 |
| **Q3** | Left nav desktop; top tabs mobile |
| **Q4** | Model + base_url CLI-only, read-only in UI |
| **Q5** | Label runtime fields “from launch flags — restart to change” |
| **Q6** | Agent loop thresholds CLI/code; read-only display if cheap |
| **Q7** | Multi-line patterns → server JSON array |
| **Q8** | Confirm Save if any `shell_*` key changes |
| **Q9** | Effective shell status chip |
| **Q10** | Reject unknown PUT keys (400 + list) |
| **Q11** | Reset defaults for Memory & Shell sections |
| **Q12** | Defer export/import |
| **Q13** | Live-apply shell settings (read DB per call / short TTL) |
| **Q14** | **Include MCP in Settings v1** (not deferred) |
| **Q15** | Settings UI prefs set defaults; existing toggles remain |

## UI sections

| Section | Content |
|---------|---------|
| **Runtime** | Read-only CLI fields + labels |
| **Memory & DB** | Editable retention / inline max; Reset defaults |
| **Shell** | Editable policy + confirm on Save; status chip; Reset defaults |
| **Agent** | Read-only loop thresholds if available |
| **MCP / Integrations** | List/enable servers from `mcp.json`; add/edit/remove or open/edit flow; health per server (**Q14**) |
| **UI** | Defaults for show closed, explorer dotfiles, etc. |
| **About** | Health, paths, schema, MCP summary chip |

## API sketch

### `GET /api/settings`

Returns `mode`, `editable`, `runtime`, `persistent`, `shell_effective`, and **`mcp`** summary (servers count, enabled, errors) for the modal.

### `PUT /api/settings`

Partial update of **known** persistent keys only; 400 on unknowns; 503 if limp.

### MCP

Prefer:

- `GET/PUT` helpers that load/save `$MEMORY/mcp.json` with validation (Cursor-compatible shape from ADR-0006), **or**  
- Dedicated `/api/mcp/...` routes already/soon present, **embedded** in the Settings MCP section.

UI must not store MCP secrets in SQLite; keep env var references as today.

## Precedence

| Concern | Winner |
|---------|--------|
| Model / base URL / workspace / memory / addr | CLI |
| `--disable-shell` | CLI over DB `shell_enabled` |
| Retention, shell lists, timeouts | DB settings (UI) |
| MCP server definitions | `mcp.json` (UI) |
| UI chrome defaults | Browser / Settings UI prefs |

## Security

- Unauthenticated local UI trust model unchanged.  
- Shell Save confirmation required.  
- MCP config edits are high impact (subprocess spawn); confirm on destructive remove / enabling servers.  
- No arbitrary SQL; no writing `schema_meta` from UI.

## Implementation order

1. `db` list/set + validation registry for known keys.  
2. `GET/PUT /api/settings` (+ shell_effective).  
3. MCP config read/write surface for UI (if not already).  
4. Settings modal + gear; sections including MCP.  
5. Confirm flows; limp disable Save.  
6. UI prefs defaults wiring.  
7. Tests: unknown keys, limp 503, shell confirm path, mcp.json round-trip.

## Open questions

None blocking. Optional later: deep-link `/settings`, export JSON.

## Acceptance criteria

- [x] Q1–Q15 answered (Q14 = include MCP)  
- [x] Runtime vs persistent vs MCP split defined  
- [x] Ready to implement  

## Changelog

| Date | Change |
|------|--------|
| 2026-07-17 | Initial proposal |
| 2026-07-18 | Locked Q1–Q15 from `0007-answers.json`; MCP in v1 |

## References

- `adr/0007-answers.json`  
- ADR-0003, ADR-0005, ADR-0006  
