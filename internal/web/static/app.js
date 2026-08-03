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
    turnCard: document.getElementById("turn-progress"),
    tpTitle: document.getElementById("tp-title"),
    tpBody: document.getElementById("tp-body"),
    tpSteps: document.getElementById("tp-steps"),
    tpStop: document.getElementById("tp-stop"),
    tpExpand: document.getElementById("tp-expand"),
    sessionModel: document.getElementById("session-model"),
    attachStage: document.getElementById("attach-stage"),
    attachInput: document.getElementById("attach-input"),
    attachWarn: document.getElementById("attach-warn"),
    attModal: document.getElementById("att-modal"),
    attModalTitle: document.getElementById("att-modal-title"),
    attModalBody: document.getElementById("att-modal-body"),
    attModalClose: document.getElementById("att-modal-close"),
  };
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

    // Follow the stream downward; do not scroll the card into view mid-transcript.
    if (p.active) {
      const nearBottom =
        els.transcript.scrollHeight - els.transcript.scrollTop - els.transcript.clientHeight < 160;
      if (nearBottom) {
        els.transcript.scrollTop = els.transcript.scrollHeight;
      }
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
      return;
    }

    placeTurnCardInTranscript(p);

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

    if (p.active) {
      els.tpTitle.textContent = `Turn in progress · ${p.phase || "…"}`;
      if (els.tpStop) {
        els.tpStop.hidden = false;
        els.tpStop.disabled = !!p.stop_requested || p.phase === "stopping";
      }
      if (els.tpExpand) els.tpExpand.hidden = true;
      turnExpanded = true; // show steps live while running
      startTurnTick();
    } else {
      // Auto-collapse when the turn ends (user can re-expand via ▸)
      if (turnProgressWasActive) turnExpanded = false;
      const nSteps = (p.steps && p.steps.length) || 0;
      els.tpTitle.textContent = `Turn ${p.phase || "done"} · ${fmtDur(turnMs)} · i${p.iter ?? 0} · tools ${p.tool_rounds ?? 0} · ${nSteps} steps`;
      if (els.tpStop) els.tpStop.hidden = true;
      if (els.tpExpand) {
        els.tpExpand.hidden = false;
        els.tpExpand.textContent = turnExpanded ? "▾" : "▸";
        els.tpExpand.title = turnExpanded ? "Collapse steps" : "Expand steps";
      }
      stopTurnTick();
    }

    const ctx =
      p.context_usage != null ? Math.round(p.context_usage * 100) + "%" : "—";
    const lat =
      p.last_model_latency_ms != null ? p.last_model_latency_ms + " ms" : "—";
    const cur = p.current_tool;
    const last = p.last_tool;
    const toolName = (cur && cur.name) || (last && last.name) || "—";
    const args = (cur && cur.args_preview) || (last && last.args_preview) || "";
    const result = (last && last.result_tail) || "";

    if (els.tpBody) {
      if (p.active || turnExpanded) {
        els.tpBody.hidden = false;
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
        els.tpBody.hidden = true;
        els.tpBody.innerHTML = "";
      }
    }

    if (els.tpSteps) {
      const steps = p.steps || [];
      const showSteps = p.active || turnExpanded;
      els.tpSteps.hidden = !showSteps || !steps.length;
      if (showSteps && steps.length) {
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

    if (p.active) applyTurnPill(p);
    else if (!busy) applyTurnPill(null);
  }

  function escHtml(s) {
    const d = document.createElement("div");
    d.textContent = s == null ? "" : String(s);
    return d.innerHTML;
  }

  function startTurnTick() {
    stopTurnTick();
    turnTickTimer = setInterval(() => {
      if (turnProgress && turnProgress.active) renderTurnCard(turnProgress);
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

  function renderTranscript() {
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
    els.transcript.scrollTop = els.transcript.scrollHeight;
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

  function bubbleEl(m) {
    const div = document.createElement("div");
    const role = m.role || "assistant";
    div.className =
      "bubble " +
      (role === "tool"
        ? "tool"
        : role === "user"
          ? "user"
          : role === "error"
            ? "system-error"
            : role === "harness"
              ? "harness"
              : role === "attachment"
                ? "attachment"
                : "assistant");
    const label = document.createElement("span");
    label.className = "role";
    if (role === "user" && (m.user_email || m.user_name)) {
      const who = m.user_name || m.user_email;
      label.textContent = who;
      label.title = m.user_email || who;
      label.classList.add("user-actor");
    } else {
      label.textContent = role === "tool" ? m.tool_name || "tool" : role;
    }
    div.appendChild(label);
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
    if (m.attachments && m.attachments.length) {
      const row = document.createElement("div");
      row.className = "attach-stage";
      row.style.marginTop = "0.4rem";
      m.attachments.forEach((a) => {
        const chip = document.createElement("span");
        chip.className = "attach-chip";
        chip.setAttribute("data-att-id", a.id || "");
        chip.setAttribute("data-att-name", a.name || "");
        chip.setAttribute("data-att-kind", a.kind || "");
        chip.setAttribute("data-att-mime", a.mime || "");
        chip.title = "Open attachment";
        const isImg = a.kind === "image" || (a.mime && a.mime.startsWith("image/"));
        chip.innerHTML = isImg
          ? `<img src="/api/sessions/${encodeURIComponent(activeId)}/attachments/${encodeURIComponent(a.id)}?inline=1" alt="" /><span class="name"></span>`
          : `📄 <span class="name"></span>`;
        chip.querySelector(".name").textContent = a.name || a.id || "file";
        row.appendChild(chip);
      });
      div.appendChild(row);
    }
    return div;
  }

  function appendMessage(m) {
    if (m.id && messages.some((x) => x.id === m.id)) return;
    messages.push(m);
    const empty = els.transcript.querySelector(".empty");
    if (empty) empty.remove();
    if (m.role === "user") lastPlacedTurnStart = null;
    els.transcript.appendChild(bubbleEl(m));
    // While a turn is live, keep the progress card under tools/harness so order is
    // tools → turn card → final assistant (assistant appends after without re-sinking the card).
    if (
      turnProgress &&
      turnProgress.active &&
      els.turnCard &&
      m.role !== "user" &&
      m.role !== "assistant"
    ) {
      els.transcript.appendChild(els.turnCard);
    }
    els.transcript.scrollTop = els.transcript.scrollHeight;
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
        messages = serverMsgs;
        renderTranscript();
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

  function ensureConfirmHost() {
    let host = document.getElementById("peer-confirm-host");
    if (host) return host;
    host = document.createElement("div");
    host.id = "peer-confirm-host";
    host.className = "peer-confirm-host";
    // Prefer above composer so buttons stay visible while tools run.
    const composer = document.getElementById("composer") || els.form;
    if (composer && composer.parentElement) {
      composer.parentElement.insertBefore(host, composer);
    } else if (els.transcript && els.transcript.parentElement) {
      els.transcript.parentElement.appendChild(host);
    } else {
      document.body.appendChild(host);
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
        return `<div class="peer-confirm-card" data-id="${escapeHtml(id)}">
  <div class="peer-confirm-title">⚠️ Confirmation required · ${comp} · risk ${risk}</div>
  <div class="peer-confirm-prompt">${prompt}</div>
  <div class="peer-confirm-actions">
    <button type="button" class="peer-confirm-accept" data-id="${escapeHtml(id)}">Accept</button>
    <button type="button" class="peer-confirm-deny" data-id="${escapeHtml(id)}">Deny</button>
  </div>
  <div class="peer-confirm-hint">You can approve here in Marble (no need to open the peer machine). Default deny after ~120s.</div>
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
      alert("Confirm failed: " + (e.message || e));
    }
  }

  async function pollPeerConfirms(sessionId) {
    if (!sessionId) return;
    try {
      const data = await api(
        `/api/computers/confirms?session_id=${encodeURIComponent(sessionId)}`
      );
      const list = (data && data.confirms) || [];
      // Drop resolved; add any new
      const seen = {};
      list.forEach((c) => {
        if (!c || !c.id) return;
        seen[c.id] = true;
        if (!pendingConfirms[c.id]) showPeerConfirm(c);
      });
      Object.keys(pendingConfirms).forEach((id) => {
        if (!seen[id]) clearPeerConfirm(id);
      });
    } catch {
      /* ignore */
    }
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
    });
    es.addEventListener("hello", () => {
      if (id !== activeId) return;
      syncTranscript(id);
      hydrateProgress(id);
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

  async function selectSession(id, opts) {
    hideCtx();
    activeId = id;
    setBusyPoll(false);
    pendingConfirms = {};
    renderPeerConfirms();
    renderSessionList();
    setSessionInfoEnabled(!!id);
    if (!(opts && opts.skipURL)) {
      syncURLToSession(id, { replace: !!(opts && opts.replaceURL) });
    }
    const data = await api(`/api/sessions/${id}`);
    const sum = data.session || {};
    setMainTitle(sum, id);
    messages = data.messages || [];
    busy = !!sum.busy;
    setStatus(sum.status === "closed" ? "closed" : busy ? "running" : "idle");
    stagedAttachments = [];
    attachUploading = 0;
    pendingUploads = [];
    renderStage();
    const me = data.model_effective || {};
    activeCapImages = !!(me.capabilities && me.capabilities.images);
    updateAttachWarn();
    renderTranscript();
    setSessionModelPicker(sum.model_id || "", sum.status === "closed" || busy);
    setComposerEnabled(sum.status !== "closed");
    if (sum.status !== "closed") {
      connectEvents(id);
      if (busy) setBusyPoll(true);
      pollPeerConfirms(id);
    } else if (es) {
      es.close();
      es = null;
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
        renderTranscript();
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
  if (els.tpExpand) {
    els.tpExpand.addEventListener("click", () => {
      turnExpanded = !turnExpanded;
      if (turnProgress) renderTurnCard(turnProgress);
    });
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
            renderTranscript();
            setComposerEnabled(false);
            setSessionInfoEnabled(false);
          }
        }
      })
      .catch((err) => alert(err.message));
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
    if (att.kind === "image" || (att.mime && att.mime.startsWith("image/"))) {
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

  els.form.addEventListener("submit", async (e) => {
    e.preventDefault();
    if (!activeId || busy || attachUploading > 0) return;
    const content = els.input.value.trim();
    if (!content && !stagedAttachments.length) return;
    const ids = stagedAttachments.map((a) => a.id);
    els.input.value = "";
    const pending = stagedAttachments.slice();
    stagedAttachments = [];
    renderStage();
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
