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
    operationSeq: 0,
    saving: false,
    effective: null
  };

  const DIMENSIONS = [
    { key: "topics", label: "主题", multi: true, max: 64 },
    { key: "content_functions", label: "内容功能", multi: true, max: 8 },
    { key: "carriers", label: "载体", multi: false, max: 1 },
    { key: "affordances", label: "潜在用途", multi: true, max: 8 }
  ];

  function nextOperationKey(action, field, term) {
    state.operationSeq += 1;
    return `curation-${bookmarkID}-${action}-${field}-${term || "none"}-${state.operationSeq}`;
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
    const current = selection[field] || [];
    switch (action) {
      case "accept":
        if (!current.includes(term)) selection[field] = [...current, term];
        break;
      case "reject":
      case "reset":
        if (action === "reject") selection[field] = current.filter((id) => id !== term);
        else selection[field] = current.filter((id) => id !== term);
        break;
      case "set_empty":
        selection[field] = [];
        break;
    }
    markDirty(true);
    render();
  }

  async function submitOverride(field, term, action) {
    if (state.saving) return;
    state.saving = true;
    setStatus("");
    try {
      const payload = await ui.fetchJSON(`/api/bookmarks/${bookmarkID}/v2-override`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          field, term, action,
          operation_key: nextOperationKey(action, field, term),
          expected_revision: state.revision
        })
      });
      state.revision = payload.revision ?? state.revision;
      setStatus("已保存");
      markDirty(false);
      await load();
    } catch (error) {
      const code = error?.message || "override_failed";
      if (code === "revision_conflict") {
        // A stale client must see the real difference and re-apply explicitly,
        // never silently last-write-win.
        setStatus("这条整理已被其他客户端更新，请确认后重新提交。", true);
        await load();
      } else {
        setStatus(`保存失败：${ui.errorLabel(code)}`, true);
      }
    } finally {
      state.saving = false;
    }
  }

  async function load() {
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
    const field = target.dataset.field;
    const term = target.dataset.term || "";
    if (action === "set_empty" || action === "reset") {
      applyLocal(field, term, action);
      submitOverride(field, term, action);
      return;
    }
    applyLocal(field, term, action);
    submitOverride(field, term, action);
  }

  function onChange(event) {
    const target = event.target;
    if (!(target instanceof HTMLInputElement)) return;
    const field = target.dataset.field;
    if (!field) return;
    const term = target.dataset.term;
    if (target.checked) applyLocal(field, term, "accept");
    else applyLocal(field, term, "reject");
    submitOverride(field, term, target.checked ? "accept" : "reject");
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
