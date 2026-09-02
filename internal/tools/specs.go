package tools

import "github.com/rendicott/marble/internal/model"

func allSpecs() []model.ToolSpec {
	return []model.ToolSpec{
		spec("file_read", "Read a text file under the workspace. Optional offset/limit are 1-based line numbers.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":   map[string]interface{}{"type": "string"},
					"offset": map[string]interface{}{"type": "integer"},
					"limit":  map[string]interface{}{"type": "integer"},
				},
				"required": []string{"path"},
			}),
		spec("file_write", "Create or overwrite a text file under the workspace.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":    map[string]interface{}{"type": "string"},
					"content": map[string]interface{}{"type": "string"},
				},
				"required": []string{"path", "content"},
			}),
		spec("list_files", "List files and directories under a workspace path (shallow or recursive). Path may be relative to the workspace or absolute if it stays under the workspace root.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":      map[string]interface{}{"type": "string", "description": "Relative or absolute path under workspace (default \".\")"},
					"recursive": map[string]interface{}{"type": "boolean"},
				},
			}),
		spec("codebase_summary", "Project layout tree with file sizes under a workspace path.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":        map[string]interface{}{"type": "string"},
					"max_depth":   map[string]interface{}{"type": "integer"},
					"max_entries": map[string]interface{}{"type": "integer"},
				},
			}),
		spec("grep", "Regex search across workspace file contents; returns path, line number, context.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pattern":          map[string]interface{}{"type": "string"},
					"path":             map[string]interface{}{"type": "string"},
					"glob":             map[string]interface{}{"type": "string"},
					"case_insensitive": map[string]interface{}{"type": "boolean"},
					"max_matches":      map[string]interface{}{"type": "integer"},
					"context_lines":    map[string]interface{}{"type": "integer"},
				},
				"required": []string{"pattern"},
			}),
		spec("glob", "Find files by recursive glob pattern (** supported) within the workspace.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pattern":     map[string]interface{}{"type": "string"},
					"path":        map[string]interface{}{"type": "string"},
					"max_results": map[string]interface{}{"type": "integer"},
				},
				"required": []string{"pattern"},
			}),
		spec("edit_file", "Targeted text replacement in an existing file. Requires file_read on the same path earlier in this turn.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":        map[string]interface{}{"type": "string"},
					"old_string":  map[string]interface{}{"type": "string"},
					"new_string":  map[string]interface{}{"type": "string"},
					"replace_all": map[string]interface{}{"type": "boolean"},
				},
				"required": []string{"path", "old_string", "new_string"},
			}),
		spec("apply_patch", "Atomic multi-file edits (add/update/delete) with rollback on failure. update requires prior file_read.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"edits": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"op":         map[string]interface{}{"type": "string", "description": "add|update|delete"},
								"path":       map[string]interface{}{"type": "string"},
								"content":    map[string]interface{}{"type": "string"},
								"old_string": map[string]interface{}{"type": "string"},
								"new_string": map[string]interface{}{"type": "string"},
							},
						},
					},
				},
				"required": []string{"edits"},
			}),
		spec("shell_execute", "Run a shell command in the workspace (policy-enforced). Default timeout 60s, max 300s.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command":     map[string]interface{}{"type": "string"},
					"cwd":         map[string]interface{}{"type": "string"},
					"timeout_sec": map[string]interface{}{"type": "integer"},
				},
				"required": []string{"command"},
			}),
		spec("start_background_task", "Start a long-running command; returns task_id.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": map[string]interface{}{"type": "string"},
					"cwd":     map[string]interface{}{"type": "string"},
					"label":   map[string]interface{}{"type": "string"},
				},
				"required": []string{"command"},
			}),
		spec("kill_background_task", "Kill a background task (SIGTERM, or force SIGKILL).",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"task_id": map[string]interface{}{"type": "string"},
					"signal":  map[string]interface{}{"type": "string", "description": "term|kill"},
				},
				"required": []string{"task_id"},
			}),
		spec("check_background_task", "Status of one task or list all for the session.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"task_id":    map[string]interface{}{"type": "string"},
					"tail_lines": map[string]interface{}{"type": "integer"},
				},
			}),
		spec("schedule_continuation", "Schedule delayed auto-resume: re-inject prompt after delay and/or when a BG task finishes.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"prompt":        map[string]interface{}{"type": "string"},
					"delay_sec":     map[string]interface{}{"type": "integer"},
					"wait_for_task": map[string]interface{}{"type": "string"},
					"label":         map[string]interface{}{"type": "string"},
				},
				"required": []string{"prompt"},
			}),
		spec("cron_list", "List durable cron jobs (recurring schedules). Optional filters: enabled_only, session_id.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"enabled_only": map[string]interface{}{"type": "boolean"},
					"session_id":   map[string]interface{}{"type": "string"},
				},
			}),
		spec("cron_get", "Get one cron job by id, including recent runs.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{"type": "string"},
				},
				"required": []string{"id"},
			}),
		spec("cron_create", "Create a durable cron job. schedule_kind=cron (5-field cron_expr) or interval (interval_sec>=60). session_id optional (created on first fire if missing). Optional model_id pins catalog model for fires.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name":          map[string]interface{}{"type": "string"},
					"enabled":       map[string]interface{}{"type": "boolean"},
					"schedule_kind": map[string]interface{}{"type": "string", "description": "cron|interval"},
					"cron_expr":     map[string]interface{}{"type": "string", "description": "5-field: min hour dom mon dow"},
					"interval_sec":  map[string]interface{}{"type": "integer"},
					"timezone":      map[string]interface{}{"type": "string", "description": "IANA or Local"},
					"session_id":    map[string]interface{}{"type": "string"},
					"prompt":        map[string]interface{}{"type": "string"},
					"max_runs":      map[string]interface{}{"type": "integer"},
					"model_id":      map[string]interface{}{"type": "string", "description": "catalog slug pin for fires (optional)"},
				},
				"required": []string{"name", "prompt", "schedule_kind"},
			}),
		spec("cron_update", "Update fields on an existing cron job by id.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id":            map[string]interface{}{"type": "string"},
					"name":          map[string]interface{}{"type": "string"},
					"enabled":       map[string]interface{}{"type": "boolean"},
					"schedule_kind": map[string]interface{}{"type": "string"},
					"cron_expr":     map[string]interface{}{"type": "string"},
					"interval_sec":  map[string]interface{}{"type": "integer"},
					"timezone":      map[string]interface{}{"type": "string"},
					"session_id":    map[string]interface{}{"type": "string"},
					"prompt":        map[string]interface{}{"type": "string"},
					"max_runs":      map[string]interface{}{"type": "integer"},
					"model_id":      map[string]interface{}{"type": "string"},
				},
				"required": []string{"id"},
			}),
		spec("model_list", "List models: process CLI default plus catalog entries (enabled only in list; no secrets). After listing, use model_add to create missing entries or model_get for full detail.",
			map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			}),
		spec("model_get", "Get one catalog model (or process) by id — includes base_url, api_key_env name, caps, limits, api_key_configured (never the secret).",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{"type": "string", "description": "catalog slug or process"},
				},
				"required": []string{"id"},
			}),
		spec("model_add", "Create a Settings → Models catalog entry. Prefer web research (mcp_tavily / web_fetch) for OpenAI-compatible base_url, context_limit, max_output, and capabilities. NEVER put API secrets in arguments — only api_key_env var NAMES (e.g. GEMINI_API_KEY). Operator puts KEY=secret in $MEMORY/env (Settings → Secrets). Gemini OpenAI path: https://generativelanguage.googleapis.com/v1beta/openai (not /interactions). Empty base_url inherits process --base-url. Empty api_key_env inherits process key; none = no Authorization.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id":              map[string]interface{}{"type": "string", "description": "slug [a-z0-9][a-z0-9._-]{0,63}, e.g. gemini-3.6"},
					"display_name":    map[string]interface{}{"type": "string", "description": "UI label (defaults to id)"},
					"model":           map[string]interface{}{"type": "string", "description": "Provider model string sent on the wire"},
					"base_url":        map[string]interface{}{"type": "string", "description": "OpenAI-compatible API root; empty = inherit process"},
					"api_key_env":     map[string]interface{}{"type": "string", "description": "Env var NAME(s), comma-separated; never the secret. Empty=inherit; none=no auth"},
					"context_limit":   map[string]interface{}{"type": "integer", "description": "Context window tokens (default 131072 if omitted)"},
					"max_output":      map[string]interface{}{"type": "integer", "description": "Max generation tokens (default 8192 if omitted)"},
					"context_reserve": map[string]interface{}{"type": "integer", "description": "0 = inherit process reserve"},
					"cap_tools":       map[string]interface{}{"type": "boolean", "description": "default true"},
					"cap_reasoning":   map[string]interface{}{"type": "boolean"},
					"cap_images":      map[string]interface{}{"type": "boolean", "description": "vision / multimodal"},
					"cap_voice":       map[string]interface{}{"type": "boolean"},
					"enabled":         map[string]interface{}{"type": "boolean", "description": "default true"},
					"sort_order":      map[string]interface{}{"type": "integer"},
					"notes":           map[string]interface{}{"type": "string"},
					"cost_input_per_1m":  map[string]interface{}{"type": "number"},
					"cost_output_per_1m": map[string]interface{}{"type": "number"},
					"cost_notes":         map[string]interface{}{"type": "string"},
				},
				"required": []string{"id", "model"},
			}),
		spec("model_update", "Update an existing catalog model by id (not process). Same fields as model_add; omitted numeric zeros may keep prior values when the server merges. Never put secrets in api_key_env.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id":              map[string]interface{}{"type": "string"},
					"display_name":    map[string]interface{}{"type": "string"},
					"model":           map[string]interface{}{"type": "string"},
					"base_url":        map[string]interface{}{"type": "string"},
					"api_key_env":     map[string]interface{}{"type": "string"},
					"context_limit":   map[string]interface{}{"type": "integer"},
					"max_output":      map[string]interface{}{"type": "integer"},
					"context_reserve": map[string]interface{}{"type": "integer"},
					"cap_tools":       map[string]interface{}{"type": "boolean"},
					"cap_reasoning":   map[string]interface{}{"type": "boolean"},
					"cap_images":      map[string]interface{}{"type": "boolean"},
					"cap_voice":       map[string]interface{}{"type": "boolean"},
					"enabled":         map[string]interface{}{"type": "boolean"},
					"sort_order":      map[string]interface{}{"type": "integer"},
					"notes":           map[string]interface{}{"type": "string"},
					"cost_input_per_1m":  map[string]interface{}{"type": "number"},
					"cost_output_per_1m": map[string]interface{}{"type": "number"},
					"cost_notes":         map[string]interface{}{"type": "string"},
				},
				"required": []string{"id"},
			}),
		spec("session_set_model", "Set this session's catalog model_id for the *next* turn (empty string = process default). Safe during the current turn. model_id must exist and be enabled (model_list / model_add first).",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"model_id": map[string]interface{}{"type": "string", "description": "enabled catalog slug from model_list, or empty for process default"},
				},
			}),
		// Desktop peer (ADR-0020)
		spec("computer_list", "List Marble Peer (aka Pier / desktop computer) agents and online status.",
			map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}),
		spec("computer_bind", "Bind this session to a computer_id for subsequent computer_* tools (empty clears). Marble Peer = remote desktop hands.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"computer_id": map[string]interface{}{"type": "string"},
				},
			}),
		spec("computer_screenshot", "Capture peer primary display (JPEG, max edge 1280) as an image attachment. meta.w/h are IMAGE pixels (click space); meta.screen_w/h are full display; meta.scale = screen/image. After a desktop click, a post-click shot is already attached — do NOT immediately re-call this. USE when CDP fails, click_text/click_button miss, or for non-browser apps. Look at pixels; if ui_unchanged after clicks → computer_confirm.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"computer_id": map[string]interface{}{"type": "string"},
				},
			}),
		spec("computer_desktop_act", "OS-level click/type/key on Marble Peer. Click coords are IMAGE pixels from the latest screenshot (meta.w×meta.h), not necessarily full screen size. Workflow: screenshot → LOOK → click button=1. Post-click screenshot returns in the same result (attachment_id top-level); if ui_unchanged=true STOP repeating coords — use click_button or computer_confirm. Near-duplicate clicks escalate. Prefer click_button for labeled web buttons.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"computer_id": map[string]interface{}{"type": "string"},
					"action":      map[string]interface{}{"type": "string", "description": "click|type|key"},
					"x":           map[string]interface{}{"type": "integer", "description": "X in latest screenshot IMAGE space (meta.w)"},
					"y":           map[string]interface{}{"type": "integer", "description": "Y in latest screenshot IMAGE space (meta.h)"},
					"text":        map[string]interface{}{"type": "string"},
					"key":         map[string]interface{}{"type": "string"},
					"button":      map[string]interface{}{"type": "string", "description": "1=left (default), 2=middle, 3=right. Never empty."},
				},
				"required": []string{"action"},
			}),
		spec("computer_browser_ensure", "On the peer: sync operator Chrome logins into a CDP-capable mirror profile and start/attach that browser. Chrome blocks debugging on the daily profile path — mirror keeps cookies/logins. Call first before other browser tools. force=true re-syncs and restarts the mirror window (does not kill daily Chrome).",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"computer_id": map[string]interface{}{"type": "string"},
					"force":       map[string]interface{}{"type": "boolean", "description": "Re-sync logins from daily Chrome and restart the automation mirror browser"},
				},
			}),
		spec("computer_browser_tabs", "List browser tabs on peer (CDP). Uses operator Chrome when browser_mode=user.",
			map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"computer_id": map[string]interface{}{"type": "string"}},
			}),
		spec("computer_browser_open", "Open URL in peer browser (operator's logged-in Chrome when configured).",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"computer_id": map[string]interface{}{"type": "string"},
					"url":         map[string]interface{}{"type": "string"},
					"new_tab":     map[string]interface{}{"type": "boolean"},
				},
				"required": []string{"url"},
			}),
		spec("computer_browser_snapshot", "Page text snapshot of active tab (CDP). For Gmail: list_rows / message.body. If result has snapshot error, cdp timeout, bot_wall/akam-sw, or empty text: immediately call computer_screenshot and steer from the image (desktop click or report UI).",
			map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"computer_id": map[string]interface{}{"type": "string"}},
			}),
		spec("computer_browser_act", "Browser CDP on Marble Peer. Prefer for web UIs. actions: open_gmail|search_gmail|click_button|click_text|open|click|type|press|eval|wait|set_input_files. Prefer click_button for labeled buttons (Create…, Save…); click_text for links/rows. Do NOT use jQuery :contains selectors. wait: text=substring and/or target=CSS, x=timeout_ms. set_input_files: absolute peer paths. On cdp timeout / not_found / ambiguous / bot wall — STOP CDP retries; screenshot then desktop click or computer_confirm.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"computer_id": map[string]interface{}{"type": "string"},
					"action":      map[string]interface{}{"type": "string", "description": "open_gmail|search_gmail|click_button|click_text|open|click|type|press|eval|wait|set_input_files"},
					"target":      map[string]interface{}{"type": "string", "description": "CSS selector (no :contains); URL for open; optional file input selector for set_input_files"},
					"text":        map[string]interface{}{"type": "string", "description": "button label / query / typed text; for wait: substring; for set_input_files: absolute peer path(s)"},
					"x":           map[string]interface{}{"type": "integer", "description": "click x; for wait: timeout_ms (default 10000, max 60000)"},
					"y":           map[string]interface{}{"type": "integer"},
				},
				"required": []string{"action"},
			}),
		spec("computer_confirm", "Ask a human to Accept/Deny a high-risk action (default DENY after 120s). Surfaces in Marble harness UI (Accept/Deny card in the session), AND on the peer (notification/tray/mini-UI). Prefer telling the user to click Accept in Marble if they are remote from the peer. Do not assume acceptance.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"computer_id": map[string]interface{}{"type": "string"},
					"prompt":      map[string]interface{}{"type": "string", "description": "Clear description of what you want permission to do"},
					"risk":        map[string]interface{}{"type": "string", "description": "e.g. high, money, auth"},
				},
				"required": []string{"prompt"},
			}),
		spec("computer_stop", "Cancel in-flight peer action.",
			map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"computer_id": map[string]interface{}{"type": "string"}},
			}),
		spec("cron_delete", "Delete a cron job by id.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{"type": "string"},
				},
				"required": []string{"id"},
			}),
		spec("cron_run", "Fire a cron job immediately (run now). Does not shift the regular schedule.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{"type": "string"},
				},
				"required": []string{"id"},
			}),
		spec("get_context_usage", "Current context usage estimate and token totals.",
			map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}),
		spec("session_compact", "Compact context by LLM-summarizing older history (system agent). Use when context is high.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"style":       map[string]interface{}{"type": "string"},
					"keep_last_n": map[string]interface{}{"type": "integer"},
				},
			}),
		spec("memory_search", "Search agent memory (session/daily/knowledge) by keywords, time, tags.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query":       map[string]interface{}{"type": "string"},
					"since":       map[string]interface{}{"type": "string"},
					"until":       map[string]interface{}{"type": "string"},
					"tags":        map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
					"scope":       map[string]interface{}{"type": "string"},
					"max_results": map[string]interface{}{"type": "integer"},
				},
				"required": []string{"query"},
			}),
		spec("memory_fetch", "Fetch full contents of a memory file by path, topic, or session_id.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":       map[string]interface{}{"type": "string"},
					"topic":      map[string]interface{}{"type": "string"},
					"session_id": map[string]interface{}{"type": "string"},
				},
			}),
		spec("memory_write", "Persist intentional knowledge under $MEMORY/knowledge/.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"topic":   map[string]interface{}{"type": "string"},
					"content": map[string]interface{}{"type": "string"},
					"tags":    map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
					"mode":    map[string]interface{}{"type": "string", "description": "append|overwrite"},
				},
				"required": []string{"topic", "content"},
			}),
		spec("skill_search", "Search available skills by keyword; metadata only.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query":       map[string]interface{}{"type": "string"},
					"max_results": map[string]interface{}{"type": "integer"},
				},
			}),
		spec("skill_load", "Load full SKILL.md or a reference file for a named skill.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{"type": "string"},
					"ref":  map[string]interface{}{"type": "string"},
				},
				"required": []string{"name"},
			}),
		spec("message_attach", "Attach an image or basic document to the chat transcript (durable chip the operator can download). Prefer this over attach_file when the user wants files in the chat. Images: png/jpeg/webp/gif. Docs: txt/md/csv/json/html/svg (SVG is a document, not an inline image). No audio/PDF. Prefer workspace path. Not re-injected as a full tool result.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string", "description": "Workspace-relative path"},
					"name": map[string]interface{}{"type": "string", "description": "Display name"},
				},
				"required": []string{"path"},
			}),
		spec("attach_file", "Attach a workspace file to the current reply for UI inline/download (not re-injected into model context).",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":   map[string]interface{}{"type": "string"},
					"as":     map[string]interface{}{"type": "string"},
					"inline": map[string]interface{}{"type": "boolean"},
				},
				"required": []string{"path"},
			}),
		// web_fetch (ADR-0012) — direct URL retrieval after search returns real URLs
		spec("web_fetch", "HTTP(S) GET a URL for deeper analysis. HTML/text is returned as markdown; JSON is returned raw. Prefer after web search (e.g. mcp_tavily_tavily_search) returns real URLs — do not invent URLs. Use Tavily extract/crawl only when fetch fails or multi-page crawl is needed. Prefer over shell curl. LAN/private hosts allowed; cloud metadata blocked.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"url": map[string]interface{}{
						"type":        "string",
						"description": "Absolute http or https URL from search results or user",
					},
					"max_bytes": map[string]interface{}{
						"type":        "integer",
						"description": "Optional download size cap (default 2MiB)",
					},
				},
				"required": []string{"url"},
			}),
		// call_agent_process (ADR-0014) — external coding harnesses (grok/claude)
		spec("call_agent_process", "Run an external coding agent headless (format=grok|claude). Prefer background=true for app/multi-file work; poll {\"task_id\"}. Judge progress by progress.cwd_mtime_changed / stuck_hint — not identical poll text. Do not kill under ~5–8m unless stuck_hint. Short implement prompts beat long CRITICAL essays. Defaults (agent_process.json): medium effort, --no-plan, max-turns~40, timeout~15m. Allowlisted extra_args e.g. --effort low|medium|high, --max-turns N, --no-plan. Kill: {\"task_id\",\"kill\":true}. Prefer Marble tools for simple edits.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"format": map[string]interface{}{
						"type":        "string",
						"description": "Driver: grok | claude",
					},
					"prompt": map[string]interface{}{
						"type":        "string",
						"description": "Focused implement task (edit these files, then stop). Avoid multi-page planning essays.",
					},
					"cwd": map[string]interface{}{
						"type":        "string",
						"description": "Working directory relative to Marble workspace (default root)",
					},
					"workdir": map[string]interface{}{
						"type":        "string",
						"description": "Dedicated subdir under cwd/workspace (created if missing). Prefer real repo root for app fixes, not empty workdirs.",
					},
					"background": map[string]interface{}{
						"type":        "boolean",
						"description": "If true, return agent_task_id immediately (preferred for multi-minute jobs)",
					},
					"task_id": map[string]interface{}{
						"type":        "string",
						"description": "Poll an existing background agent task (or use with kill=true)",
					},
					"kill": map[string]interface{}{
						"type":        "boolean",
						"description": "With task_id: terminate the agent process group",
					},
					"detail": map[string]interface{}{
						"type":        "boolean",
						"description": "On poll: include full command/prompt (default omits bulky fields while running)",
					},
					"output_format": map[string]interface{}{
						"type":        "string",
						"description": "plain | json (default json when supported)",
					},
					"timeout_sec": map[string]interface{}{
						"type":        "integer",
						"description": "Wall timeout seconds (default from agent_process.json, often 900)",
					},
					"model": map[string]interface{}{
						"type":        "string",
						"description": "Optional model id passed to the child CLI",
					},
					"extra_args": map[string]interface{}{
						"type":  "array",
						"items": map[string]interface{}{"type": "string"},
						"description": "Allowlisted CLI flags, e.g. [\"--effort\",\"low\",\"--max-turns\",\"25\",\"--no-plan\"]",
					},
				},
			}),
		// mpub (ADR-0009 + visibility) — human-facing pages under $MEMORY/mpub
		spec("mpub_publish", "Publish content to Marble mpub ($MEMORY/mpub). Default visibility is private (allowlisted admins only when OAuth is on). Set visibility=public only when the user explicitly asks to share openly. HTML preferred; markdown ok.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"slug": map[string]interface{}{
						"type":        "string",
						"description": "Required URL segment [a-z0-9-], e.g. foo-research",
					},
					"title":   map[string]interface{}{"type": "string"},
					"content": map[string]interface{}{"type": "string", "description": "Body (HTML preferred; markdown ok)"},
					"content_type": map[string]interface{}{
						"type":        "string",
						"description": "text/html (default) | text/markdown | text/plain",
					},
					"if_exists": map[string]interface{}{
						"type":        "string",
						"description": "overwrite (default) | fail",
					},
					"visibility": map[string]interface{}{
						"type":        "string",
						"description": "private (default for new) | public. Omit on overwrite to keep existing visibility.",
					},
					"tags": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				},
				"required": []string{"slug", "content"},
			}),
		spec("mpub_list", "List published mpub documents (slug, title, url, visibility).",
			map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}),
		spec("mpub_get", "Fetch an mpub document by slug (meta + content).",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"slug": map[string]interface{}{"type": "string"},
				},
				"required": []string{"slug"},
			}),
		spec("mpub_unpublish", "Delete a published mpub document by slug.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"slug": map[string]interface{}{"type": "string"},
				},
				"required": []string{"slug"},
			}),
		spec("mpub_set_visibility", "Set an mpub page to public or private (promote/demote without rewriting body).",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"slug":       map[string]interface{}{"type": "string"},
					"visibility": map[string]interface{}{"type": "string", "description": "public | private"},
				},
				"required": []string{"slug", "visibility"},
			}),
	}
}

func spec(name, desc string, params map[string]interface{}) model.ToolSpec {
	return model.ToolSpec{
		Type: "function",
		Function: model.ToolFunctionSchema{
			Name:        name,
			Description: desc,
			Parameters:  params,
		},
	}
}
