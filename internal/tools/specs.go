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
		// mpub (ADR-0009) — human-facing pages under $MEMORY/mpub, served at /mpub/{slug}
		spec("mpub_publish", "Publish content to the Marble mpub server ($MEMORY/mpub). Primary format is HTML; markdown also supported. Returns URL like http://127.0.0.1:8080/mpub/{slug}. Not for workspace project files.",
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
					"tags": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				},
				"required": []string{"slug", "content"},
			}),
		spec("mpub_list", "List published mpub documents (slug, title, url).",
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
