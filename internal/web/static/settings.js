/* Settings modal (ADR-0007) — tooltips, grouped runtime, MCP tool hover */
(() => {
  const UI_PREFS_KEY = "marble.ui.prefs";

  const TIPS = {
    // runtime / model
    model:
      "Model id sent to the OpenAI-compatible chat API (not the Marble UI). Example: Qwen/Qwen3.5-122B-A10B-FP8",
    model_auth:
      "Model HTTP auth (ADR-0016): none = no Authorization header; env = Bearer from --api-key-env. Never shows the secret.",
    model_auth_env:
      "CLI --api-key-env: comma-separated environment variable names. First non-empty wins. Secret stays in the env only.",
    model_auth_configured:
      "Whether a non-empty API key was resolved at launch from those env vars.",
    base_url:
      "Model API base URL (OpenAI-compatible /v1), NOT the Marble web server address. Marble itself is under Listen (addr). Example: http://host:8000/v1",
    context_limit:
      "Total model context window in tokens (input+output frame). Example: 262144",
    max_output: "Max generation tokens per model call. Example: 32768",
    context_reserve:
      "Tokens reserved for tool schemas and formatting, kept out of the history budget. Example: 8192",
    budget:
      "Effective prompt budget = context_limit − max_output − context_reserve. History is trimmed to fit this.",
    workspace:
      "Filesystem root for agent file tools and shell cwd (jail). Set with --workspace. Example: /home/you/project",
    memory:
      "Marble data home (sessions, daily, marble.db, mcp.json). Set with --memory. Example: ~/.marble",
    addr: "Where the Marble HTTP UI listens. Example: :8080 → http://127.0.0.1:8080 — not the model base URL.",
    persist_interval:
      "How often dirty sessions flush to disk. Example: 300s (5m)",
    mode: "normal = SQLite dual-write OK; limp = Markdown/chat only, no DB writes.",
    schema: "Binary schema version vs DB schema_version. Mismatch forces limp.",
    disable_shell:
      "CLI --disable-shell. When true, shell tools stay off even if DB shell_enabled is true.",
    mcp_config_path:
      "Path to mcp.json (default $MEMORY/mcp.json). Edit servers under the MCP section.",

    // memory
    blob_max_age_days:
      "Days before unreferenced spilled blobs can be GC’d. Example: 4. Does not delete session Markdown.",
    closed_session_max_age_days:
      "Days after close before DB rows/events for a session are pruned. Markdown files are kept. Example: 4",
    db_inline_max_bytes:
      "Max bytes stored inline in SQLite before spilling to blobs/. Example: 32768 (32 KiB)",

    // shell
    shell_enabled:
      "Master switch for shell_execute / background shell tasks (unless CLI --disable-shell). true/false",
    shell_mode:
      "deny_list = allow by default, block matching patterns. allow_list = only matching patterns run. Example: deny_list",
    shell_allow_sudo:
      "If true and sudo is removed from deny patterns, sudo-like strings may pass policy. Still no extra OS privileges beyond the harness user.",
    shell_default_timeout_sec:
      "Default timeout when the agent omits timeout_sec. Example: 60",
    shell_max_timeout_sec:
      "Hard ceiling for shell_execute. Longer jobs should use start_background_task. Example: 300 (5m)",
    shell_max_output_bytes:
      "Cap on captured stdout/stderr before truncation. Example: 524288 (512 KiB)",
    shell_cwd_strict:
      "ON (recommended): shell working directory must stay under --workspace. OFF: agent may set cwd to absolute paths outside the workspace (easier to touch out-of-tree files). This is NOT a full FS jail — even with strict ON, a command can still use absolute paths like /etc/… unless deny patterns block them. Unchecking only relaxes the starting cwd check.",
    shell_block_memory_paths:
      "Best-effort block when the command string contains the --memory path (protects harness private store). true/false",
    shell_deny_patterns:
      "One Go/RE2-style regex per line, matched against the full command. Example: (?i)\\bsudo\\b — empty lines and # comments ignored.",
    shell_allow_patterns:
      "Used only in allow_list mode. One regex per line; command must match at least one. Example: ^git (status|diff|log)",

    // agent
    max_tool_iters: "Hard stop tool rounds per user turn. CLI --max-tool-iters. Example: 80",
    tool_round_soft: "Soft advisory threshold before hard stop. Example: 65",
    soft_wall_sec:
      "Soft wall-clock of continuous tool rounds without a final reply. Example: 180 (3m)",
    tool_round_db:
      "DB copies of soft/hard (informational). Loop still primarily uses CLI flags in v1.",

    // ui
    show_closed: "Default for the session list “Show closed” checkbox on load (this browser only).",
    show_dotfiles: "Default for explorer “Dotfiles” toggle on load (this browser only).",

    // mcp
    mcp_enabled: "Whether Marble should connect this server on Save/start.",
    mcp_command:
      "stdio: executable Marble spawns and owns (PID lifecycle). Example: npx",
    mcp_args: 'stdio: JSON array of args. Example: ["-y","tavily-mcp@latest"]',
    mcp_url: "HTTP/SSE MCP endpoint (no local process). Example: https://host/mcp",
    mcp_env:
      'Environment for the child process only (stdio). Use ${VAR} placeholders. Example: {"TAVILY_API_KEY":"${TAVILY_API_KEY}"}',
  };

  const els = {
    open: document.getElementById("btn-settings"),
    modal: document.getElementById("settings-modal"),
    pane: document.getElementById("settings-pane"),
    nav: document.getElementById("settings-nav"),
    save: document.getElementById("settings-save"),
    reset: document.getElementById("settings-reset"),
    close: document.getElementById("settings-close"),
    chip: document.getElementById("settings-shell-chip"),
    banner: document.getElementById("settings-banner"),
  };
  if (!els.open || !els.modal) return;

  let data = null;
  let section = "runtime";
  let dirty = false;
  let draft = {};
  let mcpDraft = null;
  let mcpDirty = false;
  let uiPrefs = loadUIPrefs();

  function loadUIPrefs() {
    try {
      return JSON.parse(localStorage.getItem(UI_PREFS_KEY) || "{}");
    } catch {
      return {};
    }
  }
  function saveUIPrefs(p) {
    uiPrefs = p;
    localStorage.setItem(UI_PREFS_KEY, JSON.stringify(p));
    const sc = document.getElementById("show-closed");
    if (sc && p.show_closed != null) sc.checked = !!p.show_closed;
    const fh = document.getElementById("fs-hidden");
    if (fh && p.show_dotfiles != null) fh.checked = !!p.show_dotfiles;
  }

  function api(path, opts) {
    opts = opts || {};
    const method = (opts.method || "GET").toUpperCase();
    const headers = {
      "Content-Type": "application/json",
      ...(opts.headers || {}),
    };
    if (method !== "GET" && method !== "HEAD") {
      headers["X-Marble-Requested-With"] = "fetch";
    }
    return fetch(path, {
      ...opts,
      headers,
      credentials: "same-origin",
    }).then(async (res) => {
      if (res.status === 401) {
        location.href = "/auth/login?next=" + encodeURIComponent(location.pathname);
        throw new Error("auth_required");
      }
      if (!res.ok) throw new Error(await res.text());
      const ct = res.headers.get("content-type") || "";
      if (ct.includes("application/json")) return res.json();
      return res.text();
    });
  }

  function modelAuthLabel(r) {
    const mode = r.model_auth || "none";
    if (mode === "env") {
      const used = r.model_auth_env_used ? ` via ${r.model_auth_env_used}` : "";
      const cfg = r.model_auth_configured ? "configured" : "env empty";
      return `env (${cfg}${used})`;
    }
    return "none";
  }

  function tip(key) {
    const t = TIPS[key];
    if (!t) return "";
    // data-tip drives a custom floating tooltip (native title is unreliable inside overflow panes)
    return `<button type="button" class="tip" data-tip="${escapeAttr(t)}" aria-label="Help">?</button>`;
  }
  function labelWithTip(label, tipKey) {
    return `<span class="field-label">${escapeHtml(label)}${tip(tipKey)}</span>`;
  }

  // Floating tooltip (body portal) — works inside scrollable modal panes
  let tipEl = document.getElementById("settings-float-tip");
  if (!tipEl) {
    tipEl = document.createElement("div");
    tipEl.id = "settings-float-tip";
    tipEl.className = "settings-float-tip";
    tipEl.hidden = true;
    document.body.appendChild(tipEl);
  }
  let tipHideTimer = null;
  function showFloatTip(text, anchor) {
    if (!text) return;
    clearTimeout(tipHideTimer);
    tipEl.textContent = text;
    tipEl.hidden = false;
    tipEl.classList.add("show");
    // position after paint so size is known
    requestAnimationFrame(() => {
      const r = anchor.getBoundingClientRect();
      const tw = tipEl.offsetWidth || 280;
      const th = tipEl.offsetHeight || 80;
      let left = r.left + r.width / 2 - tw / 2;
      let top = r.bottom + 8;
      if (left < 8) left = 8;
      if (left + tw > window.innerWidth - 8) left = window.innerWidth - tw - 8;
      if (top + th > window.innerHeight - 8) top = r.top - th - 8;
      if (top < 8) top = 8;
      tipEl.style.left = left + "px";
      tipEl.style.top = top + "px";
    });
  }
  function hideFloatTip() {
    tipHideTimer = setTimeout(() => {
      tipEl.classList.remove("show");
      tipEl.hidden = true;
    }, 80);
  }
  // Delegate hover/focus from modal (re-rendered content)
  els.modal.addEventListener("mouseover", (e) => {
    const t = e.target.closest("[data-tip]");
    if (!t || !els.modal.contains(t)) return;
    showFloatTip(t.getAttribute("data-tip") || "", t);
  });
  els.modal.addEventListener("mouseout", (e) => {
    const t = e.target.closest("[data-tip]");
    if (!t) return;
    const to = e.relatedTarget;
    if (to && (t.contains(to) || tipEl.contains(to))) return;
    hideFloatTip();
  });
  els.modal.addEventListener(
    "focusin",
    (e) => {
      const t = e.target.closest("[data-tip]");
      if (t) showFloatTip(t.getAttribute("data-tip") || "", t);
    },
    true
  );
  els.modal.addEventListener(
    "focusout",
    (e) => {
      const t = e.target.closest("[data-tip]");
      if (t) hideFloatTip();
    },
    true
  );
  tipEl.addEventListener("mouseenter", () => clearTimeout(tipHideTimer));
  tipEl.addEventListener("mouseleave", hideFloatTip);

  function setDirty(v) {
    dirty = !!v;
    els.save.disabled =
      !dirty || (data && data.editable === false && section !== "ui" && section !== "mcp");
    if (section === "mcp")
      els.save.disabled = !mcpDirty || (data && data.runtime && data.runtime.mcp_disabled_cli);
    if (section === "ui") els.save.disabled = !dirty;
    if (section === "runtime" || section === "agent" || section === "about")
      els.save.disabled = true;
  }

  function val(key) {
    if (draft[key] != null) return draft[key];
    return (data && data.persistent && data.persistent[key]) || "";
  }

  function setVal(key, v) {
    draft[key] = v;
    setDirty(true);
  }

  function patternsToLines(jsonStr) {
    try {
      const a = JSON.parse(jsonStr || "[]");
      return Array.isArray(a) ? a.join("\n") : "";
    } catch {
      return jsonStr || "";
    }
  }

  function render() {
    if (!data) {
      els.pane.innerHTML = "<p class='hint'>Loading…</p>";
      return;
    }
    const editable = !!data.editable;
    els.banner.hidden = data.mode !== "limp";
    if (data.mode === "limp") {
      els.banner.textContent =
        "⚠ Limp mode: " +
        (data.limp_reason || "DB not writable") +
        " — persistent settings cannot be saved.";
    }
    if (data.shell_effective) {
      els.chip.textContent = "Shell: " + (data.shell_effective.label || "—");
      els.chip.title = data.shell_effective.reason || "";
    }
    els.reset.style.display =
      section === "memory" || section === "shell" ? "" : "none";

    const p = data.persistent || {};
    const r = data.runtime || {};

    if (section === "runtime") {
      els.pane.innerHTML = `
        <h3>Runtime</h3>
        <p class="hint">From launch flags — restart to change. Hover <span class="tip-inline">?</span> for details.</p>

        <div class="settings-group">
          <h4>Model (LLM API)</h4>
          <p class="hint">These talk to your OpenAI-compatible model server — not the Marble web UI.</p>
          ${ro("Model id", r.model, "model")}
          ${ro("Model base URL", r.base_url, "base_url")}
          ${ro("Model auth", modelAuthLabel(r), "model_auth")}
          ${ro("API key env (--api-key-env)", r.model_auth_env || "—", "model_auth_env")}
          ${ro("API key configured", String(!!r.model_auth_configured), "model_auth_configured")}
          ${ro("UI auth mode", r.auth_mode || "open", "auth_mode")}
          ${r.auth_mode === "google" ? ro("OAuth client id", r.oauth_client_id || "—", "oauth_client_id") : ""}
          ${r.auth_mode === "google" ? ro("OAuth redirect", r.oauth_redirect_url || "—", "oauth_redirect") : ""}
          ${r.auth_mode === "google" ? ro("Allowlisted admins", (r.oauth_allow_emails && r.oauth_allow_emails.length) ? r.oauth_allow_emails.join(", ") : "—", "oauth_allow") : ""}
          ${r.auth_mode === "google" && r.current_user ? ro("Signed in as", r.current_user.email || r.current_user.name || "—", "current_user") : ""}
          ${ro("TLS enabled", String(!!r.tls_enabled), "tls_enabled")}
          ${ro("Context limit (tokens)", r.context_limit, "context_limit")}
          ${ro("Max output (tokens)", r.max_output, "max_output")}
          ${ro("Context reserve (tokens)", r.context_reserve, "context_reserve")}
          ${ro("Prompt budget (tokens)", r.budget, "budget")}
        </div>

        <div class="settings-group">
          <h4>Harness process</h4>
          ${ro("Workspace (tool jail)", r.workspace, "workspace")}
          ${ro("Memory data home", r.memory, "memory")}
          ${ro("Marble listen address", r.addr, "addr")}
          ${ro("Persist interval", (r.persist_interval_sec || 0) + "s", "persist_interval")}
          ${ro("CLI disable shell", String(!!r.disable_shell), "disable_shell")}
          ${ro("MCP config path", r.mcp_config_path || "", "mcp_config_path")}
        </div>

        <div class="settings-group">
          <h4>Database / mode</h4>
          ${ro("Mode", data.mode + (data.limp_reason ? " — " + data.limp_reason : ""), "mode")}
          ${ro("Schema (binary / db)", `${r.schema_version_binary} / ${r.schema_version_db != null ? r.schema_version_db : "—"}`, "schema")}
        </div>
      `;
    } else if (section === "memory") {
      els.pane.innerHTML = `
        <h3>Memory &amp; DB</h3>
        <p class="hint">Persistent SQLite settings. ${editable ? "Saved to marble.db." : "Read-only (limp)."}</p>
        ${numField("blob_max_age_days", "Blob max age (days)", val("blob_max_age_days"), editable, "blob_max_age_days")}
        ${numField("closed_session_max_age_days", "Closed session prune (days)", val("closed_session_max_age_days"), editable, "closed_session_max_age_days")}
        ${numField("db_inline_max_bytes", "DB inline max bytes (spill threshold)", val("db_inline_max_bytes"), editable, "db_inline_max_bytes")}
      `;
      bindFields(editable);
    } else if (section === "shell") {
      els.pane.innerHTML = `
        <h3>Shell policy</h3>
        <p class="hint">Changes apply on Save (confirmed). CLI --disable-shell always wins. Shell is not a full OS sandbox.</p>
        ${boolField("shell_enabled", "Shell enabled", val("shell_enabled"), editable && !r.disable_shell, "shell_enabled")}
        ${selectField("shell_mode", "Mode", val("shell_mode") || "deny_list", ["deny_list", "allow_list"], editable, "shell_mode")}
        ${boolField("shell_allow_sudo", "Allow sudo patterns", val("shell_allow_sudo"), editable, "shell_allow_sudo")}
        ${numField("shell_default_timeout_sec", "Default timeout (sec)", val("shell_default_timeout_sec"), editable, "shell_default_timeout_sec")}
        ${numField("shell_max_timeout_sec", "Max timeout (sec)", val("shell_max_timeout_sec"), editable, "shell_max_timeout_sec")}
        ${numField("shell_max_output_bytes", "Max output bytes", val("shell_max_output_bytes"), editable, "shell_max_output_bytes")}
        ${boolField("shell_cwd_strict", "CWD must stay in workspace", val("shell_cwd_strict"), editable, "shell_cwd_strict")}
        <p class="field-help">${escapeHtml(TIPS.shell_cwd_strict)}</p>
        ${boolField("shell_block_memory_paths", "Block access under --memory", val("shell_block_memory_paths"), editable, "shell_block_memory_paths")}
        ${textArea("shell_deny_patterns", "Deny patterns (one regex per line)", patternsToLines(val("shell_deny_patterns")), editable, "shell_deny_patterns")}
        ${textArea("shell_allow_patterns", "Allow patterns (allow_list mode)", patternsToLines(val("shell_allow_patterns")), editable, "shell_allow_patterns")}
      `;
      bindFields(editable);
    } else if (section === "agent") {
      els.pane.innerHTML = `
        <h3>Agent loop</h3>
        <p class="hint">CLI / code defaults — read-only in Settings v1.</p>
        ${ro("Max tool iters (hard)", r.max_tool_iters, "max_tool_iters")}
        ${ro("Tool round soft", r.tool_round_soft, "tool_round_soft")}
        ${ro("Soft wall (sec)", r.soft_wall_sec, "soft_wall_sec")}
        ${ro("DB tool_round_soft / hard", `${p.tool_round_soft || "—"} / ${p.tool_round_hard || "—"}`, "tool_round_db")}
      `;
    } else if (section === "mcp") {
      renderMCP();
    } else if (section === "ui") {
      const sc = uiPrefs.show_closed != null ? !!uiPrefs.show_closed : false;
      const df = uiPrefs.show_dotfiles != null ? !!uiPrefs.show_dotfiles : true;
      els.pane.innerHTML = `
        <h3>UI preferences</h3>
        <p class="hint">Stored in this browser. Existing toggles still work; these set defaults on load.</p>
        <div class="settings-field">
          <label>${labelWithTip("Show closed sessions by default", "show_closed")}</label>
          <label class="check-row"><input type="checkbox" id="pref-show-closed" ${sc ? "checked" : ""}/> Enable</label>
        </div>
        <div class="settings-field">
          <label>${labelWithTip("Show dotfiles in explorer by default", "show_dotfiles")}</label>
          <label class="check-row"><input type="checkbox" id="pref-dotfiles" ${df ? "checked" : ""}/> Enable</label>
        </div>
      `;
      const a = document.getElementById("pref-show-closed");
      const b = document.getElementById("pref-dotfiles");
      const mark = () => {
        draft.__ui = true;
        setDirty(true);
      };
      a.addEventListener("change", mark);
      b.addEventListener("change", mark);
    } else if (section === "about") {
      const mcp = data.mcp || {};
      els.pane.innerHTML = `
        <h3>About / diagnostics</h3>
        ${ro("Mode", data.mode, "mode")}
        ${ro("Memory", r.memory, "memory")}
        ${ro("Workspace", r.workspace, "workspace")}
        ${ro("Model", r.model, "model")}
        ${ro("MCP summary", JSON.stringify(mcp, null, 0).slice(0, 280), null)}
        <p class="hint">Marble harness · settings ADR-0007 · MCP ADR-0006</p>
      `;
    }
  }

  function ro(label, value, tipKey) {
    return `<div class="settings-field">
      <label>${tipKey ? labelWithTip(label, tipKey) : escapeHtml(label)}</label>
      <div class="ro">${escapeHtml(String(value == null ? "" : value))}</div>
    </div>`;
  }
  function numField(key, label, value, ed, tipKey) {
    return `<div class="settings-field">
      <label for="f-${key}">${labelWithTip(label, tipKey || key)}</label>
      <input type="number" id="f-${key}" data-key="${key}" value="${escapeAttr(value)}" ${ed ? "" : "disabled"}/>
    </div>`;
  }
  function boolField(key, label, value, ed, tipKey) {
    const on =
      String(value).toLowerCase() === "true" || value === "1" || value === true;
    return `<div class="settings-field">
      <label class="check-row">
        <input type="checkbox" data-key="${key}" data-bool="1" ${on ? "checked" : ""} ${ed ? "" : "disabled"}/>
        ${labelWithTip(label, tipKey || key)}
      </label>
    </div>`;
  }
  function selectField(key, label, value, opts, ed, tipKey) {
    const o = opts
      .map(
        (x) =>
          `<option value="${x}" ${x === value ? "selected" : ""}>${x}</option>`
      )
      .join("");
    return `<div class="settings-field">
      <label>${labelWithTip(label, tipKey || key)}</label>
      <select data-key="${key}" ${ed ? "" : "disabled"}>${o}</select>
    </div>`;
  }
  function textArea(key, label, value, ed, tipKey) {
    return `<div class="settings-field">
      <label>${labelWithTip(label, tipKey || key)}</label>
      <textarea data-key="${key}" ${ed ? "" : "disabled"}>${escapeHtml(value)}</textarea>
    </div>`;
  }
  function escapeHtml(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }
  function escapeAttr(s) {
    return escapeHtml(s).replace(/'/g, "&#39;");
  }

  function bindFields(editable) {
    if (!editable) return;
    els.pane.querySelectorAll("[data-key]").forEach((el) => {
      const key = el.getAttribute("data-key");
      const handler = () => {
        if (el.type === "checkbox" || el.getAttribute("data-bool")) {
          setVal(key, el.checked ? "true" : "false");
        } else {
          setVal(key, el.value);
        }
      };
      el.addEventListener("input", handler);
      el.addEventListener("change", handler);
    });
  }

  function toolHoverTitle(h) {
    const lines = [];
    if (h.ok) lines.push("Connected · Marble owns process lifecycle for stdio servers.");
    else if (h.error) lines.push("Error: " + h.error);
    const names = h.marble_names || h.tool_names || [];
    if (names.length) {
      lines.push("Tools (" + names.length + "):");
      names.forEach((n) => lines.push("  • " + n));
    } else if (h.ok) {
      lines.push("No tools advertised by this server.");
    }
    if (h.resources) lines.push("Resources: " + h.resources);
    if (h.prompts) lines.push("Prompts: " + h.prompts);
    return lines.join("\n");
  }

  async function renderMCP() {
    els.pane.innerHTML = `<h3>MCP</h3><p class="hint">Loading…</p>`;
    try {
      const m = await api("/api/settings/mcp");
      if (!mcpDraft || !mcpDirty) {
        mcpDraft = m.config || { mcpServers: {} };
        if (!mcpDraft.mcpServers) mcpDraft.mcpServers = {};
      }
      const healthBy = {};
      const stList = (m.health && m.health.mcp_server_status) || [];
      if (Array.isArray(stList)) {
        stList.forEach((h) => {
          if (h && h.name) healthBy[h.name] = h;
        });
      }

      let html = `<h3>MCP / Integrations</h3>
        <p class="hint">Config: <code>${escapeHtml(m.path || "")}</code>${
          m.disabled ? " · <strong>disabled by CLI</strong>" : ""
        }. Secrets use \${ENV}. Save reloads connections.</p>
        <p class="hint"><strong>stdio</strong> servers (<code>command</code>+args): Marble <em>spawns and owns</em> the child process (start on connect, SIGTERM/KILL on shutdown or reload). <strong>HTTP</strong> servers: no local PID — Marble only opens the URL.</p>`;
      const names = Object.keys(mcpDraft.mcpServers || {}).sort();
      if (!names.length) {
        html += `<p class="hint">No servers configured. Use Add server or copy adr/mcp.json.example.</p>`;
      }
      for (const name of names) {
        const sc = mcpDraft.mcpServers[name] || {};
        const h = healthBy[name] || {};
        const ok = h.ok === true;
        const en = sc.enabled === false ? false : true;
        const kind =
          sc.url && String(sc.url).trim()
            ? "http"
            : sc.command
              ? "stdio"
              : "—";
        const hover = toolHoverTitle(h);
        const statusText = ok
          ? `ok · tools ${h.tools != null ? h.tools : "—"}`
          : h.error || "—";
        html += `<div class="mcp-server" data-name="${escapeAttr(name)}">
          <div class="mcp-head">
            <span class="mcp-name">${escapeHtml(name)} <span class="mcp-kind">${escapeHtml(kind)}</span></span>
            <span class="mcp-status ${ok ? "ok" : "bad"}" data-tip="${escapeAttr(hover)}" tabindex="0">${escapeHtml(statusText)}</span>
          </div>
          ${
            ok && (h.marble_names || []).length
              ? `<details class="mcp-tools"><summary>Tools (${h.marble_names.length})</summary><ul>${h.marble_names
                  .map((n) => `<li><code>${escapeHtml(n)}</code></li>`)
                  .join("")}</ul></details>`
              : ""
          }
          <div class="settings-field"><label class="check-row"><input type="checkbox" data-mcp="enabled" data-name="${escapeAttr(name)}" ${en ? "checked" : ""} ${m.disabled ? "disabled" : ""}/> ${labelWithTip("Enabled", "mcp_enabled")}</label></div>
          <div class="settings-field"><label>${labelWithTip("Command (stdio)", "mcp_command")}</label><input type="text" data-mcp="command" data-name="${escapeAttr(name)}" value="${escapeAttr(sc.command || "")}" ${m.disabled ? "disabled" : ""} placeholder="npx"/></div>
          <div class="settings-field"><label>${labelWithTip("Args (JSON array)", "mcp_args")}</label><input type="text" data-mcp="args" data-name="${escapeAttr(name)}" value="${escapeAttr(JSON.stringify(sc.args || []))}" ${m.disabled ? "disabled" : ""} placeholder='["-y","tavily-mcp@latest"]'/></div>
          <div class="settings-field"><label>${labelWithTip("URL (HTTP MCP)", "mcp_url")}</label><input type="text" data-mcp="url" data-name="${escapeAttr(name)}" value="${escapeAttr(sc.url || "")}" ${m.disabled ? "disabled" : ""} placeholder="https://…/mcp"/></div>
          <div class="settings-field"><label>${labelWithTip("Env (JSON, ${VAR})", "mcp_env")}</label><textarea data-mcp="env" data-name="${escapeAttr(name)}" ${m.disabled ? "disabled" : ""}>${escapeHtml(JSON.stringify(sc.env || {}, null, 2))}</textarea></div>
          <div class="mcp-actions">
            <button type="button" class="btn-sm danger" data-mcp-del="${escapeAttr(name)}" ${m.disabled ? "disabled" : ""}>Remove</button>
          </div>
        </div>`;
      }
      html += `<div class="mcp-actions">
        <button type="button" class="btn-sm" id="mcp-add" ${m.disabled ? "disabled" : ""}>Add server</button>
      </div>`;
      els.pane.innerHTML = html;

      els.pane.querySelectorAll("[data-mcp]").forEach((el) => {
        el.addEventListener("change", () => {
          applyMCPField(el);
          mcpDirty = true;
          setDirty(true);
        });
        el.addEventListener("input", () => {
          applyMCPField(el);
          mcpDirty = true;
          setDirty(true);
        });
      });
      els.pane.querySelectorAll("[data-mcp-del]").forEach((btn) => {
        btn.addEventListener("click", () => {
          const n = btn.getAttribute("data-mcp-del");
          if (!confirm(`Remove MCP server "${n}"?`)) return;
          delete mcpDraft.mcpServers[n];
          mcpDirty = true;
          setDirty(true);
          renderMCP();
        });
      });
      const add = document.getElementById("mcp-add");
      if (add) {
        add.addEventListener("click", () => {
          const n = prompt("Server name (e.g. tavily):");
          if (!n || !n.trim()) return;
          const name = n.trim();
          if (mcpDraft.mcpServers[name]) {
            alert("Already exists");
            return;
          }
          mcpDraft.mcpServers[name] = {
            command: "npx",
            args: ["-y", "tavily-mcp@latest"],
            env: { TAVILY_API_KEY: "${TAVILY_API_KEY}" },
            enabled: true,
          };
          mcpDirty = true;
          setDirty(true);
          renderMCP();
        });
      }
    } catch (e) {
      els.pane.innerHTML = `<h3>MCP</h3><p class="hint">${escapeHtml(e.message)}</p>`;
    }
  }

  function applyMCPField(el) {
    const name = el.getAttribute("data-name");
    const field = el.getAttribute("data-mcp");
    if (!mcpDraft.mcpServers[name]) mcpDraft.mcpServers[name] = {};
    const sc = mcpDraft.mcpServers[name];
    try {
      if (field === "enabled") {
        sc.enabled = el.checked;
      } else if (field === "command") {
        sc.command = el.value;
      } else if (field === "url") {
        sc.url = el.value;
        if (el.value) sc.transport = sc.transport || "http";
      } else if (field === "args") {
        sc.args = JSON.parse(el.value || "[]");
      } else if (field === "env") {
        sc.env = JSON.parse(el.value || "{}");
      }
    } catch (err) {
      sc["__invalid_" + field] = el.value;
    }
  }

  function collectDraftFromDOM() {
    els.pane.querySelectorAll("[data-key]").forEach((el) => {
      const key = el.getAttribute("data-key");
      if (el.type === "checkbox" || el.getAttribute("data-bool")) {
        draft[key] = el.checked ? "true" : "false";
      } else if (
        key === "shell_deny_patterns" ||
        key === "shell_allow_patterns"
      ) {
        draft[key] = el.value;
      } else {
        draft[key] = el.value;
      }
    });
  }

  function shellKeysChanged(updates) {
    return Object.keys(updates).some((k) => k.startsWith("shell_"));
  }

  async function save() {
    if (section === "ui") {
      const a = document.getElementById("pref-show-closed");
      const b = document.getElementById("pref-dotfiles");
      saveUIPrefs({
        show_closed: a ? a.checked : false,
        show_dotfiles: b ? b.checked : true,
      });
      dirty = false;
      setDirty(false);
      alert("UI preferences saved in this browser.");
      return;
    }
    if (section === "mcp") {
      if (!mcpDirty) return;
      if (!confirm("Save MCP config and reload server connections?")) return;
      const fc = { mcpServers: {} };
      for (const [name, sc] of Object.entries(mcpDraft.mcpServers || {})) {
        const clean = { ...sc };
        Object.keys(clean).forEach((k) => {
          if (k.startsWith("__invalid_")) delete clean[k];
        });
        fc.mcpServers[name] = clean;
      }
      await api("/api/settings/mcp", {
        method: "PUT",
        body: JSON.stringify(fc),
      });
      mcpDirty = false;
      setDirty(false);
      data = await api("/api/settings");
      await renderMCP();
      alert("MCP config saved; connections reloaded.");
      return;
    }
    if (!data || !data.editable) {
      alert("Settings not editable (limp mode).");
      return;
    }
    collectDraftFromDOM();
    const updates = { ...draft };
    delete updates.__ui;
    if (!Object.keys(updates).length) {
      setDirty(false);
      return;
    }
    if (shellKeysChanged(updates)) {
      if (
        !confirm(
          "Save shell policy changes? This affects what the agent can run."
        )
      )
        return;
    }
    const res = await api("/api/settings", {
      method: "PUT",
      body: JSON.stringify(updates),
    });
    data.persistent = res.persistent || data.persistent;
    if (res.shell_effective) data.shell_effective = res.shell_effective;
    draft = {};
    dirty = false;
    setDirty(false);
    render();
  }

  async function resetSection() {
    if (!data || !data.editable) return;
    const sec =
      section === "memory" ? "memory" : section === "shell" ? "shell" : "";
    if (!sec) return;
    if (!confirm("Reset " + sec + " settings to factory defaults?")) return;
    if (
      sec === "shell" &&
      !confirm("This will reset shell policy including deny lists. Continue?")
    )
      return;
    const res = await api("/api/settings/reset", {
      method: "POST",
      body: JSON.stringify({ section: sec }),
    });
    data.persistent = res.persistent || data.persistent;
    draft = {};
    dirty = false;
    setDirty(false);
    render();
  }

  async function openModal() {
    els.modal.hidden = false;
    section = "runtime";
    draft = {};
    mcpDraft = null;
    mcpDirty = false;
    dirty = false;
    setDirty(false);
    els.nav.querySelectorAll("button").forEach((b) => {
      b.classList.toggle("sel", b.getAttribute("data-sec") === section);
    });
    try {
      data = await api("/api/settings");
      render();
    } catch (e) {
      els.pane.innerHTML = `<p class="hint">${escapeHtml(e.message)}</p>`;
    }
  }

  function closeModal() {
    if (dirty || mcpDirty) {
      if (!confirm("Discard unsaved settings changes?")) return;
    }
    els.modal.hidden = true;
    draft = {};
    mcpDirty = false;
    dirty = false;
  }

  els.open.addEventListener("click", () =>
    openModal().catch((e) => alert(e.message))
  );
  els.close.addEventListener("click", closeModal);
  // Click dimmed backdrop (outside dialog) → close (implied dismiss)
  els.modal.addEventListener("click", (e) => {
    if (e.target === els.modal) closeModal();
  });
  const settingsDialog = document.getElementById("settings-dialog");
  if (settingsDialog) {
    settingsDialog.addEventListener("click", (e) => e.stopPropagation());
  }
  els.save.addEventListener("click", () =>
    save().catch((e) => alert(e.message))
  );
  els.reset.addEventListener("click", () =>
    resetSection().catch((e) => alert(e.message))
  );
  els.nav.querySelectorAll("button").forEach((b) => {
    b.addEventListener("click", async () => {
      if (dirty || mcpDirty) {
        if (!confirm("Switch section and discard unsaved edits in this form?"))
          return;
        draft = {};
        mcpDirty = false;
        dirty = false;
      }
      section = b.getAttribute("data-sec");
      els.nav
        .querySelectorAll("button")
        .forEach((x) => x.classList.toggle("sel", x === b));
      setDirty(false);
      if (section === "mcp") await renderMCP();
      else render();
    });
  });

  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && !els.modal.hidden) closeModal();
  });

  try {
    const sc = document.getElementById("show-closed");
    const fh = document.getElementById("fs-hidden");
    if (sc && uiPrefs.show_closed != null) sc.checked = !!uiPrefs.show_closed;
    if (fh && uiPrefs.show_dotfiles != null)
      fh.checked = !!uiPrefs.show_dotfiles;
  } catch (_) {}
})();
