/* Cron jobs modal (ADR-0015) */
(() => {
  const els = {
    open: document.getElementById("btn-cron"),
    modal: document.getElementById("cron-modal"),
    list: document.getElementById("cron-list"),
    runs: document.getElementById("cron-runs"),
    form: document.getElementById("cron-form"),
    formTitle: document.getElementById("cron-form-title"),
    close: document.getElementById("cron-close"),
    refresh: document.getElementById("cron-refresh"),
    newBtn: document.getElementById("cron-new"),
    save: document.getElementById("cron-save"),
    cancel: document.getElementById("cron-cancel"),
    preview: document.getElementById("cron-preview"),
    banner: document.getElementById("cron-banner"),
    // form fields
    id: document.getElementById("cron-id"),
    name: document.getElementById("cron-name"),
    enabled: document.getElementById("cron-enabled"),
    kind: document.getElementById("cron-kind"),
    expr: document.getElementById("cron-expr"),
    interval: document.getElementById("cron-interval"),
    tz: document.getElementById("cron-tz"),
    session: document.getElementById("cron-session"),
    model: document.getElementById("cron-model"),
    prompt: document.getElementById("cron-prompt"),
    maxRuns: document.getElementById("cron-max-runs"),
    cronFields: document.getElementById("cron-fields-cron"),
    intervalFields: document.getElementById("cron-fields-interval"),
  };
  if (!els.open || !els.modal) return;

  let jobs = [];
  let dirty = false;
  let editingId = null;

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
      const text = await res.text();
      if (!res.ok) throw new Error(text || res.statusText);
      if (!text) return null;
      try {
        return JSON.parse(text);
      } catch {
        return text;
      }
    });
  }

  function showBanner(msg, isErr) {
    if (!els.banner) return;
    if (!msg) {
      els.banner.hidden = true;
      return;
    }
    els.banner.hidden = false;
    els.banner.textContent = msg;
    els.banner.classList.toggle("err", !!isErr);
  }

  function setDirty(v) {
    dirty = !!v;
    if (els.save) els.save.disabled = !dirty;
  }

  function fmtTime(s) {
    if (!s) return "—";
    try {
      const d = new Date(s);
      if (isNaN(d.getTime())) return s;
      return d.toLocaleString();
    } catch {
      return s;
    }
  }

  function scheduleLabel(j) {
    if (j.schedule_kind === "interval") {
      return `every ${j.interval_sec || "?"}s`;
    }
    return j.cron_expr || "cron";
  }

  function showForm(show) {
    if (!els.form) return;
    els.form.hidden = !show;
    if (els.list) els.list.classList.toggle("cron-dim", show);
  }

  function toggleKind() {
    const kind = els.kind ? els.kind.value : "cron";
    if (els.cronFields) els.cronFields.hidden = kind !== "cron";
    if (els.intervalFields) els.intervalFields.hidden = kind !== "interval";
    updatePreview();
  }

  async function updatePreview() {
    if (!els.preview) return;
    const kind = els.kind ? els.kind.value : "cron";
    const body = {
      schedule_kind: kind,
      cron_expr: els.expr ? els.expr.value : "",
      interval_sec: els.interval ? parseInt(els.interval.value, 10) || 0 : 0,
      timezone: els.tz ? els.tz.value || "Local" : "Local",
      n: 5,
    };
    try {
      const data = await api("/api/cron/preview", {
        method: "POST",
        body: JSON.stringify(body),
      });
      const times = (data && data.preview) || [];
      els.preview.innerHTML =
        times.length === 0
          ? `<span class="muted">No preview</span>`
          : times.map((t) => `<div>${fmtTime(t)}</div>`).join("");
    } catch (e) {
      els.preview.innerHTML = `<span class="muted">${escapeHtml(e.message)}</span>`;
    }
  }

  function escapeHtml(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function renderJobs() {
    if (!els.list) return;
    if (!jobs.length) {
      els.list.innerHTML = `<div class="cron-empty muted">No cron jobs yet. Create one or let the agent use cron_create.</div>`;
      return;
    }
    els.list.innerHTML = jobs
      .map((j) => {
        const en = j.enabled ? "●" : "○";
        const enClass = j.enabled ? "on" : "off";
        return `<div class="cron-row" data-id="${escapeHtml(j.id)}">
          <div class="cron-row-main">
            <span class="cron-dot ${enClass}" title="${j.enabled ? "enabled" : "disabled"}">${en}</span>
            <div class="cron-row-text">
              <div class="cron-name">${escapeHtml(j.name)}
                <span class="muted mono"> ${escapeHtml(scheduleLabel(j))}</span>
              </div>
              <div class="muted cron-meta">
                next ${fmtTime(j.next_run_at)} · last ${escapeHtml(j.last_status || "—")}
                ${j.session_id ? ` · → <code>${escapeHtml(j.session_id)}</code>` : " · (new session on fire)"}
                ${j.model_id ? ` · model <code>${escapeHtml(j.model_id)}</code>` : ""}
                · runs ${j.run_count || 0}
              </div>
            </div>
          </div>
          <div class="cron-row-actions">
            <button type="button" class="icon-btn" data-act="edit" title="Edit">Edit</button>
            <button type="button" class="icon-btn" data-act="toggle" title="Enable/Disable">${j.enabled ? "Disable" : "Enable"}</button>
            <button type="button" class="icon-btn" data-act="run" title="Run now">Run now</button>
            <button type="button" class="icon-btn danger" data-act="del" title="Delete">Delete</button>
          </div>
        </div>`;
      })
      .join("");
  }

  function renderRuns(runs) {
    if (!els.runs) return;
    if (!runs || !runs.length) {
      els.runs.innerHTML = `<div class="muted">No recent runs</div>`;
      return;
    }
    els.runs.innerHTML = runs
      .slice(0, 30)
      .map(
        (r) =>
          `<div class="cron-run-row">
            <span class="mono">${fmtTime(r.scheduled_at)}</span>
            <span class="cron-status st-${escapeHtml(r.status)}">${escapeHtml(r.status)}</span>
            <span class="muted">${escapeHtml(r.job_id || "")}</span>
            ${r.session_id ? `<code>${escapeHtml(r.session_id)}</code>` : ""}
            ${r.error ? `<span class="err-text">${escapeHtml(r.error)}</span>` : ""}
          </div>`
      )
      .join("");
  }

  async function load() {
    showBanner("");
    try {
      const [jdata, rdata] = await Promise.all([
        api("/api/cron/jobs"),
        api("/api/cron/runs?limit=40"),
      ]);
      jobs = (jdata && jdata.jobs) || [];
      renderJobs();
      renderRuns((rdata && rdata.runs) || []);
    } catch (e) {
      showBanner(e.message, true);
      jobs = [];
      renderJobs();
    }
  }

  function openNew() {
    editingId = null;
    if (els.formTitle) els.formTitle.textContent = "New cron job";
    if (els.id) els.id.value = "";
    if (els.name) els.name.value = "";
    if (els.enabled) els.enabled.checked = true;
    if (els.kind) els.kind.value = "cron";
    if (els.expr) els.expr.value = "0 8 * * *";
    if (els.interval) els.interval.value = "300";
    if (els.tz) els.tz.value = "Local";
    if (els.session) els.session.value = "";
    if (els.model) els.model.value = "";
    if (els.prompt) els.prompt.value = "";
    if (els.maxRuns) els.maxRuns.value = "";
    toggleKind();
    showForm(true);
    setDirty(true);
  }

  function openEdit(j) {
    editingId = j.id;
    if (els.formTitle) els.formTitle.textContent = "Edit cron job";
    if (els.id) els.id.value = j.id;
    if (els.name) els.name.value = j.name || "";
    if (els.enabled) els.enabled.checked = !!j.enabled;
    if (els.kind) els.kind.value = j.schedule_kind || "cron";
    if (els.expr) els.expr.value = j.cron_expr || "";
    if (els.interval) els.interval.value = j.interval_sec || 60;
    if (els.tz) els.tz.value = j.timezone || "Local";
    if (els.session) els.session.value = j.session_id || "";
    if (els.model) els.model.value = j.model_id || "";
    if (els.prompt) els.prompt.value = j.prompt || "";
    if (els.maxRuns) els.maxRuns.value = j.max_runs != null ? j.max_runs : "";
    toggleKind();
    showForm(true);
    setDirty(false);
  }

  async function save() {
    const body = {
      name: els.name ? els.name.value.trim() : "",
      enabled: els.enabled ? els.enabled.checked : true,
      schedule_kind: els.kind ? els.kind.value : "cron",
      cron_expr: els.expr ? els.expr.value.trim() : "",
      interval_sec: els.interval ? parseInt(els.interval.value, 10) || 0 : 0,
      timezone: els.tz ? els.tz.value.trim() || "Local" : "Local",
      session_id: els.session ? els.session.value.trim() : "",
      model_id: els.model ? els.model.value.trim() : "",
      prompt: els.prompt ? els.prompt.value : "",
    };
    const mr = els.maxRuns && els.maxRuns.value.trim() !== "" ? parseInt(els.maxRuns.value, 10) : null;
    if (mr != null && !isNaN(mr)) body.max_runs = mr;

    try {
      if (editingId) {
        await api("/api/cron/jobs/" + encodeURIComponent(editingId), {
          method: "PUT",
          body: JSON.stringify(body),
        });
      } else {
        await api("/api/cron/jobs", {
          method: "POST",
          body: JSON.stringify(body),
        });
      }
      showForm(false);
      setDirty(false);
      await load();
      showBanner(editingId ? "Updated" : "Created");
    } catch (e) {
      showBanner(e.message, true);
    }
  }

  function tryClose() {
    if (dirty && !els.form.hidden) {
      if (!confirm("Discard unsaved cron job changes?")) return;
    }
    showForm(false);
    setDirty(false);
    els.modal.hidden = true;
  }

  function open() {
    els.modal.hidden = false;
    showForm(false);
    load();
  }

  els.open.addEventListener("click", open);
  if (els.close) els.close.addEventListener("click", tryClose);
  if (els.refresh) els.refresh.addEventListener("click", () => load());
  if (els.newBtn) els.newBtn.addEventListener("click", openNew);
  if (els.cancel) {
    els.cancel.addEventListener("click", () => {
      if (dirty && !confirm("Discard unsaved changes?")) return;
      showForm(false);
      setDirty(false);
    });
  }
  if (els.save) els.save.addEventListener("click", () => save());
  if (els.kind) els.kind.addEventListener("change", toggleKind);
  ["input", "change"].forEach((ev) => {
    [els.name, els.enabled, els.kind, els.expr, els.interval, els.tz, els.session, els.model, els.prompt, els.maxRuns].forEach((el) => {
      if (el) {
        el.addEventListener(ev, () => {
          setDirty(true);
          if (el === els.kind || el === els.expr || el === els.interval || el === els.tz) updatePreview();
        });
      }
    });
  });

  if (els.list) {
    els.list.addEventListener("click", async (e) => {
      const btn = e.target.closest("[data-act]");
      if (!btn) return;
      const row = btn.closest(".cron-row");
      if (!row) return;
      const id = row.getAttribute("data-id");
      const j = jobs.find((x) => x.id === id);
      const act = btn.getAttribute("data-act");
      try {
        if (act === "edit" && j) openEdit(j);
        if (act === "toggle" && j) {
          await api("/api/cron/jobs/" + encodeURIComponent(id), {
            method: "PUT",
            body: JSON.stringify({ enabled: !j.enabled }),
          });
          await load();
        }
        if (act === "run") {
          const run = await api("/api/cron/jobs/" + encodeURIComponent(id) + "/run", {
            method: "POST",
            body: "{}",
          });
          showBanner("Run: " + (run && run.status ? run.status : "ok"));
          await load();
        }
        if (act === "del") {
          if (!confirm("Delete this cron job?")) return;
          await api("/api/cron/jobs/" + encodeURIComponent(id), { method: "DELETE" });
          await load();
        }
      } catch (err) {
        showBanner(err.message, true);
      }
    });
  }

  // Close on backdrop click
  els.modal.addEventListener("click", (e) => {
    if (e.target === els.modal) tryClose();
  });
})();
