/* Workspace file explorer modal (ADR-0004) */
(() => {
  const STORAGE_KEY = "marble.explorer.path";

  const els = {
    openBtn: document.getElementById("btn-explorer"),
    modal: document.getElementById("fs-modal"),
    dialog: document.getElementById("fs-dialog"),
    crumbs: document.getElementById("fs-crumbs"),
    list: document.getElementById("fs-list"),
    listWrap: document.getElementById("fs-list-wrap"),
    body: document.getElementById("fs-body"),
    editorWrap: document.getElementById("fs-editor-wrap"),
    editorPath: document.getElementById("fs-editor-path"),
    editor: document.getElementById("fs-editor"),
    meta: document.getElementById("fs-meta"),
    refresh: document.getElementById("fs-refresh"),
    save: document.getElementById("fs-save"),
    close: document.getElementById("fs-close"),
    hidden: document.getElementById("fs-hidden"),
    menu: document.getElementById("fs-menu"),
    dropHint: document.getElementById("fs-drop-hint"),
  };

  if (!els.modal || !els.openBtn) return;

  let rootAbs = "";
  let cwd = ".";
  let entries = [];
  let selected = null; // entry or null
  let openFile = null; // relative path
  let openContent = "";
  let dirty = false;
  let menuTarget = null; // { type: 'entry'|'blank', entry? }

  function api(path, opts) {
    opts = opts || {};
    const method = (opts.method || "GET").toUpperCase();
    const headers = { ...(opts.headers || {}) };
    if (method !== "GET" && method !== "HEAD") {
      headers["X-Marble-Requested-With"] = "fetch";
    }
    if (!headers["Content-Type"] && opts.body && typeof opts.body === "string") {
      headers["Content-Type"] = "application/json";
    }
    return fetch(path, { ...opts, headers, credentials: "same-origin" }).then(async (res) => {
      if (res.status === 401) {
        location.href = "/auth/login?next=" + encodeURIComponent(location.pathname);
        throw new Error("auth_required");
      }
      if (!res.ok) {
        const t = await res.text();
        throw new Error(t || res.statusText);
      }
      const ct = res.headers.get("content-type") || "";
      if (ct.includes("application/json")) return res.json();
      return res;
    });
  }

  function joinPath(dir, name) {
    if (!dir || dir === "." || dir === "") return name;
    return dir.replace(/\/+$/, "") + "/" + name;
  }

  function parentPath(p) {
    if (!p || p === "." || p === "") return ".";
    const i = p.replace(/\/+$/, "").lastIndexOf("/");
    if (i <= 0) return ".";
    return p.slice(0, i);
  }

  function setDirty(v) {
    dirty = !!v;
    els.save.disabled = !dirty || !openFile;
  }

  function hideMenu() {
    els.menu.hidden = true;
    els.menu.setAttribute("aria-hidden", "true");
    menuTarget = null;
  }

  function showMenu(x, y, items) {
    els.menu.innerHTML = "";
    for (const it of items) {
      if (it.sep) {
        const s = document.createElement("div");
        s.className = "sep";
        els.menu.appendChild(s);
        continue;
      }
      const b = document.createElement("button");
      b.type = "button";
      b.textContent = it.label;
      if (it.danger) b.className = "danger";
      b.addEventListener("click", () => {
        hideMenu();
        it.action();
      });
      els.menu.appendChild(b);
    }
    els.menu.hidden = false;
    els.menu.setAttribute("aria-hidden", "false");
    const w = 200;
    const h = items.length * 36;
    els.menu.style.left = Math.min(x, window.innerWidth - w) + "px";
    els.menu.style.top = Math.min(y, window.innerHeight - h) + "px";
  }

  async function loadRoot() {
    const info = await api("/api/workspace");
    rootAbs = info.root || "";
  }

  async function loadList() {
    const q = new URLSearchParams({
      path: cwd === "." ? "" : cwd,
      hidden: els.hidden.checked ? "1" : "0",
    });
    const data = await api("/api/workspace/list?" + q.toString());
    entries = data.entries || [];
    rootAbs = data.root || rootAbs;
    renderCrumbs();
    renderList();
  }

  function renderCrumbs() {
    els.crumbs.innerHTML = "";
    const parts = cwd === "." || !cwd ? [] : cwd.split("/").filter(Boolean);
    const add = (label, path) => {
      const b = document.createElement("button");
      b.type = "button";
      b.textContent = label;
      b.addEventListener("click", () => navigate(path));
      els.crumbs.appendChild(b);
    };
    add("workspace", ".");
    let acc = "";
    for (const p of parts) {
      const sep = document.createElement("span");
      sep.className = "sep";
      sep.textContent = "/";
      els.crumbs.appendChild(sep);
      acc = acc ? acc + "/" + p : p;
      add(p, acc);
    }
  }

  function renderList() {
    els.list.innerHTML = "";
    // parent entry
    if (cwd && cwd !== ".") {
      const up = rowEl({ name: "..", path: parentPath(cwd), type: "dir", size: 0 }, true);
      els.list.appendChild(up);
    }
    const sorted = entries.slice().sort((a, b) => {
      if (a.type === "dir" && b.type !== "dir") return -1;
      if (a.type !== "dir" && b.type === "dir") return 1;
      return a.name.localeCompare(b.name);
    });
    for (const e of sorted) {
      els.list.appendChild(rowEl(e, false));
    }
    if (!sorted.length) {
      const empty = document.createElement("div");
      empty.className = "muted";
      empty.style.padding = "0.75rem";
      empty.textContent = "Empty folder";
      els.list.appendChild(empty);
    }
  }

  function rowEl(e, isUp) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "fs-row" + (selected && selected.path === e.path ? " selected" : "");
    b.draggable = !isUp && e.type !== "dir" || e.type === "dir";
    const ico = e.type === "dir" ? "📁" : e.type === "symlink" ? "🔗" : "📄";
    b.innerHTML = `<span class="ico">${isUp ? "⬆" : ico}</span><span class="name"></span><span class="size"></span>`;
    b.querySelector(".name").textContent = e.name;
    b.querySelector(".size").textContent =
      e.type === "file" ? formatSize(e.size) : "";
    b.addEventListener("click", () => onSelect(e, isUp));
    b.addEventListener("dblclick", () => {
      if (e.type === "dir" || isUp) navigate(isUp ? e.path : e.path);
    });
    b.addEventListener("contextmenu", (ev) => {
      ev.preventDefault();
      ev.stopPropagation();
      if (isUp) return;
      selected = e;
      renderList();
      openEntryMenu(ev.clientX, ev.clientY, e);
    });
    b.addEventListener("dragstart", (ev) => {
      if (isUp) return;
      // Hint browser download URL
      const base = window.location.origin;
      if (e.type === "dir") {
        ev.dataTransfer.setData(
          "DownloadURL",
          `application/gzip:${e.name}.tar.gz:${base}/api/workspace/archive?path=${encodeURIComponent(e.path)}`
        );
        ev.dataTransfer.setData("text/uri-list", `${base}/api/workspace/archive?path=${encodeURIComponent(e.path)}`);
      } else {
        ev.dataTransfer.setData(
          "DownloadURL",
          `application/octet-stream:${e.name}:${base}/api/workspace/download?path=${encodeURIComponent(e.path)}`
        );
        ev.dataTransfer.setData("text/uri-list", `${base}/api/workspace/download?path=${encodeURIComponent(e.path)}`);
      }
      ev.dataTransfer.effectAllowed = "copy";
    });
    // long-press
    let lp;
    b.addEventListener("touchstart", (ev) => {
      if (isUp) return;
      const t = ev.touches[0];
      lp = setTimeout(() => openEntryMenu(t.clientX, t.clientY, e), 500);
    }, { passive: true });
    b.addEventListener("touchend", () => clearTimeout(lp));
    b.addEventListener("touchmove", () => clearTimeout(lp));
    return b;
  }

  function formatSize(n) {
    if (n < 1024) return n + " B";
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " K";
    return (n / (1024 * 1024)).toFixed(1) + " M";
  }

  async function navigate(path) {
    if (dirty) {
      const choice = await confirmDirty();
      if (choice === "cancel") return;
      if (choice === "save") {
        try {
          await saveFile();
        } catch (e) {
          alert(e.message);
          return;
        }
      }
      setDirty(false);
    }
    cwd = path || ".";
    try {
      sessionStorage.setItem(STORAGE_KEY, cwd);
    } catch (_) {}
    selected = null;
    openFile = null;
    openContent = "";
    els.editor.value = "";
    els.editorWrap.hidden = true;
    els.meta.hidden = true;
    els.body.classList.remove("split");
    setDirty(false);
    await loadList();
  }

  async function onSelect(e, isUp) {
    if (isUp || e.type === "dir") {
      await navigate(isUp ? e.path : e.path);
      return;
    }
    selected = e;
    renderList();
    if (e.is_text && e.size <= 1024 * 1024) {
      await openText(e.path);
    } else {
      showBinaryMeta(e);
    }
  }

  async function openText(path) {
    if (dirty && openFile && openFile !== path) {
      const choice = await confirmDirty();
      if (choice === "cancel") return;
      if (choice === "save") await saveFile();
    }
    const data = await api("/api/workspace/read?path=" + encodeURIComponent(path));
    openFile = path;
    openContent = data.content || "";
    els.editor.value = openContent;
    els.editorPath.textContent = path;
    els.editorWrap.hidden = false;
    els.meta.hidden = true;
    els.editor.hidden = false;
    els.body.classList.add("split");
    setDirty(false);
  }

  function showBinaryMeta(e) {
    openFile = null;
    openContent = "";
    els.editor.value = "";
    els.editor.hidden = true;
    els.editorPath.textContent = e.path;
    els.meta.hidden = false;
    els.meta.textContent =
      (e.is_text ? "File too large to edit in browser." : "Binary file.") +
      ` Size ${formatSize(e.size)}. Use Download.`;
    els.editorWrap.hidden = false;
    els.body.classList.add("split");
    setDirty(false);
  }

  function confirmDirty() {
    return new Promise((resolve) => {
      // Three-way: Save / Discard / Cancel via two confirms
      if (confirm("You have unsaved changes.\n\nOK = Save\nCancel = Discard or keep editing…")) {
        resolve("save");
        return;
      }
      if (confirm("Discard unsaved changes?\n\nOK = Discard\nCancel = Keep editing")) {
        resolve("discard");
        return;
      }
      resolve("cancel");
    });
  }

  async function saveFile() {
    if (!openFile) return;
    await api("/api/workspace/write", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ path: openFile, content: els.editor.value }),
    });
    openContent = els.editor.value;
    setDirty(false);
  }

  function openEntryMenu(x, y, e) {
    menuTarget = { type: "entry", entry: e };
    const items = [
      {
        label: "Copy relative path",
        action: () => copyText(e.path),
      },
      {
        label: "Copy absolute path",
        action: () => copyText(absPath(e.path)),
      },
      { sep: true },
      {
        label: "Rename…",
        action: () => renameEntry(e),
      },
      {
        label: "Download" + (e.type === "dir" ? " (.tar.gz)" : ""),
        action: () => downloadEntry(e),
      },
      { sep: true },
      {
        label: "Delete…",
        danger: true,
        action: () => deleteEntry(e),
      },
    ];
    showMenu(x, y, items);
  }

  function openBlankMenu(x, y) {
    menuTarget = { type: "blank" };
    showMenu(x, y, [
      { label: "New file…", action: () => newFile() },
      { label: "New folder…", action: () => newFolder() },
    ]);
  }

  function absPath(rel) {
    if (!rel || rel === ".") return rootAbs;
    return rootAbs.replace(/\/+$/, "") + "/" + rel.replace(/^\/+/, "");
  }

  function copyText(t) {
    const text = String(t == null ? "" : t);
    // Prefer Clipboard API when available (secure contexts / localhost).
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(
        () => flashCopyOk(),
        () => copyViaTextarea(text)
      );
      return;
    }
    copyViaTextarea(text);
  }

  function copyViaTextarea(text) {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.style.position = "fixed";
    ta.style.left = "-9999px";
    ta.style.top = "0";
    document.body.appendChild(ta);
    ta.select();
    ta.setSelectionRange(0, text.length);
    let ok = false;
    try {
      ok = document.execCommand("copy");
    } catch (_) {
      ok = false;
    }
    document.body.removeChild(ta);
    if (ok) flashCopyOk();
    // No prompt/modal fallback — silent fail is better than a second popup.
  }

  function flashCopyOk() {
    // Brief non-blocking cue on the explorer title bar
    const title = document.querySelector("#fs-modal .fs-title");
    if (!title) return;
    const prev = title.textContent;
    title.textContent = "Copied path";
    clearTimeout(title._copyFlash);
    title._copyFlash = setTimeout(() => {
      title.textContent = prev || "Workspace";
    }, 900);
  }

  function downloadEntry(e) {
    const url =
      e.type === "dir"
        ? "/api/workspace/archive?path=" + encodeURIComponent(e.path)
        : "/api/workspace/download?path=" + encodeURIComponent(e.path);
    const a = document.createElement("a");
    a.href = url;
    a.download = "";
    document.body.appendChild(a);
    a.click();
    a.remove();
  }

  async function deleteEntry(e) {
    const msg =
      e.type === "dir"
        ? `Delete folder "${e.name}" and all contents permanently?`
        : `Delete file "${e.name}" permanently?`;
    if (!confirm(msg)) return;
    await api("/api/workspace/delete", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ path: e.path }),
    });
    if (openFile === e.path || (openFile && openFile.startsWith(e.path + "/"))) {
      openFile = null;
      dirty = false;
      els.editorWrap.hidden = true;
      els.body.classList.remove("split");
    }
    await loadList();
  }

  async function renameEntry(e) {
    const name = prompt("Rename to:", e.name);
    if (!name || name === e.name) return;
    const to = joinPath(parentPath(e.path), name);
    await api("/api/workspace/rename", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ from: e.path, to }),
    });
    if (openFile === e.path) {
      openFile = to;
      els.editorPath.textContent = to;
    }
    await loadList();
  }

  async function newFile() {
    const name = prompt("New file name:");
    if (!name) return;
    const path = joinPath(cwd === "." ? "" : cwd, name);
    await api("/api/workspace/write", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ path, content: "" }),
    });
    await loadList();
    await openText(path);
  }

  async function newFolder() {
    const name = prompt("New folder name:");
    if (!name) return;
    const path = joinPath(cwd === "." ? "" : cwd, name);
    await api("/api/workspace/mkdir", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ path }),
    });
    await loadList();
  }

  async function uploadFiles(fileList, force) {
    if (!fileList || !fileList.length) return;
    const fd = new FormData();
    for (const f of fileList) fd.append("file", f);
    const q = new URLSearchParams({
      path: cwd === "." ? "" : cwd,
      force: force ? "1" : "0",
    });
    const res = await fetch("/api/workspace/upload?" + q.toString(), {
      method: "POST",
      body: fd,
      credentials: "same-origin",
      headers: { "X-Marble-Requested-With": "fetch" },
    });
    if (res.status === 401) {
      location.href = "/auth/login?next=" + encodeURIComponent(location.pathname);
      throw new Error("auth_required");
    }
    if (res.status === 409) {
      if (confirm("A file already exists. Overwrite?")) {
        return uploadFiles(fileList, true);
      }
      throw new Error(await res.text());
    }
    if (!res.ok) throw new Error(await res.text());
    await loadList();
  }

  async function openModal() {
    els.modal.hidden = false;
    try {
      await loadRoot();
      let start = ".";
      try {
        start = sessionStorage.getItem(STORAGE_KEY) || ".";
      } catch (_) {}
      cwd = start;
      await loadList();
    } catch (e) {
      alert(e.message);
      closeModal(true);
    }
  }

  async function closeModal(force) {
    if (!force && dirty) {
      const choice = await confirmDirty();
      if (choice === "cancel") return;
      if (choice === "save") {
        try {
          await saveFile();
        } catch (e) {
          alert(e.message);
          return;
        }
      }
    }
    els.modal.hidden = true;
    hideMenu();
    setDirty(false);
  }

  // events
  els.openBtn.addEventListener("click", () => openModal().catch((e) => alert(e.message)));
  els.close.addEventListener("click", () => closeModal(false));
  // Click dimmed backdrop (outside dialog) → close
  els.modal.addEventListener("click", (e) => {
    if (e.target === els.modal) closeModal(false);
  });
  // Don't let dialog clicks bubble to the backdrop handler incorrectly;
  // only backdrop is modal itself. Still stop propagation from dialog for safety.
  const fsDialog = document.getElementById("fs-dialog");
  if (fsDialog) {
    fsDialog.addEventListener("click", (e) => e.stopPropagation());
  }
  els.refresh.addEventListener("click", () => {
    loadList().catch((e) => alert(e.message));
    if (openFile && !dirty) {
      openText(openFile).catch((e) => alert(e.message));
    }
  });
  els.save.addEventListener("click", () => saveFile().catch((e) => alert(e.message)));
  els.hidden.addEventListener("change", () => loadList().catch((e) => alert(e.message)));
  els.editor.addEventListener("input", () => {
    setDirty(els.editor.value !== openContent);
  });

  els.listWrap.addEventListener("contextmenu", (ev) => {
    if (ev.target.closest(".fs-row")) return;
    ev.preventDefault();
    openBlankMenu(ev.clientX, ev.clientY);
  });

  document.addEventListener("click", (e) => {
    if (!els.menu.hidden && !els.menu.contains(e.target)) hideMenu();
  });

  // Drag into modal = upload
  ["dragenter", "dragover"].forEach((evn) => {
    els.dialog.addEventListener(evn, (e) => {
      e.preventDefault();
      e.stopPropagation();
      els.dialog.classList.add("dragover");
      els.dropHint.hidden = false;
    });
  });
  ["dragleave", "drop"].forEach((evn) => {
    els.dialog.addEventListener(evn, (e) => {
      e.preventDefault();
      e.stopPropagation();
      if (evn === "dragleave" && e.target !== els.dialog) return;
      els.dialog.classList.remove("dragover");
      els.dropHint.hidden = true;
    });
  });
  els.dialog.addEventListener("drop", (e) => {
    const files = e.dataTransfer && e.dataTransfer.files;
    if (files && files.length) {
      uploadFiles(files, false).catch((err) => alert(err.message));
    }
  });

  // Escape closes
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && !els.modal.hidden) {
      closeModal(false);
    }
  });
})();
