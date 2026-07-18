/**
 * Marble ADR review kit
 *
 * Markup per question:
 *   <div class="q" data-qid="Q7" data-status="open|locked"
 *        data-rec="recommendation text" data-decision="locked decision if any">
 *     <strong>Q7</strong> prompt…
 *     <div class="rec">Rec: …</div>
 *   </div>
 *
 * Page config (optional meta or body data attrs):
 *   <body data-adr="0005" data-adr-title="…">
 *
 * Persistence:
 *   - localStorage key marble-adr-{id}-answers
 *   - Download → {id}-answers.json  (save next to the review HTML)
 *   - Import file / drag JSON onto page
 *   - Agent collects answers by reading adr/{id}-answers.json
 */
(function () {
  "use strict";

  const CHOICE_REC = "rec";
  const CHOICE_CUSTOM = "custom";
  const CHOICE_DEFER = "defer";

  function $(sel, root) {
    return (root || document).querySelector(sel);
  }

  function $all(sel, root) {
    return Array.from((root || document).querySelectorAll(sel));
  }

  function adrId() {
    const b = document.body;
    return (
      (b && b.dataset.adr) ||
      (document.documentElement.dataset.adr) ||
      guessAdrFromTitle() ||
      "unknown"
    );
  }

  function adrTitle() {
    return (
      (document.body && document.body.dataset.adrTitle) ||
      document.title ||
      ""
    );
  }

  function guessAdrFromTitle() {
    const m = (document.title || "").match(/ADR-?0*(\d+)/i);
    return m ? String(m[1]).padStart(4, "0") : null;
  }

  function storageKey() {
    return "marble-adr-" + adrId() + "-answers";
  }

  function toast(msg) {
    let el = $("#adr-toast");
    if (!el) {
      el = document.createElement("div");
      el.id = "adr-toast";
      el.className = "adr-toast";
      document.body.appendChild(el);
    }
    el.textContent = msg;
    el.classList.add("show");
    clearTimeout(el._t);
    el._t = setTimeout(function () {
      el.classList.remove("show");
    }, 2600);
  }

  function qNodes() {
    return $all(".q[data-qid]");
  }

  function recText(node) {
    if (node.dataset.rec) return node.dataset.rec.trim();
    const rec = node.querySelector(".rec");
    if (!rec) return "";
    return rec.textContent.replace(/^\s*Rec:\s*/i, "").trim();
  }

  function emptyAnswer(qid, node) {
    const locked = (node.dataset.status || "").toLowerCase() === "locked";
    const decision = (node.dataset.decision || "").trim();
    return {
      status: locked ? "locked" : "open",
      choice: locked ? CHOICE_REC : null,
      decision: locked ? decision || recText(node) : null,
      notes: "",
      question: extractQuestion(node),
      rec: recText(node),
    };
  }

  function extractQuestion(node) {
    const clone = node.cloneNode(true);
    $all(".rec, .adr-answer, .adr-locked-badge", clone).forEach(function (n) {
      n.remove();
    });
    return clone.textContent.replace(/\s+/g, " ").trim();
  }

  function loadStore() {
    try {
      const raw = localStorage.getItem(storageKey());
      if (!raw) return null;
      return JSON.parse(raw);
    } catch (e) {
      return null;
    }
  }

  function saveStore(doc) {
    try {
      localStorage.setItem(storageKey(), JSON.stringify(doc));
    } catch (e) {
      /* ignore quota */
    }
  }

  function buildDoc(answers) {
    return {
      schema: "marble-adr-answers/v1",
      adr: adrId(),
      title: adrTitle(),
      updated_at: new Date().toISOString(),
      answers: answers,
    };
  }

  function collectFromDOM() {
    const answers = {};
    qNodes().forEach(function (node) {
      const qid = node.dataset.qid;
      const base = emptyAnswer(qid, node);
      const choiceEl = node.querySelector('input[type="radio"]:checked');
      const notesEl = node.querySelector(".adr-notes");
      const locked = base.status === "locked";

      if (locked) {
        answers[qid] = base;
        return;
      }

      const choice = choiceEl ? choiceEl.value : null;
      const notes = notesEl ? notesEl.value.trim() : "";
      let decision = null;
      let status = "open";

      if (choice === CHOICE_REC) {
        decision = base.rec;
        status = "answered";
      } else if (choice === CHOICE_CUSTOM) {
        decision = notes || null;
        status = notes ? "answered" : "open";
      } else if (choice === CHOICE_DEFER) {
        decision = notes ? "defer: " + notes : "defer";
        status = "deferred";
      }

      answers[qid] = {
        status: status,
        choice: choice,
        decision: decision,
        notes: notes,
        question: base.question,
        rec: base.rec,
      };
    });
    return buildDoc(answers);
  }

  function applyDoc(doc) {
    if (!doc || !doc.answers) return;
    qNodes().forEach(function (node) {
      const qid = node.dataset.qid;
      const a = doc.answers[qid];
      if (!a) return;

      if ((node.dataset.status || "").toLowerCase() === "locked") {
        // locked stays locked; allow notes only if present in file
        const notesEl = node.querySelector(".adr-notes");
        if (notesEl && a.notes) notesEl.value = a.notes;
        paintState(node, a);
        return;
      }

      const choice = a.choice;
      if (choice) {
        const radio = node.querySelector(
          'input[type="radio"][value="' + choice + '"]'
        );
        if (radio) radio.checked = true;
      }
      const notesEl = node.querySelector(".adr-notes");
      if (notesEl) notesEl.value = a.notes || "";
      paintState(node, a);
    });
    updateProgress();
  }

  function paintState(node, answer) {
    node.classList.remove("is-open", "is-answered", "is-locked", "is-deferred");
    const st = answer && answer.status ? answer.status : "open";
    if (st === "locked") node.classList.add("is-locked");
    else if (st === "answered") node.classList.add("is-answered");
    else if (st === "deferred") node.classList.add("is-deferred");
    else node.classList.add("is-open");

    const preview = node.querySelector(".adr-decision-preview");
    if (preview) {
      if (answer && answer.decision) {
        preview.textContent = "→ " + answer.decision;
        preview.hidden = false;
      } else {
        preview.textContent = "";
        preview.hidden = true;
      }
    }
  }

  function updateProgress() {
    const doc = collectFromDOM();
    const ids = Object.keys(doc.answers);
    const total = ids.length;
    let done = 0;
    ids.forEach(function (id) {
      const s = doc.answers[id].status;
      if (s === "answered" || s === "locked" || s === "deferred") done++;
    });
    const pct = total ? Math.round((done / total) * 100) : 0;
    const label = $("#adr-progress-label");
    const bar = $("#adr-progress-bar");
    if (label) label.innerHTML = "<strong>" + done + "/" + total + "</strong> answered";
    if (bar) bar.style.width = pct + "%";
    saveStore(doc);
    syncEmbedded(doc);
    return doc;
  }

  function syncEmbedded(doc) {
    let el = $("#marble-adr-answers");
    if (!el) {
      el = document.createElement("script");
      el.type = "application/json";
      el.id = "marble-adr-answers";
      document.body.appendChild(el);
    }
    el.textContent = JSON.stringify(doc, null, 2);
  }

  function injectUI(node) {
    if (node.querySelector(".adr-answer")) return;

    const locked = (node.dataset.status || "").toLowerCase() === "locked";
    const qid = node.dataset.qid;
    const rec = recText(node);

    if (locked && !node.querySelector(".adr-locked-badge")) {
      const strong = node.querySelector("strong");
      const badge = document.createElement("span");
      badge.className = "adr-locked-badge";
      badge.textContent = "LOCKED";
      if (strong) strong.insertAdjacentElement("afterend", badge);
      else node.insertAdjacentElement("afterbegin", badge);
    }

    const wrap = document.createElement("div");
    wrap.className = "adr-answer";

    if (locked) {
      const prev = document.createElement("div");
      prev.className = "adr-decision-preview";
      prev.textContent =
        "→ " + ((node.dataset.decision || "").trim() || rec);
      wrap.appendChild(prev);
      const notes = document.createElement("textarea");
      notes.className = "adr-notes";
      notes.placeholder = "Optional note (locked decision)";
      notes.dataset.qid = qid;
      notes.addEventListener("input", onChange);
      wrap.appendChild(notes);
      node.appendChild(wrap);
      paintState(node, emptyAnswer(qid, node));
      return;
    }

    const controls = document.createElement("div");
    controls.className = "adr-answer-controls";

    const choices = document.createElement("div");
    choices.className = "adr-choices";
    choices.innerHTML =
      '<label><input type="radio" name="ans-' +
      qid +
      '" value="rec" /> Use rec</label>' +
      '<label><input type="radio" name="ans-' +
      qid +
      '" value="custom" /> Custom</label>' +
      '<label><input type="radio" name="ans-' +
      qid +
      '" value="defer" /> Defer</label>';

    $all('input[type="radio"]', choices).forEach(function (r) {
      r.addEventListener("change", onChange);
    });

    const notes = document.createElement("textarea");
    notes.className = "adr-notes";
    notes.placeholder =
      "Notes or custom decision (required if Custom)…";
    notes.addEventListener("input", onChange);

    const prev = document.createElement("div");
    prev.className = "adr-decision-preview";
    prev.hidden = true;

    controls.appendChild(choices);
    controls.appendChild(notes);
    controls.appendChild(prev);
    wrap.appendChild(controls);
    node.appendChild(wrap);
    node.classList.add("is-open");
  }

  function onChange() {
    const doc = updateProgress();
    qNodes().forEach(function (node) {
      paintState(node, doc.answers[node.dataset.qid]);
    });
  }

  function acceptAllRecs() {
    qNodes().forEach(function (node) {
      if ((node.dataset.status || "").toLowerCase() === "locked") return;
      const radio = node.querySelector('input[type="radio"][value="rec"]');
      if (radio) radio.checked = true;
    });
    onChange();
    toast("Applied “use rec” to all open questions");
  }

  function downloadJSON() {
    const doc = updateProgress();
    const name = adrId() + "-answers.json";
    const blob = new Blob([JSON.stringify(doc, null, 2) + "\n"], {
      type: "application/json",
    });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = name;
    document.body.appendChild(a);
    a.click();
    setTimeout(function () {
      URL.revokeObjectURL(a.href);
      a.remove();
    }, 500);
    toast("Downloaded " + name + " — save it into marble/adr/");
  }

  async function saveWithFilePicker() {
    const doc = updateProgress();
    if (!window.showSaveFilePicker) {
      downloadJSON();
      return;
    }
    try {
      const handle = await window.showSaveFilePicker({
        suggestedName: adrId() + "-answers.json",
        types: [
          {
            description: "ADR answers JSON",
            accept: { "application/json": [".json"] },
          },
        ],
      });
      const writable = await handle.createWritable();
      await writable.write(JSON.stringify(doc, null, 2) + "\n");
      await writable.close();
      toast("Saved answers JSON");
    } catch (e) {
      if (e && e.name === "AbortError") return;
      downloadJSON();
    }
  }

  function copyAgentBlock() {
    const doc = updateProgress();
    const lines = [
      "ADR-" + doc.adr + " answers (" + doc.updated_at + ")",
      "File: adr/" + doc.adr + "-answers.json",
      "",
    ];
    Object.keys(doc.answers)
      .sort(naturalQid)
      .forEach(function (qid) {
        const a = doc.answers[qid];
        const st = (a.status || "open").toUpperCase();
        const dec = a.decision || "(none)";
        lines.push(qid + " [" + st + "]: " + dec);
        if (a.notes && a.choice === CHOICE_CUSTOM) {
          /* decision already notes */
        } else if (a.notes) {
          lines.push("  notes: " + a.notes);
        }
      });
    const text = lines.join("\n");
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(
        function () {
          toast("Copied answer summary for agent");
        },
        function () {
          fallbackCopy(text);
        }
      );
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
      toast("Copied answer summary for agent");
    } catch (e) {
      toast("Copy failed — use Download JSON");
    }
    ta.remove();
  }

  function naturalQid(a, b) {
    const pa = a.match(/^Q(\d+)([a-z]*)$/i);
    const pb = b.match(/^Q(\d+)([a-z]*)$/i);
    if (pa && pb) {
      const na = parseInt(pa[1], 10);
      const nb = parseInt(pb[1], 10);
      if (na !== nb) return na - nb;
      return (pa[2] || "").localeCompare(pb[2] || "");
    }
    return a.localeCompare(b);
  }

  function importJSONText(text) {
    let doc;
    try {
      doc = JSON.parse(text);
    } catch (e) {
      toast("Invalid JSON");
      return;
    }
    if (!doc.answers) {
      toast("JSON missing answers map");
      return;
    }
    applyDoc(doc);
    saveStore(doc);
    syncEmbedded(doc);
    toast("Imported answers for ADR-" + (doc.adr || adrId()));
  }

  function onFile(file) {
    if (!file) return;
    const reader = new FileReader();
    reader.onload = function () {
      importJSONText(String(reader.result || ""));
    };
    reader.readAsText(file);
  }

  function buildToolbar() {
    if ($("#adr-toolbar")) return;
    const bar = document.createElement("div");
    bar.id = "adr-toolbar";
    bar.className = "adr-toolbar";
    bar.innerHTML =
      '<div class="left">' +
      '<span class="progress" id="adr-progress-label"><strong>0/0</strong> answered</span>' +
      '<span class="bar"><i id="adr-progress-bar"></i></span>' +
      "</div>" +
      '<div class="right">' +
      '<button type="button" class="adr-btn ok" data-act="accept-recs">Use rec (all open)</button>' +
      '<button type="button" class="adr-btn primary" data-act="save">Save answers.json</button>' +
      '<button type="button" class="adr-btn" data-act="download">Download</button>' +
      '<button type="button" class="adr-btn" data-act="copy">Copy for agent</button>' +
      '<button type="button" class="adr-btn" data-act="import">Import…</button>' +
      '<button type="button" class="adr-btn warn" data-act="clear">Clear local</button>' +
      "</div>" +
      '<input type="file" accept="application/json,.json" class="adr-hidden" id="adr-import-file" />';

    const main = $("main") || document.body;
    main.insertBefore(bar, main.firstChild);

    bar.addEventListener("click", function (ev) {
      const btn = ev.target.closest("[data-act]");
      if (!btn) return;
      const act = btn.dataset.act;
      if (act === "accept-recs") acceptAllRecs();
      else if (act === "save") saveWithFilePicker();
      else if (act === "download") downloadJSON();
      else if (act === "copy") copyAgentBlock();
      else if (act === "import") $("#adr-import-file").click();
      else if (act === "clear") {
        localStorage.removeItem(storageKey());
        qNodes().forEach(function (node) {
          if ((node.dataset.status || "").toLowerCase() === "locked") return;
          $all('input[type="radio"]', node).forEach(function (r) {
            r.checked = false;
          });
          const notes = node.querySelector(".adr-notes");
          if (notes) notes.value = "";
        });
        onChange();
        toast("Cleared local answers (locked unchanged)");
      }
    });

    $("#adr-import-file").addEventListener("change", function (ev) {
      const f = ev.target.files && ev.target.files[0];
      onFile(f);
      ev.target.value = "";
    });
  }

  function buildHelp() {
    const qs = $("#questions");
    if (!qs || qs.querySelector(".adr-help")) return;
    const p = document.createElement("p");
    p.className = "adr-help";
    p.innerHTML =
      "Answer inline below. Then <strong>Save answers.json</strong> into <code>marble/adr/</code> " +
      "(filename <code>" +
      adrId() +
      "-answers.json</code>). " +
      "Tell the agent: <em>collect answers from ADR-" +
      adrId() +
      "</em>.";
    const h = qs.querySelector("h2");
    if (h) h.insertAdjacentElement("afterend", p);
    else qs.insertAdjacentElement("afterbegin", p);
  }

  function wireDragDrop() {
    document.addEventListener("dragover", function (e) {
      if (e.dataTransfer && e.dataTransfer.types.indexOf("Files") >= 0) {
        e.preventDefault();
      }
    });
    document.addEventListener("drop", function (e) {
      const f = e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files[0];
      if (!f || !/\.json$/i.test(f.name)) return;
      e.preventDefault();
      onFile(f);
    });
  }

  function tryLoadSidecarHint() {
    // If served over http, attempt to fetch sibling answers file.
    if (location.protocol === "file:") return;
    const url = adrId() + "-answers.json";
    fetch(url, { cache: "no-store" })
      .then(function (r) {
        if (!r.ok) throw new Error("no sidecar");
        return r.json();
      })
      .then(function (doc) {
        applyDoc(doc);
        saveStore(doc);
        toast("Loaded " + url);
      })
      .catch(function () {
        /* no sidecar yet */
      });
  }

  function init() {
    buildToolbar();
    buildHelp();
    qNodes().forEach(injectUI);

    const stored = loadStore();
    if (stored) applyDoc(stored);
    else {
      // paint locked defaults
      qNodes().forEach(function (node) {
        paintState(node, emptyAnswer(node.dataset.qid, node));
      });
    }

    updateProgress();
    wireDragDrop();
    tryLoadSidecarHint();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
