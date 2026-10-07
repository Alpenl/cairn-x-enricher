import { api, errorLabel, newOperationKey } from "./api.js";
import { byId, h } from "./dom.js";
import { icon } from "./icons.js";
import { emit, getItem, mergeItem } from "./store.js";
import { loadCustomTags, termActive, termLabel, terms, vocab } from "./taxonomy.js";
import { confirmAction, openDialog, openMenu, toast } from "./ui.js";
import { PRIMARY_TAG_FIELDS, findTagName, parseTagRef, previewTagActions, rejectedTags, reviewCandidates, tagFieldState, tagOrigin, tagRef, tagStatusText } from "./tag-state.js";

const sessions = new Map();
let currentID = 0;
let active = false;
let renderSupplement = null;
export function setTagSupplement(renderer) { renderSupplement = renderer; }
function sessionFor(id) {
  if (!sessions.has(id)) sessions.set(id, { id, status: "idle", payload: null, draft: null, queue: [], saving: false, blocked: null, epoch: 0, identity: "" });
  if (sessions.size > 150) for (const [key, value] of sessions) {
    if (key !== currentID && !value.queue.length && !value.saving && !value.blocked) sessions.delete(key);
    if (sessions.size <= 100) break;
  }
  return sessions.get(id);
}
function loadCatalog() {
  return loadCustomTags().catch(() => {});
}
export function hasUnsavedTagWork() { return [...sessions.values()].some((session) => session.queue.length || session.saving || session.blocked); }
export function tagSystemReady(id) { return sessionFor(id).status === "ready"; }
export function tagSystemOverview(id) {
  const session = sessions.get(id);
  if (session?.status !== "ready") return null;
  if (!session.queue.length && !session.saving && !session.blocked && session.identity !== JSON.stringify(getItem(id)?.cache_identity || {})) return null;
  const payload = session.draft || session.payload;
  return { selection: payload.selection, custom_tags: payload.custom_tags || [], blocked: Boolean(session.blocked) };
}
export function showTagSystem(id) {
  if (currentID !== id) suspendTagSystem();
  currentID = id; active = true;
  const session = sessionFor(id);
  if (vocab.tagSystemAvailable === false) { session.status = "unsupported"; return; }
  if (!byId("curate")?.open) return;
  if (["idle", "error"].includes(session.status)) load(session);
  else if (session.status === "ready") refreshTagSystem(id);
}
export function refreshTagSystem(id) {
  const session = sessionFor(id);
  if (!active || !byId("curate")?.open || id !== currentID) return;
  const identity = JSON.stringify(getItem(id)?.cache_identity || {});
  if (session.status === "ready" && identity !== session.identity && !session.queue.length && !session.blocked && !session.saving) load(session);
}
export function suspendTagSystem() {
  active = false;
  const session = sessions.get(currentID);
  if (session?.controller && !session.queue.length && !session.saving) session.controller.abort();
}
function load(session) {
  if (session.queue.length || session.saving || session.blocked) return;
  if (session.flight) return session.flight;
  let outcome;
  const flight = loadSession(session).then(value => { outcome = value; }).finally(() => {
    if (session.flight === flight) session.flight = null;
    if (!active || session.id !== currentID || !byId("curate")?.open) return;
    if (outcome === "aborted") load(session);
    else if (outcome === "success") refreshTagSystem(session.id);
  });
  session.flight = flight;
  return flight;
}
async function loadSession(session) {
  const epoch = ++session.epoch;
  const identity = JSON.stringify(getItem(session.id)?.cache_identity || {});
  const controller = new AbortController(); session.controller = controller;
  if (!session.payload) session.status = "loading";
  try {
    const payload = await api.tags(session.id, { signal: controller.signal });
    if (epoch !== session.epoch || session.queue.length || session.saving || session.blocked) return;
    if (payload.available === false || !payload.selection || !Number.isSafeInteger(payload.revision)) {
      session.status = "unsupported";
      vocab.tagSystemAvailable = false;
      emit("tag-system:fallback", session.id);
      return;
    }
    session.payload = payload;
    session.draft = structuredClone(payload);
    session.status = "ready";
    session.identity = identity;
    vocab.tagSystemAvailable = true;
    void loadCatalog();
    return "success";
  } catch (error) {
    if (error.name === "AbortError") { session.status = session.payload ? "ready" : "idle"; return "aborted"; }
    if ([404, 405].includes(error.status) || ["tag_system_unsupported", "v2_unsupported"].includes(error.message)) {
      session.status = "unsupported";
      if (!Array.isArray(vocab.v2?.resource_kinds)) vocab.tagSystemAvailable = false;
      emit("tag-system:fallback", session.id);
    } else {
      session.status = session.payload ? "ready" : "error";
      session.error = errorLabel(error.message);
    }
  } finally {
    if (session.controller === controller) session.controller = null;
    if (session.id === currentID) renderTagSystem(session.id);
  }
}
function labelFor(ref) {
  const parsed = parseTagRef(ref);
  return parsed ? termLabel(parsed.field, parsed.term) : String(ref || "标签");
}
function queue(session, actions) {
  if (session.blocked) { toast("请先重试或放弃尚未保存的标签修改", { tone: "error" }); return; }
  let resolve;
  const completion = new Promise((done) => { resolve = done; });
  const entry = { actions, key: newOperationKey(`tags-${session.id}`), request: null, resolve };
  session.queue.push(entry);
  session.draft = previewTagActions(session.draft || session.payload, actions, vocab.custom);
  renderTagSystem(session.id);
  pump(session);
  return completion;
}
async function pump(session) {
  if (session.saving || session.blocked || !session.queue.length) return;
  const entry = session.queue[0];
  session.saving = true;
  entry.request ||= { operation_key: entry.key, expected_revision: session.payload.revision,
    expected_decision_id: session.payload.decision_id ?? null, expected_content_revision: session.payload.content_revision, actions: entry.actions };
  renderTagSystem(session.id);
  try {
    const result = await api.editTags(session.id, entry.request);
    entry.resolve?.(true);
    session.payload = result;
    session.queue.shift();
    session.saving = false;
    session.draft = session.queue.reduce((payload, queued) => previewTagActions(payload, queued.actions, vocab.custom), structuredClone(result));
    if (entry.actions.some((action) => action.action !== "undo")) {
      toast("标签已保存", { action: { label: "撤销", key: "Z", run: () => queue(session, [{ action: "undo", operation_id: result.operation_id || entry.key }]) } });
    } else toast("已撤销，自动标签使用最新判断");
    emit("tags:changed", session.id);
    if (session.queue.length) { pump(session); return; }
    try { const item = await api.detailFresh(session.id); if (item) mergeItem(item); emit("item", session.id); } catch { /* saved tags remain visible */ }
  } catch (error) {
    entry.resolve?.(false);
    session.saving = false;
    const conflict = ["revision_conflict", "snapshot_conflict", "input_changed", "decision_conflict"].includes(error.message);
    session.blocked = { conflict, error: errorLabel(error.message), current: null };
    if (conflict) {
      try { session.blocked.current = await api.tags(session.id); } catch { /* a retry will refresh */ }
    }
  }
  renderTagSystem(session.id);
}
function tagMenu(session, field, term, anchor) {
  const payload = session.draft;
  const state = tagFieldState(payload, field);
  const human = (state.values || []).find((entry) => entry.term === term)?.origin === "human";
  const options = [
    { label: "确认这个标签", icon: "check", run: () => queue(session, [{ action: "confirm", tag_ref: tagRef(field, term) }]) },
    { label: "替换为其他标签", icon: "pencil", run: () => openPicker(session, { field, from: term }) },
    { label: "恢复这个标签的自动判断", icon: "refresh", disabled: !human && !rejectedTags(payload, field).includes(term), run: () => queue(session, [{ action: "reset", tag_ref: tagRef(field, term) }]) }
  ];
  openMenu(anchor, options, { onClose: () => anchor.focus({ preventScroll: true }) });
}
function effectiveChip(session, field, term) {
  const label = termLabel(field, term);
  const origin = tagOrigin(session.draft, field, term);
  const name = h("button.tag-name", { type: "button", title: `${origin}；点击筛选`, onclick: () => emit("tag-filter-request", { field, term }) }, label);
  const menu = h("button.tag-action", { type: "button", "aria-label": `编辑${label}`, title: `编辑${label}` }, icon("more", 12));
  menu.addEventListener("click", () => tagMenu(session, field, term, menu));
  const kind = origin === "自动标签" ? "auto" : origin === "你已确认" ? "confirmed" : origin === "你添加" ? "added" : "unknown";
  return h("span.tag-system-chip", { title: origin, dataset: { origin: kind } }, h("span.tag-dot", { "aria-hidden": "true" }), name, h("small.tag-origin.visually-hidden", origin), menu,
    h("button.tag-remove", { type: "button", "aria-label": `移除${label}`, title: "从这条收藏移除", onclick: () => queue(session, [{ action: "reject", tag_ref: tagRef(field, term) }]) }, icon("x", 12)));
}
function groupMenu(session, field, label, anchor) {
  openMenu(anchor, [
    { label: "这一组都不适用", hint: "清空本组并关闭自动新增", run: async () => {
      if (await confirmAction({ title: `清空${label}`, message: `会移除这条收藏的全部${label}并关闭本组自动新增。其他组和自定义标记保持原样。`, confirmLabel: "清空这一组" })) queue(session, [{ action: "set_empty", dimension: field }]);
    } },
    { label: "这一组恢复自动", hint: "解除本组全部人工决定", run: async () => {
      if (await confirmAction({ title: `${label}恢复自动`, message: "会解除本组采用、排除和关闭自动新增的人工决定，使用最新自动结果。", confirmLabel: "恢复这一组" })) queue(session, [{ action: "reset_group", dimension: field }]);
    } }
  ], { onClose: () => anchor.focus({ preventScroll: true }) });
}
function conflictView(session) {
  const blocked = session.blocked;
  if (!blocked) return null;
  const retry = h("button.btn.btn-sm.btn-primary", { type: "button" }, blocked.conflict ? "重新应用我的修改" : "重试保存");
  retry.addEventListener("click", async () => {
    if (blocked.conflict) {
      try {
        session.payload = await api.tags(session.id);
        for (const entry of session.queue) { entry.key = newOperationKey(`tags-${session.id}`); entry.request = null; }
      } catch (error) { toast(errorLabel(error.message), { tone: "error" }); return; }
    }
    session.blocked = null;
    pump(session);
  });
  const discard = h("button.btn.btn-sm", { type: "button", onclick: async () => {
    session.queue = []; session.blocked = null; session.draft = structuredClone(session.payload); await load(session);
  } }, "放弃我的修改");
  const difference = blocked.current ? PRIMARY_TAG_FIELDS.map(({ key, label }) => `${label}：${(blocked.current.selection?.[key] || []).map((term) => termLabel(key, term)).join("、") || "无"}`).join("；") : "";
  return h("div.tag-system-conflict", { role: "alert" }, h("p", `${blocked.error}。你的修改还保留着。`), difference ? h("p", `最新结果：${difference}`) : null, h("div.conflict-actions", retry, discard));
}
export function renderTagSystem(id) {
  const session = sessionFor(id);
  if (session.status === "idle" || session.status === "unsupported") return false;
  if (id !== currentID) return true;
  const holder = byId("tag-rows");
  if (!holder) return false;
  byId("confirm-classification").hidden = true;
  byId("reset-classification").hidden = true;
  byId("review-state").textContent = "";
  byId("v2-conflict").hidden = true;
  if (session.status === "loading") {
    holder.replaceChildren(h("p.tag-system-status", { role: "status" }, "正在读取标签…"));
    emit("tag-system:render", id);
    return true;
  }
  if (session.status === "error") {
    holder.replaceChildren(h("p.tag-system-status", { role: "alert" }, session.error), h("button.btn.btn-sm", { type: "button", onclick: () => load(session) }, "重试读取标签"));
    emit("tag-system:render", id);
    return true;
  }
  const active = document.activeElement;
  const savedFocus = holder.contains(active) ? {
    id: active.id, label: active.getAttribute("aria-label"), className: active.className, text: active.textContent,
    value: active instanceof HTMLInputElement ? active.value : undefined, start: active.selectionStart, end: active.selectionEnd
  } : null;
  const disclosures = new Map([...holder.querySelectorAll("details")].map((details) => [details.className, details.open]));
  const payload = session.draft || session.payload;
  const rows = PRIMARY_TAG_FIELDS.map(({ key, label }) => {
    const selected = payload.selection?.[key] || [];
    const content = h("div.tag-system-values", selected.map((term) => effectiveChip(session, key, term)));
    if (!selected.length) content.append(h("span.chip-empty", tagStatusText(payload, key) || "暂无匹配标签"));
    const groupMore = h("button.icon-btn", { type: "button", "aria-label": `${label}更多操作` }, icon("more", 14));
    groupMore.addEventListener("click", () => groupMenu(session, key, label, groupMore));
    const stateText = selected.length ? tagStatusText(payload, key) : "";
    return h("div.tag-system-row", { dataset: { dimension: key } }, h("div.tag-system-head", h("span.curate-label", label), groupMore), content,
      stateText ? h("small.tag-system-status", stateText) : null);
  });
  if (payload.source_state?.status === "empty") rows.unshift(h("p.tag-system-status", "暂无足够内容判断标签；已有人工标签保留，可以继续添加。"));
  else if (payload.source_state?.status === "partial") rows.unshift(h("p.tag-system-status", "标签仅依据已存档片段，原文补齐后可重新判断。"));
  if (payload.custom_tags?.length) rows.push(h("div.tag-system-row", { dataset: { dimension: "custom_tags" } }, h("div.tag-system-head", h("span.curate-label", "自定义标记")), h("div.tag-system-values", payload.custom_tags.map((tag) => {
    const menu = h("button.tag-action", { type: "button", "aria-label": `管理${tag.label}` }, icon("more", 12));
    menu.addEventListener("click", () => customMenu(session, tag, menu));
    return h("span.tag-system-chip.custom", h("button.tag-name", { type: "button", onclick: () => emit("tag-filter-request", { field: "custom_tags", term: tag.id }) }, tag.label), menu,
      h("button.tag-remove", { type: "button", "aria-label": `移除${tag.label}`, onclick: () => queue(session, [{ action: "detach", tag_ref: tag.tag_ref }]) }, icon("x", 12)));
  }))));
  const suggestions = PRIMARY_TAG_FIELDS.flatMap(({ key }) => reviewCandidates(payload, key).map((candidate) => ({ ...candidate, field: key })));
  if (suggestions.length) rows.push(h("details.tag-suggestions", h("summary", `其他建议（${suggestions.length}）`), h("p.tag-system-status", "这些候选尚未作为标签，不参与默认筛选。"),
    h("div.tag-system-values", suggestions.map((entry) => h("button.chip.option", { type: "button", title: "采用这项建议", onclick: () => queue(session, [{ action: "accept", tag_ref: tagRef(entry.field, entry.term_id) }]) }, termLabel(entry.field, entry.term_id), icon("plus", 12))))));
  const autoCount = PRIMARY_TAG_FIELDS.reduce((n, { key }) => n + (payload.selection?.[key] || []).filter((term) => tagOrigin(payload, key, term) === "自动标签").length, 0);
  rows.push(h("div.tag-legend", h("span", h("i.is-auto"), "自动标签"), h("span", h("i.is-confirmed"), "你已确认"), h("span", h("i.is-added"), "你添加"),
    autoCount ? h("button.link-btn.small", { type: "button", title: "确认全部自动标签 (A)", onclick: () => queue(session, PRIMARY_TAG_FIELDS.flatMap(({ key }) => (payload.selection?.[key] || []).filter((term) => tagOrigin(payload, key, term) === "自动标签").map((term) => ({ action: "confirm", tag_ref: tagRef(key, term) })))) }, `确认 ${autoCount} 个自动标签`) : null));
  rows.push(h("div.tag-system-toolbar", h("button.btn.btn-sm", { type: "button", onclick: () => openPicker(session) }, icon("plus", 14), "添加标签"),
    h("button.link-btn.small", { type: "button", onclick: () => showHistory(session) }, "查看变更"),
    vocab.custom.length ? h("button.link-btn.small", { type: "button", onclick: () => manageCustomTags(session) }, "管理自定义标记") : null,
    h("span.tag-save-state", { role: "status", "aria-live": "polite" }, session.saving || session.queue.length ? "保存中…" : "")));
  const conflict = conflictView(session);
  if (conflict) rows.push(conflict);
  holder.replaceChildren(...rows);
  renderSupplement?.();
  for (const details of holder.querySelectorAll("details")) {
    if (disclosures.has(details.className)) details.open = disclosures.get(details.className);
  }
  if (savedFocus) {
    const previous = [...holder.querySelectorAll("button, input")].find((node) => savedFocus.id ? node.id === savedFocus.id
      : node.className === savedFocus.className && (savedFocus.label ? node.getAttribute("aria-label") === savedFocus.label : node.textContent === savedFocus.text));
    if (previous && savedFocus.value !== undefined) {
      previous.value = savedFocus.value;
      previous.setSelectionRange(savedFocus.start, savedFocus.end);
    }
    (previous || holder.querySelector(".tag-system-toolbar button"))?.focus({ preventScroll: true });
  }
  emit("tag-system:render", id);
  return true;
}
function catalogEntries() {
  return PRIMARY_TAG_FIELDS.flatMap(({ key, label }) => terms(key).map((term) => ({ ...term, field: key, group: label, tag_ref: tagRef(key, term.id) })))
    .concat(vocab.custom.map((tag) => ({ ...tag, field: "custom_tags", group: "自定义", tag_ref: tag.tag_ref })));
}
function focusToolbar(session, label) {
  if (session.id !== currentID || !byId("curate")?.open || document.querySelector("wa-dialog[open], dialog[open]")) return;
  [...byId("tag-rows").querySelectorAll(".tag-system-toolbar button")].find((button) => button.textContent === label)?.focus({ preventScroll: true });
}
async function openPicker(session, { field, from } = {}) {
  await loadCatalog();
  if (session.id !== currentID || !byId("curate")?.open) return;
  const input = h("input.tag-search", { type: "search", placeholder: "搜索标签或创建自定义标记", "aria-label": "搜索标签", maxLength: 80 });
  const results = h("div.tag-search-results");
  const error = h("p.tag-system-status", { role: "alert" });
  let createRequest = null;
  let dialog;
  const render = () => {
    const query = input.value.trim();
    const needle = query.normalize("NFKC").toLowerCase();
    const catalog = catalogEntries();
    const matches = catalog.filter((tag) => (!field || tag.field === field) && tag.active !== false &&
      (!needle || [tag.label, ...(tag.aliases || [])].some((name) => name.toLowerCase().includes(needle))));
    results.replaceChildren();
    for (const tag of matches) {
      const selected = tag.field === "custom_tags" ? session.draft.custom_tags.some((entry) => entry.id === tag.id) : (session.draft.selection[tag.field] || []).includes(tag.id);
      const button = h("button.tag-search-result", { type: "button", disabled: selected, onclick: () => {
        queue(session, [from ? { action: "replace", from_tag_ref: tagRef(field, from), to_tag_ref: tag.tag_ref }
          : { action: tag.field === "custom_tags" ? "attach" : "accept", tag_ref: tag.tag_ref }]); dialog.close();
      } }, h("span", tag.label), h("small", `${tag.group}${selected ? " · 已添加" : ""}`));
      results.append(button);
    }
    if (!matches.length) results.append(h("p.tag-system-status", "没有找到匹配标签"));
    if (!field && query && !findTagName(query, catalog)) {
      results.append(h("button.tag-search-result", { type: "button", onclick: async () => {
        createRequest = createRequest?.label === query ? createRequest : { label: query, operation_key: newOperationKey("create-custom") };
        try {
          const result = await api.createCustomTag(createRequest);
          await loadCatalog();
          queue(session, [{ action: "attach", tag_ref: result.tag.tag_ref }]); dialog.close();
        } catch (failure) { error.textContent = errorLabel(failure.message); }
      } }, icon("plus", 14), h("span", `创建自定义标记「${query}」`)));
    }
    const rejected = PRIMARY_TAG_FIELDS.filter((entry) => !field || field === entry.key).flatMap(({ key }) => rejectedTags(session.draft, key).map((term) => ({ key, term })));
    if (!query && rejected.length) {
      results.append(h("p.tag-system-status", "你已移除的标签"));
      for (const { key, term } of rejected) results.append(h("div.tag-search-result", h("span", termLabel(key, term)),
        h("button.link-btn.small", { type: "button", onclick: () => { queue(session, [{ action: "accept", tag_ref: tagRef(key, term) }]); dialog.close(); } }, "重新添加"),
        h("button.link-btn.small", { type: "button", "aria-label": `恢复${termLabel(key, term)}的自动判断`, onclick: () => { queue(session, [{ action: "reset", tag_ref: tagRef(key, term) }]); dialog.close(); } }, "恢复自动")));
    }
  };
  input.addEventListener("input", render);
  dialog = openDialog({ title: from ? `替换「${termLabel(field, from)}」` : "添加标签", body: [input, error, results], initialFocus: () => input,
    onClose: () => focusToolbar(session, "添加标签") });
  render();
}
function customMenu(session, tag, anchor) {
  openMenu(anchor, [
    { label: "修改标记名称", hint: "所有关联收藏使用同一个名称", run: () => {
      const input = h("input.tag-search", { value: tag.label, maxLength: 80, "aria-label": "自定义标记名称" });
      const message = h("p.tag-system-status");
      let request = null;
      openDialog({ title: "修改自定义标记", body: [h("p", "此操作修改这个标记的全库显示名称，归属和历史保持原身份。"), input, message],
        actions: [{ label: "取消" }, { label: "保存名称", primary: true, run: async () => {
          const label = input.value.trim();
          if (!label) { message.textContent = "请输入名称"; return false; }
          const duplicate = findTagName(label, catalogEntries().filter((entry) => entry.tag_ref !== tag.tag_ref));
          if (duplicate) { message.textContent = `已有「${duplicate.label}」，请使用可区分的名称`; return false; }
          request = request?.label === label ? request : { label, expected_revision: tag.revision, operation_key: newOperationKey("rename-custom") };
          try { await api.renameCustomTag(tag.id, request); await loadCatalog(); await load(session); return true; }
          catch (error) { message.textContent = errorLabel(error.message); return false; }
        } }] });
    } },
    { label: "管理自定义标记", run: () => manageCustomTags(session) }
  ], { onClose: () => anchor.focus({ preventScroll: true }) });
}
function manageCustomTags(session) {
  const body = h("div");
  for (const tag of vocab.custom.filter((entry) => entry.active !== false)) {
    const archive = h("button.link-btn.small", { type: "button", onclick: async () => {
      const count = tag.link_count || 0;
      if (!await confirmAction({ title: `停用「${tag.label}」`, message: count ? `这个标记关联 ${count} 条收藏。停用并移除全部归属后，历史仍可追溯。` : "停用后不再供新添加，历史仍可追溯。", confirmLabel: "停用标记" })) return;
      try { await api.archiveCustomTag(tag.id, { expected_revision: tag.revision, operation_key: newOperationKey("archive-custom"), detach_all: true }); await loadCatalog(); await load(session); archive.closest(".tag-manager-row")?.remove(); }
      catch (error) { toast(errorLabel(error.message), { tone: "error" }); }
    } }, "停用");
    body.append(h("div.tag-manager-row", h("span", tag.label), h("small", `${tag.link_count || 0} 条收藏`), archive));
  }
  openDialog({ title: "自定义标记", body });
}
async function showHistory(session) {
  const body = h("div.tag-history", h("p", "正在读取变更…"));
  let before = null;
  let loading = false;
  const more = h("button.btn.btn-sm", { type: "button" }, "加载更早记录");
  const names = { accept: "添加", confirm: "确认", reject: "移除", reset: "恢复自动", replace: "替换", set_empty: "清空一组", reset_group: "整组恢复自动", attach: "添加自定义标记", detach: "移除自定义标记", undo: "撤销" };
  const seen = new Set();
  const read = async () => {
    if (loading) return;
    loading = true; more.disabled = true;
    try {
      const payload = await api.tagHistory(session.id, before);
      if (!before) body.replaceChildren();
      for (const event of payload.events || []) {
        const operation = event.operation_id || event.operation_key;
        const replace = event.actions?.find((action) => action.action === "replace");
        const message = replace ? `将「${labelFor(replace.from_tag_ref)}」替换为「${labelFor(replace.to_tag_ref)}」`
          : `${names[event.action] || event.action || "标签变更"}${event.tag_ref ? `「${labelFor(event.tag_ref)}」` : ""}`;
        const row = h("div.tag-history-entry", h("p", message), h("small", `${event.actor_type === "model" ? "AI" : event.actor_type === "migration" ? "迁移" : event.actor_type === "human" || event.actor_type === "user" ? "你" : "历史操作者未知"} · ${event.created_at || event.at || "时间未知"}`));
        if (event.actions?.length) row.append(h("details.tag-history-detail", h("summary", "查看变更范围"),
          h("ul", event.actions.map((action) => h("li", action.action === "replace"
            ? `将「${labelFor(action.from_tag_ref)}」替换为「${labelFor(action.to_tag_ref)}」`
            : `${names[action.action] || action.action}${action.tag_ref ? `「${labelFor(action.tag_ref)}」` : action.dimension ? `：${PRIMARY_TAG_FIELDS.find((field) => field.key === action.dimension)?.label || action.dimension}` : ""}`)))));
        if (operation && !seen.has(operation) && event.action !== "undo") row.append(h("button.link-btn.small", { type: "button", onclick: () => queue(session, [{ action: "undo", operation_id: operation }]) }, "撤销这次操作"));
        seen.add(operation);
        body.append(row);
      }
      if (!(payload.events || []).length && !before) body.append(h("p", "还没有人工标签变更"));
      before = payload.next_before_id;
      more.hidden = !before;
      if (before) body.append(more);
    } catch (error) { body.append(h("p", { role: "alert" }, errorLabel(error.message))); }
    loading = false; more.disabled = false;
  };
  more.addEventListener("click", read);
  openDialog({ title: "标签变更", body, onClose: () => focusToolbar(session, "查看变更") });
  read();
}
export async function confirmTagSystem(id) {
  const session = sessionFor(id);
  if (session.status === "idle") await load(session);
  if (session.status !== "ready") return null;
  const actions = PRIMARY_TAG_FIELDS.flatMap(({ key }) => (session.draft.selection?.[key] || [])
    .filter((term) => tagOrigin(session.draft, key, term) === "自动标签")
    .map((term) => ({ action: "confirm", tag_ref: tagRef(key, term) })));
  if (!actions.length) return false;
  return queue(session, actions);
}
export function editTagSystem(id) {
  const session = sessionFor(id);
  if (session.status === "unsupported" || !Array.isArray(vocab.v2?.resource_kinds)) return false;
  if (session.status === "ready") { openPicker(session); return true; }
  Promise.resolve(load(session)).then(loadCatalog).then(() => {
    if (session.status === "ready" && id === currentID && byId("curate")?.open) openPicker(session);
  });
  return true;
}
