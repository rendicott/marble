/* System prompt viewer + every-turn soul editor (ADR-0013) */
(() => {
  const HELP =
    "The system prompt is built into the harness (tools, policies, defaults). It is shown for transparency and cannot be changed from this UI.\n\n" +
    "Every-turn context (soul) is optional text you write — house rules, persona, standing preferences. When non-empty it is injected on every model call right after the system prompt as a second system message. Leave empty for stock Marble behavior.\n\n" +
    "Saved as $MEMORY/soul.md — not written into the chat transcript. Applies to user sessions only (not system agents). Live after Save (no restart).";

  const els = {
    open: document.getElementById("btn-prompt"),
    modal: document.getElementById("prompt-modal"),
    system: document.getElementById("prompt-system"),
    soul: document.getElementById("prompt-soul"),
    save: document.getElementById("prompt-save"),
    close: document.getElementById("prompt-close"),
    help: document.getElementById("prompt-help"),
    count: document.getElementById("prompt-count"),
    path: document.getElementById("prompt-path"),
  };
  if (!els.open || !els.modal) return;

  let maxChars = 65536;
  let loadedSoul = "";
  let dirty = false;

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

  function setDirty(v) {
    dirty = !!v;
    if (els.save) els.save.disabled = !dirty;
  }

  function updateCount() {
    const n = (els.soul && els.soul.value) ? els.soul.value.length : 0;
    if (els.count) {
      els.count.textContent = `${n} / ${maxChars}`;
      els.count.classList.toggle("over", n > maxChars);
    }
  }

  async function load() {
    const data = await api("/api/prompt");
    maxChars = data.soul_max_chars || 65536;
    if (els.system) els.system.textContent = data.system_prompt || "";
    loadedSoul = data.soul != null ? String(data.soul) : "";
    if (els.soul) els.soul.value = loadedSoul;
    if (els.path) {
      const p = data.soul_path_abs || data.soul_path || "soul.md";
      els.path.textContent = "Path: " + p;
      els.path.title = p;
    }
    setDirty(false);
    updateCount();
  }

  async function save() {
    const soul = els.soul ? els.soul.value : "";
    if (soul.length > maxChars) {
      alert(`Soul exceeds max size (${maxChars} characters).`);
      return;
    }
    await api("/api/prompt", {
      method: "PUT",
      body: JSON.stringify({ soul }),
    });
    loadedSoul = soul;
    setDirty(false);
    updateCount();
  }

  function open() {
    els.modal.hidden = false;
    load().catch((e) => {
      if (els.system) els.system.textContent = "Failed to load: " + e.message;
    });
  }

  function tryClose() {
    if (dirty) {
      if (!confirm("Discard unsaved every-turn context changes?")) return;
    }
    els.modal.hidden = true;
    if (els.soul) els.soul.value = loadedSoul;
    setDirty(false);
    updateCount();
  }

  els.open.addEventListener("click", () => open());
  if (els.close) els.close.addEventListener("click", tryClose);
  if (els.save) {
    els.save.addEventListener("click", () => {
      save().catch((e) => alert(e.message || String(e)));
    });
  }
  if (els.help) {
    els.help.addEventListener("click", (e) => {
      e.preventDefault();
      e.stopPropagation();
      alert(HELP);
    });
    els.help.title = HELP.split("\n\n")[0];
  }
  if (els.soul) {
    els.soul.addEventListener("input", () => {
      setDirty(els.soul.value !== loadedSoul);
      updateCount();
    });
  }
  els.modal.addEventListener("click", (e) => {
    if (e.target === els.modal) tryClose();
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && !els.modal.hidden) {
      e.stopPropagation();
      tryClose();
    }
  });
})();
