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
  };

  let sessions = [];
  let activeId = null;
  let messages = [];
  let es = null;
  let busy = false;
  let ctxSessionId = null;
  let longPressTimer = null;
  let showClosed = false;
  let sysExpanded = sessionStorage.getItem("marble-sys-agents-open") !== "0";
  let turnProgress = null;
  let turnExpanded = false;
  let turnTickTimer = null;
  let lastPlacedTurnStart = null; // re-place card after the user msg when a new turn starts

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

  function setComposerEnabled(on) {
    const closed = sessions.find((x) => x.id === activeId)?.status === "closed";
    els.input.disabled = !on || closed;
    els.send.disabled = !on || busy || closed;
  }

  async function api(path, opts) {
    const res = await fetch(path, {
      headers: { "Content-Type": "application/json", ...(opts && opts.headers) },
      ...opts,
    });
    if (!res.ok) {
      const t = await res.text();
      throw new Error(t || res.statusText);
    }
    if (res.status === 204) return null;
    const ct = res.headers.get("content-type") || "";
    if (ct.includes("application/json")) return res.json();
    return res.text();
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
    els.ctx.style.left = Math.min(x, window.innerWidth - 180) + "px";
    els.ctx.style.top = Math.min(y, window.innerHeight - 80) + "px";
    els.ctx.classList.add("open");
    els.ctx.setAttribute("aria-hidden", "false");
  }

  function openSessionInfo(id) {
    hideCtx();
    if (!id || !window.MarbleSessionInfo) return;
    window.MarbleSessionInfo.open(id);
  }

  function setSessionInfoEnabled(on) {
    if (els.sessionInfo) els.sessionInfo.disabled = !on;
  }

  function sessionRow(s) {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className =
      "sess" +
      (s.id === activeId ? " active" : "") +
      (s.status === "closed" ? " closed" : "") +
      (s.kind === "system" ? " system" : "");
    btn.innerHTML = `<div class="title"></div><div class="meta"></div>`;
    btn.querySelector(".title").textContent = s.title || s.id;
    const when = s.updated_at ? new Date(s.updated_at).toLocaleString() : "";
    const flags = [];
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
    label.textContent = role === "tool" ? m.tool_name || "tool" : role;
    div.appendChild(label);
    const body = document.createElement("div");
    body.className = "bubble-body";
    const content = m.content || "";
    if (roleUsesMarkdown(role)) {
      body.classList.add("md");
      body.innerHTML = renderMarkdown(content);
      // External links: new tab + noopener
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

  function connectEvents(id) {
    if (es) {
      es.close();
      es = null;
    }
    es = new EventSource(`/api/sessions/${id}/events`);
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
        if (!busy && data.turn.phase === "complete") {
          setStatus("idle");
        }
        setComposerEnabled(!!activeId);
        if (window.MarbleSessionInfo) window.MarbleSessionInfo.refreshIfSession(id);
      } else if (data.type === "status") {
        busy =
          data.status === "running" ||
          data.status === "calling_model" ||
          data.status === "stopping";
        if (data.status === "idle" || data.status === "closed") busy = false;
        if (data.status === "stopping") setStatus("stopping");
        else if (!(turnProgress && turnProgress.active)) setStatus(data.status || "idle");
        setComposerEnabled(!!activeId);
        if (data.status === "closed") refreshSessions();
        if (window.MarbleSessionInfo) window.MarbleSessionInfo.refreshIfSession(id);
      } else if (data.type === "error") {
        appendMessage({
          id: "err-" + Date.now(),
          role: "error",
          content: data.error || "unknown error",
        });
        setStatus("error");
        busy = false;
        setComposerEnabled(!!activeId);
        if (window.MarbleSessionInfo) window.MarbleSessionInfo.refreshIfSession(id);
      } else if (data.type === "tool" && data.tool) {
        if (data.tool.phase === "start" && !(turnProgress && turnProgress.active)) {
          setStatus("running");
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

  async function selectSession(id, opts) {
    hideCtx();
    activeId = id;
    renderSessionList();
    setSessionInfoEnabled(!!id);
    if (!(opts && opts.skipURL)) {
      syncURLToSession(id, { replace: !!(opts && opts.replaceURL) });
    }
    const data = await api(`/api/sessions/${id}`);
    const sum = data.session || {};
    els.title.textContent = `${sum.title || id} · ${id}`;
    messages = data.messages || [];
    busy = !!sum.busy;
    setStatus(sum.status === "closed" ? "closed" : busy ? "running" : "idle");
    renderTranscript();
    setComposerEnabled(sum.status !== "closed");
    if (sum.status !== "closed") connectEvents(id);
    else if (es) {
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

  els.form.addEventListener("submit", async (e) => {
    e.preventDefault();
    if (!activeId || busy) return;
    const content = els.input.value.trim();
    if (!content) return;
    els.input.value = "";
    busy = true;
    setComposerEnabled(true);
    setStatus("running");
    try {
      await api(`/api/sessions/${activeId}/messages`, {
        method: "POST",
        body: JSON.stringify({ content }),
      });
    } catch (err) {
      busy = false;
      setStatus("error");
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
  setInterval(refreshHealth, 30000);
})();
