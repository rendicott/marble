/* Live chips for continuations, background tasks and agent tasks.
 *
 * Tool rows that scheduled/started something carry m.refs [{kind,id}]; they get a
 * chip that tracks it (countdown, running time, exit). Anything still pending also
 * shows in the bar above the composer. State comes from GET /api/sessions/{id}/pending,
 * refetched on the session's "pending" SSE event; a 1s ticker updates the clocks.
 */
(() => {
  const bar = document.getElementById("pending-bar");

  let sessionId = null;
  let data = null; // last /pending response for sessionId
  let skewMs = 0; // server clock − client clock
  let ticker = null;
  let slowTimer = null;
  let inflight = false;
  let again = false;

  function nowMs() {
    return Date.now() + skewMs;
  }

  function fmtDur(ms) {
    if (!(ms >= 0)) ms = 0;
    const s = Math.floor(ms / 1000);
    if (s < 60) return s + "s";
    const m = Math.floor(s / 60);
    if (m < 60) return m + "m " + String(s % 60).padStart(2, "0") + "s";
    const h = Math.floor(m / 60);
    return h + "h " + String(m % 60).padStart(2, "0") + "m";
  }

  function clock(iso) {
    const d = new Date(iso);
    if (isNaN(d)) return "";
    return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  }

  async function refresh(id) {
    if (!id || id !== sessionId) return;
    if (inflight) {
      again = true;
      return;
    }
    inflight = true;
    try {
      const res = await fetch(`/api/sessions/${encodeURIComponent(id)}/pending`, {
        credentials: "same-origin",
      });
      if (!res.ok) return;
      const j = await res.json();
      if (id !== sessionId) return;
      data = j;
      const srv = Date.parse(j.now);
      if (!isNaN(srv)) skewMs = srv - Date.now();
      paint();
    } catch {
      /* offline: keep the last state */
    } finally {
      inflight = false;
      if (again) {
        again = false;
        refresh(sessionId);
      }
    }
  }

  function setSession(id) {
    sessionId = id || null;
    data = null;
    paint();
    if (sessionId) refresh(sessionId);
  }

  function lookup(kind, id) {
    if (!data) return null;
    const list =
      kind === "continuation" ? data.continuations : kind === "bg_task" ? data.bg_tasks : data.agent_tasks;
    return (list || []).find((x) => x.id === id) || null;
  }

  /** {text, tone, title, active} for one tracked item. */
  function describe(kind, id) {
    const it = lookup(kind, id);
    if (!it) {
      const what = kind === "continuation" ? "continuation" : kind === "bg_task" ? "bg task" : "agent task";
      return data
        ? { text: `${what} ${id} · no longer tracked (harness restarted?)`, tone: "muted", title: id, active: false }
        : { text: `${what} ${id} · …`, tone: "muted", title: id, active: false };
    }
    const now = nowMs();
    if (kind === "continuation") {
      const name = it.label || "continuation";
      const title = `continuation ${it.id}\n${it.prompt}` + (it.wait_for_task ? `\nwaits for ${it.wait_for_task}` : "");
      if (it.state === "cancelled") return { text: `✕ ${name} · cancelled`, tone: "muted", title, active: false };
      if (it.state === "fired") {
        const why = it.reason === "task_done" ? " · task finished" : "";
        const out = it.outcome || "";
        if (out.startsWith("waiting")) {
          return { text: `⏳ ${name} · due, session busy, retrying`, tone: "warn", title, active: true };
        }
        if (out.startsWith("dropped")) return { text: `⚠ ${name} · ${out}`, tone: "bad", title, active: false };
        return { text: `✓ ${name} · fired ${clock(it.fired_at)}${why}`, tone: "ok", title, active: false };
      }
      const left = Date.parse(it.fire_at) - now;
      let when = left > 0 ? `in ${fmtDur(left)}` : "firing…";
      if (it.wait_for_task) {
        // Wait-only jobs carry a far ceiling; the task is the real trigger.
        when = left > 6 * 3600e3 ? `when ${it.wait_for_task} finishes` : `when ${it.wait_for_task} finishes or ${when}`;
      }
      return { text: `⏱ ${name} · ${when}`, tone: "pending", title, active: true };
    }
    const started = Date.parse(it.started_at);
    const ended = it.ended_at ? Date.parse(it.ended_at) : NaN;
    const name = kind === "bg_task" ? it.label || "bg task" : it.format || "agent";
    const icon = kind === "bg_task" ? "⚙" : "🤖";
    const title =
      `${kind === "bg_task" ? "background task" : "agent task"} ${it.id}\n` +
      (kind === "bg_task" ? it.command : it.prompt) +
      (it.error ? `\nerror: ${it.error}` : "");
    if (it.status === "running") {
      let text = `${icon} ${name} ${it.id} · ${it.phase || "running"} ${fmtDur(now - started)}`;
      let tone = "running";
      if (it.stuck_hint) {
        text += " · stuck?";
        tone = "warn";
      }
      return { text, tone, title, active: true };
    }
    const dur = !isNaN(ended) ? ` · ${fmtDur(ended - started)}` : "";
    const code = it.exit_code;
    if (it.status === "exited" && code === 0) {
      return { text: `✓ ${name} ${it.id} · exit 0${dur}`, tone: "ok", title, active: false };
    }
    const how = it.status === "exited" ? `exit ${code}` : it.status;
    return { text: `✗ ${name} ${it.id} · ${how}${dur}`, tone: "bad", title, active: false };
  }

  function chipEl(kind, id) {
    const el = document.createElement("span");
    el.className = "ref-chip";
    el.dataset.refKind = kind;
    el.dataset.refId = id;
    el.setAttribute("role", "button");
    el.tabIndex = 0;
    const copy = (e) => {
      e.preventDefault();
      e.stopPropagation();
      if (navigator.clipboard) navigator.clipboard.writeText(id).catch(() => {});
      el.classList.add("copied");
      setTimeout(() => el.classList.remove("copied"), 900);
    };
    el.addEventListener("click", copy);
    el.addEventListener("keydown", (e) => {
      if (e.key === "Enter" || e.key === " ") copy(e);
    });
    paintChip(el);
    return el;
  }

  function paintChip(el) {
    const d = describe(el.dataset.refKind, el.dataset.refId);
    el.textContent = d.text;
    el.title = d.title + "\n(click to copy id)";
    el.className = "ref-chip tone-" + d.tone + (el.classList.contains("copied") ? " copied" : "");
    return d.active;
  }

  /** Chip row for a transcript message's refs (null when it has none). */
  function chipsRow(refs) {
    if (!Array.isArray(refs) || !refs.length) return null;
    const row = document.createElement("div");
    row.className = "ref-chips";
    refs.forEach((r) => {
      if (r && r.kind && r.id) row.appendChild(chipEl(r.kind, r.id));
    });
    return row.childNodes.length ? row : null;
  }

  function activeItems() {
    if (!data) return [];
    const out = [];
    (data.continuations || []).forEach((c) => {
      if (c.state === "pending" || (c.state === "fired" && (c.outcome || "").startsWith("waiting"))) {
        out.push(["continuation", c.id]);
      }
    });
    (data.bg_tasks || []).forEach((t) => t.status === "running" && out.push(["bg_task", t.id]));
    (data.agent_tasks || []).forEach((t) => t.status === "running" && out.push(["agent_task", t.id]));
    return out;
  }

  function paintBar(items) {
    if (!bar) return;
    const key = items.map((x) => x.join(":")).join("|");
    if (bar.dataset.key !== key) {
      bar.dataset.key = key;
      bar.innerHTML = "";
      if (items.length) {
        const lab = document.createElement("span");
        lab.className = "pending-label";
        lab.textContent = "Waiting on";
        bar.appendChild(lab);
        items.forEach(([kind, id]) => bar.appendChild(chipEl(kind, id)));
      }
    }
    bar.hidden = items.length === 0;
  }

  function paint() {
    const items = activeItems();
    paintBar(items);
    let anyActive = items.length > 0;
    document.querySelectorAll(".ref-chip").forEach((el) => {
      if (paintChip(el)) anyActive = true;
    });
    // Clocks tick locally; running agents/bg tasks also change phase without an
    // event, so refetch slowly while anything runs.
    if (anyActive && !ticker) {
      ticker = setInterval(() => {
        document.querySelectorAll(".ref-chip").forEach(paintChip);
      }, 1000);
      slowTimer = setInterval(() => refresh(sessionId), 15000);
    } else if (!anyActive && ticker) {
      clearInterval(ticker);
      clearInterval(slowTimer);
      ticker = slowTimer = null;
    }
  }

  window.MarblePending = {
    setSession,
    refresh: (id) => refresh(id),
    chipsRow,
  };
})();
