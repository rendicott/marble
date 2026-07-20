/* Session info modal (ADR-0008) */
(() => {
  const els = {
    modal: document.getElementById("session-info-modal"),
    body: document.getElementById("session-info-body"),
    title: document.getElementById("session-info-title"),
    refresh: document.getElementById("session-info-refresh"),
    close: document.getElementById("session-info-close"),
    closeSess: document.getElementById("session-info-close-sess"),
    partial: document.getElementById("session-info-partial"),
  };
  if (!els.modal || !els.body) return;

  let openId = null;
  let pollTimer = null;
  let lastData = null;
  let lastFingerprint = "";
  let loadInFlight = false;
  let pendingSilent = false;
  let debounceTimer = null;
  let refreshBusyOnly = false;

  function api(path, opts) {
    return fetch(path, {
      headers: { "Content-Type": "application/json", ...(opts && opts.headers) },
      ...opts,
    }).then(async (res) => {
      if (!res.ok) throw new Error(await res.text());
      const ct = res.headers.get("content-type") || "";
      if (ct.includes("application/json")) return res.json();
      return res.text();
    });
  }

  function esc(s) {
    const d = document.createElement("div");
    d.textContent = s == null ? "" : String(s);
    return d.innerHTML;
  }

  function fmtTime(ts) {
    if (!ts) return "—";
    try {
      return new Date(ts).toLocaleString();
    } catch {
      return ts;
    }
  }

  function copyText(text) {
    if (!text) return;
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).catch(() => fallbackCopy(text));
    } else {
      fallbackCopy(text);
    }
  }

  function fallbackCopy(text) {
    const ta = document.createElement("textarea");
    ta.value = text;
    document.body.appendChild(ta);
    ta.select();
    try {
      document.execCommand("copy");
    } catch {
      /* ignore */
    }
    ta.remove();
  }

  /** Stable fingerprint for change detection (ignore object key order via JSON of sorted shape). */
  function fingerprint(data) {
    try {
      return JSON.stringify(data);
    } catch {
      return String(Date.now());
    }
  }

  function row(label, valueHtml, actionsHtml) {
    return `<div class="si-row">
      <span class="si-k">${esc(label)}</span>
      <span class="si-v">${valueHtml}</span>
      ${actionsHtml || ""}
    </div>`;
  }

  function copyBtn(label, text) {
    if (!text) return "";
    return `<button type="button" class="si-copy" data-copy="${esc(text)}" title="Copy ${esc(label)}">Copy</button>`;
  }

  function availableToolsHtml(list) {
    const tools = list || [];
    if (!tools.length) {
      return `<div class="si-muted">No tools registered</div>`;
    }
    const native = tools.filter((t) => t.source !== "mcp");
    const mcp = tools.filter((t) => t.source === "mcp");
    const chip = (t) => {
      const isMcp = t.source === "mcp";
      const cls = isMcp ? "si-chip si-chip-mcp" : "si-chip si-chip-native";
      let title = t.description || t.name;
      if (isMcp && t.server) {
        title = `MCP server: ${t.server}` + (t.description ? `\n${t.description}` : "");
      }
      return `<span class="${cls}" title="${esc(title)}">${esc(t.name)}</span>`;
    };
    return `
      <div class="si-tool-groups">
        <div class="si-tool-group">
          <div class="si-tool-group-h">
            <span class="si-chip si-chip-native si-legend">native</span>
            <span class="si-muted">${native.length}</span>
          </div>
          <div class="si-chips">${native.map(chip).join("") || `<span class="si-muted">—</span>`}</div>
        </div>
        <div class="si-tool-group">
          <div class="si-tool-group-h">
            <span class="si-chip si-chip-mcp si-legend">mcp</span>
            <span class="si-muted">${mcp.length}</span>
          </div>
          <div class="si-chips">${mcp.map(chip).join("") || `<span class="si-muted">None connected</span>`}</div>
        </div>
      </div>`;
  }

  function render(data) {
    lastData = data;
    const s = data.session || {};
    const u = data.usage || {};
    const tools = data.tools || [];
    const available = data.available_tools || [];
    const events = data.recent_events || [];

    refreshBusyOnly = !!s.busy;

    if (els.title) {
      if (s.cron) {
        const jobs = Array.isArray(s.cron_jobs) && s.cron_jobs.length
          ? `Cron: ${s.cron_jobs.join(", ")}`
          : "Cron job session";
        els.title.innerHTML = `<span class="cron-badge" title="${esc(jobs)}">🕐</span> Session info · ${esc(s.id || "")}`;
      } else {
        els.title.textContent = `Session info · ${s.id || ""}`;
      }
    }
    if (els.partial) {
      if (data.partial) {
        els.partial.hidden = false;
        els.partial.textContent =
          "Partial data (limp / no DB aggregates) — metadata only; no token timeline.";
      } else {
        els.partial.hidden = true;
        els.partial.textContent = "";
      }
    }
    if (els.closeSess) {
      els.closeSess.disabled = s.status === "closed" || s.busy;
      els.closeSess.title =
        s.status === "closed"
          ? "Already closed"
          : s.busy
            ? "Session busy"
            : "Close session";
    }

    const statusBits = [s.status || "—"];
    if (s.cron) statusBits.push("cron");
    if (s.busy) statusBits.push("busy");
    if (s.dirty) statusBits.push("dirty");
    if (s.system) statusBits.push("system");
    if (!s.loaded) statusBits.push("not loaded");

    let toolLine = "—";
    if (tools.length) {
      toolLine = tools
        .map((t) => `${t.name}×${t.calls}${t.errors ? ` (err ${t.errors})` : ""}`)
        .join(" · ");
    }

    let evHtml = `<div class="si-muted">No events</div>`;
    if (events.length) {
      evHtml = `<div class="si-events">${events
        .map((e) => {
          const bits = [e.kind || "?"];
          if (e.tool_name) bits.push(e.tool_name);
          if (e.latency_ms != null) bits.push(`${e.latency_ms}ms`);
          if (e.tokens_in_reported != null || e.tokens_out_reported != null) {
            bits.push(`in=${e.tokens_in_reported ?? "—"} out=${e.tokens_out_reported ?? "—"}`);
          }
          const err = e.error
            ? `<div class="si-err">${esc(e.error)}</div>`
            : "";
          const t = e.ts ? new Date(e.ts).toLocaleTimeString() : "";
          return `<div class="si-ev">
            <span class="si-ev-t">${esc(t)}</span>
            <span class="si-ev-b">${esc(bits.join(" · "))}</span>
            ${err}
          </div>`;
        })
        .join("")}</div>`;
    }

    const scrollTop = els.body.scrollTop;

    els.body.innerHTML = `
      <section class="si-sec">
        ${row("Title", (s.cron ? `<span class="cron-badge" title="Cron job session">🕐</span> ` : "") + esc(s.title || "—"))}
        ${row("Id", `<code>${esc(s.id || "")}</code>`, copyBtn("id", s.id))}
        ${row("Status", esc(statusBits.join(" · ")))}
        ${s.cron ? row("Cron", esc(Array.isArray(s.cron_jobs) && s.cron_jobs.length ? s.cron_jobs.join(", ") : "yes — durable schedule target")) : ""}
        ${row("Created", esc(fmtTime(s.created_at)))}
        ${row("Updated", esc(fmtTime(s.updated_at)))}
        ${row("Closed", esc(fmtTime(s.closed_at)))}
        ${row("Model", esc(s.model || "—"))}
        ${row("Workspace", `<code class="si-path">${esc(s.workspace || "—")}</code>`)}
        ${row(
          "Markdown",
          `<code class="si-path">${esc(s.md_path || "—")}</code>`,
          copyBtn("md", s.md_path) + copyBtn("abs", s.md_path_abs)
        )}
        ${s.md_path_abs ? row("MD abs", `<code class="si-path">${esc(s.md_path_abs)}</code>`, copyBtn("abs", s.md_path_abs)) : ""}
        ${row("Messages", esc(String(s.message_count ?? 0)))}
        ${row("Source", esc((data.source || "?") + (data.partial ? " · partial" : "")))}
      </section>
      <section class="si-sec">
        <div class="si-sec-h">Available tools</div>
        ${availableToolsHtml(available)}
      </section>
      <section class="si-sec">
        <div class="si-sec-h">Usage</div>
        ${row("Events (DB)", esc(String(u.event_count ?? 0)))}
        ${row("User / assistant", esc(`${u.user_messages ?? 0} / ${u.assistant_messages ?? 0}`))}
        ${row("Model calls", esc(String(u.model_calls ?? 0)))}
        ${row(
          "Tokens in",
          esc(`${u.tokens_in_reported ?? 0} rep / ${u.tokens_in_est ?? 0} est`)
        )}
        ${row(
          "Tokens out",
          esc(`${u.tokens_out_reported ?? 0} rep / ${u.tokens_out_est ?? 0} est`)
        )}
        ${row("Tool calls / results", esc(`${u.tool_calls ?? 0} / ${u.tool_results ?? 0}`))}
        ${row("Errors", esc(String(u.errors ?? 0)))}
        ${row("Blob spills", esc(String(u.blob_count ?? 0)))}
        ${row(
          "Latency (model)",
          esc(`avg ${u.latency_ms_avg ?? 0} · max ${u.latency_ms_max ?? 0} · sum ${u.latency_ms_sum ?? 0} ms`)
        )}
        ${row("Tools used", esc(toolLine))}
      </section>
      <section class="si-sec">
        <div class="si-sec-h">Recent activity (${events.length})</div>
        ${evHtml}
      </section>
    `;

    // Restore scroll so background refresh doesn't jump the panel
    els.body.scrollTop = scrollTop;
  }

  /**
   * @param {string} id
   * @param {{ silent?: boolean, force?: boolean }} [opts]
   */
  async function load(id, opts) {
    if (!id) return;
    const silent = !!(opts && opts.silent);
    const force = !!(opts && opts.force);

    if (loadInFlight) {
      if (silent) pendingSilent = true;
      return;
    }
    loadInFlight = true;

    if (!silent) {
      els.body.innerHTML = `<p class="si-muted">Loading…</p>`;
      lastFingerprint = "";
    }

    try {
      const data = await api(`/api/sessions/${encodeURIComponent(id)}/info`);
      if (openId !== id) return;
      const fp = fingerprint(data);
      if (!force && silent && fp === lastFingerprint) {
        // still track busy for poll policy
        refreshBusyOnly = !!(data.session && data.session.busy);
        return;
      }
      lastFingerprint = fp;
      render(data);
    } catch (e) {
      if (!silent || !lastData) {
        els.body.innerHTML = `<p class="si-err">Failed to load info: ${esc(e.message)}</p>`;
      }
      // silent failure: keep showing last good render
    } finally {
      loadInFlight = false;
      if (pendingSilent && openId === id) {
        pendingSilent = false;
        load(id, { silent: true });
      }
    }
  }

  function stopPoll() {
    if (pollTimer) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  function startPoll() {
    stopPoll();
    // Slow backup only while busy; idle sessions rely on SSE + manual refresh
    pollTimer = setInterval(() => {
      if (!openId || els.modal.hidden) {
        stopPoll();
        return;
      }
      if (!refreshBusyOnly) return;
      load(openId, { silent: true });
    }, 5000);
  }

  function open(id) {
    if (!id) return;
    openId = id;
    lastFingerprint = "";
    lastData = null;
    els.modal.hidden = false;
    load(id, { force: true });
    startPoll();
  }

  function close() {
    openId = null;
    lastData = null;
    lastFingerprint = "";
    pendingSilent = false;
    if (debounceTimer) {
      clearTimeout(debounceTimer);
      debounceTimer = null;
    }
    stopPoll();
    els.modal.hidden = true;
  }

  function isOpen() {
    return !els.modal.hidden && !!openId;
  }

  /** Debounced silent refresh for SSE (avoids flash storms during tool loops). */
  function refreshIfSession(id) {
    if (!isOpen() || openId !== id) return;
    if (debounceTimer) clearTimeout(debounceTimer);
    debounceTimer = setTimeout(() => {
      debounceTimer = null;
      if (isOpen() && openId === id) load(openId, { silent: true });
    }, 1200);
  }

  els.body.addEventListener("click", (e) => {
    const btn = e.target.closest("button[data-copy]");
    if (!btn) return;
    copyText(btn.getAttribute("data-copy"));
    btn.textContent = "✓";
    setTimeout(() => {
      btn.textContent = "Copy";
    }, 800);
  });

  if (els.close) els.close.addEventListener("click", close);
  if (els.refresh) {
    els.refresh.addEventListener("click", () => {
      if (openId) load(openId, { silent: true, force: true });
    });
  }
  if (els.closeSess) {
    els.closeSess.addEventListener("click", async () => {
      if (!openId) return;
      const title = (lastData && lastData.session && lastData.session.title) || openId;
      if (!confirm(`Close session “${title}”?`)) return;
      try {
        await api(`/api/sessions/${encodeURIComponent(openId)}/close`, {
          method: "POST",
          body: "{}",
        });
        const closedId = openId;
        close();
        window.dispatchEvent(
          new CustomEvent("marble:session-closed", { detail: { id: closedId } })
        );
      } catch (err) {
        alert(err.message || String(err));
      }
    });
  }

  els.modal.addEventListener("click", (e) => {
    if (e.target === els.modal) close();
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && isOpen()) {
      e.stopPropagation();
      close();
    }
  });

  window.MarbleSessionInfo = {
    open,
    close,
    isOpen,
    refreshIfSession,
    currentId: () => openId,
  };
})();
