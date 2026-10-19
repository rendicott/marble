/* Session search modal (ADR-0037). The system prompt lives on the Settings Prompt tab. */
(() => {
  const els = {
    open: document.getElementById("btn-prompt"),
    modal: document.getElementById("search-modal"),
    q: document.getElementById("search-q"),
    hits: document.getElementById("search-hits"),
    status: document.getElementById("search-status"),
    close: document.getElementById("search-close"),
  };
  if (!els.open || !els.modal || !els.q) return;

  let timer = null;
  let seq = 0;

  function escapeHtml(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function setStatus(text) {
    if (els.status) els.status.textContent = text || "";
  }

  function clearHits() {
    if (els.hits) els.hits.innerHTML = "";
  }

  function open() {
    els.modal.hidden = false;
    els.q.value = "";
    clearHits();
    setStatus("");
    els.q.focus();
  }

  function close() {
    els.modal.hidden = true;
    clearTimeout(timer);
    seq++;
  }

  function renderHits(hits) {
    clearHits();
    if (!hits || !hits.length) {
      setStatus("No matching sessions");
      return;
    }
    setStatus("");
    const html = hits
      .map((h) => {
        const id = h.id || "";
        const desc = h.description || id;
        const when = h.updated_at || "";
        return `<li><button type="button" class="search-hit" data-id="${escapeHtml(id)}">
          <span class="search-hit-desc">${escapeHtml(desc)}</span>
          <span class="search-hit-meta">${escapeHtml(id)}${when ? " · " + escapeHtml(when) : ""}</span>
        </button></li>`;
      })
      .join("");
    els.hits.innerHTML = html;
  }

  async function run(q, my) {
    setStatus("Searching…");
    try {
      const res = await fetch("/api/sessions/search?q=" + encodeURIComponent(q), {
        credentials: "same-origin",
        headers: { Accept: "application/json" },
      });
      if (my !== seq) return;
      if (res.status === 401) {
        location.href = "/auth/login?next=" + encodeURIComponent(location.pathname);
        return;
      }
      if (res.status === 503) {
        clearHits();
        setStatus("Search is unavailable");
        return;
      }
      if (!res.ok) {
        clearHits();
        setStatus(await res.text());
        return;
      }
      const data = await res.json();
      if (my !== seq) return;
      renderHits(data.hits || []);
    } catch (e) {
      if (my !== seq) return;
      clearHits();
      setStatus(e.message || String(e));
    }
  }

  function schedule() {
    clearTimeout(timer);
    seq++;
    const q = els.q.value.trim();
    if (q.length < 2) {
      clearHits();
      setStatus(q.length === 0 ? "" : "Type at least 2 characters");
      return;
    }
    const my = seq;
    timer = setTimeout(() => {
      if (my !== seq) return;
      run(q, my);
    }, 150);
  }

  els.open.addEventListener("click", () => open());
  if (els.close) els.close.addEventListener("click", close);
  els.modal.addEventListener("click", (e) => {
    if (e.target === els.modal) close();
  });
  els.q.addEventListener("input", schedule);
  if (els.hits) {
    els.hits.addEventListener("click", (e) => {
      const btn = e.target.closest(".search-hit");
      if (!btn) return;
      const id = btn.getAttribute("data-id");
      if (!id) return;
      close();
      const app = window.MarbleApp;
      if (app && typeof app.selectSession === "function") {
        Promise.resolve(app.selectSession(id)).catch((err) => alert(err.message || String(err)));
      }
    });
  }
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && !els.modal.hidden) {
      e.stopPropagation();
      close();
    }
  });
})();
