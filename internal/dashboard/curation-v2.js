// B06: field-level multidimensional curation.
//
// Design goals:
//   - Already-usable tags are shown by default; raw probabilities stay folded.
//   - One ambiguous candidate never blocks reading or forces a whole-record review.
//   - accept / reject / set-empty / reset are distinct, explicit actions.
//   - Only the touched field is submitted, and every submission carries an
//     operation id plus the expected revision so a retry is idempotent and a
//     stale client cannot silently overwrite a newer decision.
//   - why/status are saved on their own and never produce an accept event.
(() => {
  "use strict";

  const ui = window.CairnUI;
  const match = window.location.pathname.match(/^\/bookmarks\/([1-9][0-9]*)$/);
  const bookmarkID = match ? Number(match[1]) : 0;
  if (!bookmarkID || !ui) return;

  const state = {
    selection: null,
    taxonomy: null,
    available: false,
    dirty: false,
    revision: 0,
    saving: false,
    effective: null,
    // Every new logical action gets its own UUID. Only a retry of the same
    // action reuses it, so the same click on a later visit can never replay a
    // stored response as a silent no-op (F12).
    queue: [],
    inFlight: false,
    conflicted: null
  };

  const DIMENSIONS = [
    { key: "topics", label: "主题", multi: true, max: 64 },
    { key: "content_functions", label: "内容功能", multi: true, max: 8 },
    { key: "carriers", label: "载体", multi: false, max: 1 },
    { key: "affordances", label: "潜在用途", multi: true, max: 8 }
  ];

  function newActionID() {
    if (window.crypto?.randomUUID) return window.crypto.randomUUID();
    return `curation-${bookmarkID}-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  }

  function markDirty(value) {
    state.dirty = value;
    const badge = ui.byId("v2-dirty");
    if (badge) badge.hidden = !value;
  }

  function setStatus(message, isError) {
    const node = ui.byId("v2-status");
    if (!node) return;
    node.textContent = message || "";
    node.hidden = !message;
    node.classList.toggle("error", Boolean(isError));
  }

  function termLabel(dimension, id) {
    const terms = state.taxonomy?.[dimension] || [];
    const term = terms.find((entry) => entry.id === id);
    if (!term) return id;
    return term.active ? term.label : `${term.label}（停用）`;
  }

  function renderDimension(dimension) {
    const holder = ui.byId(`v2-${dimension.key}`);
    if (!holder) return;
    holder.replaceChildren();
    const terms = state.taxonomy?.[dimension.key] || [];
    const selected = state.selection?.[dimension.key] || [];
    let visible = terms.filter((term) => term.active);
    // Historical/inactive values stay readable and removable.
    for (const id of selected) {
      if (!terms.some((term) => term.id === id)) {
        visible.push({ id, label: id, active: false });
      }
    }
    for (const term of visible) {
      const label = ui.element("label", "v2-term");
      const input = document.createElement("input");
      input.type = dimension.multi ? "checkbox" : "radio";
      input.name = `v2-${dimension.key}`;
      input.value = term.id;
      input.checked = selected.includes(term.id);
      input.dataset.field = dimension.key;
      input.dataset.term = term.id;
      input.dataset.active = String(term.active !== false);
      label.append(input, document.createTextNode(termLabel(dimension.key, term.id)));
      holder.append(label);
    }
    // An explicit "nothing applies" control, distinct from resetting.
    const emptyRow = ui.element("div", "v2-empty-row");
    const emptyButton = ui.element("button", "text-btn", "这一项都不适用");
    emptyButton.type = "button";
    emptyButton.dataset.action = "set_empty";
    emptyButton.dataset.field = dimension.key;
    const resetButton = ui.element("button", "text-btn", "恢复自动");
    resetButton.type = "button";
    resetButton.dataset.action = "reset";
    resetButton.dataset.field = dimension.key;
    emptyRow.append(emptyButton, resetButton);
    holder.append(emptyRow);
  }

  function render() {
    const panel = ui.byId("v2-curation");
    if (!panel) return;
    if (!state.available) {
      panel.hidden = true;
      return;
    }
    panel.hidden = false;
    for (const dimension of DIMENSIONS) renderDimension(dimension);
    // The fourth effective topic stays visible; the note explains the fold.
    const topics = state.selection?.topics || [];
    const folded = ui.byId("v2-folded-note");
    if (folded) {
      folded.hidden = topics.length <= 3;
      folded.textContent = topics.length > 3 ? `卡片只展示前 3 个主题；另外 ${topics.length - 3} 个仍然保留。` : "";
    }
  }

  // applyLocal updates the draft without a round trip so the UI responds
  // immediately. The authoritative value is only the server response.
  function applyLocal(field, term, action) {
    const selection = state.selection;
    if (!selection) return;
    const dimension = DIMENSIONS.find((entry) => entry.key === field);
    const current = selection[field] || [];
    switch (action) {
      case "accept":
        // A single-valued dimension replaces; a multi-valued one accumulates.
        if (dimension && dimension.multi === false) selection[field] = term ? [term] : [];
        else if (!current.includes(term)) selection[field] = [...current, term];
        break;
      case "reject":
      case "reset":
        selection[field] = current.filter((id) => id !== term);
        break;
      case "set_empty":
        selection[field] = [];
        break;
    }
    markDirty(true);
    render();
  }

  // enqueue records one logical action and serialises submission. Nothing is
  // dropped while a request is in flight: the action is queued and submitted
  // afterwards with its own identity (F12).
  function enqueue(field, term, action) {
    if (state.conflicted) {
      setStatus("请先处理上面的冲突（重新应用或放弃修改），再继续编辑。", true);
      return;
    }
    const entry = { id: newActionID(), field, term, action };
    applyLocal(field, term, action);
    state.queue.push(entry);
    setBusy(true);
    pump();
  }

  // setBusy marks the panel busy for assistive technology but never disables
  // the controls: a rapid second action is queued, not silently dropped (F12).
  function setBusy(busy) {
    const panel = ui.byId("v2-curation");
    if (!panel) return;
    panel.setAttribute("aria-busy", busy ? "true" : "false");
  }

  async function pump() {
    if (state.inFlight || state.queue.length === 0 || state.conflicted) return;
    const entry = state.queue[0];
    state.inFlight = true;
    state.saving = true;
    setStatus("保存中…");
    try {
      const payload = await ui.fetchJSON(`/api/bookmarks/${bookmarkID}/v2-override`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          field: entry.field, term: entry.term, action: entry.action,
          operation_key: entry.id,
          expected_revision: state.revision
        })
      });
      state.revision = payload.revision ?? state.revision;
      state.queue.shift();
      setStatus(state.queue.length > 0 ? "保存中…" : "已保存");
      if (state.queue.length === 0) markDirty(false);
      state.inFlight = false;
      state.saving = false;
      setBusy(state.queue.length > 0);
      if (state.queue.length > 0) {
        pump();
      } else {
        await load();
      }
    } catch (error) {
      state.inFlight = false;
      state.saving = false;
      const code = error?.message || "override_failed";
      if (code === "revision_conflict" || code === "snapshot_conflict") {
        // Keep the draft and the queued action, adopt the server's revision and
        // require an explicit re-apply. Loading the server value here would
        // silently discard the user's unsaved intent.
        if (typeof error.revision === "number") state.revision = error.revision;
        state.conflicted = entry;
        setStatus("这条整理已被其他客户端更新。草稿已保留，请点“重新应用”提交你的修改。", true);
        renderConflict();
      } else {
        // A transient failure keeps the same action id, so the retry is the
        // same logical commit rather than a new one.
        setStatus(`保存失败：${ui.errorLabel(code)}。可重试。`, true);
        renderConflict();
      }
      setBusy(false);
    }
  }

  function renderConflict() {
    const holder = ui.byId("v2-conflict");
    if (!holder) return;
    holder.replaceChildren();
    holder.hidden = false;
    const retry = ui.element("button", "text-btn", "重试保存");
    retry.type = "button";
    retry.addEventListener("click", () => {
      holder.hidden = true;
      // Clear the conflict before pumping, otherwise the guard would refuse to
      // resubmit the preserved action.
      state.conflicted = null;
      setBusy(true);
      pump();
    });
    const discard = ui.element("button", "text-btn", "放弃我的修改");
    discard.type = "button";
    discard.addEventListener("click", async () => {
      holder.hidden = true;
      state.queue = [];
      state.conflicted = null;
      markDirty(false);
      setStatus("");
      await load();
    });
    holder.append(retry, discard);
  }

  async function load() {
    // A poll or refresh must never overwrite an unsaved draft or a pending
    // action (F12).
    if (state.dirty || state.queue.length > 0 || state.conflicted) return;
    try {
      const response = await ui.fetchJSON(`/api/bookmarks/${bookmarkID}/v2-selection`);
      state.available = Boolean(response.available);
      if (state.available) {
        state.selection = response.selection;
        // The revision authorises a CAS write; without it the client must not
        // pretend to have one.
        if (typeof response.revision === "number") state.revision = response.revision;
        render();
      }
    } catch (_) {
      state.available = false;
      render();
    }
  }

  async function loadTaxonomy() {
    try {
      const vocabulary = await ui.fetchJSON("/api/v2-taxonomy");
      if (vocabulary && vocabulary.available === false) return;
      state.taxonomy = vocabulary;
    } catch (_) {
      state.taxonomy = null;
    }
  }

  function onClick(event) {
    const target = event.target;
    if (!(target instanceof HTMLElement)) return;
    const action = target.dataset?.action;
    if (!action) return;
    enqueue(target.dataset.field, target.dataset.term || "", action);
  }

  function onChange(event) {
    const target = event.target;
    if (!(target instanceof HTMLInputElement)) return;
    const field = target.dataset.field;
    if (!field) return;
    enqueue(field, target.dataset.term, target.checked ? "accept" : "reject");
  }

  document.addEventListener("click", onClick);
  document.addEventListener("change", onChange);
  window.addEventListener("beforeunload", (event) => {
    if (state.dirty) {
      event.preventDefault();
      event.returnValue = "";
    }
  });

  loadTaxonomy().then(load);
})();
