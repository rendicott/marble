(() => {
  const els = {
    app: document.getElementById("app"),
    list: document.getElementById("session-list"),
    systemList: document.getElementById("system-list"),
    sysToggle: document.getElementById("sys-agents-toggle"),
    sysCount: document.getElementById("sys-agents-count"),
    title: document.getElementById("session-title"),
    transcript: document.getElementById("transcript"),
    input: document.getElementById("input"),
    send: document.getElementById("btn-send"),
    form: document.getElementById("composer"),
    newBtn: document.getElementById("btn-new"),
    sessionsBtn: document.getElementById("btn-sessions"),
    status: document.getElementById("status-pill"),
    model: document.getElementById("model-label"),
    mcpChip: document.getElementById("mcp-chip"),
    health: document.getElementById("health"),
    ctx: document.getElementById("ctx-menu"),
    limp: document.getElementById("limp-banner"),
    showClosed: document.getElementById("show-closed"),
    sessionInfo: document.getElementById("btn-session-info"),
    btnSinks: document.getElementById("btn-sinks"),
    sinksPopover: document.getElementById("sinks-popover"),
    sinksPopList: document.getElementById("sinks-pop-list"),
    sinksPauseAll: document.getElementById("sinks-pause-all"),
    turnCard: document.getElementById("turn-progress"),
    tpTitle: document.getElementById("tp-title"),
    tpBody: document.getElementById("tp-body"),
    tpSteps: document.getElementById("tp-steps"),
    tpStop: document.getElementById("tp-stop"),
    tpRollup: document.getElementById("tp-rollup"),
    tpDetail: document.getElementById("tp-detail"),
    sessionModel: document.getElementById("session-model"),
    toggleTools: document.getElementById("btn-toggle-tools"),
    toggleThinking: document.getElementById("btn-toggle-thinking"),
    reasoningPopover: document.getElementById("reasoning-popover"),
    reasoningSlider: document.getElementById("reasoning-slider"),
    reasoningValue: document.getElementById("reasoning-value"),
    thinkingLive: document.getElementById("thinking-live"),
    attachStage: document.getElementById("attach-stage"),
    attachInput: document.getElementById("attach-input"),
    attachWarn: document.getElementById("attach-warn"),
    attModal: document.getElementById("att-modal"),
    attModalTitle: document.getElementById("att-modal-title"),
    attModalBody: document.getElementById("att-modal-body"),
    attModalClose: document.getElementById("att-modal-close"),
  };

  // ADR-0026: separate expand defaults for tools vs thinking (+ per-session thinking store)
  const LS_TOOLS_EXPANDED = "marble.toolsExpandedDefault";
  const LS_THINK_EXPANDED = "marble.thinkingExpandedDefault";
  const LS_REASONING_EFFORT = "marble.reasoningEffortDefault";
  const REASONING_LEVELS = ["none", "low", "medium", "high"];
  /** @type {Record<string, Array<object>>} */
  let sessionThinking = {};
  /** Live thinking segment: { phaseKey, label, detail, startedAt } */
  let liveThinkSeg = null;
  /** Session reasoning effort: none|low|medium|high|"" */
  let sessionReasoningEffort = "";
  /** @type {Record<string, string>} */
  let sessionSinkOverrides = {};
  /** @type {Array<{id:string,type:string,enabled:boolean,valid?:boolean}>} */
  let sessionSinks = [];
  let sinksPopoverOpen = false;
  let reasoningLongPressTimer = null;
  let reasoningLongPressFired = false;
  let reasoningPopoverOpen = false;
  // Composer send history (shell-style ↑/↓ recall)
  let composeHistory = []; // oldest → newest
  let composeHistIdx = -1; // -1 = live draft; else index into composeHistory
  let composeDraftBackup = "";
  let stagedAttachments = []; // {id,name,mime,kind,size}
  // In-flight stage uploads — Send stays disabled until all complete.
  let attachUploading = 0;
  let pendingUploads = []; // {localId, name} shown while POSTing
  let composerWanted = false; // last setComposerEnabled(on) intent
  let activeCapImages = false;

  let sessions = [];
  let activeId = null;
  let messages = [];
  let es = null;
  let busy = false;
  let ctxSessionId = null;
  // Peer computer_confirm waiting for harness-side Accept/Deny
  let pendingConfirms = {}; // id -> confirm object
  let longPressTimer = null;
  let showClosed = false;
  // Collapsed by default (mobile-friendly); only open when user expanded this browser tab.
  let sysExpanded = sessionStorage.getItem("marble-sys-agents-open") === "1";
  let turnProgress = null;
  let turnExpanded = false;
  // While a turn is live: user can "roll up" the card to free vertical space (esp. mobile).
  // Expanded by default; only reset when turn_started_at changes (new turn).
  let turnRolledUp = false;
  let turnRollKey = ""; // turn_started_at of the turn roll state applies to
  let turnTickTimer = null;
  let lastPlacedTurnStart = null; // re-place card after the user msg when a new turn starts
  let busyPollTimer = null; // backup poll while turn runs (SSE can drop/reconnect)
  let syncInFlight = false;

  function isMobileLayout() {
    return window.matchMedia("(max-width: 720px)").matches;
  }

  function setSessionsOpen(open) {
    els.app.classList.toggle("sessions-open", !!open);
    if (els.sessionsBtn) {
      els.sessionsBtn.setAttribute("aria-expanded", open ? "true" : "false");
    }
  }

  function toggleSessions() {
    setSessionsOpen(!els.app.classList.contains("sessions-open"));
  }

  function setStatus(s) {
    // Prefer enriched pill from turn progress when busy
    if (turnProgress && turnProgress.active && s !== "idle" && s !== "closed" && s !== "error") {
      applyTurnPill(turnProgress);
      return;
    }
    els.status.textContent = s || "idle";
    els.status.className = "pill " + (s || "idle");
  }

  function fmtDur(ms) {
    if (ms == null || ms < 0 || !isFinite(ms)) return "—";
    const s = Math.floor(ms / 1000);
    if (s < 60) return s + "s";
    const m = Math.floor(s / 60);
    const r = s % 60;
    if (m < 60) return m + "m " + r + "s";
    const h = Math.floor(m / 60);
    return h + "h " + (m % 60) + "m";
  }

  function applyTurnPill(p) {
    if (!p) {
      els.status.textContent = "idle";
      els.status.className = "pill idle";
      return;
    }
    const phaseMs = p.phase_started_at
      ? Date.now() - new Date(p.phase_started_at).getTime()
      : 0;
    let text = p.phase || "idle";
    let cls = "idle";
    if (p.active) {
      if (p.phase === "calling_model") {
        text = `model · i${p.iter ?? 0}/${p.iter_hard ?? "?"} · ${fmtDur(phaseMs)}`;
        cls = "calling_model";
      } else if (p.phase === "running_tool") {
        const tn = (p.current_tool && p.current_tool.name) || p.last_tool?.name || "tool";
        text = `tool ${tn} · i${p.iter ?? 0}`;
        cls = "running";
      } else if (p.phase === "stopping") {
        text = "stopping…";
        cls = "stopping";
      } else {
        text = `${p.phase || "running"} · i${p.iter ?? 0}/${p.iter_hard ?? "?"}`;
        cls = "running";
      }
    } else if (p.phase === "error") {
      text = "error";
      cls = "error";
    } else if (p.phase === "complete" || p.phase === "stopping") {
      // after turn, brief then idle is fine
      text = p.phase === "stopping" ? "stopped" : "idle";
      cls = p.phase === "stopping" ? "stopping" : "idle";
    }
    els.status.textContent = text;
    els.status.className = "pill " + cls;
  }

  function isZeroTime(ts) {
    return !ts || ts.startsWith("0001-01-01");
  }

  function hasTurnContent(p) {
    if (!p) return false;
    if (p.active) return true;
    if (p.steps && p.steps.length) return true;
    if (!isZeroTime(p.turn_started_at) && p.phase && p.phase !== "idle") return true;
    return false;
  }

  /** Park the card outside the transcript so innerHTML clears don't destroy it. */
  function detachTurnCard() {
    if (!els.turnCard) return;
    els.turnCard.classList.add("tp-parked");
    els.turnCard.hidden = true;
    if (els.turnCard.parentElement === els.transcript) {
      els.transcript.after(els.turnCard);
    }
    hideThinkingLive();
  }

  /** How close to the bottom counts as "following" the live stream. */
  const TRANSCRIPT_NEAR_BOTTOM_PX = 160;
  /** When true, new events keep the viewport pinned to the bottom. */
  let transcriptPinnedToBottom = true;

  function isTranscriptNearBottom(threshold) {
    const el = els.transcript;
    if (!el) return true;
    const px = threshold == null ? TRANSCRIPT_NEAR_BOTTOM_PX : threshold;
    return el.scrollHeight - el.scrollTop - el.clientHeight < px;
  }

  /** Scroll to bottom only if the user is following (or force=true, e.g. open session). */
  function scrollTranscriptToBottom(force) {
    if (!els.transcript) return;
    if (force || transcriptPinnedToBottom) {
      els.transcript.scrollTop = els.transcript.scrollHeight;
      transcriptPinnedToBottom = true;
    }
  }

  function syncTranscriptPinFromScroll() {
    transcriptPinnedToBottom = isTranscriptNearBottom();
  }

  if (els.transcript) {
    els.transcript.addEventListener("scroll", syncTranscriptPinFromScroll, { passive: true });
  }

  /**
   * Keep the live turn card at the bottom of the transcript while tools run so
   * scroll follows: user → tools… → turn card → final assistant.
   * (Previously the card sat right after the user bubble and scrollIntoView
   * yanked the viewport up every time a tool message was appended.)
   */
  function placeTurnCardInTranscript(p) {
    if (!els.turnCard || !els.transcript) return;
    if (!hasTurnContent(p)) {
      detachTurnCard();
      lastPlacedTurnStart = null;
      return;
    }

    const startKey = isZeroTime(p.turn_started_at) ? "" : p.turn_started_at;
    if (p.active && startKey) lastPlacedTurnStart = startKey;

    if (p.active) {
      // Sink to end so tool bubbles stay above the card.
      els.transcript.appendChild(els.turnCard);
    } else if (els.turnCard.parentElement !== els.transcript) {
      // Completed but detached (e.g. after full re-render): sit before last assistant if any.
      const assistants = els.transcript.querySelectorAll(".bubble.assistant");
      const lastAsst = assistants[assistants.length - 1];
      if (lastAsst) els.transcript.insertBefore(els.turnCard, lastAsst);
      else els.transcript.appendChild(els.turnCard);
    }

    els.turnCard.classList.remove("tp-parked");
    els.turnCard.hidden = false;
    placeThinkingLive();

    // Only follow the stream if the user is already near the bottom.
    if (p.active) scrollTranscriptToBottom(false);
  }

  function turnStartKey(p) {
    if (!p || isZeroTime(p.turn_started_at)) return "";
    return String(p.turn_started_at);
  }

  /** Apply roll-up / completed-collapse via bottom button only (no header chevron). */
  function applyTurnRollUI() {
    if (!els.turnCard) return;
    const live = !!(turnProgress && turnProgress.active);
    const complete = !!(turnProgress && !turnProgress.active && hasTurnContent(turnProgress));
    // Live: roll up hides detail. Complete: turnExpanded controls detail (default collapsed).
    const detailHidden = live ? turnRolledUp : !turnExpanded;
    els.turnCard.classList.toggle("rolled-up", live && turnRolledUp);
    if (els.tpDetail) {
      els.tpDetail.hidden = detailHidden;
    }
    if (!els.tpRollup) return;
    if (!live && !complete) {
      els.tpRollup.hidden = true;
      return;
    }
    els.tpRollup.hidden = false;
    if (live) {
      if (turnRolledUp) {
        els.tpRollup.textContent = "▾ Show details";
        els.tpRollup.title = "Expand turn card details";
        els.tpRollup.setAttribute("aria-expanded", "false");
      } else {
        els.tpRollup.textContent = "▴ Roll up";
        els.tpRollup.title = "Roll up turn card to see tools and thinking above";
        els.tpRollup.setAttribute("aria-expanded", "true");
      }
    } else {
      // Completed turn: bottom control expands/collapses step detail
      if (turnExpanded) {
        els.tpRollup.textContent = "▴ Hide steps";
        els.tpRollup.title = "Collapse turn step log";
        els.tpRollup.setAttribute("aria-expanded", "true");
      } else {
        els.tpRollup.textContent = "▾ Show steps";
        els.tpRollup.title = "Expand turn step log";
        els.tpRollup.setAttribute("aria-expanded", "false");
      }
    }
  }

  function setTurnRolledUp(rolled) {
    turnRolledUp = !!rolled;
    // Remember which live turn this preference is for
    if (turnProgress && turnProgress.active) {
      turnRollKey = turnStartKey(turnProgress) || turnRollKey;
    }
    applyTurnRollUI();
    // Keep title/summary in sync when rolled (compact one-liner)
    if (turnProgress) {
      refreshTurnTitle(turnProgress);
    }
  }

  function refreshTurnTitle(p) {
    if (!els.tpTitle || !p) return;
    const turnMs = !isZeroTime(p.turn_started_at)
      ? (p.turn_ended_at ? new Date(p.turn_ended_at).getTime() : Date.now()) -
        new Date(p.turn_started_at).getTime()
      : 0;
    const cur = p.current_tool;
    const last = p.last_tool;
    const toolName = (cur && cur.name) || (last && last.name) || "";

    if (p.active) {
      if (turnRolledUp) {
        const toolBit = toolName ? " · " + toolName : "";
        els.tpTitle.textContent =
          `Turn · ${p.phase || "…"} · i${p.iter ?? 0}${toolBit} · ${fmtDur(turnMs)}`;
      } else {
        els.tpTitle.textContent = `Turn in progress · ${p.phase || "…"}`;
      }
    } else {
      const nSteps = (p.steps && p.steps.length) || 0;
      els.tpTitle.textContent = `Turn ${p.phase || "done"} · ${fmtDur(turnMs)} · i${p.iter ?? 0} · tools ${p.tool_rounds ?? 0} · ${nSteps} steps`;
    }
  }

  function renderTurnCard(p) {
    const prev = turnProgress;
    const turnProgressWasActive = !!(prev && prev.active);
    turnProgress = p;
    if (!els.turnCard) return;
    if (!hasTurnContent(p)) {
      detachTurnCard();
      stopTurnTick();
      turnRolledUp = false;
      turnRollKey = "";
      applyTurnRollUI();
      return;
    }

    // Only reset roll-up when a *new* turn starts (different turn_started_at).
    const key = turnStartKey(p);
    if (p.active) {
      if (key && key !== turnRollKey) {
        turnRollKey = key;
        turnRolledUp = false; // expanded by default for each new turn
      }
    } else {
      turnRolledUp = false;
      // keep turnRollKey so we don't thrash if a stale complete snapshot arrives
    }

    placeTurnCardInTranscript(p);
    // ADR-0026: live thinking line + commit collapsed history on phase change / idle
    updateLiveThinking(p);

    els.turnCard.classList.toggle("active", !!p.active);
    els.turnCard.classList.toggle("complete", !p.active && (p.phase === "complete" || p.phase === "stopping"));
    els.turnCard.classList.toggle("stopping", p.phase === "stopping" || !!p.stop_requested);

    const turnMs = !isZeroTime(p.turn_started_at)
      ? (p.turn_ended_at ? new Date(p.turn_ended_at).getTime() : Date.now()) -
        new Date(p.turn_started_at).getTime()
      : 0;
    const phaseMs = !isZeroTime(p.phase_started_at)
      ? Date.now() - new Date(p.phase_started_at).getTime()
      : 0;

    const cur = p.current_tool;
    const last = p.last_tool;
    const toolName = (cur && cur.name) || (last && last.name) || "—";
    const args = (cur && cur.args_preview) || (last && last.args_preview) || "";
    const result = (last && last.result_tail) || "";

    refreshTurnTitle(p);

    if (p.active) {
      if (els.tpStop) {
        els.tpStop.hidden = false;
        els.tpStop.disabled = !!p.stop_requested || p.phase === "stopping";
      }
      startTurnTick();
    } else {
      // Auto-collapse detail when the turn ends (re-expand via bottom “Show steps”)
      if (turnProgressWasActive) turnExpanded = false;
      if (els.tpStop) els.tpStop.hidden = true;
      stopTurnTick();
    }

    const ctx =
      p.context_usage != null ? Math.round(p.context_usage * 100) + "%" : "—";
    const lat =
      p.last_model_latency_ms != null ? p.last_model_latency_ms + " ms" : "—";

    // Live: keep filling body while active (CSS/class hides when rolled up).
    // Complete: use turnExpanded for post-turn step inspection.
    const fillDetail = p.active || turnExpanded;

    if (els.tpBody) {
      if (fillDetail) {
        els.tpBody.innerHTML = `
          <div class="tp-row"><span class="tp-k">Phase</span><span class="tp-v">${escHtml(p.phase || "—")} · iter ${p.iter ?? 0} / hard ${p.iter_hard ?? "—"}</span></div>
          <div class="tp-row"><span class="tp-k">Tools</span><span class="tp-v">round ${p.tool_rounds ?? 0} · soft ${p.tool_soft ?? "—"}</span></div>
          <div class="tp-row"><span class="tp-k">Elapsed</span><span class="tp-v">turn ${fmtDur(turnMs)}${p.active ? " · phase " + fmtDur(phaseMs) : ""}</span></div>
          <div class="tp-row"><span class="tp-k">Context</span><span class="tp-v">${escHtml(ctx)} · last model ${escHtml(lat)}</span></div>
          <div class="tp-row"><span class="tp-k">Tool</span><span class="tp-v">${escHtml(toolName)}${args ? " · <code>" + escHtml(args) + "</code>" : ""}</span></div>
          ${result ? `<div class="tp-row"><span class="tp-k">Result</span><span class="tp-v">${escHtml(result)}</span></div>` : ""}
          ${p.message ? `<div class="tp-row"><span class="tp-k">Note</span><span class="tp-v">${escHtml(p.message)}</span></div>` : ""}
        `;
      } else {
        els.tpBody.innerHTML = "";
      }
    }

    if (els.tpSteps) {
      const steps = p.steps || [];
      const showSteps = fillDetail && steps.length > 0 && (p.active || turnExpanded);
      els.tpSteps.hidden = !showSteps;
      if (showSteps) {
        els.tpSteps.innerHTML = steps
          .slice()
          .reverse()
          .map((st) => {
            const t = st.at ? new Date(st.at).toLocaleTimeString() : "";
            const bits = [st.kind || "?"];
            if (st.tool) bits.push(st.tool);
            if (st.latency_ms != null) bits.push(st.latency_ms + "ms");
            if (st.detail) bits.push(st.detail);
            return `<div class="tp-step"><span class="t">${escHtml(t)}</span>${escHtml(bits.join(" · "))}</div>`;
          })
          .join("");
      } else {
        els.tpSteps.innerHTML = "";
      }
    }

    applyTurnRollUI();

    if (p.active) applyTurnPill(p);
    else if (!busy) applyTurnPill(null);
  }

  function escHtml(s) {
    const d = document.createElement("div");
    d.textContent = s == null ? "" : String(s);
    return d.innerHTML;
  }

  function startTurnTick() {
    // Don't restart the interval on every SSE/render — that races with roll-up taps.
    if (turnTickTimer) return;
    turnTickTimer = setInterval(() => {
      if (turnProgress && turnProgress.active) {
        // Light refresh: title + body numbers; preserves turnRolledUp via applyTurnRollUI
        renderTurnCard(turnProgress);
      }
    }, 1000);
  }
  function stopTurnTick() {
    if (turnTickTimer) {
      clearInterval(turnTickTimer);
      turnTickTimer = null;
    }
  }

  async function hydrateProgress(id) {
    if (!id) {
      renderTurnCard(null);
      return;
    }
    try {
      const p = await api(`/api/sessions/${encodeURIComponent(id)}/progress`);
      if (id !== activeId) return;
      turnExpanded = false;
      renderTurnCard(p);
      if (p && p.active) {
        busy = true;
        setComposerEnabled(true);
      }
    } catch {
      /* ignore */
    }
  }

  async function stopTurn() {
    if (!activeId) return;
    try {
      await api(`/api/sessions/${encodeURIComponent(activeId)}/stop`, {
        method: "POST",
        body: "{}",
      });
      if (els.tpStop) els.tpStop.disabled = true;
    } catch (e) {
      alert(e.message || String(e));
    }
  }

  let catalogModels = []; // cached for picker
  let modelPickerBusy = false;

  async function loadCatalogModels() {
    try {
      const data = await api("/api/models");
      catalogModels = data.models || [];
      fillModelSelect(els.sessionModel, true);
      const cronSel = document.getElementById("cron-model");
      if (cronSel) fillModelSelect(cronSel, false);
    } catch {
      catalogModels = [];
    }
  }

  function fillModelSelect(sel, includeProcessEmpty) {
    if (!sel) return;
    const cur = sel.value;
    sel.innerHTML = "";
    const opt0 = document.createElement("option");
    opt0.value = "";
    opt0.textContent = includeProcessEmpty ? "Process default" : "Process / session default";
    sel.appendChild(opt0);
    for (const m of catalogModels) {
      if (m.id === "process") continue;
      if (m.enabled === false) continue;
      const o = document.createElement("option");
      o.value = m.id || "";
      o.textContent = (m.display_name || m.id) + (m.model ? " · " + m.model : "");
      sel.appendChild(o);
    }
    if ([...sel.options].some((o) => o.value === cur)) sel.value = cur;
  }

  function setSessionModelPicker(modelId, disabled) {
    if (!els.sessionModel) return;
    modelPickerBusy = true;
    fillModelSelect(els.sessionModel, true);
    els.sessionModel.value = modelId || "";
    els.sessionModel.disabled = !!disabled;
    modelPickerBusy = false;
  }

  function setComposerEnabled(on) {
    composerWanted = !!on;
    const closed = sessions.find((x) => x.id === activeId)?.status === "closed";
    els.input.disabled = !on || closed;
    // Block Send while attachments are still uploading (avoids race / missing ids).
    const uploading = attachUploading > 0;
    els.send.disabled = !on || busy || closed || uploading;
    if (els.send) {
      els.send.title = uploading
        ? "Wait for attachment upload to finish"
        : busy
          ? "Turn in progress"
          : "";
    }
    if (els.sessionModel) {
      els.sessionModel.disabled = !activeId || !!closed || !!busy;
    }
  }

  async function api(path, opts) {
    opts = opts || {};
    const method = (opts.method || "GET").toUpperCase();
    const headers = {
      "Content-Type": "application/json",
      ...(opts.headers || {}),
    };
    // ADR-0017 CSRF: mutating SPA calls
    if (method !== "GET" && method !== "HEAD") {
      headers["X-Marble-Requested-With"] = "fetch";
    }
    const res = await fetch(path, {
      ...opts,
      headers,
      credentials: "same-origin",
    });
    if (res.status === 401) {
      const next = encodeURIComponent(location.pathname + location.search);
      location.href = "/auth/login?next=" + next;
      throw new Error("auth_required");
    }
    if (!res.ok) {
      const t = await res.text();
      throw new Error(t || res.statusText);
    }
    if (res.status === 204) return null;
    const ct = res.headers.get("content-type") || "";
    if (ct.includes("application/json")) return res.json();
    return res.text();
  }

  let currentUser = null;
  let authMode = "open";

  async function refreshAuth() {
    try {
      const me = await fetch("/auth/me", { credentials: "same-origin" }).then((r) => {
        if (r.status === 401) return { auth_mode: "google", user: null };
        return r.json();
      });
      authMode = me.auth_mode || "open";
      currentUser = me.user || null;
      renderAuthBar();
    } catch {
      /* ignore */
    }
  }

  function renderAuthBar() {
    const el = document.getElementById("auth-bar");
    if (!el) return;
    if (authMode !== "google") {
      el.hidden = true;
      return;
    }
    el.hidden = false;
    if (currentUser) {
      const label = currentUser.name || currentUser.email || "signed in";
      el.innerHTML = `<span class="auth-user" title="${escapeHtml(currentUser.email || "")}">${escapeHtml(label)}</span>
        <button type="button" id="btn-logout" class="icon-btn" title="Sign out">Logout</button>`;
      const btn = document.getElementById("btn-logout");
      if (btn) {
        btn.addEventListener("click", async () => {
          try {
            await api("/auth/logout", { method: "POST", body: "{}" });
          } catch { /* */ }
          location.href = "/auth/login";
        });
      }
    } else {
      el.innerHTML = `<a class="icon-btn" href="/auth/login?next=${encodeURIComponent(location.pathname)}">Sign in</a>`;
    }
  }

  async function refreshHealth() {
    try {
      const h = await api("/api/health");
      els.model.textContent = h.model || "";
      const mem = h.memory_path || h.memory || "";
      const mode = h.mode || "normal";
      const dirty = h.dirty_sessions != null ? ` · dirty ${h.dirty_sessions}` : "";
      const modeTag = mode === "limp" ? " · LIMP" : "";
      els.health.textContent = h.model_ok
        ? `model ok · budget ${h.budget}${dirty}${modeTag}`
        : `model down: ${h.model_error || "error"}`;
      els.health.title = mem ? `memory: ${mem}` : "";
      els.health.style.color = mode === "limp" ? "var(--warn)" : h.model_ok ? "var(--ok)" : "var(--danger)";
      if (els.mcpChip) {
        if (h.mcp_enabled === false) {
          els.mcpChip.textContent = "MCP: off";
          els.mcpChip.title = "MCP disabled";
        } else {
          const n = h.mcp_servers_ok != null ? h.mcp_servers_ok : h.mcp_servers || 0;
          const tools = h.mcp_tools != null ? h.mcp_tools : 0;
          els.mcpChip.textContent = `MCP: ${n} server${n === 1 ? "" : "s"} · ${tools} tools`;
          const st = h.mcp_server_status || [];
          els.mcpChip.title = st.length
            ? st.map((s) => `${s.name}: ${s.ok ? "ok" : s.error || "fail"}`).join("\n")
            : "No MCP servers configured ($MEMORY/mcp.json)";
        }
      }
      if (els.limp) {
        if (mode === "limp") {
          els.limp.hidden = false;
          els.limp.textContent =
            "⚠ Limp mode: " +
            (h.limp_reason || "database schema incompatible") +
            " — chat and Markdown still work; DB writes disabled.";
        } else {
          els.limp.hidden = true;
          els.limp.textContent = "";
        }
      }
    } catch (e) {
      els.health.textContent = "health check failed";
      els.health.style.color = "var(--danger)";
    }
  }

  async function refreshSessions() {
    const data = await api("/api/sessions");
    sessions = data.sessions || [];
    renderSessionList();
  }

  function hideCtx() {
    if (!els.ctx) return;
    els.ctx.classList.remove("open");
    els.ctx.setAttribute("aria-hidden", "true");
    ctxSessionId = null;
  }

  function showCtx(x, y, sessionId) {
    if (!els.ctx) return;
    ctxSessionId = sessionId;
    const s = sessions.find((x) => x.id === sessionId);
    const closeBtn = els.ctx.querySelector('[data-action="close"]');
    if (closeBtn) {
      closeBtn.disabled = !s || s.status === "closed";
      closeBtn.style.display = !s || s.status === "closed" ? "none" : "";
    }
    // Keep menu on-screen (extra height for copy-id item).
    els.ctx.style.left = Math.min(x, window.innerWidth - 200) + "px";
    els.ctx.style.top = Math.min(y, window.innerHeight - 120) + "px";
    els.ctx.classList.add("open");
    els.ctx.setAttribute("aria-hidden", "false");
  }

  async function copySessionId(id) {
    hideCtx();
    if (!id) return;
    try {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        await navigator.clipboard.writeText(id);
      } else {
        const ta = document.createElement("textarea");
        ta.value = id;
        ta.setAttribute("readonly", "");
        ta.style.position = "fixed";
        ta.style.left = "-9999px";
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        document.body.removeChild(ta);
      }
      // brief non-blocking feedback via status pill if present
      if (els.health) {
        const prev = els.health.textContent;
        els.health.textContent = "copied session id " + id;
        setTimeout(() => {
          if (els.health && els.health.textContent.indexOf("copied session id") === 0) {
            els.health.textContent = prev;
          }
        }, 1800);
      }
    } catch (e) {
      prompt("Copy session ID:", id);
    }
  }

  function openSessionInfo(id) {
    hideCtx();
    if (!id || !window.MarbleSessionInfo) return;
    window.MarbleSessionInfo.open(id);
  }

  function setSessionInfoEnabled(on) {
    if (els.sessionInfo) els.sessionInfo.disabled = !on;
    if (els.btnSinks) els.btnSinks.disabled = !on;
  }

  function isCronSession(s) {
    if (!s) return false;
    if (s.cron) return true;
    const t = (s.title || "").trim().toLowerCase();
    return t.startsWith("cron:");
  }

  function sessionRow(s) {
    const btn = document.createElement("button");
    btn.type = "button";
    const cron = isCronSession(s);
    btn.className =
      "sess" +
      (s.id === activeId ? " active" : "") +
      (s.status === "closed" ? " closed" : "") +
      (s.kind === "system" ? " system" : "") +
      (cron ? " cron" : "");
    btn.innerHTML = `<div class="title"></div><div class="meta"></div>`;
    const titleEl = btn.querySelector(".title");
    if (cron) {
      const jobs = Array.isArray(s.cron_jobs) && s.cron_jobs.length
        ? `Cron: ${s.cron_jobs.join(", ")}`
        : "Cron job session";
      titleEl.innerHTML = `<span class="cron-badge" title="${jobs.replace(/"/g, "&quot;")}">🕐</span> `;
      titleEl.appendChild(document.createTextNode(s.title || s.id));
    } else {
      titleEl.textContent = s.title || s.id;
    }
    const when = s.updated_at ? new Date(s.updated_at).toLocaleString() : "";
    const flags = [];
    if (cron) flags.push("cron");
    if (s.status === "closed") flags.push("closed");
    if (s.dirty) flags.push("unsaved");
    if (s.kind === "system") flags.push("system");
    const flagStr = flags.length ? ` · ${flags.join(", ")}` : "";
    btn.querySelector(".meta").textContent = `${s.id} · ${s.message_count || 0} msgs · ${when}${flagStr}`;
    btn.addEventListener("click", () => selectSession(s.id));
    btn.addEventListener("contextmenu", (e) => {
      e.preventDefault();
      showCtx(e.clientX, e.clientY, s.id);
    });
    btn.addEventListener("touchstart", (e) => {
      if (longPressTimer) clearTimeout(longPressTimer);
      const t = e.touches[0];
      longPressTimer = setTimeout(() => {
        showCtx(t.clientX, t.clientY, s.id);
      }, 500);
    }, { passive: true });
    btn.addEventListener("touchend", () => {
      if (longPressTimer) clearTimeout(longPressTimer);
    });
    btn.addEventListener("touchmove", () => {
      if (longPressTimer) clearTimeout(longPressTimer);
    });
    return btn;
  }

  function renderSessionList() {
    els.list.innerHTML = "";
    if (els.systemList) els.systemList.innerHTML = "";
    const visible = sessions.filter((s) => showClosed || s.status !== "closed");
    const users = visible.filter((s) => (s.kind || "user") !== "system");
    const systems = visible.filter((s) => s.kind === "system");

    if (!users.length) {
      const empty = document.createElement("div");
      empty.className = "muted";
      empty.style.padding = "0.9rem";
      empty.textContent = showClosed ? "No user sessions yet" : "No open sessions";
      els.list.appendChild(empty);
    } else {
      for (const s of users) els.list.appendChild(sessionRow(s));
    }

    if (els.sysCount) els.sysCount.textContent = String(systems.length);
    if (els.systemList) {
      if (sysExpanded) {
        for (const s of systems) els.systemList.appendChild(sessionRow(s));
        if (!systems.length) {
          const empty = document.createElement("div");
          empty.className = "muted";
          empty.style.padding = "0.4rem 0.9rem";
          empty.style.fontSize = "0.78rem";
          empty.textContent = "None yet";
          els.systemList.appendChild(empty);
        }
      }
    }
    if (els.sysToggle) {
      els.sysToggle.setAttribute("aria-expanded", sysExpanded ? "true" : "false");
      const label = els.sysToggle.querySelector("span");
      if (label) label.textContent = (sysExpanded ? "▾" : "▸") + " System agents";
    }
  }

  /**
   * @param {{ forceScroll?: boolean }} [opts]
   * forceScroll: jump to bottom (session open / send). Otherwise keep place if user scrolled up.
   */
  function renderTranscript(opts) {
    const forceScroll = !!(opts && opts.forceScroll);
    const pinBefore = transcriptPinnedToBottom;
    const prevTop = els.transcript ? els.transcript.scrollTop : 0;
    const prevHeight = els.transcript ? els.transcript.scrollHeight : 0;

    // Preserve turn card node across transcript rebuilds
    detachTurnCard();
    lastPlacedTurnStart = null;
    els.transcript.innerHTML = "";
    if (!activeId) {
      const d = document.createElement("div");
      d.className = "empty";
      d.textContent = "Create a session to start chatting with the local model.";
      els.transcript.appendChild(d);
      return;
    }
    if (!messages.length) {
      const d = document.createElement("div");
      d.className = "empty";
      d.textContent = "Send a message. History is included on every turn (within context budget).";
      els.transcript.appendChild(d);
      return;
    }
    for (const m of messages) {
      els.transcript.appendChild(bubbleEl(m));
    }
    // Re-inject progress after the latest user turn (tools/assistant already below when live)
    if (turnProgress && hasTurnContent(turnProgress)) {
      renderTurnCard(turnProgress);
    }
    updateDensityButtons();

    if (forceScroll || pinBefore) {
      scrollTranscriptToBottom(true);
    } else if (els.transcript) {
      // Keep the same content under the viewport after a rebuild (anchor by offset).
      const delta = els.transcript.scrollHeight - prevHeight;
      els.transcript.scrollTop = Math.max(0, prevTop + Math.max(0, delta));
      transcriptPinnedToBottom = false;
    }
  }

  function configureMarkdown() {
    if (typeof marked === "undefined" || !marked.parse) return false;
    if (configureMarkdown._done) return true;
    const opts = {
      gfm: true,
      breaks: true, // single newlines → <br> (chat-friendly)
      pedantic: false,
    };
    if (typeof marked.setOptions === "function") {
      marked.setOptions(opts);
    } else if (typeof marked.use === "function") {
      marked.use(opts);
    }
    configureMarkdown._done = true;
    return true;
  }

  /** Render markdown → safe HTML for chat bubbles. */
  function renderMarkdown(src) {
    const text = src == null ? "" : String(src);
    if (!text) return "";
    if (!configureMarkdown()) {
      // Fallback: escape + preserve newlines
      const d = document.createElement("div");
      d.textContent = text;
      return d.innerHTML.replace(/\n/g, "<br>");
    }
    let html;
    try {
      html = marked.parse(text, { async: false });
    } catch (e) {
      const d = document.createElement("div");
      d.textContent = text;
      return d.innerHTML.replace(/\n/g, "<br>");
    }
    if (typeof DOMPurify !== "undefined" && DOMPurify.sanitize) {
      return DOMPurify.sanitize(html, {
        USE_PROFILES: { html: true },
        ADD_ATTR: ["target", "rel"],
      });
    }
    return html;
  }

  function roleUsesMarkdown(role) {
    return role === "user" || role === "assistant" || !role;
  }

  function toolsExpandedDefault() {
    try {
      return localStorage.getItem(LS_TOOLS_EXPANDED) === "1";
    } catch {
      return false;
    }
  }

  function thinkingExpandedDefault() {
    try {
      return localStorage.getItem(LS_THINK_EXPANDED) === "1";
    } catch {
      return false;
    }
  }

  function setToolsExpandedDefault(on) {
    try {
      localStorage.setItem(LS_TOOLS_EXPANDED, on ? "1" : "0");
    } catch {
      /* ignore */
    }
  }

  function setThinkingExpandedDefault(on) {
    try {
      localStorage.setItem(LS_THINK_EXPANDED, on ? "1" : "0");
    } catch {
      /* ignore */
    }
  }

  /** ADR-0026 Q5: same day → time only; else short date+time; ISO in title. */
  function formatMsgTime(iso) {
    if (!iso) return { text: "", title: "" };
    const d = new Date(iso);
    if (isNaN(d.getTime())) return { text: "", title: String(iso) };
    const now = new Date();
    const sameDay =
      d.getFullYear() === now.getFullYear() &&
      d.getMonth() === now.getMonth() &&
      d.getDate() === now.getDate();
    const text = d.toLocaleString(
      undefined,
      sameDay
        ? { hour: "numeric", minute: "2-digit" }
        : { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" }
    );
    return { text, title: d.toISOString() };
  }

  function oneLinePreview(s, n) {
    const max = n || 120;
    const t = String(s || "")
      .replace(/\s+/g, " ")
      .trim();
    if (!t) return "";
    return t.length > max ? t.slice(0, max - 1) + "…" : t;
  }

  function makeTimeEl(iso) {
    const t = formatMsgTime(iso);
    if (!t.text) return null;
    const span = document.createElement("span");
    span.className = "msg-time";
    span.textContent = t.text;
    span.title = t.title;
    return span;
  }

  // Raster images only. SVG is stored as kind=document (never inline image/svg+xml).
  function isRasterImageAtt(a) {
    if (!a) return false;
    const mime = String(a.mime || "").toLowerCase();
    if (mime === "image/svg+xml" || a.kind === "document") return false;
    return a.kind === "image" || mime.startsWith("image/");
  }

  function attachChipsRow(m) {
    if (!m.attachments || !m.attachments.length) return null;
    const row = document.createElement("div");
    row.className = "attach-stage";
    m.attachments.forEach((a) => {
      const chip = document.createElement("span");
      chip.className = "attach-chip";
      chip.setAttribute("data-att-id", a.id || "");
      chip.setAttribute("data-att-name", a.name || "");
      chip.setAttribute("data-att-kind", a.kind || "");
      chip.setAttribute("data-att-mime", a.mime || "");
      const srcBits = [];
      if (a.source_url) srcBits.push("Source: " + a.source_url);
      if (a.credit) srcBits.push(a.credit);
      chip.title = srcBits.length ? srcBits.join(" · ") : "Open attachment";
      const isImg = isRasterImageAtt(a);
      const alt = a.alt || "";
      chip.innerHTML = isImg
        ? `<img src="/api/sessions/${encodeURIComponent(activeId)}/attachments/${encodeURIComponent(a.id)}?inline=1" alt="" /><span class="name"></span>`
        : `📄 <span class="name"></span>`;
      if (isImg) {
        const img = chip.querySelector("img");
        if (img) img.alt = alt;
      }
      chip.querySelector(".name").textContent = a.name || a.id || "file";
      row.appendChild(chip);
    });
    return row;
  }

  function setCollapsibleExpanded(div, expanded) {
    div.classList.toggle("collapsed", !expanded);
    div.classList.toggle("expanded", expanded);
    const body = div.querySelector(".bubble-body");
    if (body) body.hidden = !expanded;
    const chev = div.querySelector(".chev");
    if (chev) chev.textContent = expanded ? "▾" : "▸";
    const preview = div.querySelector(".collapsible-preview");
    if (preview) preview.hidden = !!expanded;
  }

  /** Expand/collapse all rows of a kind: "tool" | "thinking". */
  function setCollapsiblesKind(kind, expanded) {
    if (!els.transcript) return;
    const sel =
      kind === "thinking" ? ".bubble.collapsible.thinking" : ".bubble.collapsible.tool";
    els.transcript.querySelectorAll(sel).forEach((el) => {
      setCollapsibleExpanded(el, expanded);
    });
    if (kind === "thinking") setThinkingExpandedDefault(expanded);
    else setToolsExpandedDefault(expanded);
    paintDensityToggles();
  }

  function reasoningEffortIndex(effort) {
    const e = String(effort || "none").toLowerCase();
    const i = REASONING_LEVELS.indexOf(e);
    return i >= 0 ? i : 0;
  }

  function paintReasoningUI() {
    const idx = reasoningEffortIndex(sessionReasoningEffort || "none");
    const label = REASONING_LEVELS[idx] || "none";
    if (els.reasoningSlider) els.reasoningSlider.value = String(idx);
    if (els.reasoningValue) els.reasoningValue.textContent = label;
    if (els.reasoningPopover) {
      els.reasoningPopover.querySelectorAll(".reasoning-labels span").forEach((sp) => {
        sp.classList.toggle("is-active", sp.getAttribute("data-v") === String(idx));
      });
    }
    if (els.toggleThinking) {
      const has = label !== "none" && !!sessionReasoningEffort;
      els.toggleThinking.classList.toggle("has-effort", has);
      const expandHint = thinkingExpandedDefault()
        ? "Collapse thinking"
        : "Expand thinking";
      els.toggleThinking.title =
        expandHint + " · right-click / long-press: reasoning (" + label + ")";
    }
  }

  function hideReasoningPopover() {
    reasoningPopoverOpen = false;
    if (els.reasoningPopover) els.reasoningPopover.hidden = true;
  }

  function hideSinksPopover() {
    sinksPopoverOpen = false;
    if (els.sinksPopover) els.sinksPopover.hidden = true;
  }

  function sinkEffective(sink) {
    const ov = (sessionSinkOverrides[sink.id] || "").toLowerCase();
    if (ov === "on") return true;
    if (ov === "off") return false;
    return !!sink.enabled;
  }

  function paintSinksPopover() {
    if (!els.sinksPopList) return;
    const sinks = sessionSinks || [];
    if (!sinks.length) {
      els.sinksPopList.innerHTML = `<p class="hint" style="margin:0">No sinks configured. Add one in Settings → Sinks.</p>`;
      if (els.sinksPauseAll) els.sinksPauseAll.textContent = "Pause all sinks";
      return;
    }
    const allOff = sinks.every((s) => sinkEffective(s) === false);
    if (els.sinksPauseAll) {
      els.sinksPauseAll.textContent = allOff ? "Resume all (inherit)" : "Pause all sinks";
    }
    els.sinksPopList.innerHTML = sinks
      .map((s) => {
        const ov = (sessionSinkOverrides[s.id] || "").toLowerCase();
        const mode = ov === "on" || ov === "off" ? ov : "inherit";
        const eff = sinkEffective(s) ? "on" : "off";
        const g = s.enabled ? "on" : "off";
        const note = s.valid === false ? " · invalid config" : "";
        return `<div class="sinks-pop-row">
          <span class="sink-id">${escapeHtml(s.id)}</span>
          <select data-sink-id="${escapeAttr(s.id)}" aria-label="${escapeAttr(s.id)} override">
            <option value="inherit" ${mode === "inherit" ? "selected" : ""}>inherit</option>
            <option value="on" ${mode === "on" ? "selected" : ""}>on</option>
            <option value="off" ${mode === "off" ? "selected" : ""}>off</option>
          </select>
          <span class="sink-eff">${escapeHtml(eff)} · (global: ${escapeHtml(g)})${escapeHtml(note)}</span>
        </div>`;
      })
      .join("");
    els.sinksPopList.querySelectorAll("select[data-sink-id]").forEach((sel) => {
      sel.addEventListener("change", () => {
        const id = sel.getAttribute("data-sink-id");
        const v = sel.value;
        if (v === "inherit") delete sessionSinkOverrides[id];
        else sessionSinkOverrides[id] = v;
        saveSinkOverrides();
      });
    });
  }

  async function saveSinkOverrides() {
    if (!activeId) return;
    const body = { sink_overrides: { ...sessionSinkOverrides } };
    try {
      const data = await api(`/api/sessions/${encodeURIComponent(activeId)}`, {
        method: "PATCH",
        body: JSON.stringify(body),
      });
      const sum = data.session || {};
      sessionSinkOverrides = sum.sink_overrides && typeof sum.sink_overrides === "object" ? { ...sum.sink_overrides } : {};
      paintSinksPopover();
    } catch (e) {
      console.warn("sink_overrides patch failed", e);
    }
  }

  async function showSinksPopover() {
    if (!els.sinksPopover || !activeId) return;
    try {
      const [sess, settings] = await Promise.all([
        api(`/api/sessions/${encodeURIComponent(activeId)}`),
        api("/api/settings/sinks").catch(() => null),
      ]);
      const sum = (sess && sess.session) || {};
      sessionSinkOverrides = sum.sink_overrides && typeof sum.sink_overrides === "object" ? { ...sum.sink_overrides } : {};
      sessionSinks = Array.isArray(sess && sess.sinks)
        ? sess.sinks
        : settings && settings.config && Array.isArray(settings.config.sinks)
          ? settings.config.sinks
          : sessionSinks;
    } catch (e) {
      console.warn("sinks popover load", e);
    }
    paintSinksPopover();
    sinksPopoverOpen = true;
    els.sinksPopover.hidden = false;
  }

  function showReasoningPopover() {
    if (!els.reasoningPopover || !activeId) return;
    paintReasoningUI();
    reasoningPopoverOpen = true;
    els.reasoningPopover.hidden = false;
  }

  async function applyReasoningEffort(level) {
    const norm = REASONING_LEVELS.includes(level) ? level : "none";
    sessionReasoningEffort = norm;
    try {
      localStorage.setItem(LS_REASONING_EFFORT, norm);
    } catch {
      /* ignore */
    }
    paintReasoningUI();
    if (!activeId) return;
    try {
      await api(`/api/sessions/${encodeURIComponent(activeId)}`, {
        method: "PATCH",
        body: JSON.stringify({ reasoning_effort: norm }),
      });
    } catch (e) {
      console.warn("reasoning_effort patch failed", e);
    }
  }

  function paintDensityToggles() {
    const on = !!activeId;
    const toolsOn = toolsExpandedDefault();
    const thinkOn = thinkingExpandedDefault();
    if (els.toggleTools) {
      els.toggleTools.disabled = !on;
      els.toggleTools.classList.toggle("is-on", toolsOn);
      els.toggleTools.setAttribute("aria-pressed", toolsOn ? "true" : "false");
      els.toggleTools.title = toolsOn
        ? "Collapse tool results"
        : "Expand tool results";
    }
    if (els.toggleThinking) {
      els.toggleThinking.disabled = !on;
      els.toggleThinking.classList.toggle("is-on", thinkOn);
      els.toggleThinking.setAttribute("aria-pressed", thinkOn ? "true" : "false");
    }
    paintReasoningUI();
  }

  function updateDensityButtons() {
    paintDensityToggles();
  }

  /** Collapsible tool or thinking row (ADR-0026 Q1/Q8). */
  function collapsibleBubble(m, role) {
    const div = document.createElement("div");
    div.className =
      "bubble collapsible collapsed " + (role === "thinking" ? "thinking" : "tool");
    if (m.id) div.dataset.msgId = m.id;

    const head = document.createElement("div");
    head.className = "collapsible-head";
    head.setAttribute("role", "button");
    head.tabIndex = 0;

    const chev = document.createElement("span");
    chev.className = "chev";
    chev.textContent = "▸";
    head.appendChild(chev);

    const label = document.createElement("span");
    label.className = "role";
    if (role === "thinking") {
      label.textContent = m.summary || "thinking";
    } else {
      label.textContent = m.tool_name || "tool";
    }
    head.appendChild(label);

    const timeEl = makeTimeEl(m.created_at);
    if (timeEl) head.appendChild(timeEl);

    if (m.duration_ms != null && m.duration_ms >= 0) {
      const dur = document.createElement("span");
      dur.className = "msg-time";
      dur.textContent = fmtDur(m.duration_ms);
      head.appendChild(dur);
    }

    const preview = document.createElement("span");
    preview.className = "collapsible-preview";
    const content = (m.content || "").trim();
    preview.textContent =
      role === "thinking"
        ? oneLinePreview(content || m.summary || "thinking…", 100)
        : oneLinePreview(content, 120);
    head.appendChild(preview);

    head.addEventListener("click", (e) => {
      e.preventDefault();
      const open = div.classList.contains("collapsed");
      setCollapsibleExpanded(div, open);
    });
    head.addEventListener("keydown", (e) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        head.click();
      }
    });
    div.appendChild(head);

    // Attachments always visible when collapsed (Q10)
    const chips = attachChipsRow(m);
    if (chips) div.appendChild(chips);

    const body = document.createElement("div");
    body.className = "bubble-body plain";
    body.hidden = true;
    // Thinking: only show model reasoning text. Never invent placeholder / tool timelines.
    if (role === "thinking") {
      body.textContent = content || "";
      if (!content) body.classList.add("thinking-empty");
    } else {
      body.textContent = content;
    }
    div.appendChild(body);

    const defExp =
      role === "thinking" ? thinkingExpandedDefault() : toolsExpandedDefault();
    if (defExp || m._expanded) {
      setCollapsibleExpanded(div, true);
    }
    return div;
  }

  function bubbleEl(m) {
    const role = m.role || "assistant";
    if (role === "tool" || role === "thinking") {
      return collapsibleBubble(m, role);
    }

    const div = document.createElement("div");
    div.className =
      "bubble " +
      (role === "user"
        ? "user"
        : role === "error"
          ? "system-error"
          : role === "harness"
            ? "harness"
            : role === "attachment"
              ? "attachment"
              : "assistant");
    if (m.id) div.dataset.msgId = m.id;

    const meta = document.createElement("div");
    meta.className = "bubble-meta";
    const label = document.createElement("span");
    label.className = "role";
    if (role === "user" && (m.user_email || m.user_name)) {
      const who = m.user_name || m.user_email;
      label.textContent = who;
      label.title = m.user_email || who;
      label.classList.add("user-actor");
    } else {
      label.textContent = role;
    }
    meta.appendChild(label);
    const timeEl = makeTimeEl(m.created_at);
    if (timeEl) meta.appendChild(timeEl);
    div.appendChild(meta);

    const body = document.createElement("div");
    body.className = "bubble-body";
    const content = m.content || "";
    if (roleUsesMarkdown(role)) {
      body.classList.add("md");
      body.innerHTML = renderMarkdown(content);
      body.querySelectorAll("a[href]").forEach((a) => {
        const href = a.getAttribute("href") || "";
        if (/^https?:\/\//i.test(href)) {
          a.setAttribute("target", "_blank");
          a.setAttribute("rel", "noopener noreferrer");
        }
      });
    } else {
      body.classList.add("plain");
      body.textContent = content;
    }
    div.appendChild(body);
    if (role === "assistant" && content.trim()) {
      const actions = document.createElement("div");
      actions.className = "bubble-actions";
      const speakBtn = document.createElement("button");
      speakBtn.type = "button";
      speakBtn.className = "icon-btn bubble-speak";
      speakBtn.title = "Play / stop narration";
      speakBtn.setAttribute("aria-label", "Play narration");
      speakBtn.textContent = "▶";
      speakBtn.addEventListener("click", (e) => {
        e.stopPropagation();
        toggleAssistantSpeech(content, m.id, speakBtn);
      });
      actions.appendChild(speakBtn);
      div.appendChild(actions);
    }
    const chips = attachChipsRow(m);
    if (chips) {
      chips.style.marginTop = "0.4rem";
      div.appendChild(chips);
    }
    return div;
  }

  let _ttsStatusCache = null;
  let _ttsStatusAt = 0;
  /** @type {{ stop: () => void, btn: HTMLElement | null } | null} */
  let _activeSpeech = null;

  async function getTTSStatus() {
    const now = Date.now();
    if (_ttsStatusCache && now - _ttsStatusAt < 15000) return _ttsStatusCache;
    try {
      const res = await fetch("/api/tts/status");
      _ttsStatusCache = await res.json();
      _ttsStatusAt = now;
    } catch {
      _ttsStatusCache = { enabled: false, configured: false };
      _ttsStatusAt = now;
    }
    return _ttsStatusCache;
  }

  function setSpeakBtnIdle(btn) {
    if (!btn) return;
    btn.disabled = false;
    btn.textContent = "▶";
    btn.title = "Play / stop narration";
    btn.setAttribute("aria-label", "Play narration");
    btn.classList.remove("playing");
  }

  function setSpeakBtnPlaying(btn) {
    if (!btn) return;
    btn.disabled = false;
    btn.textContent = "■";
    btn.title = "Stop narration";
    btn.setAttribute("aria-label", "Stop narration");
    btn.classList.add("playing");
  }

  function setSpeakBtnLoading(btn) {
    if (!btn) return;
    btn.disabled = false;
    btn.textContent = "■";
    btn.title = "Stop (loading…)";
    btn.setAttribute("aria-label", "Stop narration");
    btn.classList.add("playing");
  }

  function stopActiveSpeech() {
    const prevBtn = _activeSpeech && _activeSpeech.btn;
    if (_activeSpeech && typeof _activeSpeech.stop === "function") {
      try {
        _activeSpeech.stop();
      } catch (_) {}
    }
    _activeSpeech = null;
    if (window.speechSynthesis) window.speechSynthesis.cancel();
    if (prevBtn) setSpeakBtnIdle(prevBtn);
  }

  /** Toggle: if this button is already playing/loading, stop; otherwise start. */
  function toggleAssistantSpeech(text, messageId, btn) {
    if (_activeSpeech && _activeSpeech.btn === btn) {
      stopActiveSpeech();
      return;
    }
    speakAssistantText(text, messageId, btn);
  }

  async function speakAssistantText(text, messageId, btn) {
    const plain = String(text || "")
      .replace(/```[\s\S]*?```/g, " ")
      .replace(/[#>*_`\[\]]/g, " ")
      .replace(/\s+/g, " ")
      .trim()
      .slice(0, 4000);
    if (!plain) return;
    stopActiveSpeech();

    let cancelled = false;
    _activeSpeech = {
      stop: () => {
        cancelled = true;
      },
      btn: btn || null,
    };
    setSpeakBtnLoading(btn);

    const sid = activeId;
    const st = await getTTSStatus();
    if (cancelled) {
      setSpeakBtnIdle(btn);
      if (_activeSpeech && _activeSpeech.btn === btn) _activeSpeech = null;
      return;
    }
    try {
      if (sid && st && st.enabled && st.configured) {
        const res = await fetch("/api/sessions/" + encodeURIComponent(sid) + "/tts", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ text: plain, message_id: messageId || "" }),
        });
        const data = await res.json().catch(() => ({}));
        if (cancelled) {
          setSpeakBtnIdle(btn);
          if (_activeSpeech && _activeSpeech.btn === btn) _activeSpeech = null;
          return;
        }
        if (res.ok && data.url) {
          const audio = new Audio(
            data.url + (data.url.includes("?") ? "&" : "?") + "inline=1",
          );
          const clear = () => {
            try {
              audio.pause();
              audio.removeAttribute("src");
              audio.load();
            } catch (_) {}
          };
          _activeSpeech = {
            stop: () => {
              cancelled = true;
              clear();
            },
            btn: btn || null,
          };
          audio.onended = () => {
            if (_activeSpeech && _activeSpeech.btn === btn) {
              _activeSpeech = null;
              setSpeakBtnIdle(btn);
            }
          };
          audio.onerror = () => {
            if (cancelled) return;
            if (_activeSpeech && _activeSpeech.btn === btn) {
              clear();
              _activeSpeech = null;
              browserSpeak(plain, btn);
            }
          };
          await audio.play();
          if (cancelled) {
            clear();
            setSpeakBtnIdle(btn);
            if (_activeSpeech && _activeSpeech.btn === btn) _activeSpeech = null;
            return;
          }
          if (_activeSpeech && _activeSpeech.btn === btn) {
            setSpeakBtnPlaying(btn);
          }
          return;
        }
      }
      if (!cancelled) browserSpeak(plain, btn);
      else setSpeakBtnIdle(btn);
    } catch {
      if (!cancelled) browserSpeak(plain, btn);
      else setSpeakBtnIdle(btn);
    }
  }

  function browserSpeak(plain, btn) {
    if (!window.speechSynthesis) {
      setSpeakBtnIdle(btn);
      return;
    }
    stopActiveSpeech();
    const u = new SpeechSynthesisUtterance(plain);
    const clearBtn = () => {
      if (_activeSpeech && _activeSpeech.btn === btn) {
        _activeSpeech = null;
        setSpeakBtnIdle(btn);
      }
    };
    u.onend = clearBtn;
    u.onerror = clearBtn;
    _activeSpeech = {
      stop: () => {
        window.speechSynthesis.cancel();
      },
      btn: btn || null,
    };
    window.speechSynthesis.speak(u);
    setSpeakBtnPlaying(btn);
  }

  // ── ADR-0026 thinking: live line + collapsed history ────────────────────

  function thinkingPhaseKey(p) {
    if (!p || !p.active) return null;
    const tool = (p.current_tool && p.current_tool.name) || "";
    return (p.phase || "") + "|" + tool + "|" + (p.iter ?? 0);
  }

  function thinkingLabelFromTurn(p) {
    if (!p) return "thinking";
    const phase = p.phase || "working";
    if (phase === "calling_model") return "thinking · calling model";
    if (phase === "running_tool") {
      const n = (p.current_tool && p.current_tool.name) || "tool";
      const args = (p.current_tool && p.current_tool.args_preview) || "";
      return "thinking · running " + n + (args ? " · " + oneLinePreview(args, 50) : "");
    }
    if (phase === "starting") return "thinking · starting";
    if (phase === "finishing") return "thinking · finishing";
    if (phase === "stopping") return "thinking · stopping";
    return "thinking · " + phase;
  }

  function placeThinkingLive() {
    if (!els.thinkingLive || !els.transcript) return;
    // Sit just above the turn card when both are in transcript
    if (els.turnCard && els.turnCard.parentElement === els.transcript) {
      els.transcript.insertBefore(els.thinkingLive, els.turnCard);
    } else if (els.thinkingLive.parentElement !== els.transcript) {
      els.transcript.appendChild(els.thinkingLive);
    }
  }

  function hideThinkingLive() {
    if (!els.thinkingLive) return;
    els.thinkingLive.hidden = true;
    els.thinkingLive.textContent = "";
    if (els.thinkingLive.parentElement === els.transcript) {
      // park next to turn card host (main)
      const host = els.turnCard && els.turnCard.parentElement;
      if (host && host !== els.transcript) host.insertBefore(els.thinkingLive, els.turnCard);
    }
  }

  /**
   * Live chip only — do NOT promote turn-progress status into thinking bubbles.
   * Real chain-of-thought arrives as harness role=thinking messages (publishThinking).
   * When the provider returns no reasoning, expanded thinking stays empty / simple label.
   */
  function updateLiveThinking(p) {
    if (!els.thinkingLive) return;
    if (!p || !p.active) {
      liveThinkSeg = null;
      hideThinkingLive();
      return;
    }
    const key = thinkingPhaseKey(p);
    const label = thinkingLabelFromTurn(p);
    if (!liveThinkSeg || liveThinkSeg.phaseKey !== key) {
      liveThinkSeg = { phaseKey: key, label, startedAt: Date.now() };
    } else {
      liveThinkSeg.label = label;
    }
    const phaseMs = liveThinkSeg.startedAt ? Date.now() - liveThinkSeg.startedAt : 0;
    // Short status only — never dump tool timeline / placeholder prose into the chip.
    els.thinkingLive.hidden = false;
    els.thinkingLive.textContent = "⋯ " + label + " · " + fmtDur(phaseMs);
    placeThinkingLive();
  }

  /** True if this looks like the old client-synthesized status-timeline "thinking" row. */
  function isSyntheticStatusThinking(m) {
    if (!m || m.role !== "thinking") return false;
    const id = String(m.id || "");
    if (id.startsWith("think-")) return true;
    const c = String(m.content || "");
    if (c.includes("Status timeline only")) return true;
    if (c.includes("Recent steps:")) return true;
    return false;
  }

  function mergeThinkingMessages(serverMsgs) {
    const base = (serverMsgs || []).filter((m) => m.role !== "thinking");
    const serverThink = (serverMsgs || []).filter(
      (m) => m.role === "thinking" && !isSyntheticStatusThinking(m),
    );
    const clientThink = (
      activeId && sessionThinking[activeId] ? sessionThinking[activeId] : []
    ).filter((m) => !isSyntheticStatusThinking(m));
    // Server CoT wins on id clash; keep client-only harness SSE copies too.
    const byId = new Map();
    clientThink.forEach((m) => {
      if (m && m.id) byId.set(m.id, m);
    });
    serverThink.forEach((m) => {
      if (m && m.id) byId.set(m.id, m);
    });
    const think = Array.from(byId.values());
    // Refresh client store without synthetic junk
    if (activeId) sessionThinking[activeId] = think.slice();
    if (!think.length) return base;
    const all = base.concat(think);
    all.sort((a, b) => {
      const ta = a.created_at ? new Date(a.created_at).getTime() : 0;
      const tb = b.created_at ? new Date(b.created_at).getTime() : 0;
      return ta - tb;
    });
    return all;
  }

  function appendMessage(m) {
    if (m.id && messages.some((x) => x.id === m.id)) return;
    messages.push(m);
    const empty = els.transcript.querySelector(".empty");
    if (empty) empty.remove();
    if (m.role === "user") lastPlacedTurnStart = null;
    els.transcript.appendChild(bubbleEl(m));
    // While a turn is live, keep progress + live thinking under tools/harness.
    if (
      turnProgress &&
      turnProgress.active &&
      els.turnCard &&
      m.role !== "user" &&
      m.role !== "assistant"
    ) {
      placeThinkingLive();
      els.transcript.appendChild(els.turnCard);
    }
    // Stick to bottom only while the user is following the stream.
    scrollTranscriptToBottom(false);
  }

  /**
   * Reload transcript from the server and reconcile with local state.
   * SSE has no catch-up: reconnects and full event buffers can miss the final
   * assistant message after a tool loop — this is the recovery path.
   */
  async function syncTranscript(id) {
    if (!id || id !== activeId) return;
    if (syncInFlight) return;
    syncInFlight = true;
    try {
      const data = await api(`/api/sessions/${encodeURIComponent(id)}`);
      if (id !== activeId) return;
      const serverMsgs = data.messages || [];
      const sum = data.session || {};

      const lastLocal = messages.length ? messages[messages.length - 1] : null;
      const lastServer = serverMsgs.length ? serverMsgs[serverMsgs.length - 1] : null;
      const sameLen = messages.length === serverMsgs.length;
      const sameTail =
        (!lastLocal && !lastServer) ||
        (lastLocal && lastServer && lastLocal.id && lastLocal.id === lastServer.id);

      if (!sameLen || !sameTail) {
        // Preserve harness CoT thinking rows only (drop old synthetic status timelines)
        const think = messages.filter(
          (m) => m.role === "thinking" && !isSyntheticStatusThinking(m),
        );
        if (think.length) {
          sessionThinking[id] = think;
        } else if (sessionThinking[id]) {
          sessionThinking[id] = sessionThinking[id].filter(
            (m) => !isSyntheticStatusThinking(m),
          );
        }
        messages = mergeThinkingMessages(serverMsgs);
        renderTranscript({ forceScroll: false });
      }

      const wasBusy = busy;
      busy = !!sum.busy;
      if (sum.status === "closed") {
        setStatus("closed");
        setBusyPoll(false);
      } else if (busy) {
        setStatus(
          turnProgress && turnProgress.active ? turnProgress.phase || "running" : "running"
        );
        setBusyPoll(true);
      } else {
        if (wasBusy || (turnProgress && turnProgress.active)) {
          setStatus("idle");
        }
        setBusyPoll(false);
      }
      setComposerEnabled(sum.status !== "closed");
    } catch {
      /* ignore transient errors */
    } finally {
      syncInFlight = false;
    }
  }

  function setBusyPoll(on) {
    if (busyPollTimer) {
      clearInterval(busyPollTimer);
      busyPollTimer = null;
    }
    if (!on) return;
    busyPollTimer = setInterval(() => {
      if (!activeId || !busy) {
        setBusyPoll(false);
        return;
      }
      const id = activeId;
      hydrateProgress(id);
      syncTranscript(id);
      // Catch confirms if SSE event was missed while reconnecting.
      pollPeerConfirms(id);
    }, 4000);
  }

  let confirmPollTimer = null;

  function ensureConfirmHost() {
    let host = document.getElementById("peer-confirm-host");
    if (host) return host;
    host = document.createElement("div");
    host.id = "peer-confirm-host";
    host.className = "peer-confirm-host";
    // Sticky under the header so cards are never buried in the transcript.
    const main = document.querySelector(".main") || document.getElementById("app") || document.body;
    const header = main.querySelector(".main-header") || main.querySelector("header");
    if (header && header.parentElement) {
      header.after(host);
    } else if (els.transcript && els.transcript.parentElement) {
      els.transcript.parentElement.insertBefore(host, els.transcript);
    } else {
      document.body.prepend(host);
    }
    return host;
  }

  function showPeerConfirm(c) {
    if (!c || !c.id) return;
    pendingConfirms[c.id] = c;
    renderPeerConfirms();
  }

  function clearPeerConfirm(id) {
    delete pendingConfirms[id];
    renderPeerConfirms();
  }

  function renderPeerConfirms() {
    const host = ensureConfirmHost();
    const ids = Object.keys(pendingConfirms);
    if (!ids.length) {
      host.innerHTML = "";
      host.hidden = true;
      return;
    }
    host.hidden = false;
    host.innerHTML = ids
      .map((id) => {
        const c = pendingConfirms[id];
        const prompt = escapeHtml(c.prompt || "High-risk peer action");
        const risk = escapeHtml(c.risk || "high");
        const comp = escapeHtml(c.computer_id || "peer");
        const stale = c.expired ? " · stale/expired" : "";
        const link = c.url
          ? `<div class="peer-confirm-hint"><a href="${escapeHtml(c.url)}" target="_blank" rel="noopener">Open confirm page</a> (works over Tailscale)</div>`
          : `<div class="peer-confirm-hint"><a href="/confirm/${escapeHtml(id)}" target="_blank" rel="noopener">Open confirm page</a></div>`;
        return `<div class="peer-confirm-card" data-id="${escapeHtml(id)}">
  <div class="peer-confirm-title">⚠️ Confirmation required · ${comp} · risk ${risk}${stale}</div>
  <div class="peer-confirm-prompt">${prompt}</div>
  <div class="peer-confirm-actions">
    <button type="button" class="peer-confirm-accept" data-id="${escapeHtml(id)}" ${c.expired ? "disabled" : ""}>Accept</button>
    <button type="button" class="peer-confirm-deny" data-id="${escapeHtml(id)}">Deny / Dismiss</button>
  </div>
  ${link}
  <div class="peer-confirm-hint">Approve or dismiss here in Marble. Stale cards block computer_* until dismissed. Default deny after ~120s on the peer.</div>
</div>`;
      })
      .join("");
    host.querySelectorAll(".peer-confirm-accept").forEach((btn) => {
      btn.onclick = () => resolvePeerConfirm(btn.getAttribute("data-id"), true);
    });
    host.querySelectorAll(".peer-confirm-deny").forEach((btn) => {
      btn.onclick = () => resolvePeerConfirm(btn.getAttribute("data-id"), false);
    });
  }

  async function resolvePeerConfirm(id, accept) {
    if (!id) return;
    try {
      await api(`/api/computers/confirms/${encodeURIComponent(id)}`, {
        method: "POST",
        body: JSON.stringify({ accept: !!accept }),
      });
      clearPeerConfirm(id);
    } catch (e) {
      // Still clear locally if server says unknown — avoids stuck cards.
      const msg = e.message || String(e);
      if (/unknown confirm|already dismissed|not found/i.test(msg)) {
        clearPeerConfirm(id);
        return;
      }
      alert("Confirm failed: " + msg);
    }
  }

  /** Poll ALL pending confirms (any session) so cards always surface in the UI. */
  async function pollPeerConfirms(_sessionId) {
    try {
      const data = await api(`/api/computers/confirms`);
      const list = (data && data.confirms) || [];
      const seen = {};
      list.forEach((c) => {
        if (!c || !c.id) return;
        seen[c.id] = true;
        pendingConfirms[c.id] = c;
      });
      Object.keys(pendingConfirms).forEach((id) => {
        if (!seen[id]) delete pendingConfirms[id];
      });
      renderPeerConfirms();
    } catch {
      /* ignore */
    }
  }

  function setConfirmPoll(on) {
    if (confirmPollTimer) {
      clearInterval(confirmPollTimer);
      confirmPollTimer = null;
    }
    if (!on) return;
    confirmPollTimer = setInterval(() => {
      pollPeerConfirms(activeId);
    }, 3000);
  }

  function connectEvents(id) {
    if (es) {
      es.close();
      es = null;
    }
    es = new EventSource(`/api/sessions/${id}/events`);

    // Catch-up after connect / auto-reconnect (missed events while disconnected).
    es.addEventListener("open", () => {
      if (id !== activeId) return;
      syncTranscript(id);
      hydrateProgress(id);
      pollPeerConfirms(id);
    });
    es.addEventListener("hello", () => {
      if (id !== activeId) return;
      syncTranscript(id);
      hydrateProgress(id);
      pollPeerConfirms(id);
    });
    es.onerror = () => {
      // Browser will auto-reconnect; while down, keep polling if a turn is live.
      if (id === activeId && busy) setBusyPoll(true);
    };

    es.addEventListener("message", (ev) => {
      let data;
      try {
        data = JSON.parse(ev.data);
      } catch {
        return;
      }
      if (data.type === "message" && data.message) {
        // Harness-originated thinking (model reasoning) — keep in client store too
        if (data.message.role === "thinking" && data.message.id) {
          if (!sessionThinking[id]) sessionThinking[id] = [];
          if (!sessionThinking[id].some((x) => x.id === data.message.id)) {
            sessionThinking[id].push(data.message);
          }
        }
        appendMessage(data.message);
        refreshSessions();
        if (window.MarbleSessionInfo) window.MarbleSessionInfo.refreshIfSession(id);
      } else if (data.type === "harness" && data.status) {
        appendMessage({
          id: "h-" + Date.now(),
          role: "harness",
          content: data.status,
        });
      } else if (data.type === "attachment" && data.attachment) {
        const a = data.attachment;
        appendMessage({
          id: "att-" + Date.now(),
          role: "attachment",
          content: a.inline && a.preview
            ? `📎 ${a.name || a.path}\n\n${a.preview}`
            : `📎 ${a.name || a.path} (${a.size || 0} bytes)`,
        });
      } else if (data.type === "turn" && data.turn) {
        renderTurnCard(data.turn);
        busy = !!data.turn.active;
        setBusyPoll(busy);
        if (!busy) {
          setStatus(data.turn.phase === "error" ? "error" : "idle");
          // Final assistant may have been dropped from the SSE buffer or lost
          // during a reconnect — always reconcile when the turn ends.
          syncTranscript(id);
        }
        setComposerEnabled(!!activeId);
        if (window.MarbleSessionInfo) window.MarbleSessionInfo.refreshIfSession(id);
      } else if (data.type === "status") {
        busy =
          data.status === "running" ||
          data.status === "calling_model" ||
          data.status === "stopping";
        if (data.status === "idle" || data.status === "closed") busy = false;
        setBusyPoll(busy);
        if (data.status === "stopping") setStatus("stopping");
        else if (!(turnProgress && turnProgress.active)) setStatus(data.status || "idle");
        setComposerEnabled(!!activeId);
        if (data.status === "closed") refreshSessions();
        if (data.status === "idle") syncTranscript(id);
        if (window.MarbleSessionInfo) window.MarbleSessionInfo.refreshIfSession(id);
      } else if (data.type === "error") {
        appendMessage({
          id: "err-" + Date.now(),
          role: "error",
          content: data.error || "unknown error",
        });
        setStatus("error");
        busy = false;
        setBusyPoll(false);
        setComposerEnabled(!!activeId);
        syncTranscript(id);
        if (window.MarbleSessionInfo) window.MarbleSessionInfo.refreshIfSession(id);
      } else if (data.type === "tool" && data.tool) {
        if (data.tool.phase === "start" && !(turnProgress && turnProgress.active)) {
          setStatus("running");
        }
        if (window.MarbleSessionInfo) window.MarbleSessionInfo.refreshIfSession(id);
      } else if (data.type === "confirm" && data.confirm) {
        showPeerConfirm(data.confirm);
      } else if (data.type === "session_meta") {
        if (data.model_id !== undefined && data.model_id !== null) {
          setSessionModelPicker(data.model_id || "", busy);
        }
        if (data.model_effective && data.model_effective.capabilities) {
          activeCapImages = !!data.model_effective.capabilities.images;
          updateAttachWarn();
        }
        // Title auto-update (last user message) or permanent rename
        if (data.title) {
          const i = sessions.findIndex((x) => x.id === id);
          if (i >= 0) {
            sessions[i] = {
              ...sessions[i],
              title: data.title,
              title_custom: !!data.title_custom || sessions[i].title_custom,
            };
          }
          renderSessionList();
          if (activeId === id) {
            setMainTitle(sessions[i] || { title: data.title, id }, id);
          }
        }
        if (window.MarbleSessionInfo) window.MarbleSessionInfo.refreshIfSession(id);
      }
    });
  }

  /** Path form: /s/{sessionId} — shareable deep link (SPA fallback serves index). */
  function sessionIdFromURL() {
    const path = (location.pathname || "/").replace(/\/+$/, "") || "/";
    let m = path.match(/^\/s\/([A-Za-z0-9_-]{4,64})$/);
    if (m) return m[1];
    // hash fallback: #/s/{id} or #s/{id}
    const h = (location.hash || "").replace(/^#/, "");
    m = h.match(/^\/?s\/([A-Za-z0-9_-]{4,64})$/);
    if (m) return m[1];
    const q = new URLSearchParams(location.search).get("session");
    if (q && /^[A-Za-z0-9_-]{4,64}$/.test(q)) return q;
    return null;
  }

  function syncURLToSession(id, { replace } = {}) {
    if (!id) return;
    const target = `/s/${id}`;
    if (location.pathname === target) return;
    const state = { sessionId: id };
    try {
      if (replace) history.replaceState(state, "", target);
      else history.pushState(state, "", target);
    } catch {
      /* ignore (file:// etc.) */
    }
  }

  function setMainTitle(sum, id) {
    if (!els.title) return;
    const title = (sum && sum.title) || id || "Select or create a session";
    if (sum && isCronSession(sum)) {
      const jobs = Array.isArray(sum.cron_jobs) && sum.cron_jobs.length
        ? `Cron: ${sum.cron_jobs.join(", ")}`
        : "Cron job session";
      els.title.innerHTML = `<span class="cron-badge" title="${String(jobs).replace(/"/g, "&quot;")}">🕐</span> ${escapeHtml(title)} · <span class="muted">${escapeHtml(id || "")}</span>`;
    } else if (id) {
      els.title.textContent = `${title} · ${id}`;
    } else {
      els.title.textContent = title;
    }
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

  async function selectSession(id, opts) {
    hideCtx();
    hideSinksPopover();
    // Flush any in-progress live thinking for the previous session (store only)
    if (liveThinkSeg && activeId && activeId !== id) {
      liveThinkSeg = null;
    }
    activeId = id;
    setBusyPoll(false);
    pendingConfirms = {};
    liveThinkSeg = null;
    hideThinkingLive();
    renderPeerConfirms();
    renderSessionList();
    setSessionInfoEnabled(!!id);
    updateDensityButtons();
    if (!(opts && opts.skipURL)) {
      syncURLToSession(id, { replace: !!(opts && opts.replaceURL) });
    }
    const data = await api(`/api/sessions/${id}`);
    const sum = data.session || {};
    setMainTitle(sum, id);
    messages = mergeThinkingMessages(data.messages || []);
    busy = !!sum.busy;
    setStatus(sum.status === "closed" ? "closed" : busy ? "running" : "idle");
    // Reasoning effort: session field, else last local default
    sessionSinkOverrides = sum.sink_overrides && typeof sum.sink_overrides === "object" ? { ...sum.sink_overrides } : {};
    sessionSinks = Array.isArray(data.sinks) ? data.sinks : sessionSinks;
    sessionReasoningEffort = sum.reasoning_effort || "";
    if (!sessionReasoningEffort) {
      try {
        sessionReasoningEffort = localStorage.getItem(LS_REASONING_EFFORT) || "none";
      } catch {
        sessionReasoningEffort = "none";
      }
      // Persist default onto session once so server applies it on next model call
      if (activeId && sessionReasoningEffort) {
        applyReasoningEffort(sessionReasoningEffort);
      }
    }
    stagedAttachments = [];
    attachUploading = 0;
    pendingUploads = [];
    renderStage();
    const me = data.model_effective || {};
    activeCapImages = !!(me.capabilities && me.capabilities.images);
    updateAttachWarn();
    renderTranscript({ forceScroll: true });
    setSessionModelPicker(sum.model_id || "", sum.status === "closed" || busy);
    setComposerEnabled(sum.status !== "closed");
    paintDensityToggles();
    if (sum.status !== "closed") {
      connectEvents(id);
      if (busy) setBusyPoll(true);
      pollPeerConfirms(id);
      setConfirmPoll(true);
    } else if (es) {
      es.close();
      es = null;
      setConfirmPoll(false);
    }
    if (isMobileLayout()) setSessionsOpen(false);
    if (window.MarbleSessionInfo) window.MarbleSessionInfo.refreshIfSession(id);
    await hydrateProgress(id);
  }

  async function createSession() {
    const s = await api("/api/sessions", {
      method: "POST",
      body: JSON.stringify({ title: "New session" }),
    });
    await refreshSessions();
    await selectSession(s.id);
    els.input.focus();
  }

  async function renameSession(id) {
    hideCtx();
    if (!id) return;
    const s = sessions.find((x) => x.id === id);
    const cur = (s && s.title) || "";
    const next = prompt("Rename session (permanent — will not follow new messages):", cur);
    if (next === null) return; // cancel
    const title = String(next).trim();
    if (!title) {
      alert("Title cannot be empty.");
      return;
    }
    const res = await api(`/api/sessions/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify({ title }),
    });
    const sum = (res && res.session) || res;
    if (sum && sum.id) {
      const i = sessions.findIndex((x) => x.id === sum.id);
      if (i >= 0) sessions[i] = { ...sessions[i], ...sum };
      else await refreshSessions();
    } else {
      await refreshSessions();
    }
    renderSessionList();
    if (activeId === id) setMainTitle(sum || sessions.find((x) => x.id === id), id);
  }

  async function closeSession(id) {
    hideCtx();
    const s = sessions.find((x) => x.id === id);
    const label = (s && s.title) || id;
    if (!confirm(`Close session “${label}”?`)) return;
    await api(`/api/sessions/${id}/close`, { method: "POST", body: "{}" });
    if (window.MarbleSessionInfo && window.MarbleSessionInfo.currentId && window.MarbleSessionInfo.currentId() === id) {
      window.MarbleSessionInfo.close();
    }
    await refreshSessions();
    if (activeId === id) {
      if (sessions.length) {
        const next = sessions.find((x) => x.status !== "closed") || sessions[0];
        await selectSession(next.id);
      } else {
        activeId = null;
        messages = [];
        renderTranscript({ forceScroll: true });
        setComposerEnabled(false);
        setSessionInfoEnabled(false);
      }
    }
  }

  if (els.sessionsBtn) {
    els.sessionsBtn.addEventListener("click", () => toggleSessions());
  }

  if (els.showClosed) {
    els.showClosed.addEventListener("change", () => {
      showClosed = !!els.showClosed.checked;
      renderSessionList();
    });
  }

  if (els.sysToggle) {
    els.sysToggle.addEventListener("click", () => {
      sysExpanded = !sysExpanded;
      sessionStorage.setItem("marble-sys-agents-open", sysExpanded ? "1" : "0");
      renderSessionList();
    });
  }

  els.newBtn.addEventListener("click", () => {
    if (isMobileLayout()) setSessionsOpen(true);
    createSession().catch((e) => alert(e.message));
  });

  if (els.ctx) {
    els.ctx.addEventListener("click", (e) => {
      const btn = e.target.closest("button[data-action]");
      if (!btn || !ctxSessionId) return;
      if (btn.dataset.action === "info") {
        openSessionInfo(ctxSessionId);
      } else if (btn.dataset.action === "rename") {
        renameSession(ctxSessionId).catch((err) => alert(err.message));
      } else if (btn.dataset.action === "copy-id") {
        copySessionId(ctxSessionId);
      } else if (btn.dataset.action === "close") {
        closeSession(ctxSessionId).catch((err) => alert(err.message));
      }
    });
  }

  if (els.sessionInfo) {
    els.sessionInfo.addEventListener("click", () => {
      if (activeId) openSessionInfo(activeId);
    });
  }

  if (els.tpStop) {
    els.tpStop.addEventListener("click", () => {
      stopTurn().catch((e) => alert(e.message));
    });
  }
  // Bottom control only: live roll-up, or completed-turn show/hide steps.
  function onRollupActivate(ev) {
    if (ev) {
      ev.preventDefault();
      ev.stopPropagation();
    }
    if (!turnProgress) return;
    if (turnProgress.active) {
      setTurnRolledUp(!turnRolledUp);
      return;
    }
    // Completed turn: toggle step detail
    turnExpanded = !turnExpanded;
    renderTurnCard(turnProgress);
  }
  if (els.tpRollup) {
    els.tpRollup.addEventListener("click", onRollupActivate);
  }

  window.addEventListener("marble:session-closed", (ev) => {
    const id = ev.detail && ev.detail.id;
    if (!id) return;
    refreshSessions()
      .then(async () => {
        if (activeId === id) {
          if (sessions.length) {
            const next = sessions.find((x) => x.status !== "closed") || sessions[0];
            await selectSession(next.id);
          } else {
            activeId = null;
            messages = [];
            renderTranscript({ forceScroll: true });
            setComposerEnabled(false);
            setSessionInfoEnabled(false);
          }
        }
      })
      .catch((err) => alert(err.message));
  });

  window.addEventListener("marble:session-renamed", (ev) => {
    const d = ev.detail || {};
    const id = d.id;
    if (!id) return;
    const sum = d.session || { id, title: d.title, title_custom: !!d.title_custom };
    const i = sessions.findIndex((x) => x.id === id);
    if (i >= 0) {
      sessions[i] = {
        ...sessions[i],
        ...sum,
        title: sum.title || d.title || sessions[i].title,
        title_custom: true,
      };
    }
    renderSessionList();
    if (activeId === id) {
      setMainTitle(sessions[i] || sum, id);
    }
  });
  document.addEventListener("click", (e) => {
    if (els.ctx && !els.ctx.contains(e.target)) hideCtx();
  });

  window.matchMedia("(max-width: 720px)").addEventListener("change", (ev) => {
    if (!ev.matches) setSessionsOpen(false);
  });

  if (els.sessionModel) {
    els.sessionModel.addEventListener("change", async () => {
      if (modelPickerBusy || !activeId || busy) return;
      const modelId = els.sessionModel.value || "";
      try {
        const res = await api(`/api/sessions/${encodeURIComponent(activeId)}`, {
          method: "PATCH",
          body: JSON.stringify({ model_id: modelId }),
        });
        const sum = res.session || {};
        setSessionModelPicker(sum.model_id || modelId, false);
      } catch (e) {
        alert(e.message || String(e));
        // reload picker from session
        try {
          const data = await api(`/api/sessions/${encodeURIComponent(activeId)}`);
          setSessionModelPicker((data.session && data.session.model_id) || "", busy);
        } catch {
          /* ignore */
        }
      }
    });
  }

  // ADR-0026: independent toggles for tool vs thinking expansion
  if (els.toggleTools) {
    els.toggleTools.addEventListener("click", () => {
      setCollapsiblesKind("tool", !toolsExpandedDefault());
    });
  }
  if (els.toggleThinking) {
    // Mobile: long-press for reasoning slider. Desktop: right-click (contextmenu).
    const LONG_MS = 480;
    const clearLP = () => {
      if (reasoningLongPressTimer) {
        clearTimeout(reasoningLongPressTimer);
        reasoningLongPressTimer = null;
      }
    };
    const isCoarsePointer = () => {
      try {
        return window.matchMedia("(pointer: coarse)").matches || isMobileLayout();
      } catch {
        return isMobileLayout();
      }
    };
    const startLP = (ev) => {
      if (els.toggleThinking.disabled) return;
      // Skip long-press on mouse / fine pointer (desktop uses right-click)
      if (ev.pointerType === "mouse" || (!ev.pointerType && !isCoarsePointer())) {
        return;
      }
      if (ev.pointerType === "pen" || ev.pointerType === "touch" || isCoarsePointer()) {
        reasoningLongPressFired = false;
        clearLP();
        reasoningLongPressTimer = setTimeout(() => {
          reasoningLongPressFired = true;
          showReasoningPopover();
          if (navigator.vibrate) {
            try {
              navigator.vibrate(12);
            } catch {
              /* ignore */
            }
          }
        }, LONG_MS);
      }
    };
    const endLP = () => {
      clearLP();
    };
    els.toggleThinking.addEventListener("pointerdown", startLP);
    els.toggleThinking.addEventListener("pointerup", endLP);
    els.toggleThinking.addEventListener("pointerleave", endLP);
    els.toggleThinking.addEventListener("pointercancel", endLP);
    els.toggleThinking.addEventListener("contextmenu", (ev) => {
      if (els.toggleThinking.disabled) return;
      ev.preventDefault();
      ev.stopPropagation();
      reasoningLongPressFired = true; // suppress following click if any
      showReasoningPopover();
    });
    els.toggleThinking.addEventListener("click", (ev) => {
      if (reasoningLongPressFired) {
        ev.preventDefault();
        ev.stopPropagation();
        reasoningLongPressFired = false;
        return;
      }
      if (reasoningPopoverOpen) {
        hideReasoningPopover();
        return;
      }
      setCollapsiblesKind("thinking", !thinkingExpandedDefault());
    });
  }
  if (els.reasoningSlider) {
    const onSlide = () => {
      const idx = Math.max(0, Math.min(3, parseInt(els.reasoningSlider.value, 10) || 0));
      const level = REASONING_LEVELS[idx] || "none";
      applyReasoningEffort(level);
    };
    els.reasoningSlider.addEventListener("input", onSlide);
    els.reasoningSlider.addEventListener("change", onSlide);
  }
  if (els.reasoningPopover) {
    els.reasoningPopover.querySelectorAll(".reasoning-labels span").forEach((sp) => {
      sp.style.cursor = "pointer";
      sp.addEventListener("click", () => {
        const v = parseInt(sp.getAttribute("data-v") || "0", 10);
        const level = REASONING_LEVELS[v] || "none";
        if (els.reasoningSlider) els.reasoningSlider.value = String(v);
        applyReasoningEffort(level);
      });
    });
  }
  document.addEventListener("pointerdown", (ev) => {
    if (!reasoningPopoverOpen) return;
    const t = ev.target;
    if (
      els.reasoningPopover &&
      !els.reasoningPopover.contains(t) &&
      t !== els.toggleThinking &&
      !(els.toggleThinking && els.toggleThinking.contains(t))
    ) {
      hideReasoningPopover();
    }
  });
  document.addEventListener("pointerdown", (ev) => {
    if (!sinksPopoverOpen) return;
    const t = ev.target;
    if (
      els.sinksPopover &&
      !els.sinksPopover.contains(t) &&
      t !== els.btnSinks &&
      !(els.btnSinks && els.btnSinks.contains(t))
    ) {
      hideSinksPopover();
    }
  });
  if (els.btnSinks) {
    els.btnSinks.addEventListener("click", () => {
      if (els.btnSinks.disabled) return;
      if (sinksPopoverOpen) hideSinksPopover();
      else showSinksPopover();
    });
  }
  if (els.sinksPauseAll) {
    els.sinksPauseAll.addEventListener("click", () => {
      const sinks = sessionSinks || [];
      const allOff = sinks.length > 0 && sinks.every((s) => sinkEffective(s) === false);
      if (allOff) {
        sessionSinkOverrides = {};
      } else {
        const next = {};
        sinks.forEach((s) => {
          next[s.id] = "off";
        });
        sessionSinkOverrides = next;
      }
      saveSinkOverrides();
    });
  }
  updateDensityButtons();

  function renderStage() {
    if (!els.attachStage) return;
    const hasReady = stagedAttachments.length > 0;
    const hasPending = pendingUploads.length > 0;
    if (!hasReady && !hasPending) {
      els.attachStage.hidden = true;
      els.attachStage.innerHTML = "";
    } else {
      els.attachStage.hidden = false;
      const pendingHtml = pendingUploads
        .map((p) => {
          const label = escapeHtml(p.name || "uploading…");
          return `<span class="attach-chip attach-chip-uploading" data-local="${escapeHtml(p.localId)}" title="Uploading…"><span class="attach-spin" aria-hidden="true"></span><span class="name">${label}</span></span>`;
        })
        .join("");
      const readyHtml = stagedAttachments
        .map((a) => {
          const label = escapeHtml(a.name || a.id);
          const thumb =
            a.kind === "image"
              ? `<img src="/api/sessions/${encodeURIComponent(activeId)}/attachments/${encodeURIComponent(a.id)}?inline=1" alt="" />`
              : "📄";
          return `<span class="attach-chip" data-id="${escapeHtml(a.id)}">${thumb}<span class="name">${label}</span><button type="button" data-rm="${escapeHtml(a.id)}" title="Remove">×</button></span>`;
        })
        .join("");
      els.attachStage.innerHTML = pendingHtml + readyHtml;
      els.attachStage.querySelectorAll("[data-rm]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          const id = btn.getAttribute("data-rm");
          try {
            await api(
              `/api/sessions/${encodeURIComponent(activeId)}/attachments/${encodeURIComponent(id)}`,
              { method: "DELETE" }
            );
          } catch {
            /* ignore */
          }
          stagedAttachments = stagedAttachments.filter((x) => x.id !== id);
          renderStage();
          updateAttachWarn();
        });
      });
    }
    updateAttachWarn();
  }

  function updateAttachWarn() {
    if (!els.attachWarn) return;
    if (attachUploading > 0) {
      els.attachWarn.hidden = false;
      els.attachWarn.textContent =
        attachUploading === 1
          ? "Uploading attachment… Send unlocks when finished."
          : `Uploading ${attachUploading} attachments… Send unlocks when finished.`;
      return;
    }
    const hasImg = stagedAttachments.some((a) => a.kind === "image");
    if (hasImg && !activeCapImages) {
      els.attachWarn.hidden = false;
      els.attachWarn.textContent =
        "Active model has no image support — images stay in the chat and are sent when you switch to a vision model.";
    } else {
      els.attachWarn.hidden = true;
      els.attachWarn.textContent = "";
    }
  }

  async function stageFiles(fileList) {
    if (!activeId || busy) return;
    const files = Array.from(fileList || []).filter(Boolean);
    if (!files.length) return;
    for (const file of files) {
      const localId = "up-" + Date.now() + "-" + Math.random().toString(36).slice(2, 8);
      pendingUploads.push({ localId, name: file.name || "image" });
      attachUploading += 1;
      setComposerEnabled(composerWanted);
      renderStage();
      const fd = new FormData();
      fd.append("file", file, file.name);
      try {
        const res = await fetch(`/api/sessions/${encodeURIComponent(activeId)}/attachments`, {
          method: "POST",
          body: fd,
          credentials: "same-origin",
          headers: { "X-Marble-Requested-With": "fetch" },
        });
        if (res.status === 401) {
          location.href = "/auth/login?next=" + encodeURIComponent(location.pathname);
          return;
        }
        if (!res.ok) throw new Error(await res.text());
        const row = await res.json();
        stagedAttachments.push(row);
      } catch (e) {
        alert(e.message || String(e));
      } finally {
        pendingUploads = pendingUploads.filter((p) => p.localId !== localId);
        attachUploading = Math.max(0, attachUploading - 1);
        setComposerEnabled(composerWanted);
        renderStage();
      }
    }
  }

  function openAttModal(att, sessionId) {
    if (!els.attModal) return;
    const sid = sessionId || activeId;
    els.attModalTitle.textContent = att.name || att.id || "Attachment";
    els.attModalBody.innerHTML = "";
    const url = `/api/sessions/${encodeURIComponent(sid)}/attachments/${encodeURIComponent(att.id)}`;
    if (isRasterImageAtt(att)) {
      const img = document.createElement("img");
      img.src = url + "?inline=1";
      img.alt = att.name || "";
      els.attModalBody.appendChild(img);
    } else {
      fetch(url, { credentials: "same-origin" })
        .then((r) => r.text())
        .then((text) => {
          const isMd = (att.mime || "").includes("markdown") || /\.md$/i.test(att.name || "");
          if (isMd && typeof marked !== "undefined" && window.DOMPurify) {
            const div = document.createElement("div");
            div.className = "md";
            div.innerHTML = DOMPurify.sanitize(marked.parse(text));
            els.attModalBody.appendChild(div);
          } else {
            const pre = document.createElement("pre");
            pre.textContent = text;
            els.attModalBody.appendChild(pre);
          }
        })
        .catch((e) => {
          els.attModalBody.textContent = e.message || String(e);
        });
    }
    els.attModal.hidden = false;
  }

  if (els.attModalClose) {
    els.attModalClose.addEventListener("click", () => {
      els.attModal.hidden = true;
    });
  }
  if (els.attModal) {
    els.attModal.addEventListener("click", (e) => {
      if (e.target === els.attModal) els.attModal.hidden = true;
    });
  }
  if (els.attachInput) {
    els.attachInput.addEventListener("change", () => {
      if (els.attachInput.files && els.attachInput.files.length) {
        stageFiles([...els.attachInput.files]);
        els.attachInput.value = "";
      }
    });
  }
  if (els.input) {
    els.input.addEventListener("paste", (e) => {
      const items = e.clipboardData && e.clipboardData.items;
      if (!items) return;
      const files = [];
      for (const it of items) {
        if (it.type && it.type.startsWith("image/")) {
          const f = it.getAsFile();
          if (f) files.push(f);
        }
      }
      if (files.length) {
        e.preventDefault();
        stageFiles(files);
      }
    });
  }
  if (els.form) {
    els.form.addEventListener("dragover", (e) => {
      e.preventDefault();
    });
    els.form.addEventListener("drop", (e) => {
      e.preventDefault();
      if (e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files.length) {
        stageFiles([...e.dataTransfer.files]);
      }
    });
  }

  // Click attachment chips in transcript
  if (els.transcript) {
    els.transcript.addEventListener("click", (e) => {
      const chip = e.target.closest("[data-att-id]");
      if (!chip) return;
      openAttModal(
        {
          id: chip.getAttribute("data-att-id"),
          name: chip.getAttribute("data-att-name") || "",
          kind: chip.getAttribute("data-att-kind") || "",
          mime: chip.getAttribute("data-att-mime") || "",
        },
        activeId
      );
    });
  }

  function pushComposeHistory(text) {
    const t = String(text || "").trim();
    if (!t) return;
    const last = composeHistory.length
      ? composeHistory[composeHistory.length - 1]
      : "";
    if (last === t) {
      composeHistIdx = -1;
      composeDraftBackup = "";
      return;
    }
    composeHistory.push(t);
    if (composeHistory.length > 100) composeHistory.shift();
    composeHistIdx = -1;
    composeDraftBackup = "";
  }

  function caretOnFirstLine(ta) {
    if (!ta) return true;
    const pos = ta.selectionStart ?? 0;
    const before = ta.value.slice(0, pos);
    return before.indexOf("\n") < 0;
  }

  function caretOnLastLine(ta) {
    if (!ta) return true;
    const pos = ta.selectionStart ?? 0;
    const after = ta.value.slice(pos);
    return after.indexOf("\n") < 0;
  }

  function setComposerValue(text) {
    els.input.value = text;
    const n = els.input.value.length;
    try {
      els.input.setSelectionRange(n, n);
    } catch {
      /* ignore */
    }
  }

  els.form.addEventListener("submit", async (e) => {
    e.preventDefault();
    if (!activeId || busy || attachUploading > 0) return;
    const content = els.input.value.trim();
    if (!content && !stagedAttachments.length) return;
    const ids = stagedAttachments.map((a) => a.id);
    pushComposeHistory(content);
    els.input.value = "";
    composeHistIdx = -1;
    composeDraftBackup = "";
    const pending = stagedAttachments.slice();
    stagedAttachments = [];
    renderStage();
    // Sending re-engages follow mode so the new turn stays in view.
    transcriptPinnedToBottom = true;
    busy = true;
    setBusyPoll(true);
    setComposerEnabled(true);
    setStatus("running");
    try {
      await api(`/api/sessions/${activeId}/messages`, {
        method: "POST",
        body: JSON.stringify({ content, attachment_ids: ids }),
      });
    } catch (err) {
      busy = false;
      setBusyPoll(false);
      setStatus("error");
      stagedAttachments = pending;
      renderStage();
      appendMessage({ id: "err-" + Date.now(), role: "error", content: err.message });
      setComposerEnabled(true);
    }
  });

  els.input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      els.form.requestSubmit();
      return;
    }
    // ↑/↓ cycle sent message history (shell-style). Only when caret is on
    // first/last line so multi-line editing still works with arrows.
    if (e.altKey || e.ctrlKey || e.metaKey) return;
    if (e.key === "ArrowUp" && !e.shiftKey) {
      if (!caretOnFirstLine(els.input)) return;
      if (!composeHistory.length) return;
      e.preventDefault();
      if (composeHistIdx === -1) {
        composeDraftBackup = els.input.value;
        composeHistIdx = composeHistory.length - 1;
      } else if (composeHistIdx > 0) {
        composeHistIdx--;
      }
      setComposerValue(composeHistory[composeHistIdx]);
      return;
    }
    if (e.key === "ArrowDown" && !e.shiftKey) {
      if (composeHistIdx === -1) return;
      if (!caretOnLastLine(els.input)) return;
      e.preventDefault();
      if (composeHistIdx < composeHistory.length - 1) {
        composeHistIdx++;
        setComposerValue(composeHistory[composeHistIdx]);
      } else {
        composeHistIdx = -1;
        setComposerValue(composeDraftBackup);
      }
    }
  });

  window.addEventListener("popstate", (ev) => {
    const id =
      (ev.state && ev.state.sessionId) || sessionIdFromURL();
    if (id && id !== activeId) {
      selectSession(id, { skipURL: true }).catch((e) => alert(e.message));
    }
  });

  setComposerEnabled(false);
  setSessionInfoEnabled(false);
  loadCatalogModels();
  refreshAuth().finally(() => {
  refreshHealth();
  refreshSessions()
    .then(async () => {
      const fromURL = sessionIdFromURL();
      if (fromURL) {
        const known = sessions.some((s) => s.id === fromURL);
        if (known || true) {
          // EnsureLoaded on server if id exists on disk; 404 → fall through
          try {
            await selectSession(fromURL, { replaceURL: true });
            return;
          } catch (e) {
            console.warn("session from URL not found:", fromURL, e.message);
          }
        }
      }
      const open = sessions.find((x) => x.status !== "closed");
      if (open) {
        await selectSession(open.id, { replaceURL: true });
      } else if (sessions.length && showClosed) {
        await selectSession(sessions[0].id, { replaceURL: true });
      } else {
        await createSession();
      }
    })
    .catch((e) => {
      els.health.textContent = e.message;
      els.health.style.color = "var(--danger)";
    });
  });
  setInterval(refreshHealth, 30000);

  // Expose for Clerk dashboard (ADR-0023) and other panels
  window.MarbleApp = {
    selectSession: (id, opts) => selectSession(id, opts || {}),
    refreshSessions: () => refreshSessions(),
  };
})();
