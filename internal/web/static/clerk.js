/* ADR-0023 Clerk — session attention dashboard */
(() => {
  const POLL_MS = 4000;
  const SNOOZE_OPTS = [
    { d: "1h", label: "1h" },
    { d: "4h", label: "4h" },
    { d: "1d", label: "1d" },
    { d: "tomorrow", label: "tmr" },
    { d: "1w", label: "1w" },
  ];

  const els = {
    btn: document.getElementById("btn-clerk"),
    modal: document.getElementById("clerk-modal"),
    body: document.getElementById("clerk-body"),
    status: document.getElementById("clerk-status"),
    close: document.getElementById("clerk-close"),
    refresh: document.getElementById("clerk-refresh"),
    showClosed: document.getElementById("clerk-show-closed"),
    showSnoozed: document.getElementById("clerk-show-snoozed"),
    snoozedBadge: document.getElementById("clerk-snoozed-badge"),
  };

  let open = false;
  let pollTimer = null;
  let loading = false;
  let menuEl = null;

  function api(path, opts) {
    if (window.MarbleAPI && typeof window.MarbleAPI.api === "function") {
      return window.MarbleAPI.api(path, opts);
    }
    const o = opts || {};
    return fetch(path, {
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", ...(o.headers || {}) },
      ...o,
    }).then(async (r) => {
      const t = await r.text();
      let data = null;
      try {
        data = t ? JSON.parse(t) : null;
      } catch {
        data = t;
      }
      if (!r.ok) {
        const msg = (data && data.error) || t || r.statusText;
        throw new Error(msg || String(r.status));
      }
      return data;
    });
  }

  function esc(s) {
    return String(s ?? "")
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function fmtIdle(sec) {
    if (sec == null || sec < 0) return "";
    if (sec < 60) return sec + "s idle";
    if (sec < 3600) return Math.floor(sec / 60) + "m idle";
    const h = Math.floor(sec / 3600);
    const m = Math.floor((sec % 3600) / 60);
    return h + "h " + m + "m idle";
  }

  function fmtLeft(sec) {
    if (sec == null || sec < 0) return "";
    if (sec < 60) return "<1m";
    if (sec < 3600) return Math.floor(sec / 60) + "m";
    if (sec < 86400) {
      const h = Math.floor(sec / 3600);
      const m = Math.floor((sec % 3600) / 60);
      return m ? h + "h " + m + "m" : h + "h";
    }
    const d = Math.floor(sec / 86400);
    const h = Math.floor((sec % 86400) / 3600);
    return h ? d + "d " + h + "h" : d + "d";
  }

  function attentionIcon(att, snoozed) {
    if (snoozed) return { cls: "snoozed", glyph: "💤", label: "snoozed" };
    if (att === "needs_user") return { cls: "needs", glyph: "⚠", label: "needs you" };
    if (att === "working") return { cls: "working", glyph: "◌", label: "working" };
    return { cls: "idle", glyph: "○", label: "idle" };
  }

  function lineFor(row) {
    if (row.busy || row.attention === "working") {
      return row.last_user_snippet || row.title || row.session_id;
    }
    return row.summary || row.last_user_snippet || row.title || row.session_id;
  }

  function closeMenu() {
    if (menuEl) {
      menuEl.remove();
      menuEl = null;
    }
  }

  function openSnoozeMenu(anchor, sessionId) {
    closeMenu();
    const menu = document.createElement("div");
    menu.className = "clerk-snooze-menu";
    menu.setAttribute("role", "menu");
    menu.innerHTML =
      `<div class="clerk-snooze-menu-title muted">Snooze</div>` +
      SNOOZE_OPTS.map(
        (o) =>
          `<button type="button" role="menuitem" data-snooze="${esc(o.d)}" data-id="${esc(sessionId)}">${esc(o.label)}</button>`
      ).join("") +
      `<button type="button" role="menuitem" class="clerk-snooze-clear" data-snooze="clear" data-id="${esc(sessionId)}">Clear</button>`;
    document.body.appendChild(menu);
    menuEl = menu;

    const rect = anchor.getBoundingClientRect();
    const mw = menu.offsetWidth || 120;
    const mh = menu.offsetHeight || 180;
    let left = rect.right - mw;
    let top = rect.bottom + 4;
    if (left < 8) left = 8;
    if (top + mh > window.innerHeight - 8) top = Math.max(8, rect.top - mh - 4);
    menu.style.left = left + "px";
    menu.style.top = top + "px";
  }

  function renderRows(rows) {
    if (!els.body) return;
    if (!rows || !rows.length) {
      els.body.innerHTML = `<div class="clerk-empty muted">No sessions to show. All quiet.</div>`;
      return;
    }
    els.body.innerHTML = rows
      .map((row) => {
        const snoozed = !!row.snoozed;
        const ic = attentionIcon(row.attention, snoozed);
        const idle = row.busy ? "" : fmtIdle(row.idle_for_sec);
        const snz = snoozed ? "zzz " + fmtLeft(row.snooze_left_sec) : "";
        const meta = [ic.label, snz, idle, row.session_id].filter(Boolean).join(" · ");
        const items = Array.isArray(row.action_items) ? row.action_items : [];
        const itemsHtml =
          !row.busy && !snoozed && items.length
            ? `<ul class="clerk-actions">${items
                .slice(0, 5)
                .map((a) => `<li>${esc(a)}</li>`)
                .join("")}</ul>`
            : "";
        const err =
          row.summary_error && !row.busy
            ? `<div class="clerk-err muted">summary: ${esc(row.summary_error).slice(0, 120)}</div>`
            : "";
        const snoozeBtn = snoozed
          ? `<button type="button" class="icon-btn clerk-unsnooze" data-unsnooze="${esc(row.session_id)}" title="Unsnooze">Wake</button>`
          : `<button type="button" class="icon-btn clerk-snooze" data-snooze-menu="${esc(row.session_id)}" title="Snooze">💤</button>`;
        return `<article class="clerk-row clerk-${ic.cls}${snoozed ? " clerk-is-snoozed" : ""}" data-id="${esc(row.session_id)}">
          <div class="clerk-row-top">
            <span class="clerk-icon" title="${esc(ic.label)}">${ic.glyph}</span>
            <div class="clerk-main">
              <div class="clerk-line">${esc(lineFor(row))}</div>
              <div class="clerk-meta muted">${esc(meta)}</div>
              ${itemsHtml}
              ${err}
            </div>
            <div class="clerk-row-actions">
              ${snoozeBtn}
              <button type="button" class="icon-btn clerk-open" data-open="${esc(row.session_id)}">Open</button>
            </div>
          </div>
        </article>`;
      })
      .join("");
  }

  function updateSnoozedBadge(n) {
    if (!els.snoozedBadge) return;
    if (n > 0) {
      els.snoozedBadge.hidden = false;
      els.snoozedBadge.textContent = String(n);
    } else {
      els.snoozedBadge.hidden = true;
      els.snoozedBadge.textContent = "";
    }
  }

  async function load() {
    if (!els.body || loading) return;
    loading = true;
    try {
      const params = new URLSearchParams();
      if (els.showClosed && els.showClosed.checked) params.set("include_closed", "1");
      if (els.showSnoozed && els.showSnoozed.checked) params.set("include_snoozed", "1");
      const q = params.toString() ? "?" + params.toString() : "";
      const data = await api("/api/clerk" + q);
      renderRows(data.sessions || []);
      updateSnoozedBadge(data.snoozed_count || 0);
      if (els.status) {
        els.status.hidden = true;
        els.status.textContent = "";
      }
    } catch (e) {
      if (els.status) {
        els.status.hidden = false;
        els.status.textContent = "Clerk: " + (e.message || e);
      }
    } finally {
      loading = false;
    }
  }

  function startPoll() {
    stopPoll();
    pollTimer = setInterval(() => {
      if (open) load();
    }, POLL_MS);
  }

  function stopPoll() {
    if (pollTimer) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  function openClerk() {
    if (!els.modal) return;
    open = true;
    els.modal.hidden = false;
    load();
    startPoll();
  }

  function closeClerk() {
    open = false;
    closeMenu();
    if (els.modal) els.modal.hidden = true;
    stopPoll();
  }

  async function doRefresh() {
    if (els.refresh) els.refresh.disabled = true;
    try {
      await api("/api/clerk/refresh", { method: "POST", body: "{}" });
      if (els.status) {
        els.status.hidden = false;
        els.status.textContent = "Refresh queued…";
      }
      setTimeout(load, 800);
    } catch (e) {
      if (els.status) {
        els.status.hidden = false;
        els.status.textContent = e.message || String(e);
      }
    } finally {
      if (els.refresh) els.refresh.disabled = false;
    }
  }

  async function doSnooze(sessionId, duration) {
    closeMenu();
    try {
      await api("/api/clerk/snooze", {
        method: "POST",
        body: JSON.stringify({ session_id: sessionId, duration }),
      });
      // If we just snoozed and "show snoozed" is off, row disappears — good.
      await load();
    } catch (e) {
      if (els.status) {
        els.status.hidden = false;
        els.status.textContent = e.message || String(e);
      }
    }
  }

  function openSession(id) {
    closeClerk();
    if (window.MarbleApp && typeof window.MarbleApp.selectSession === "function") {
      window.MarbleApp.selectSession(id);
      return;
    }
    location.href = "/s/" + encodeURIComponent(id);
  }

  if (els.btn) {
    els.btn.addEventListener("click", () => openClerk());
  }
  if (els.close) {
    els.close.addEventListener("click", () => closeClerk());
  }
  if (els.modal) {
    els.modal.addEventListener("click", (e) => {
      if (e.target === els.modal) closeClerk();
    });
  }
  if (els.refresh) {
    els.refresh.addEventListener("click", () => doRefresh());
  }
  if (els.showClosed) {
    els.showClosed.addEventListener("change", () => load());
  }
  if (els.showSnoozed) {
    els.showSnoozed.addEventListener("change", () => load());
  }
  if (els.body) {
    els.body.addEventListener("click", (e) => {
      const snoozeMenu = e.target.closest("[data-snooze-menu]");
      if (snoozeMenu) {
        e.preventDefault();
        e.stopPropagation();
        openSnoozeMenu(snoozeMenu, snoozeMenu.dataset.snoozeMenu);
        return;
      }
      const unsnooze = e.target.closest("[data-unsnooze]");
      if (unsnooze) {
        e.preventDefault();
        e.stopPropagation();
        doSnooze(unsnooze.dataset.unsnooze, "clear");
        return;
      }
      const btn = e.target.closest("[data-open]");
      if (btn && btn.dataset.open) {
        openSession(btn.dataset.open);
        return;
      }
      // Don't open session when clicking action area
      if (e.target.closest(".clerk-row-actions")) return;
      const row = e.target.closest(".clerk-row[data-id]");
      if (row && row.dataset.id) openSession(row.dataset.id);
    });
  }
  document.addEventListener("click", (e) => {
    if (menuEl && !menuEl.contains(e.target) && !e.target.closest("[data-snooze-menu]")) {
      closeMenu();
    }
  });
  document.addEventListener("click", (e) => {
    const item = e.target.closest(".clerk-snooze-menu [data-snooze]");
    if (item && item.dataset.id) {
      e.preventDefault();
      doSnooze(item.dataset.id, item.dataset.snooze);
    }
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && open) {
      if (menuEl) closeMenu();
      else closeClerk();
    }
  });

  window.MarbleClerk = { open: openClerk, close: closeClerk, refresh: load };
})();
