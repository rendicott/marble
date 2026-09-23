/* Settings modal (ADR-0007) — tooltips, grouped runtime, MCP tool hover */
(() => {
  const UI_PREFS_KEY = "marble.ui.prefs";
  const THEME_COOKIE = "marble_theme";
  const THEME_IDS = ["dark", "light", "tan"];

  function normalizeTheme(id) {
    return THEME_IDS.indexOf(id) >= 0 ? id : "dark";
  }
  function readThemeCookie() {
    const m = document.cookie.match(/(?:^|; )marble_theme=([^;]*)/);
    return normalizeTheme(m ? decodeURIComponent(m[1]) : "dark");
  }
  function writeThemeCookie(id) {
    const v = normalizeTheme(id);
    let c =
      THEME_COOKIE +
      "=" +
      encodeURIComponent(v) +
      "; Path=/; Max-Age=31536000; SameSite=Lax";
    if (location.protocol === "https:") c += "; Secure";
    document.cookie = c;
  }
  function applyTheme(id) {
    const v = normalizeTheme(id);
    document.documentElement.setAttribute("data-theme", v);
    document.documentElement.style.colorScheme = v === "dark" ? "dark" : "light";
    writeThemeCookie(v);
  }
  function themeSwatchesHtml() {
    const cur = readThemeCookie();
    return (
      `<div class="theme-swatches" role="radiogroup" aria-label="Theme">` +
      THEME_IDS.map((id) => {
        const sel = id === cur;
        const note = id === "dark" ? `<span class="sw-note">default</span>` : "";
        return `<button type="button" class="theme-swatch${sel ? " sel" : ""}" data-theme-choice="${id}" role="radio" aria-checked="${sel ? "true" : "false"}">
        <span class="theme-swatch-preview" aria-hidden="true"><span class="sw-bg"></span><span class="sw-bar"><i></i><i></i><i></i></span></span>
        <span class="theme-swatch-cap">${id}${note}</span>
      </button>`;
      }).join("") +
      `</div>`
    );
  }
  function bindThemeSwatches() {
    els.pane.querySelectorAll(".theme-swatch").forEach((btn) => {
      btn.addEventListener("click", () => {
        const id = btn.getAttribute("data-theme-choice");
        applyTheme(id);
        els.pane.querySelectorAll(".theme-swatch").forEach((b) => {
          const on = b.getAttribute("data-theme-choice") === normalizeTheme(id);
          b.classList.toggle("sel", on);
          b.setAttribute("aria-checked", on ? "true" : "false");
        });
      });
    });
  }

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
    sinks:
      "Turn sinks (ADR-0028): when a session goes idle, mirror the final assistant message to Orb/Slack/ntfy/Discord/webhook/stdout. Secrets are env-var names only.",
    agents:
      "Subprocess presets (ADR-0030): named grok/claude/opencode invocations. Detected binaries can lock a session or route a single send.",
    subprocess_context:
      "What Marble prepends to a grok/claude/opencode prompt. Resolution: this call → session_set_subprocess_context → this preset → this global default. Toggle sources (multi-select); shortcuts replace the whole set. Default is full+memory. Isolated (none) sends only the user prompt.",
    subprocess_context_max:
      "Hard cap on the assembled context block (characters, not tokens). Default 16000. Oversized sources are truncated, then dropped in order: files → memory → full → compact.",
    ctx_full:
      "Full: recent raw transcript (newest first, ~12k chars) plus a digest of anything older (~6k). Use for handoffs that need the conversation. Already includes a compact of older turns — no need to also toggle Compact.",
    ctx_compact:
      "Compact: extractive digest of the session only (~6k chars). Cheaper than Full. Ignored if Full is also on.",
    ctx_memory:
      "Memory: top 5 hits from $MEMORY (session/daily/knowledge) seeded from the prompt plus recent user messages (~4k chars). Good with Full for “what did we decide?”.",
    ctx_read_paths:
      "Files: workspace paths read or written this Marble turn (~1.5k chars). Useful when the child should open the same files; it still has to read them from disk.",
    ctx_default:
      "Shortcut for Full + Memory — the shipped global default. Transcript plus memory hits.",
    ctx_isolated:
      "Shortcut none: inject nothing. The child sees only the user prompt. Use for cheap throwaway runs.",
    ctx_auto:
      "Shortcut auto: Full+Memory if the prompt looks like a handoff (“this session”, “earlier”, a session id, …); otherwise Files+Compact.",
    ctx_inherit:
      "Clear this preset’s override so it uses the global default at the top of Agents.",

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
      "Soft wall-clock of continuous tool rounds before first advisory (not a stop). CLI --soft-wall. Example: 1200 (20m)",
    hard_wall_sec:
      "Hard wall-clock deadline for an entire user turn; ends turn with timeout error. CLI --hard-wall. Example: 2700 (45m)",
    auto_continue_reserve:
      "When remaining max-tool-iters ≤ this, hard-stop and auto schedule_continuation. CLI --auto-continue-reserve. 0 disables. Example: 10",
    anti_repeat_n:
      "ADR-0022: hard-fail after N consecutive identical tool+args. CLI --anti-repeat-n. Default 0 (off) — too crude for poll loops; set 3 to enable",
    stuck_escalate_k:
      "ADR-0022: escalate lock after K consecutive computer_* failures. CLI --stuck-escalate-k. Default 3",
    block_sleep_shell:
      "ADR-0022: hard-reject pure sleep/timeout shell_execute. CLI --block-sleep-shell. Default true",
    eval_mutate_max:
      "ADR-0022: hard error after M mutate browser evals in last 20 tools. CLI --eval-mutate-max. 0=warn only. Default 5",
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

  async function copyText(text, okMsg) {
    try {
      await navigator.clipboard.writeText(text);
      flashBanner(okMsg || "Copied", false);
    } catch {
      // Fallback
      const ta = document.createElement("textarea");
      ta.value = text;
      document.body.appendChild(ta);
      ta.select();
      try {
        document.execCommand("copy");
        flashBanner(okMsg || "Copied", false);
      } catch (e) {
        flashBanner("Copy failed: " + e, true);
      }
      ta.remove();
    }
  }

  function flashBanner(msg, isErr) {
    if (!els.banner) return;
    els.banner.hidden = false;
    els.banner.textContent = msg;
    els.banner.classList.toggle("err", !!isErr);
    clearTimeout(flashBanner._t);
    flashBanner._t = setTimeout(() => {
      if (data && data.mode === "limp") {
        els.banner.textContent =
          "⚠ Limp mode: " + (data.limp_reason || "DB not writable");
        els.banner.classList.remove("err");
      } else {
        els.banner.hidden = true;
        els.banner.textContent = "";
        els.banner.classList.remove("err");
      }
    }, 2200);
  }

  async function renderTTSSection() {
    try {
      const res = await api("/api/settings/tts");
      const cfg = res.config || {};
      const st = res.status || {};
      const path = res.config_path || "—";
      const forced = !!res.forced_off;
      const chip = st.ready
        ? `<span class="settings-chip ok">ready</span>`
        : st.configured
          ? `<span class="settings-chip warn">not ready</span>`
          : st.enabled
            ? `<span class="settings-chip warn">incomplete</span>`
            : `<span class="settings-chip muted">off</span>`;
      const hint = st.ready_hint || st.last_error || "";
      els.pane.innerHTML = `
        <h3>Server-side TTS</h3>
        <p class="hint">ADR-0027 neural narration for Wonderstand / web ▶. Secrets stay in Settings → Secrets (<code>api_key_env</code> name only).</p>
        <div class="settings-group">
          <p class="hint" style="margin-top:0"><strong>Config file:</strong> <code class="mono">${escapeHtml(path)}</code> ${chip}</p>
          <p class="hint">Status: provider <code>${escapeHtml(st.provider || "none")}</code>
            · enabled ${st.enabled ? "yes" : "no"}
            · ready ${st.ready ? "yes" : "no"}
            · voice <code>${escapeHtml(st.default_voice || "(empty)")}</code>
            · cache hits ${st.cache_hits ?? 0}
            · synth ok ${st.synth_calls ?? 0} / err ${st.synth_errors ?? 0}
            ${forced ? " · <strong>forced off by --tts-disable</strong>" : ""}</p>
          ${hint ? `<p class="hint model-editor-err">${escapeHtml(hint)}</p>` : ""}
          <p class="hint">Wonderstand immersion shows <strong>Neural</strong> vs <strong>Phone TTS</strong> in the mute chrome while narrating. Web assistant ▶ uses the same status.</p>
        </div>
        <div class="settings-group">
          <div class="settings-field">
            <label><input type="checkbox" id="tts-enabled" ${cfg.enabled ? "checked" : ""} ${forced ? "disabled" : ""}/> Enabled</label>
          </div>
          <div class="settings-field">
            <label>Provider</label>
            <select id="tts-provider" ${forced ? "disabled" : ""}>
              <option value="none" ${cfg.provider === "none" ? "selected" : ""}>none</option>
              <option value="elevenlabs" ${cfg.provider === "elevenlabs" ? "selected" : ""}>elevenlabs</option>
              <option value="openai" ${cfg.provider === "openai" ? "selected" : ""}>openai</option>
            </select>
          </div>
          <div class="settings-field">
            <label>API key env name</label>
            <input type="text" id="tts-api-key-env" class="mono" value="${escapeAttr(cfg.api_key_env || "")}" ${forced ? "disabled" : ""}/>
          </div>
          <div class="settings-field">
            <label>Default voice</label>
            <input type="text" id="tts-voice" class="mono" value="${escapeAttr(cfg.default_voice || "")}" ${forced ? "disabled" : ""} placeholder="ElevenLabs voice id / OpenAI alloy"/>
          </div>
          <div class="settings-field">
            <label>Default model</label>
            <input type="text" id="tts-model" class="mono" value="${escapeAttr(cfg.default_model || "")}" ${forced ? "disabled" : ""}/>
          </div>
          <div class="settings-field">
            <label><input type="checkbox" id="tts-cache" ${cfg.cache !== false ? "checked" : ""} ${forced ? "disabled" : ""}/> Cache</label>
          </div>
          <div class="settings-field">
            <label><input type="checkbox" id="tts-eager" ${cfg.eager_on_wonderstand_turn ? "checked" : ""} ${forced ? "disabled" : ""}/> Eager fill phase.audio on Wonderstand turns</label>
          </div>
          <div class="settings-field">
            <label>Max chars / request</label>
            <input type="number" id="tts-max-chars" value="${escapeAttr(String(cfg.max_chars_per_request || 4000))}" ${forced ? "disabled" : ""}/>
          </div>
          <button type="button" class="icon-btn" id="tts-save" ${forced ? "disabled" : ""}>Save tts.json</button>
          <p class="hint model-editor-err" id="tts-err" hidden></p>
        </div>
      `;
      const saveBtn = document.getElementById("tts-save");
      if (saveBtn) {
        saveBtn.onclick = async () => {
          const errEl = document.getElementById("tts-err");
          try {
            await api("/api/settings/tts", {
              method: "PUT",
              body: JSON.stringify({
                enabled: !!(document.getElementById("tts-enabled") || {}).checked,
                provider: (document.getElementById("tts-provider") || {}).value || "none",
                api_key_env: (document.getElementById("tts-api-key-env") || {}).value || "",
                default_voice: (document.getElementById("tts-voice") || {}).value || "",
                default_model: (document.getElementById("tts-model") || {}).value || "",
                format: cfg.format || "mp3",
                cache: !!(document.getElementById("tts-cache") || {}).checked,
                max_chars_per_request: parseInt((document.getElementById("tts-max-chars") || {}).value || "4000", 10),
                max_concurrent: cfg.max_concurrent || 2,
                eager_on_wonderstand_turn: !!(document.getElementById("tts-eager") || {}).checked,
              }),
            });
            flashBanner("TTS settings saved", false);
            renderTTSSection();
          } catch (e) {
            if (errEl) {
              errEl.hidden = false;
              errEl.textContent = e.message || String(e);
            }
            flashBanner(String(e.message || e), true);
          }
        };
      }
    } catch (e) {
      els.pane.innerHTML = `<h3>TTS</h3><p class="hint model-editor-err">${escapeHtml(e.message || String(e))}</p>`;
    }
  }

  const SINK_TYPES = [
    { id: "orb", label: "orb — publish to an Orb topic" },
    { id: "webhook", label: "webhook — any HTTP endpoint" },
    { id: "slack", label: "slack — Slack incoming webhook" },
    { id: "discord", label: "discord — Discord webhook" },
    { id: "ntfy", label: "ntfy — ntfy.sh / self-hosted" },
    { id: "stdout", label: "stdout — local test sink" },
  ];
  const SINK_KINDS = ["complete", "error", "stop", "cron", "continuation"];

  function uniqueSinkId(type, sinks) {
    const used = new Set((sinks || []).map((s) => s.id));
    if (!used.has(type)) return type;
    for (let i = 2; i < 100; i++) {
      const id = type + "-" + i;
      if (!used.has(id)) return id;
    }
    return type + "-" + Date.now().toString(36);
  }

  function blankSink(type, sinks) {
    return {
      id: uniqueSinkId(type, sinks),
      type,
      enabled: true,
      deep_link_base: "",
      secret_env: type === "orb" ? "ORB_AK" : type === "slack" ? "SLACK_WEBHOOK_URL" : type === "discord" ? "DISCORD_WEBHOOK_URL" : "",
      filters: {
        kinds: ["complete", "error"],
        skip_empty: true,
        skip_cron: true,
        only_sessions: [],
        skip_sessions: [],
        min_chars: 0,
        min_interval_sec: 0,
      },
      topic_id: "",
      api_base: "",
      title_prefix: type === "orb" ? "marble: " : "",
      url: "",
      method: "POST",
      headers: { "Content-Type": "application/json" },
      template: "{\"text\":{{json .Preview}},\"link\":{{json .DeepLink}}}",
      channel: "",
      username: "marble",
      icon_emoji: "",
      avatar_url: "",
      server: "https://ntfy.sh",
      topic: "",
      priority: 3,
      format: "plain",
    };
  }

  function envDatalist(names, id) {
    const opts = (names || []).map((n) => `<option value="${escapeAttr(n)}"></option>`).join("");
    return `<datalist id="${id}">${opts}</datalist>`;
  }

  async function renderSinksSection() {
    try {
      const res = await api("/api/settings/sinks");
      const cfg = res.config || { sinks: [] };
      const sinks = Array.isArray(cfg.sinks) ? cfg.sinks : [];
      const path = res.config_path || "—";
      const st = res.status || {};
      const hist = res.history || [];
      const envNames = res.env_names || [];
      const fallback = res.fallback_deep_link_base || "";
      const chip = (st.enabled || 0) > 0
        ? `<span class="settings-chip ok">${st.enabled} enabled</span>`
        : `<span class="settings-chip muted">off</span>`;

      const list = sinks
        .map((s, i) => {
          const note = s.validation_note || "";
          const on = !!s.enabled && !note;
          const dot = note ? "warn" : on ? "on" : "";
          const extra = [];
          if (s.type === "orb") extra.push("topic " + (s.topic_id || "—"));
          if (s.secret_env) extra.push("secret " + s.secret_env);
          if (s.type === "ntfy") extra.push("topic " + (s.topic || "—"));
          if (s.type === "stdout") extra.push("format " + (s.format || "plain"));
          if (note) extra.push(note);
          return `<div class="settings-group sink-row" data-idx="${i}">
            <div class="sink-row-main">
              <div><span class="sink-dot ${dot}"></span><code>${escapeHtml(s.id || "")}</code>
                <span class="muted"> type: ${escapeHtml(s.type || "")}</span></div>
              <p class="hint" style="margin:0.25rem 0 0">${escapeHtml(extra.join(" · ") || "—")}</p>
            </div>
            <div class="sink-row-actions">
              <button type="button" class="icon-btn sink-edit" data-idx="${i}">Edit</button>
              <button type="button" class="icon-btn sink-test" data-idx="${i}">Test</button>
              <button type="button" class="icon-btn sink-toggle" data-idx="${i}">${s.enabled ? "Disable" : "Enable"}</button>
              <button type="button" class="icon-btn danger sink-del" data-idx="${i}">Delete</button>
            </div>
          </div>`;
        })
        .join("");

      const histLines = hist
        .slice()
        .reverse()
        .slice(0, 20)
        .map((h) => {
          const t = h.at ? new Date(h.at).toISOString().slice(11, 19) : "";
          return `${t} ${h.status || ""} ${h.sink_id || ""} ${h.kind || ""} ${h.reason || h.error || ""}`.trim();
        })
        .join("\n");

      els.pane.innerHTML = `
        <h3>Sinks</h3>
        <p class="hint">Mirror finished turns to external channels (ADR-0028). Secrets stay in Settings → Secrets (env-var <em>names</em> only).</p>
        <div class="settings-group">
          <p class="hint" style="margin-top:0"><strong>Config file:</strong> <code class="mono">${escapeHtml(path)}</code> ${chip}</p>
          <p class="hint">Status: ${st.sinks || 0} configured · ${st.enabled || 0} enabled · delivered ${st.ok ?? 0} · dropped ${st.dropped ?? 0}</p>
          <div class="settings-field">
            <label>Global deep link base <span class="muted">(per-sink can override)</span></label>
            <input type="text" id="sink-global-base" class="mono" value="${escapeAttr(cfg.deep_link_base || "")}" placeholder="${escapeAttr(fallback || "https://host:8080")}"/>
          </div>
          <p class="hint">Fallback origin: <code>${escapeHtml(fallback || "—")}</code> · Orb <code>return_url</code> accepts <code>http://</code> or <code>https://</code>.</p>
          <button type="button" class="icon-btn" id="sink-save-base">Save deep_link_base</button>
        </div>
        <div class="settings-group">
          <h4>Add sink</h4>
          <div class="sink-add-types">
            ${SINK_TYPES.map((t) => `<button type="button" class="icon-btn sink-add" data-type="${t.id}">+ ${escapeHtml(t.id)}</button>`).join("")}
          </div>
        </div>
        ${list || "<p class='hint'>No sinks configured yet.</p>"}
        <div id="sink-editor" class="settings-group" hidden></div>
        <div class="settings-group">
          <h4>Recent deliveries</h4>
          <pre class="sink-history">${escapeHtml(histLines || "(none this process)")}</pre>
        </div>
        ${envDatalist(envNames, "sink-env-names")}
        <p class="hint model-editor-err" id="sink-err" hidden></p>
      `;

      const errEl = () => document.getElementById("sink-err");
      const showErr = (msg) => {
        const el = errEl();
        if (!el) return;
        el.hidden = !msg;
        el.textContent = msg || "";
      };

      async function putConfig(next, okMsg) {
        await api("/api/settings/sinks", {
          method: "PUT",
          body: JSON.stringify(next),
        });
        flashBanner(okMsg || "Sinks saved", false);
        renderSinksSection();
      }

      const currentCfg = () => ({
        deep_link_base: (document.getElementById("sink-global-base") || {}).value || cfg.deep_link_base || "",
        sinks: sinks.slice(),
      });

      const saveBase = document.getElementById("sink-save-base");
      if (saveBase) {
        saveBase.onclick = async () => {
          try {
            const next = currentCfg();
            next.deep_link_base = (document.getElementById("sink-global-base") || {}).value || "";
            await putConfig(next, "Deep link base saved");
          } catch (e) {
            showErr(e.message || String(e));
            flashBanner(String(e.message || e), true);
          }
        };
      }

      els.pane.querySelectorAll(".sink-add").forEach((btn) => {
        btn.onclick = () => {
          const type = btn.getAttribute("data-type") || "webhook";
          openSinkEditor(blankSink(type, sinks), -1);
        };
      });
      els.pane.querySelectorAll(".sink-edit").forEach((btn) => {
        btn.onclick = () => {
          const i = parseInt(btn.getAttribute("data-idx"), 10);
          openSinkEditor(sinks[i], i);
        };
      });
      els.pane.querySelectorAll(".sink-del").forEach((btn) => {
        btn.onclick = async () => {
          const i = parseInt(btn.getAttribute("data-idx"), 10);
          const s = sinks[i];
          if (!s || !confirm("Delete sink " + s.id + "?")) return;
          try {
            const next = currentCfg();
            next.sinks = sinks.filter((_, j) => j !== i);
            await putConfig(next, "Deleted " + s.id);
          } catch (e) {
            showErr(e.message || String(e));
          }
        };
      });
      els.pane.querySelectorAll(".sink-toggle").forEach((btn) => {
        btn.onclick = async () => {
          const i = parseInt(btn.getAttribute("data-idx"), 10);
          try {
            const next = currentCfg();
            next.sinks = sinks.map((s, j) => (j === i ? { ...s, enabled: !s.enabled } : s));
            await putConfig(next, next.sinks[i].enabled ? "Enabled" : "Disabled");
          } catch (e) {
            showErr(e.message || String(e));
          }
        };
      });
      els.pane.querySelectorAll(".sink-test").forEach((btn) => {
        btn.onclick = async () => {
          const i = parseInt(btn.getAttribute("data-idx"), 10);
          const s = sinks[i];
          if (!s) return;
          try {
            const rec = await api("/api/settings/sinks/test", {
              method: "POST",
              body: JSON.stringify({ id: s.id }),
            });
            const ok = rec.status === "ok";
            flashBanner(ok ? "Test ok (" + s.id + ")" : "Test failed: " + (rec.error || rec.status), !ok);
            showErr(ok ? "" : rec.error || rec.status);
          } catch (e) {
            showErr(e.message || String(e));
            flashBanner(String(e.message || e), true);
          }
        };
      });

      function openSinkEditor(spec, idx) {
        const ed = document.getElementById("sink-editor");
        if (!ed) return;
        const f = spec.filters || {};
        const kinds = Array.isArray(f.kinds) ? f.kinds : [];
        const skipEmpty = f.skip_empty !== false;
        const typeFields = () => {
          switch (spec.type) {
            case "orb":
              return `
                <div class="settings-field"><label>Topic id</label>
                  <input type="text" id="se-topic" class="mono" value="${escapeAttr(spec.topic_id || "")}" placeholder="t_…"/></div>
                <div class="settings-field"><label>Secret env</label>
                  <input type="text" id="se-secret" class="mono" list="sink-env-names" value="${escapeAttr(spec.secret_env || "")}"/></div>
                <div class="settings-field"><label>API base</label>
                  <input type="text" id="se-apibase" class="mono" value="${escapeAttr(spec.api_base || "")}" placeholder="https://api.dev.orbnet.app"/></div>
                <div class="settings-field"><label>Title prefix</label>
                  <input type="text" id="se-prefix" value="${escapeAttr(spec.title_prefix || "")}" placeholder="marble: "/></div>`;
            case "webhook":
              return `
                <div class="settings-field"><label>URL</label>
                  <input type="text" id="se-url" class="mono" value="${escapeAttr(spec.url || "")}"/></div>
                <div class="settings-field"><label>Method</label>
                  <select id="se-method">
                    ${["POST", "PUT", "PATCH"].map((m) => `<option ${((spec.method || "POST").toUpperCase() === m) ? "selected" : ""}>${m}</option>`).join("")}
                  </select></div>
                <div class="settings-field"><label>Headers (JSON object)</label>
                  <textarea id="se-headers" class="mono">${escapeHtml(JSON.stringify(spec.headers || { "Content-Type": "application/json" }, null, 2))}</textarea></div>
                <div class="settings-field"><label>Template (Go text/template over TurnEvent)</label>
                  <textarea id="se-template" class="mono">${escapeHtml(spec.template || "")}</textarea></div>
                <div class="settings-field"><label>Secret env <span class="muted">(optional Bearer)</span></label>
                  <input type="text" id="se-secret" class="mono" list="sink-env-names" value="${escapeAttr(spec.secret_env || "")}"/></div>`;
            case "slack":
              return `
                <div class="settings-field"><label>Secret env (webhook URL)</label>
                  <input type="text" id="se-secret" class="mono" list="sink-env-names" value="${escapeAttr(spec.secret_env || "")}"/></div>
                <div class="settings-field"><label>Channel</label>
                  <input type="text" id="se-channel" value="${escapeAttr(spec.channel || "")}" placeholder="#ops"/></div>
                <div class="settings-field"><label>Username</label>
                  <input type="text" id="se-user" value="${escapeAttr(spec.username || "")}"/></div>
                <div class="settings-field"><label>Icon emoji</label>
                  <input type="text" id="se-icon" class="mono" value="${escapeAttr(spec.icon_emoji || "")}" placeholder=":robot_face:"/></div>`;
            case "discord":
              return `
                <div class="settings-field"><label>Secret env (webhook URL)</label>
                  <input type="text" id="se-secret" class="mono" list="sink-env-names" value="${escapeAttr(spec.secret_env || "")}"/></div>
                <div class="settings-field"><label>Username</label>
                  <input type="text" id="se-user" value="${escapeAttr(spec.username || "")}"/></div>
                <div class="settings-field"><label>Avatar URL</label>
                  <input type="text" id="se-avatar" class="mono" value="${escapeAttr(spec.avatar_url || "")}"/></div>`;
            case "ntfy":
              return `
                <div class="settings-field"><label>Server</label>
                  <input type="text" id="se-server" class="mono" value="${escapeAttr(spec.server || "https://ntfy.sh")}"/></div>
                <div class="settings-field"><label>Topic</label>
                  <input type="text" id="se-ntfy-topic" class="mono" value="${escapeAttr(spec.topic || "")}"/></div>
                <div class="settings-field"><label>Priority (1–5)</label>
                  <input type="number" id="se-prio" min="1" max="5" value="${escapeAttr(String(spec.priority || 3))}"/></div>
                <div class="settings-field"><label>Secret env <span class="muted">(optional token)</span></label>
                  <input type="text" id="se-secret" class="mono" list="sink-env-names" value="${escapeAttr(spec.secret_env || "")}"/></div>`;
            case "stdout":
              return `
                <div class="settings-field"><label>Format</label>
                  <select id="se-format">
                    <option value="plain" ${spec.format !== "json" ? "selected" : ""}>plain</option>
                    <option value="json" ${spec.format === "json" ? "selected" : ""}>json</option>
                  </select></div>`;
            default:
              return "";
          }
        };
        ed.hidden = false;
        ed.innerHTML = `
          <h4>${idx < 0 ? "Add" : "Edit"} sink · ${escapeHtml(spec.type)}</h4>
          <div class="settings-field"><label>Id</label>
            <input type="text" id="se-id" class="mono" value="${escapeAttr(spec.id || "")}" ${idx >= 0 ? "readonly" : ""}/></div>
          <div class="settings-field">
            <label class="check-row"><input type="checkbox" id="se-enabled" ${spec.enabled ? "checked" : ""}/> Enabled (global default)</label>
          </div>
          <div class="settings-field"><label>Deep link base <span class="muted">(optional override)</span></label>
            <input type="text" id="se-base" class="mono" value="${escapeAttr(spec.deep_link_base || "")}" placeholder="${escapeAttr(cfg.deep_link_base || fallback || "")}"/></div>
          <h4>Filters</h4>
          <div class="sink-kinds">
            ${SINK_KINDS.map((k) => `<label><input type="checkbox" class="se-kind" value="${k}" ${kinds.length === 0 || kinds.indexOf(k) >= 0 ? "checked" : ""}/> ${k}</label>`).join("")}
          </div>
          <div class="settings-field">
            <label class="check-row"><input type="checkbox" id="se-skip-empty" ${skipEmpty ? "checked" : ""}/> Skip empty</label>
          </div>
          <div class="settings-field">
            <label class="check-row"><input type="checkbox" id="se-skip-cron" ${f.skip_cron ? "checked" : ""}/> Skip cron / continuation</label>
          </div>
          <div class="settings-field"><label>Min chars</label>
            <input type="number" id="se-min-chars" min="0" value="${escapeAttr(String(f.min_chars || 0))}"/></div>
          <div class="settings-field"><label>Coalesce min_interval_sec <span class="muted">(0 = off)</span></label>
            <input type="number" id="se-min-int" min="0" value="${escapeAttr(String(f.min_interval_sec || 0))}"/></div>
          <div class="settings-field"><label>Only sessions <span class="muted">(regex, one per line)</span></label>
            <textarea id="se-only">${escapeHtml((f.only_sessions || []).join("\n"))}</textarea></div>
          <div class="settings-field"><label>Skip sessions <span class="muted">(regex, one per line)</span></label>
            <textarea id="se-skip">${escapeHtml((f.skip_sessions || []).join("\n"))}</textarea></div>
          ${typeFields()}
          <div class="model-editor-actions">
            <button type="button" class="icon-btn" id="se-save">Save sink</button>
            <button type="button" class="icon-btn" id="se-cancel">Cancel</button>
          </div>
        `;
        ed.scrollIntoView({ block: "nearest" });
        document.getElementById("se-cancel").onclick = () => {
          ed.hidden = true;
          ed.innerHTML = "";
        };
        document.getElementById("se-save").onclick = async () => {
          try {
            const kindsSel = Array.from(ed.querySelectorAll(".se-kind:checked")).map((el) => el.value);
            const lines = (id) =>
              String((document.getElementById(id) || {}).value || "")
                .split("\n")
                .map((x) => x.trim())
                .filter(Boolean);
            const nextSpec = {
              ...spec,
              id: ((document.getElementById("se-id") || {}).value || "").trim(),
              enabled: !!(document.getElementById("se-enabled") || {}).checked,
              deep_link_base: ((document.getElementById("se-base") || {}).value || "").trim(),
              secret_env: ((document.getElementById("se-secret") || {}).value || "").trim(),
              filters: {
                kinds: kindsSel.length === SINK_KINDS.length ? [] : kindsSel,
                skip_empty: !!(document.getElementById("se-skip-empty") || {}).checked,
                skip_cron: !!(document.getElementById("se-skip-cron") || {}).checked,
                min_chars: parseInt((document.getElementById("se-min-chars") || {}).value || "0", 10) || 0,
                min_interval_sec: parseInt((document.getElementById("se-min-int") || {}).value || "0", 10) || 0,
                only_sessions: lines("se-only"),
                skip_sessions: lines("se-skip"),
              },
            };
            const val = (id) => ((document.getElementById(id) || {}).value || "").trim();
            if (spec.type === "orb") {
              nextSpec.topic_id = val("se-topic");
              nextSpec.api_base = val("se-apibase");
              nextSpec.title_prefix = (document.getElementById("se-prefix") || {}).value || "";
            }
            if (spec.type === "webhook") {
              nextSpec.url = val("se-url");
              nextSpec.method = val("se-method") || "POST";
              nextSpec.template = (document.getElementById("se-template") || {}).value || "";
              try {
                nextSpec.headers = JSON.parse((document.getElementById("se-headers") || {}).value || "{}");
              } catch {
                throw new Error("headers must be a JSON object");
              }
            }
            if (spec.type === "slack") {
              nextSpec.channel = val("se-channel");
              nextSpec.username = val("se-user");
              nextSpec.icon_emoji = val("se-icon");
            }
            if (spec.type === "discord") {
              nextSpec.username = val("se-user");
              nextSpec.avatar_url = val("se-avatar");
            }
            if (spec.type === "ntfy") {
              nextSpec.server = val("se-server");
              nextSpec.topic = val("se-ntfy-topic");
              nextSpec.priority = parseInt(val("se-prio") || "3", 10) || 3;
            }
            if (spec.type === "stdout") {
              nextSpec.format = val("se-format") || "plain";
            }
            const next = currentCfg();
            if (idx >= 0) next.sinks[idx] = nextSpec;
            else next.sinks.push(nextSpec);
            await putConfig(next, "Saved " + nextSpec.id);
          } catch (e) {
            showErr(e.message || String(e));
            flashBanner(String(e.message || e), true);
          }
        };
      }
    } catch (e) {
      els.pane.innerHTML = `<h3>Sinks</h3><p class="hint model-editor-err">${escapeHtml(e.message || String(e))}</p>`;
    }
  }

  async function renderSecretsSection() {
    try {
      const res = await api("/api/settings/env");
      const path = res.managed_path || "—";
      const entries = res.entries || [];
      const sec = res.security || {};
      const rows = entries
        .map((e) => {
          const name = e.name || "";
          const flags = [];
          if (e.in_managed_file) flags.push("file");
          if (e.in_process) flags.push("process");
          const revealed = !!e._reveal;
          const shadowWarn = e.process_differs
            ? `<p class="hint env-shadow-warn">⚠ Also set in the running process with a <em>different</em> (stale) value. The file value now wins — edits apply live. Restart <code>marble-harness</code> to clear the stale process copy.</p>`
            : "";
          return `<div class="settings-group env-row" data-name="${escapeAttr(name)}">
            <div class="env-row-head">
              <code class="env-name">${escapeHtml(name)}</code>
              <span class="settings-chip muted">${escapeHtml(flags.join(" · ") || "—")}</span>
            </div>
            ${shadowWarn}
            <div class="env-value-row">
              <input type="${revealed ? "text" : "password"}" class="env-value mono" data-name="${escapeAttr(name)}" value="${escapeAttr(e.value || "")}" spellcheck="false" autocomplete="off" />
              <button type="button" class="icon-btn env-toggle" data-name="${escapeAttr(name)}" title="Show / hide">👁</button>
            </div>
            <div class="env-actions">
              <button type="button" class="icon-btn env-save" data-name="${escapeAttr(name)}">Update</button>
              <button type="button" class="icon-btn env-copy-name" data-name="${escapeAttr(name)}" title="Copy name">Copy name</button>
              <button type="button" class="icon-btn env-copy-val" data-name="${escapeAttr(name)}" title="Copy value">Copy value</button>
              <button type="button" class="icon-btn danger env-del" data-name="${escapeAttr(name)}">Delete</button>
            </div>
          </div>`;
        })
        .join("");

      els.pane.innerHTML = `
        <h3 class="env-heading">
          Secrets
          <button type="button" class="icon-btn tip env-docs-btn" id="env-docs-open" title="How env sources work" aria-label="Secrets documentation">?</button>
        </h3>
        <div class="settings-group env-secure-callout">
          <p class="hint" style="margin-top:0">
            Browser → harness API → local file. <strong>Not sent to the model.</strong>
            Catalog/MCP store <em>names</em> only. File mode ${escapeHtml(String(sec.file_mode || "0600"))}.
          </p>
          <p class="hint"><strong>This tab writes:</strong> <code class="env-path">${escapeHtml(path)}</code></p>
          <p class="hint">The <code>file</code> value is authoritative (edits apply live, no restart). A <code>process</code> chip means the name is <em>also</em> set on the running process — tap <strong>?</strong> for load order.</p>
        </div>

        <div class="settings-group">
          <h4>Add secret</h4>
          <div class="settings-field">
            <label>Name</label>
            <input type="text" id="env-new-name" class="mono" placeholder="GEMINI_API_KEY" autocomplete="off" spellcheck="false" />
          </div>
          <div class="settings-field">
            <label>Value</label>
            <input type="password" id="env-new-value" class="mono" placeholder="••••••••" autocomplete="new-password" spellcheck="false" />
          </div>
          <button type="button" class="icon-btn" id="env-add">Add / upsert</button>
        </div>

        <h4>Stored keys (${entries.length})</h4>
        ${rows || "<p class='hint'>No keys in managed file yet.</p>"}
      `;

      const docsBtn = document.getElementById("env-docs-open");
      if (docsBtn) {
        docsBtn.addEventListener("click", () => openEnvDocsModal(path));
      }

      const addBtn = document.getElementById("env-add");
      if (addBtn) {
        addBtn.addEventListener("click", async () => {
          const name = (document.getElementById("env-new-name") || {}).value || "";
          const value = (document.getElementById("env-new-value") || {}).value || "";
          try {
            await api("/api/settings/env", {
              method: "PUT",
              body: JSON.stringify({ name, value }),
            });
            flashBanner("Saved " + name.trim(), false);
            renderSecretsSection();
          } catch (e) {
            flashBanner(String(e.message || e), true);
          }
        });
      }

      els.pane.querySelectorAll(".env-toggle").forEach((btn) => {
        btn.addEventListener("click", () => {
          const row = btn.closest(".env-row");
          const input = row && row.querySelector(".env-value");
          if (!input) return;
          input.type = input.type === "password" ? "text" : "password";
        });
      });
      els.pane.querySelectorAll(".env-save").forEach((btn) => {
        btn.addEventListener("click", async () => {
          const name = btn.getAttribute("data-name");
          const row = btn.closest(".env-row");
          const input = row && row.querySelector(".env-value");
          try {
            await api("/api/settings/env", {
              method: "PUT",
              body: JSON.stringify({ name, value: input ? input.value : "" }),
            });
            flashBanner("Updated " + name, false);
            renderSecretsSection();
          } catch (e) {
            flashBanner(String(e.message || e), true);
          }
        });
      });
      els.pane.querySelectorAll(".env-copy-name").forEach((btn) => {
        btn.addEventListener("click", () => copyText(btn.getAttribute("data-name") || "", "Name copied"));
      });
      els.pane.querySelectorAll(".env-copy-val").forEach((btn) => {
        btn.addEventListener("click", () => {
          const row = btn.closest(".env-row");
          const input = row && row.querySelector(".env-value");
          copyText(input ? input.value : "", "Value copied");
        });
      });
      els.pane.querySelectorAll(".env-del").forEach((btn) => {
        btn.addEventListener("click", async () => {
          const name = btn.getAttribute("data-name");
          if (!confirm(`Delete ${name} from ${path}?`)) return;
          try {
            await api("/api/settings/env?name=" + encodeURIComponent(name), {
              method: "DELETE",
            });
            flashBanner("Deleted " + name, false);
            renderSecretsSection();
          } catch (e) {
            flashBanner(String(e.message || e), true);
          }
        });
      });
    } catch (e) {
      els.pane.innerHTML = `<h3>Secrets</h3><p class="hint err">${escapeHtml(String(e.message || e))}</p>`;
    }
  }

  function openEnvDocsModal(managedPath) {
    let overlay = document.getElementById("env-docs-overlay");
    if (!overlay) {
      overlay = document.createElement("div");
      overlay.id = "env-docs-overlay";
      overlay.className = "model-test-overlay env-docs-overlay";
      overlay.innerHTML = `
        <div class="model-test-dialog env-docs-dialog" role="dialog" aria-labelledby="env-docs-title">
          <div class="model-test-top">
            <div class="model-test-title" id="env-docs-title">Secrets &amp; env sources</div>
            <div class="model-test-actions">
              <button type="button" class="icon-btn" id="env-docs-close" title="Close">×</button>
            </div>
          </div>
          <div class="env-docs-body" id="env-docs-body" tabindex="0"></div>
          <p class="hint model-test-foot muted">Esc / backdrop closes</p>
        </div>`;
      document.body.appendChild(overlay);
      const close = () => {
        overlay.hidden = true;
      };
      overlay.addEventListener("click", (e) => {
        if (e.target === overlay) close();
      });
      overlay.querySelector("#env-docs-close").onclick = close;
      document.addEventListener("keydown", (e) => {
        if (e.key === "Escape" && overlay && !overlay.hidden) close();
      });
    }
    const body = overlay.querySelector("#env-docs-body");
    body.innerHTML = `
      <p class="env-docs-lede">
        Marble resolves API keys and similar secrets by <strong>env var name</strong>
        (e.g. catalog <code>api_key_env=GEMINI_API_KEY</code>). Values live in the
        process environment or <code>$MEMORY/env</code> — never in SQLite, and never in the model transcript.
        This Settings tab is a <strong>model bypass</strong>: browser → HTTP API → file write only.
      </p>

      <h4>Load order (first match wins)</h4>
      <p>When Marble needs a secret, it looks in this order and <strong>stops at the first non-empty value</strong>:</p>
      <ol class="env-docs-ol">
        <li><strong>Managed file</strong> — <code>${escapeHtml(managedPath || "$MEMORY/env")}</code> — <strong>this is what Secrets edits</strong>. Authoritative; re-read live (~2s) so edits apply without restart.</li>
        <li><strong>Process environment</strong> — used only as a <em>fallback</em> for names not present in the managed file (e.g. <code>export</code> before launch, or systemd <code>EnvironmentFile=</code>).</li>
      </ol>
      <pre class="env-docs-pre">1. $MEMORY/env   ← Secrets tab writes here (authoritative, live)
2. process env   ← fallback for names not in the file</pre>

      <h4>Pros / cons</h4>
      <table class="env-docs-table">
        <thead>
          <tr><th>Location</th><th>Pros</th><th>Cons</th></tr>
        </thead>
        <tbody>
          <tr>
            <td><strong>Process</strong><br/><span class="muted">systemd / shell export</span></td>
            <td>Familiar; good for baseline host secrets at boot</td>
            <td>Fallback only — ignored for a name once the managed file defines it; a stale process copy needs a restart to clear</td>
          </tr>
          <tr>
            <td><strong>Managed file</strong><br/><code class="muted">${escapeHtml(managedPath || "$MEMORY/env")}</code></td>
            <td><strong>Recommended</strong>; mode 0600; live re-read; authoritative; edits apply without restart</td>
            <td>Gone if you wipe <code>--memory</code>; readable by anything running as your OS user</td>
          </tr>
        </tbody>
      </table>

      <h4>What the chips mean</h4>
      <ul class="env-docs-ul">
        <li><code>file</code> — key exists in <code>$MEMORY/env</code> (authoritative; what this tab edits and what Marble uses)</li>
        <li><code>process</code> — key is <em>also</em> set on the running process (used only as a fallback when the name is not in the file)</li>
      </ul>

      <h4>Recommendation</h4>
      <p>
        Put secrets in the managed file via this tab (or point systemd
        <code>EnvironmentFile=</code> at the same <code>$MEMORY/env</code> path).
        The managed file is authoritative, so edits here apply live. If a name is also set in
        process env, its stale copy is ignored — restart when convenient to clear it.
      </p>
    `;
    overlay.hidden = false;
    if (body) body.focus();
  }

  async function renderComputersSection(editable) {
    try {
      const res = await api("/api/computers");
      const computers = res.computers || [];
      const rows = computers
        .map((c) => {
          const id = c.id || "";
          const on = c.online ? "online" : "offline";
          return `<div class="settings-group model-row">
            <div class="model-row-head">
              <div class="model-row-title">
                <span class="model-row-name">${escapeHtml(c.display_name || id)}</span>
                <span class="settings-chip">${escapeHtml(on)}</span>
              </div>
              <div class="model-row-actions">
                ${editable ? `<button type="button" class="icon-btn pc-revoke" data-id="${escapeAttr(id)}">Revoke</button>` : ""}
              </div>
            </div>
            <div class="model-row-meta mono muted">
              <span>${escapeHtml(id)}</span>
              <span>${escapeHtml(c.os || "")}</span>
            </div>
          </div>`;
        })
        .join("");
      els.pane.innerHTML = `
        <h3>Computers</h3>
        <p class="hint">Desktop peers (marble-peer). Pair with mutual H-code / P-code. See docs/peer-protocol.md.</p>
        ${rows || "<p class='hint'>No computers paired yet.</p>"}
        <div class="model-list-actions">
          ${editable ? `<button type="button" class="icon-btn" id="pc-pair">Pair computer</button>` : ""}
        </div>
        <div id="pc-pair-panel" class="settings-group" hidden></div>
      `;
      const pairBtn = els.pane.querySelector("#pc-pair");
      const panel = els.pane.querySelector("#pc-pair-panel");
      if (pairBtn && panel) {
        pairBtn.onclick = async () => {
          const start = await api("/api/computers/pair/start", { method: "POST", body: "{}" });
          panel.hidden = false;
          panel.innerHTML = `
            <h4>Pair new computer</h4>
            <p class="hint">On the desktop run:<br>
            <code class="mono">marble-peer pair --harness ${escapeHtml(start.harness_url_hint || "")} --code ${escapeHtml(start.h_code)}</code></p>
            <p>H-code: <strong class="mono">${escapeHtml(start.h_code)}</strong></p>
            <div class="settings-field">
              <label>P-code (from peer)</label>
              <input type="text" id="pc-pcode" class="mono" autocomplete="off" />
            </div>
            <div class="settings-field">
              <label>Display name</label>
              <input type="text" id="pc-name" value="laptop" />
            </div>
            <div class="settings-field">
              <label>Id slug</label>
              <input type="text" id="pc-id" class="mono" value="laptop" />
            </div>
            <button type="button" class="icon-btn" id="pc-confirm">Confirm pair</button>
            <p class="hint model-editor-err" id="pc-err" hidden></p>
          `;
          panel.querySelector("#pc-confirm").onclick = async () => {
            const errEl = panel.querySelector("#pc-err");
            try {
              await api("/api/computers/pair/confirm", {
                method: "POST",
                body: JSON.stringify({
                  pairing_id: start.pairing_id,
                  p_code: panel.querySelector("#pc-pcode").value.trim(),
                  display_name: panel.querySelector("#pc-name").value.trim(),
                  id: panel.querySelector("#pc-id").value.trim(),
                }),
              });
              renderComputersSection(editable);
            } catch (e) {
              errEl.hidden = false;
              errEl.textContent = e.message || String(e);
            }
          };
        };
      }
      els.pane.querySelectorAll(".pc-revoke").forEach((btn) => {
        btn.onclick = async () => {
          const id = btn.getAttribute("data-id");
          if (!confirm("Revoke computer “" + id + "”?")) return;
          await api("/api/computers/" + encodeURIComponent(id), { method: "DELETE" });
          renderComputersSection(editable);
        };
      });
    } catch (e) {
      els.pane.innerHTML = `<h3>Computers</h3><p class="hint model-editor-err">${escapeHtml(e.message || String(e))}</p>`;
    }
  }

  const CONTEXT_SOURCES = [
    { token: "full", label: "Full", tip: "ctx_full" },
    { token: "compact", label: "Compact", tip: "ctx_compact" },
    { token: "memory", label: "Memory", tip: "ctx_memory" },
    { token: "read_paths", label: "Files", tip: "ctx_read_paths" },
  ];
  const CONTEXT_SHORTCUTS = [
    { label: "Default", value: "full,memory", tip: "ctx_default" },
    { label: "Isolated", value: "none", tip: "ctx_isolated" },
    { label: "Auto", value: "auto", tip: "ctx_auto" },
  ];
  const CONTEXT_SOURCE_ORDER = CONTEXT_SOURCES.map((s) => s.token);

  function parseCtxTokens(v) {
    if (v == null) return [];
    const raw = Array.isArray(v) ? v.join(",") : String(v);
    return raw
      .split(/[,\s]+/)
      .map((s) => s.trim().toLowerCase())
      .filter(Boolean);
  }

  function composeCtxValue(tokens) {
    const special = tokens.find((t) => t === "none" || t === "auto");
    if (special) return special;
    const seen = new Set();
    const out = [];
    for (const t of CONTEXT_SOURCE_ORDER) {
      if (tokens.includes(t) && !seen.has(t)) {
        seen.add(t);
        out.push(t);
      }
    }
    for (const t of tokens) {
      if (!seen.has(t) && CONTEXT_SOURCE_ORDER.includes(t)) {
        seen.add(t);
        out.push(t);
      }
    }
    return out.join(",");
  }

  function contextPresetButtons(inputId, current, extraShortcuts) {
    const tokens = parseCtxTokens(current);
    const isNone = tokens.length === 1 && tokens[0] === "none";
    const isAuto = tokens.length === 1 && tokens[0] === "auto";
    const isInherit = tokens.length === 0;
    const shortcuts = (extraShortcuts || []).concat(CONTEXT_SHORTCUTS);
    const shortcutChips = shortcuts
      .map((p) => {
        const val = (p.value || "").trim();
        let sel = false;
        if (val === "" && isInherit) sel = true;
        else if (val === "none" && isNone) sel = true;
        else if (val === "auto" && isAuto) sel = true;
        else if (val && val !== "none" && val !== "auto" && composeCtxValue(parseCtxTokens(val)) === composeCtxValue(tokens) && !isNone && !isAuto && !isInherit) sel = true;
        return `<button type="button" class="ctx-chip ctx-shortcut${sel ? " sel" : ""}" data-ctx-for="${escapeAttr(inputId)}" data-ctx-mode="set" data-ctx-value="${escapeAttr(val)}" data-tip="${escapeAttr(TIPS[p.tip] || p.hint || p.label)}">${escapeHtml(p.label)}</button>`;
      })
      .join("");
    const sourceChips = CONTEXT_SOURCES.map((s) => {
      const on = !isNone && !isAuto && tokens.includes(s.token);
      return `<button type="button" class="ctx-chip ctx-source${on ? " sel" : ""}" data-ctx-for="${escapeAttr(inputId)}" data-ctx-mode="toggle" data-ctx-value="${escapeAttr(s.token)}" aria-pressed="${on ? "true" : "false"}" data-tip="${escapeAttr(TIPS[s.tip] || s.token)}">${escapeHtml(s.label)}</button>`;
    }).join("");
    return `<div class="ctx-presets ctx-shortcuts" role="group" aria-label="Context shortcuts">${shortcutChips}</div>
      <div class="ctx-presets ctx-sources" role="group" aria-label="Context sources (multi-select)">${sourceChips}</div>
      <p class="hint ctx-presets-hint">Hover a chip for what it injects. Toggle sources (multi-select); shortcuts replace the whole set.</p>`;
  }

  function syncCtxChips(input) {
    if (!input) return;
    const id = input.id;
    const tokens = parseCtxTokens(input.value);
    const isNone = tokens.length === 1 && tokens[0] === "none";
    const isAuto = tokens.length === 1 && tokens[0] === "auto";
    const isInherit = tokens.length === 0;
    const composed = composeCtxValue(tokens);
    document.querySelectorAll(`.ctx-source[data-ctx-for="${CSS.escape(id)}"]`).forEach((btn) => {
      const tok = btn.getAttribute("data-ctx-value") || "";
      const on = !isNone && !isAuto && tokens.includes(tok);
      btn.classList.toggle("sel", on);
      btn.setAttribute("aria-pressed", on ? "true" : "false");
    });
    document.querySelectorAll(`.ctx-shortcut[data-ctx-for="${CSS.escape(id)}"]`).forEach((btn) => {
      const val = (btn.getAttribute("data-ctx-value") || "").trim();
      let sel = false;
      if (val === "" && isInherit) sel = true;
      else if (val === "none" && isNone) sel = true;
      else if (val === "auto" && isAuto) sel = true;
      else if (val && val !== "none" && val !== "auto" && composeCtxValue(parseCtxTokens(val)) === composed && !isNone && !isAuto && !isInherit) sel = true;
      btn.classList.toggle("sel", sel);
    });
  }

  function bindContextPresetButtons(root) {
    const scope = root || document;
    scope.querySelectorAll(".ctx-chip").forEach((btn) => {
      btn.addEventListener("click", () => {
        const id = btn.getAttribute("data-ctx-for");
        const input = id ? document.getElementById(id) : null;
        if (!input) return;
        const mode = btn.getAttribute("data-ctx-mode") || "set";
        const val = btn.getAttribute("data-ctx-value") || "";
        if (mode === "toggle") {
          let tokens = parseCtxTokens(input.value).filter((t) => t !== "none" && t !== "auto");
          if (tokens.includes(val)) tokens = tokens.filter((t) => t !== val);
          else tokens.push(val);
          if (val === "full") tokens = tokens.filter((t) => t !== "compact");
          if (val === "compact") tokens = tokens.filter((t) => t !== "full");
          input.value = composeCtxValue(tokens);
        } else {
          input.value = val;
        }
        syncCtxChips(input);
      });
    });
    scope.querySelectorAll("input.mono[id$='-ctx'], input#ag-ctx, input#ae-ctx").forEach((input) => {
      input.addEventListener("input", () => syncCtxChips(input));
    });
  }

  async function renderAgentsSection(editable) {
    try {
      const res = await api("/api/settings/agents");
      const presets = res.presets || [];
      const drivers = res.drivers || ["grok", "claude", "opencode"];
      const caps = res.caps || {};
      const rows = presets
        .map((p) => {
          const det = p.detected
            ? `<span class="settings-chip ok">detected${p.detected_version ? " · " + escapeHtml(p.detected_version) : ""}</span>`
            : `<span class="settings-chip muted">not found</span>`;
          const on = p.enabled !== false;
          return `<div class="settings-group model-row" data-id="${escapeAttr(p.id || "")}">
            <div class="model-row-head">
              <div class="model-row-title">
                <span class="model-row-name">${escapeHtml(p.display_name || p.id)}</span>
                <span class="settings-chip">${escapeHtml(p.driver || "")}</span>
                ${det}
                ${on ? "" : `<span class="settings-chip">disabled</span>`}
              </div>
              <div class="model-row-actions">
                ${editable ? `<button type="button" class="icon-btn agent-edit" data-id="${escapeAttr(p.id)}">Edit</button>` : ""}
                ${editable ? `<button type="button" class="icon-btn agent-del" data-id="${escapeAttr(p.id)}">Delete</button>` : ""}
              </div>
            </div>
            <div class="model-row-meta mono muted">
              <span>${escapeHtml(p.id || "")}</span>
              <span>${escapeHtml(p.command || "")}</span>
              ${p.model ? `<span>${escapeHtml(p.model)}</span>` : ""}
              <span>ctx ${escapeHtml(Array.isArray(p.context) && p.context.length ? p.context.join("+") : "inherit")}</span>
              ${p.detected_path ? `<span>${escapeHtml(p.detected_path)}</span>` : ""}
            </div>
          </div>`;
        })
        .join("");
      const gctx = Array.isArray(caps.context_default) ? caps.context_default.join(",") : "full,memory";
      const gmax = caps.context_max_chars || 16000;
      els.pane.innerHTML = `
        <h3>Agents / Subprocesses</h3>
        <p class="hint">Named local harness CLIs (ADR-0030). Global caps live in <code class="mono">${escapeHtml(caps.config_path || "$MEMORY/agent_process.json")}</code>. Detection is advisory — fire still LookPaths. Max 32 presets.</p>
        <p class="hint">Timeouts: default ${caps.default_timeout_sec || "—"}s · max ${caps.max_timeout_sec || "—"}s · max/session ${caps.max_per_session || "—"}</p>
        <div class="settings-group" id="agent-global-ctx">
          <h4>Default subprocess context ${tip("subprocess_context")}</h4>
          <p class="hint">Used when a preset does not set its own sources, and the session/call does not override. Per-call <code>context=[]</code> / <code>none</code> still isolates a run.</p>
          <div class="settings-field"><label>${labelWithTip("Sources", "subprocess_context")}</label>
            <input type="text" id="ag-ctx" class="mono" value="${escapeAttr(gctx)}" ${editable ? "" : "readonly"}/></div>
          ${contextPresetButtons("ag-ctx", gctx)}
          <div class="settings-field"><label>${labelWithTip("Max chars", "subprocess_context_max")}</label>
            <input type="number" id="ag-ctxmax" value="${escapeAttr(String(gmax))}" ${editable ? "" : "readonly"}/></div>
          ${editable ? `<div class="model-list-actions"><button type="button" class="icon-btn" id="ag-ctx-save">Save default</button></div>` : ""}
        </div>
        ${rows || "<p class='hint'>No presets yet.</p>"}
        <div class="model-list-actions">
          ${editable ? `<button type="button" class="icon-btn" id="agent-add">+ Add preset</button>
          <button type="button" class="icon-btn" id="agent-detect">Detect</button>` : "<p class='hint'>Read-only (limp).</p>"}
        </div>
        <div id="agent-editor" class="settings-group model-editor" hidden></div>
        <p class="hint model-editor-err" id="agent-err" hidden></p>
      `;
      const errEl = () => document.getElementById("agent-err");
      const showErr = (msg) => {
        const el = errEl();
        if (!el) return;
        el.hidden = !msg;
        el.textContent = msg || "";
      };
      const editor = document.getElementById("agent-editor");
      const openEditor = (p, isNew) => {
        if (!editor) return;
        editor.hidden = false;
        const args = Array.isArray(p.default_args) ? p.default_args.join(" ") : "";
        editor.innerHTML = `
          <h4>${isNew ? "Add preset" : "Edit " + escapeHtml(p.id || "")}</h4>
          <div class="settings-field"><label>Id</label>
            <input type="text" id="ae-id" class="mono" value="${escapeAttr(p.id || "")}" ${isNew ? "" : "readonly"}/></div>
          <div class="settings-field"><label>Driver</label>
            <select id="ae-driver">${drivers.map((d) => `<option ${p.driver === d ? "selected" : ""}>${d}</option>`).join("")}</select></div>
          <div class="settings-field"><label>Display name</label>
            <input type="text" id="ae-name" value="${escapeAttr(p.display_name || "")}"/></div>
          <div class="settings-field"><label>Command</label>
            <input type="text" id="ae-cmd" class="mono" value="${escapeAttr(p.command || "")}" placeholder="grok"/></div>
          <div class="settings-field"><label>Model (optional)</label>
            <input type="text" id="ae-model" class="mono" value="${escapeAttr(p.model || "")}"/></div>
          <div class="settings-field"><label>Default args</label>
            <input type="text" id="ae-args" class="mono" value="${escapeAttr(args)}" placeholder="--no-plan --effort medium"/></div>
          <div class="settings-field"><label>Timeout sec (0 = global default)</label>
            <input type="number" id="ae-timeout" value="${escapeAttr(String(p.timeout_sec || 0))}"/></div>
          <div class="settings-field"><label>${labelWithTip("Context sources", "subprocess_context")}</label>
            <input type="text" id="ae-ctx" class="mono" value="${escapeAttr(Array.isArray(p.context) ? p.context.join(",") : "")}" placeholder="empty = inherit global default"/></div>
          ${contextPresetButtons("ae-ctx", Array.isArray(p.context) ? p.context.join(",") : "", [{ label: "Inherit", value: "", tip: "ctx_inherit" }])}
          <div class="settings-field"><label>${labelWithTip("Context max chars", "subprocess_context_max")}</label>
            <input type="number" id="ae-ctxmax" value="${escapeAttr(String(p.context_max_chars || 0))}"/></div>
          <div class="settings-field"><label><input type="checkbox" id="ae-en" ${p.enabled !== false ? "checked" : ""}/> Enabled</label></div>
          <div class="settings-field"><label>Notes</label>
            <input type="text" id="ae-notes" value="${escapeAttr(p.notes || "")}"/></div>
          <div class="model-list-actions">
            <button type="button" class="icon-btn" id="ae-save">Save</button>
            <button type="button" class="icon-btn" id="ae-cancel">Cancel</button>
          </div>
        `;
        bindContextPresetButtons(editor);
        document.getElementById("ae-cancel").onclick = () => {
          editor.hidden = true;
        };
        document.getElementById("ae-save").onclick = async () => {
          const body = {
            id: document.getElementById("ae-id").value.trim(),
            driver: document.getElementById("ae-driver").value,
            display_name: document.getElementById("ae-name").value.trim(),
            command: document.getElementById("ae-cmd").value.trim(),
            model: document.getElementById("ae-model").value.trim(),
            default_args: document.getElementById("ae-args").value.trim(),
            timeout_sec: parseInt(document.getElementById("ae-timeout").value, 10) || 0,
            context: document.getElementById("ae-ctx").value.trim(),
            context_max_chars: parseInt(document.getElementById("ae-ctxmax").value, 10) || 0,
            enabled: document.getElementById("ae-en").checked,
            notes: document.getElementById("ae-notes").value.trim(),
          };
          if (body.context === "") delete body.context;
          try {
            if (isNew) {
              await api("/api/settings/agents/presets", { method: "POST", body: JSON.stringify(body) });
            } else {
              await api("/api/settings/agents/presets/" + encodeURIComponent(body.id), {
                method: "PUT",
                body: JSON.stringify(body),
              });
            }
            flashBanner("Preset saved", false);
            renderAgentsSection(editable);
          } catch (e) {
            showErr(e.message || String(e));
          }
        };
      };
      bindContextPresetButtons(document.getElementById("agent-global-ctx"));
      const gsave = document.getElementById("ag-ctx-save");
      if (gsave) {
        gsave.onclick = async () => {
          const raw = (document.getElementById("ag-ctx").value || "").trim();
          let def = ["full", "memory"];
          if (raw === "none") {
            def = ["none"];
          } else if (raw) {
            def = raw.split(/[,\s]+/).map((s) => s.trim()).filter(Boolean);
          }
          const max = parseInt(document.getElementById("ag-ctxmax").value, 10) || 16000;
          try {
            await api("/api/settings/agents/context", {
              method: "PUT",
              body: JSON.stringify({ default: def, max_chars: max }),
            });
            flashBanner("Global context default saved", false);
            renderAgentsSection(editable);
          } catch (e) {
            showErr(e.message || String(e));
          }
        };
      }
      const add = document.getElementById("agent-add");
      if (add) add.onclick = () => openEditor({ driver: "grok", enabled: true, command: "grok" }, true);
      const det = document.getElementById("agent-detect");
      if (det) {
        det.onclick = async () => {
          try {
            await api("/api/settings/agents/detect", { method: "POST", body: "{}" });
            flashBanner("Detection refreshed", false);
            renderAgentsSection(editable);
          } catch (e) {
            showErr(e.message || String(e));
          }
        };
      }
      els.pane.querySelectorAll(".agent-edit").forEach((btn) => {
        btn.onclick = () => {
          const p = presets.find((x) => x.id === btn.getAttribute("data-id"));
          if (p) openEditor(p, false);
        };
      });
      els.pane.querySelectorAll(".agent-del").forEach((btn) => {
        btn.onclick = async () => {
          const id = btn.getAttribute("data-id");
          if (!id || !confirm("Delete preset " + id + "?")) return;
          try {
            await api("/api/settings/agents/presets/" + encodeURIComponent(id), { method: "DELETE" });
            flashBanner("Deleted " + id, false);
            renderAgentsSection(editable);
          } catch (e) {
            showErr(e.message || String(e));
          }
        };
      });
    } catch (e) {
      els.pane.innerHTML = `<h3>Agents</h3><p class="hint model-editor-err">${escapeHtml(e.message || String(e))}</p>`;
    }
  }

  async function renderModelsSection(editable) {
    try {
      const res = await api("/api/models");
      const models = res.models || [];
      const envPaths = Array.isArray(res.env_file_paths) ? res.env_file_paths : [];
      const envPathsHint =
        envPaths.length > 0
          ? envPaths.map((p) => `<code class="mono">${escapeHtml(p)}</code>`).join(" or ")
          : `<code class="mono">$MEMORY/env</code>`;
      const rows = models
        .map((m) => {
          const id = m.id || "";
          const ro = !!m.read_only;
          const caps = m.capabilities || {};
          const badge = ro
            ? "process"
            : m.enabled === false
              ? "disabled"
              : "catalog";
          const budget = m.budget != null ? m.budget : "—";
          const keyEnv = (m.api_key_env || "").trim();
          const keyMode = m.api_key_mode || "";
          let keyChip = "";
          if (keyMode === "none" || keyMode === "none_forced" || (!keyEnv && keyMode !== "env")) {
            if (keyMode === "none_forced") keyChip = `<span class="settings-chip">auth none</span>`;
            else if (m.api_key_configured) keyChip = `<span class="settings-chip ok">key ok</span>`;
            else if (keyMode === "inherit" || !keyEnv) keyChip = m.api_key_configured
              ? `<span class="settings-chip ok">key inherit</span>`
              : `<span class="settings-chip muted">key inherit (empty)</span>`;
          } else if (m.api_key_configured) {
            keyChip = `<span class="settings-chip ok" title="${escapeAttr(keyEnv)}">key ok</span>`;
          } else {
            keyChip = `<span class="settings-chip warn" title="${escapeAttr(m.auth_hint || keyEnv + " not loaded")}">key missing</span>`;
          }
          const capBits = [
            caps.tools !== false ? "tools" : null,
            caps.reasoning ? "reasoning" : null,
            caps.images ? "images" : null,
            caps.voice ? "voice" : null,
          ]
            .filter(Boolean)
            .join(" · ");
          const hintLine =
            m.auth_hint && !m.api_key_configured
              ? `<div class="model-row-auth-hint muted">${escapeHtml(m.auth_hint)}</div>`
              : "";
          return `<div class="settings-group model-row" data-id="${escapeAttr(id)}">
            <div class="model-row-head">
              <div class="model-row-title">
                <span class="model-row-name">${escapeHtml(m.display_name || id)}</span>
                <span class="settings-chip">${escapeHtml(badge)}</span>
                ${m.kind === "image" ? `<span class="settings-chip">image</span>` : ""}
                ${keyChip}
              </div>
              <div class="model-row-actions">
                ${editable ? `<button type="button" class="icon-btn model-copy" data-id="${escapeAttr(id)}" title="Open Add model prefilled from this entry">Copy to new</button>` : ""}
                ${ro ? "" : `<button type="button" class="icon-btn model-edit" data-id="${escapeAttr(id)}">Edit</button>`}
                ${ro ? "" : `<button type="button" class="icon-btn model-del" data-id="${escapeAttr(id)}">Delete</button>`}
                <button type="button" class="icon-btn model-test" data-id="${escapeAttr(id)}">Test</button>
              </div>
            </div>
            <div class="model-row-meta mono muted">
              <span>${escapeHtml(id)}</span>
              <span>${escapeHtml(m.model || "")}</span>
              <span>ctx ${escapeHtml(String(m.context_limit || "—"))}</span>
              <span>budget ${escapeHtml(String(budget))}</span>
              ${keyEnv ? `<span>env ${escapeHtml(keyEnv)}</span>` : ""}
              ${capBits ? `<span>${escapeHtml(capBits)}</span>` : ""}
            </div>
            ${hintLine}
          </div>`;
        })
        .join("");
      els.pane.innerHTML = `
        <h3>Models</h3>
        <p class="hint">Process default is always available (CLI — restart to change). Catalog entries are selectable per session and optional on cron. Cost fields are stored for a future spend ADR — Marble does not bill. Max 32 entries.</p>
        <p class="hint">API keys never go in the catalog — only <strong>env var names</strong>. Put <code class="mono">NAME=secret</code> in ${envPathsHint}. Files are re-read automatically (no harness restart for catalog models). Process env (systemd) is checked first.</p>
        ${rows || "<p class='hint'>No catalog entries yet.</p>"}
        <div class="model-list-actions">
          ${editable ? `<button type="button" class="icon-btn" id="model-add">+ Add model</button>` : "<p class='hint'>Read-only (limp).</p>"}
        </div>
        <div id="model-editor" class="settings-group model-editor" hidden></div>
      `;
      const editor = els.pane.querySelector("#model-editor");
      // Prefill Add-model form from process or an existing catalog row (mobile-friendly).
      const cloneAsNew = (src) => {
        const caps = (src && src.capabilities) || {};
        // Prefer configured (catalog) fields; process has only resolved values — copy those so
        // the operator sees real base_url / limits without flipping between screens.
        const base =
          src.base_url_configured != null ? src.base_url_configured : src.base_url || "";
        const reserve =
          src.context_reserve_configured != null
            ? src.context_reserve_configured
            : src.context_reserve != null
              ? src.context_reserve
              : 0;
        const nameBase = (src.display_name || src.id || "model").replace(/\s*\(copy\)\s*$/i, "");
        return {
          id: "",
          display_name: nameBase + " (copy)",
          model: src.model || "",
          base_url: base,
          base_url_configured: base,
          api_key_env: src.api_key_env || "",
          context_limit: src.context_limit || 131072,
          max_output: src.max_output || 8192,
          context_reserve: reserve,
          context_reserve_configured: reserve,
          capabilities: {
            tools: caps.tools !== false,
            reasoning: !!caps.reasoning,
            images: !!caps.images,
            voice: !!caps.voice,
          },
          enabled: true,
          sort_order: src.sort_order || 0,
          notes: src.notes || "",
          cost_input_per_1m: src.cost_input_per_1m != null ? src.cost_input_per_1m : null,
          cost_output_per_1m: src.cost_output_per_1m != null ? src.cost_output_per_1m : null,
          cost_notes: src.cost_notes || "",
          kind: src.kind === "image" ? "image" : "chat",
          _copiedFrom: src.id || "",
        };
      };
      const openEditor = (m) => {
        const row = m || {
          id: "",
          display_name: "",
          model: "",
          base_url: "",
          api_key_env: "",
          context_limit: 131072,
          max_output: 8192,
          context_reserve: 0,
          cap_tools: true,
          cap_reasoning: false,
          cap_images: false,
          cap_voice: false,
          kind: "chat",
          enabled: true,
          sort_order: 0,
          notes: "",
          cost_input_per_1m: null,
          cost_output_per_1m: null,
          cost_notes: "",
        };
        // New when no id (blank Add or Copy to new). Edit always has a catalog id.
        const isNew = !row.id;
        const isCopy = isNew && !!row._copiedFrom;
        const caps = row.capabilities || {
          tools: row.cap_tools !== false,
          reasoning: !!row.cap_reasoning,
          images: !!row.cap_images,
          voice: !!row.cap_voice,
        };
        const kindVal = row.kind === "image" ? "image" : "chat";
        const baseVal = row.base_url_configured != null ? row.base_url_configured : row.base_url || "";
        const resVal =
          row.context_reserve_configured != null
            ? row.context_reserve_configured
            : row.context_reserve || 0;
        editor.hidden = false;
        const title = isCopy ? "Copy to new model" : isNew ? "Add model" : "Edit model";
        const hint = isCopy
          ? "Prefill from <code class=\"mono\">" +
            escapeHtml(row._copiedFrom) +
            "</code>. Choose a new id slug, tweak fields, then Save."
          : isNew
            ? "Create a catalog entry selectable per session and on cron."
            : "Updating <code class=\"mono\">" + escapeHtml(row.id) + "</code>. Id cannot be renamed.";
        editor.innerHTML = `
          <h4>${title}</h4>
          <p class="hint">${hint}</p>

          <div class="settings-field">
            <label for="me-id">Id (slug)</label>
            <input type="text" id="me-id" class="mono" ${isNew ? "" : "disabled"} value="${escapeAttr(row.id || "")}" placeholder="qwen-local" autocomplete="off" />
            <p class="field-help">Lowercase letters, digits, dot, hyphen, underscore (e.g. grok-4.5). Reserved: process.</p>
          </div>
          <div class="settings-field">
            <label for="me-name">Display name</label>
            <input type="text" id="me-name" value="${escapeAttr(row.display_name || "")}" placeholder="Qwen local large" />
          </div>
          <div class="settings-field">
            <label for="me-model">Model string</label>
            <input type="text" id="me-model" class="mono" value="${escapeAttr(row.model || "")}" placeholder="Qwen/Qwen3.5-…" />
            <p class="field-help">Provider model id sent to the OpenAI-compatible API.</p>
          </div>
          <div class="settings-field">
            <label for="me-kind">Model kind</label>
            <select id="me-kind">
              <option value="chat" ${kindVal === "chat" ? "selected" : ""}>chat</option>
              <option value="image" ${kindVal === "image" ? "selected" : ""}>image</option>
            </select>
            <p class="field-help">chat calls <code class="mono">/chat/completions</code>. image is for gpt-image-* (Images API via the generate_image tool) and cannot be the session model.</p>
          </div>

          <h4>Endpoint &amp; auth</h4>
          <div class="settings-field">
            <label for="me-base">Base URL</label>
            <input type="text" id="me-base" class="mono" value="${escapeAttr(baseVal)}" placeholder="(inherit process)" />
            <p class="field-help">Empty inherits process --base-url. Must be an <strong>OpenAI-compatible</strong> root (Marble calls <code class="mono">/chat/completions</code> and <code class="mono">/models</code>). Gemini: <code class="mono">https://generativelanguage.googleapis.com/v1beta/openai</code> — not <code class="mono">/v1beta/interactions</code> (native Google API).</p>
          </div>
          <div class="settings-field">
            <label for="me-keyenv">API key env</label>
            <input type="text" id="me-keyenv" class="mono" value="${escapeAttr(row.api_key_env || "")}" placeholder="(inherit) | none | OPENAI_API_KEY" />
            <p class="field-help">Empty = inherit process key. <code>none</code> = no Authorization. Otherwise env var name(s) (comma-separated), <strong>never the secret</strong>. After Save, if the list shows <em>key missing</em>, append <code class="mono">NAME=…</code> to ${envPathsHint} — Marble re-reads those files (no restart). Systemd-only process env still needs <code class="mono">systemctl --user restart marble-harness</code> if the var is not in a re-read file.</p>
          </div>

          <h4>Context limits</h4>
          <div class="settings-field-grid">
            <div class="settings-field">
              <label for="me-ctx">Context limit</label>
              <input type="number" id="me-ctx" value="${escapeAttr(String(row.context_limit || 131072))}" min="1" />
            </div>
            <div class="settings-field">
              <label for="me-out">Max output</label>
              <input type="number" id="me-out" value="${escapeAttr(String(row.max_output || 8192))}" min="1" />
            </div>
            <div class="settings-field">
              <label for="me-res">Reserve</label>
              <input type="number" id="me-res" value="${escapeAttr(String(resVal))}" min="0" />
              <p class="field-help">0 = inherit process --context-reserve.</p>
            </div>
            <div class="settings-field">
              <label for="me-sort">Sort order</label>
              <input type="number" id="me-sort" value="${escapeAttr(String(row.sort_order || 0))}" />
            </div>
          </div>

          <h4>Capabilities</h4>
          <div class="settings-field model-caps">
            <label class="check-row"><input type="checkbox" id="me-tools" ${caps.tools !== false ? "checked" : ""}/> Tools (uncheck only for non-agent endpoints)</label>
            <label class="check-row"><input type="checkbox" id="me-reason" ${caps.reasoning ? "checked" : ""}/> Reasoning</label>
            <label class="check-row"><input type="checkbox" id="me-img" ${caps.images ? "checked" : ""}/> Images</label>
            <label class="check-row"><input type="checkbox" id="me-voice" ${caps.voice ? "checked" : ""}/> Voice</label>
            <label class="check-row"><input type="checkbox" id="me-en" ${row.enabled !== false ? "checked" : ""}/> Enabled</label>
          </div>

          <h4>Cost metadata <span class="muted" style="font-weight:500;text-transform:none;letter-spacing:0">(optional)</span></h4>
          <div class="settings-field-grid">
            <div class="settings-field">
              <label for="me-cin">Input $/1M tokens</label>
              <input type="number" step="0.0001" min="0" id="me-cin" value="${escapeAttr(row.cost_input_per_1m != null ? String(row.cost_input_per_1m) : "")}" placeholder="—" />
            </div>
            <div class="settings-field">
              <label for="me-cout">Output $/1M tokens</label>
              <input type="number" step="0.0001" min="0" id="me-cout" value="${escapeAttr(row.cost_output_per_1m != null ? String(row.cost_output_per_1m) : "")}" placeholder="—" />
            </div>
          </div>
          <div class="settings-field">
            <label for="me-cnotes">Cost notes</label>
            <input type="text" id="me-cnotes" value="${escapeAttr(row.cost_notes || "")}" placeholder="e.g. OpenRouter list price" />
          </div>
          <div class="settings-field">
            <label for="me-notes">Notes</label>
            <textarea id="me-notes" rows="2">${escapeHtml(row.notes || "")}</textarea>
          </div>

          <div class="model-editor-actions">
            <button type="button" class="icon-btn" id="me-save">Save</button>
            <button type="button" class="icon-btn" id="me-cancel">Cancel</button>
          </div>
          <p class="hint model-editor-err" id="me-err" hidden></p>
        `;
        const applyModelKind = () => {
          const kindEl = editor.querySelector("#me-kind");
          const image = !!(kindEl && kindEl.value === "image");
          const hide = (sel, asLabel) => {
            const el = editor.querySelector(sel);
            if (!el) return;
            const box = asLabel ? el.closest("label") : el.closest(".settings-field");
            if (box) box.hidden = image;
          };
          hide("#me-tools", true);
          hide("#me-reason", true);
          hide("#me-ctx", false);
          hide("#me-out", false);
        };
        const kindEl = editor.querySelector("#me-kind");
        if (kindEl) {
          kindEl.onchange = applyModelKind;
          applyModelKind();
        }
        editor.scrollIntoView({ block: "nearest", behavior: "smooth" });
        if (isNew) {
          const idInput = editor.querySelector("#me-id");
          if (idInput) {
            // Mobile: land on the only field that must be typed fresh.
            setTimeout(() => idInput.focus(), 50);
          }
        }
        editor.querySelector("#me-cancel").onclick = () => {
          editor.hidden = true;
          editor.innerHTML = "";
        };
        editor.querySelector("#me-save").onclick = async () => {
          const errEl = editor.querySelector("#me-err");
          if (errEl) {
            errEl.hidden = true;
            errEl.textContent = "";
          }
          const kindPick = editor.querySelector("#me-kind");
          const kind = kindPick && kindPick.value === "image" ? "image" : "chat";
          const toolsEl = editor.querySelector("#me-tools");
          const reasonEl = editor.querySelector("#me-reason");
          const ctxEl = editor.querySelector("#me-ctx");
          const outEl = editor.querySelector("#me-out");
          let ctxLim = ctxEl ? parseInt(ctxEl.value, 10) || 0 : row.context_limit || 131072;
          let maxOut = outEl ? parseInt(outEl.value, 10) || 0 : row.max_output || 8192;
          if (kind === "image") {
            if (ctxLim <= 0) ctxLim = 131072;
            if (maxOut <= 0) maxOut = 8192;
          }
          const body = {
            id: editor.querySelector("#me-id").value.trim(),
            display_name: editor.querySelector("#me-name").value.trim(),
            model: editor.querySelector("#me-model").value.trim(),
            kind: kind,
            base_url: editor.querySelector("#me-base").value.trim(),
            api_key_env: editor.querySelector("#me-keyenv").value.trim(),
            context_limit: ctxLim,
            max_output: maxOut,
            context_reserve: parseInt(editor.querySelector("#me-res").value, 10) || 0,
            cap_tools: kind === "image" ? false : toolsEl ? toolsEl.checked : true,
            cap_reasoning: kind === "image" ? false : !!(reasonEl && reasonEl.checked),
            cap_images: editor.querySelector("#me-img").checked,
            cap_voice: editor.querySelector("#me-voice").checked,
            enabled: editor.querySelector("#me-en").checked,
            sort_order: parseInt(editor.querySelector("#me-sort").value, 10) || 0,
            notes: editor.querySelector("#me-notes").value,
            cost_notes: editor.querySelector("#me-cnotes").value,
          };
          const cin = editor.querySelector("#me-cin").value;
          const cout = editor.querySelector("#me-cout").value;
          if (cin !== "") body.cost_input_per_1m = parseFloat(cin);
          if (cout !== "") body.cost_output_per_1m = parseFloat(cout);
          try {
            let saved;
            if (isNew) {
              saved = await api("/api/models", { method: "POST", body: JSON.stringify(body) });
            } else {
              saved = await api("/api/models/" + encodeURIComponent(row.id), {
                method: "PUT",
                body: JSON.stringify(body),
              });
            }
            await renderModelsSection(editable);
            if (saved && saved.auth_hint && !saved.api_key_configured) {
              const banner = document.createElement("p");
              banner.className = "hint model-editor-err";
              banner.style.marginTop = "0.75rem";
              banner.textContent = "Saved, but key not loaded yet: " + saved.auth_hint;
              const listActions = els.pane.querySelector(".model-list-actions");
              if (listActions) listActions.before(banner);
            }
          } catch (e) {
            if (errEl) {
              errEl.hidden = false;
              errEl.textContent = e.message || String(e);
            }
          }
        };
      };
      const add = els.pane.querySelector("#model-add");
      if (add) add.onclick = () => openEditor(null);
      els.pane.querySelectorAll(".model-copy").forEach((btn) => {
        btn.onclick = async () => {
          const id = btn.getAttribute("data-id");
          try {
            const m = await api("/api/models/" + encodeURIComponent(id));
            openEditor(cloneAsNew(m));
          } catch (e) {
            alert(e.message || String(e));
          }
        };
      });
      els.pane.querySelectorAll(".model-edit").forEach((btn) => {
        btn.onclick = async () => {
          const id = btn.getAttribute("data-id");
          const m = await api("/api/models/" + encodeURIComponent(id));
          openEditor(m);
        };
      });
      els.pane.querySelectorAll(".model-del").forEach((btn) => {
        btn.onclick = async () => {
          const id = btn.getAttribute("data-id");
          if (!confirm("Delete model “" + id + "”?")) return;
          await api("/api/models/" + encodeURIComponent(id), { method: "DELETE" });
          renderModelsSection(editable);
        };
      });
      els.pane.querySelectorAll(".model-test").forEach((btn) => {
        btn.onclick = async () => {
          const id = btn.getAttribute("data-id");
          btn.disabled = true;
          try {
            const h = await api("/api/models/" + encodeURIComponent(id) + "/health", {
              method: "POST",
              body: "{}",
            });
            showModelTestResult(h);
          } catch (e) {
            showModelTestResult({ ok: false, id, error: e.message || String(e) });
          } finally {
            btn.disabled = false;
          }
        };
      });
    } catch (e) {
      els.pane.innerHTML = `<h3>Models</h3><p class="hint model-editor-err">${escapeHtml(e.message || String(e))}</p>`;
    }
  }

  /** Selectable / copyable health result (native alert() is not copy-friendly). */
  function showModelTestResult(h) {
    h = h || {};
    const ok = !!h.ok;
    const title = ok ? "Health OK" : "Health failed";
    const lines = [
      title,
      "id: " + (h.id || "—"),
      "model: " + (h.model || "—"),
      "base_url: " + (h.base_url || "—"),
      "api_key_env: " + (h.api_key_env || "—"),
      "api_key_configured: " + String(h.api_key_configured),
      "api_key_mode: " + (h.api_key_mode || "—"),
      "",
      ok ? "(provider /models responded OK)" : h.error || "unknown error",
    ];
    const text = lines.join("\n");
    let overlay = document.getElementById("model-test-overlay");
    if (!overlay) {
      overlay = document.createElement("div");
      overlay.id = "model-test-overlay";
      overlay.className = "model-test-overlay";
      overlay.innerHTML = `
        <div class="model-test-dialog" role="dialog" aria-labelledby="model-test-title">
          <div class="model-test-top">
            <div class="model-test-title" id="model-test-title">Model test</div>
            <div class="model-test-actions">
              <button type="button" class="icon-btn" id="model-test-copy" title="Copy full text">Copy</button>
              <button type="button" class="icon-btn" id="model-test-close" title="Close">×</button>
            </div>
          </div>
          <pre class="model-test-body" id="model-test-body" tabindex="0"></pre>
          <p class="hint model-test-foot muted">Select text or use Copy. Esc / backdrop closes.</p>
        </div>`;
      document.body.appendChild(overlay);
      const close = () => {
        overlay.hidden = true;
      };
      overlay.addEventListener("click", (e) => {
        if (e.target === overlay) close();
      });
      overlay.querySelector("#model-test-close").onclick = close;
      overlay.querySelector("#model-test-copy").onclick = async () => {
        const body = overlay.querySelector("#model-test-body");
        const t = body ? body.textContent : "";
        try {
          await navigator.clipboard.writeText(t);
          const b = overlay.querySelector("#model-test-copy");
          if (b) {
            const prev = b.textContent;
            b.textContent = "Copied";
            setTimeout(() => {
              b.textContent = prev;
            }, 1200);
          }
        } catch {
          // Fallback: select pre contents
          const range = document.createRange();
          range.selectNodeContents(body);
          const sel = window.getSelection();
          sel.removeAllRanges();
          sel.addRange(range);
        }
      };
      document.addEventListener("keydown", (e) => {
        if (e.key === "Escape" && overlay && !overlay.hidden) {
          overlay.hidden = true;
        }
      });
    }
    const titleEl = overlay.querySelector("#model-test-title");
    const bodyEl = overlay.querySelector("#model-test-body");
    if (titleEl) {
      titleEl.textContent = title + (h.id ? " · " + h.id : "");
      titleEl.classList.toggle("ok", ok);
      titleEl.classList.toggle("fail", !ok);
    }
    if (bodyEl) bodyEl.textContent = text;
    overlay.hidden = false;
    if (bodyEl) bodyEl.focus();
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
    if (
      section === "runtime" ||
      section === "agent" ||
      section === "about" ||
      section === "models" ||
      section === "computers" ||
      section === "secrets" ||
      section === "tts" ||
      section === "sinks"
    )
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
          <p class="hint">These talk to your OpenAI-compatible model server — not the Marble web UI. Extra models live under <strong>Models</strong>.</p>
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
    } else if (section === "models") {
      els.pane.innerHTML = `<h3>Models</h3><p class="hint">Loading catalog…</p>`;
      renderModelsSection(editable);
    } else if (section === "agents") {
      els.pane.innerHTML = `<h3>Agents</h3><p class="hint">Loading presets…</p>`;
      renderAgentsSection(editable);
    } else if (section === "computers") {
      els.pane.innerHTML = `<h3>Computers</h3><p class="hint">Loading peers…</p>`;
      renderComputersSection(editable);
    } else if (section === "secrets") {
      els.pane.innerHTML = `<h3>Secrets</h3><p class="hint">Loading…</p>`;
      renderSecretsSection();
    } else if (section === "tts") {
      els.pane.innerHTML = `<h3>TTS</h3><p class="hint">Loading…</p>`;
      renderTTSSection();
    } else if (section === "sinks") {
      els.pane.innerHTML = `<h3>Sinks</h3><p class="hint">Loading…</p>`;
      renderSinksSection();
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
        ${ro("Hard wall (sec)", r.hard_wall_sec, "hard_wall_sec")}
        ${ro("Auto-continue reserve", r.auto_continue_reserve, "auto_continue_reserve")}
        ${ro("Anti-repeat N", r.anti_repeat_n, "anti_repeat_n")}
        ${ro("Stuck escalate K", r.stuck_escalate_k, "stuck_escalate_k")}
        ${ro("Block sleep shell", r.block_sleep_shell, "block_sleep_shell")}
        ${ro("Eval mutate max", r.eval_mutate_max, "eval_mutate_max")}
        ${ro("DB tool_round_soft / hard", `${p.tool_round_soft || "—"} / ${p.tool_round_hard || "—"}`, "tool_round_db")}
      `;
    } else if (section === "mcp") {
      renderMCP();
    } else if (section === "ui") {
      const sc = uiPrefs.show_closed != null ? !!uiPrefs.show_closed : false;
      const df = uiPrefs.show_dotfiles != null ? !!uiPrefs.show_dotfiles : true;
      els.pane.innerHTML = `
        <h3>UI preferences</h3>
        <p class="hint">Theme is stored in a cookie on this browser. Applies immediately. Other prefs save to this browser on Save.</p>
        <div class="settings-field">
          <label>Theme</label>
          ${themeSwatchesHtml()}
        </div>
        <div class="settings-field">
          <label>${labelWithTip("Show closed sessions by default", "show_closed")}</label>
          <label class="check-row"><input type="checkbox" id="pref-show-closed" ${sc ? "checked" : ""}/> Enable</label>
        </div>
        <div class="settings-field">
          <label>${labelWithTip("Show dotfiles in explorer by default", "show_dotfiles")}</label>
          <label class="check-row"><input type="checkbox" id="pref-dotfiles" ${df ? "checked" : ""}/> Enable</label>
        </div>
      `;
      bindThemeSwatches();
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
    // ADR-0032 Q11: UI-section Reset does not change theme.
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
