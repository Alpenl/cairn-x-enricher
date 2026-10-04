// The library list: one dense, keyboard-navigable column of bookmarks grouped
// by day, with search highlighting, infinite scroll and multi-select.
import { api, errorLabel, imagePath, invalidateQueryReads, previewSearch } from "./api.js";
import { append, byId, clear, h, highlightInto } from "./dom.js";
import {
  bucketLabel, curationShort, displaySummary, displayTitle, formatFull, highlightRanges, isWorking,
  listTime, searchExcerpt, searchTerms, sourceLabels
} from "./format.js";
import { icon } from "./icons.js";
import { activeView, apiParams, facetFilterCount, needsFilterContract, splitList, VIEWS } from "./query.js";
import { emit, getItem, mergeItem, on, state } from "./store.js";
import { termLabel, terms } from "./taxonomy.js";
import { primaryTags } from "./topic-presentation.js";
import { queueImage, prioritizeSearch } from "./image-loader.js";

export const PAGE_SIZE = 60;
const POLL_INTERVAL = 10000;

let controller = null;
let requestVersion = 0;
let firstPageSnapshot = "";
let displayedQuery = "";
let refreshing = false;
let searchPreview = false;
let pollBusy = false;
let anchorId = 0; // shift-click range anchor
let hooks = {};

const els = {};

export function bookmarkHref(id) {
  return `/bookmarks/${id}${hooks.querySuffix ? hooks.querySuffix() : ""}`;
}

// --- Rendering ------------------------------------------------------------

function imageThumb(item, existing) {
  const images = Array.isArray(item.images) ? item.images : [];
  if (!images.length) return null;
  const src = imagePath(images[0].key, { size: 160 });
  if (existing?.dataset.updated === (item.enriched_at || "") && existing.querySelector("img")?.dataset.imageSource === src) return existing;
  const box = h("div.row-thumb");
  box.dataset.updated = item.enriched_at || "";
  const image = h("img", { alt: "", decoding: "async", width: 160, height: 160 });
  queueImage(image, src);
  // Fade in once decoded so a slow image does not pop into view.
  if (image.complete && image.naturalWidth) image.classList.add("ready");
  image.addEventListener("load", () => image.classList.add("ready"));
  image.addEventListener("error", () => box.remove());
  box.append(image);
  return box;
}

function textWithHighlights(tag, text, terms) {
  const node = h(tag);
  return highlightInto(node, text, highlightRanges(text, terms));
}

function statusBadge(item) {
  const status = item.curation_status || "inbox";
  const view = activeView(state.filters);
  if (view === status || status === "inbox") return null;
  return h("span.badge.status-badge", { dataset: { status } }, h("i.dot"), curationShort[status] || status);
}

function processBadge(item) {
  if (item.status === "processing") return h("span.badge.badge-work", icon("loader", 12, "spin"), "处理中");
  if (item.status === "pending") return h("span.badge.badge-work", icon("clock", 12), "排队中");
  if (item.status === "failed") return h("span.badge.badge-danger", icon("alert", 12), "读取失败");
  if (item.status === "exhausted") return h("span.badge.badge-danger", icon("alert", 12), "需人工处理");
  return null;
}

function rowMeta(item) {
  const labels = primaryTags(item.classification, item.custom_tags, terms("topics"))
    .map((tag) => ({ ...tag, label: tag.label || termLabel(tag.field, tag.id) }));
  const visible = labels.slice(0, 5);
  const hidden = labels.slice(5);
  return h("div.row-meta",
    statusBadge(item),
    processBadge(item),
    ...visible.map((tag) => h("button.tag.tag-filter", {
      type: "button", class: tag.field === "resource_kinds" ? "tag-resource" : tag.field === "custom_tags" ? "tag-custom" : "",
      title: `筛选：${tag.label}`, dataset: { tagField: tag.field, tagId: tag.id },
      onclick: () => emit("tag-filter-request", { field: tag.field, term: tag.id })
    }, tag.label)),
    hidden.length ? h("span.tag-more", { title: hidden.map((tag) => tag.label).join(" / "), "aria-label": `另有 ${hidden.length} 个标签` }, `+${hidden.length}`) : null,
    // Most bookmarks come from X, so only a different source is worth a label.
    item.source && item.source !== "x" ? h("span.row-source", sourceLabels[item.source] || item.source) : null
  );
}

export function renderRow(item, { thumbnail } = {}) {
  const terms = searchTerms(state.search);
  const title = displayTitle(item);
  const summary = state.search ? searchExcerpt(item, terms) : displaySummary(item);
  const selected = item.id === state.selectedId;
  const checked = state.checked.has(item.id);
  const row = h("li.row", { dataset: { id: String(item.id) } });
  row.classList.toggle("selected", selected);
  row.classList.toggle("checked", checked);
  row.classList.toggle("dimmed", item.curation_status === "drop" && activeView(state.filters) !== "drop");

  const check = h("button.row-check", {
    type: "button", role: "checkbox", "aria-checked": String(checked), "aria-label": "选择这条收藏", tabindex: "-1"
  }, icon("check", 12));
  const link = h("a.row-main", { href: bookmarkHref(item.id), "aria-current": selected ? "true" : null });
  const titleNode = textWithHighlights("span.row-title", title.text, terms);
  if (title.raw) titleNode.classList.add("raw");
  const top = h("div.row-top", titleNode,
    h("time.row-time", { dateTime: item.created_at || "", title: formatFull(item.created_at) }, listTime(item.created_at)));
  const summaryNode = summary.wait ? null : textWithHighlights("p.row-summary", summary.text, terms);
  const personal = item.why || item.note;
  const personalNode = personal
    ? h("p.row-why", { class: item.why ? "" : "note" }, icon(item.why ? "quote" : "pencil", 12), textWithHighlights("span", personal, terms))
    : null;
  append(link, [h("div.row-body", top, summaryNode, personalNode), imageThumb(item, thumbnail)]);
  row.append(check, link, rowMeta(item));
  return row;
}

function bucketRow(label) {
  return h("li.bucket", { role: "presentation", dataset: { bucket: label } }, h("span", label));
}

function rowElement(id) {
  return els.rows.querySelector(`li.row[data-id="${id}"]`);
}

function rowFocus(container) {
  const control = document.activeElement;
  if (!container.contains(control)) return null;
  const row = control.closest("li.row[data-id]");
  if (!row) return null;
  if (control.matches(".tag-filter")) return { id: Number(row.dataset.id), field: control.dataset.tagField, term: control.dataset.tagId };
  const selector = control.matches(".row-main") ? ".row-main" : control.matches(".row-check") ? ".row-check" : "";
  return selector ? { id: Number(row.dataset.id), selector } : null;
}

function restoreRowFocus(focus) {
  if (!focus) return;
  const row = rowElement(focus.id);
  const control = focus.selector ? row?.querySelector(focus.selector) : [...(row?.querySelectorAll(".tag-filter") || [])]
    .find(node => node.dataset.tagField === focus.field && node.dataset.tagId === focus.term);
  // Replacement is synchronous: only restore the control focused immediately
  // before it. A removed row/tag must not focus a different item or field.
  control?.focus({ preventScroll: true });
}

function renderRows(ids, { append = false, reuseImages = true } = {}) {
  const focus = append ? null : rowFocus(els.rows);
  const fragment = document.createDocumentFragment();
  const thumbnails = new Map();
  if (!append && reuseImages) for (const row of els.rows.querySelectorAll("li.row[data-id]")) {
    const thumbnail = row.querySelector(".row-thumb");
    if (thumbnail) thumbnails.set(Number(row.dataset.id), thumbnail);
  }
  let lastBucket = null;
  if (append) {
    const lastRow = [...els.rows.querySelectorAll("li.row")].at(-1);
    if (lastRow) lastBucket = bucketLabel(getItem(Number(lastRow.dataset.id))?.created_at);
  }
  for (const id of ids) {
    const item = getItem(id);
    if (!item) continue;
    const bucket = bucketLabel(item.created_at);
    if (bucket !== lastBucket) {
      fragment.append(bucketRow(bucket));
      lastBucket = bucket;
    }
    fragment.append(renderRow(item, { thumbnail: thumbnails.get(id) }));
  }
  // Build detached and attach once: appending rows one by one to a live list
  // forces style and layout work per row during scroll-triggered loads.
  if (append) els.rows.append(fragment);
  else els.rows.replaceChildren(fragment);
  restoreRowFocus(focus);
}

export function updateRow(id) {
  const current = rowElement(id);
  const item = getItem(id);
  if (!current || !item) return;
  const focus = rowFocus(current);
  current.replaceWith(renderRow(item, { thumbnail: current.querySelector(".row-thumb") }));
  restoreRowFocus(focus);
}

function pruneBuckets() {
  for (const bucket of els.rows.querySelectorAll("li.bucket")) {
    const next = bucket.nextElementSibling;
    if (!next || next.classList.contains("bucket")) bucket.remove();
  }
}

// removeRows drops rows that left the current view and returns the id that
// should take over the selection.
export function removeRows(ids) {
  const removing = new Set(ids);
  let successor = 0;
  if (removing.has(state.selectedId)) {
    const index = state.order.indexOf(state.selectedId);
    successor = state.order.slice(index + 1).find((id) => !removing.has(id))
      || state.order.slice(0, index).reverse().find((id) => !removing.has(id)) || 0;
  }
  state.order = state.order.filter((id) => !removing.has(id));
  for (const id of removing) {
    rowElement(id)?.remove();
    state.checked.delete(id);
  }
  pruneBuckets();
  if (state.total !== null) state.total = Math.max(0, state.total - ids.filter(Boolean).length);
  updateHeader();
  updateFooter();
  renderBatchBar();
  return successor;
}

// restoreRow puts an item back at its id-ordered position, used by undo.
export function restoreRow(item) {
  if (state.order.includes(item.id)) { updateRow(item.id); return; }
  const lastLoaded = state.order.at(-1);
  if (state.nextBeforeID && lastLoaded && item.id < lastLoaded) return; // older than what is loaded
  const index = state.order.findIndex((id) => id < item.id);
  const position = index === -1 ? state.order.length : index;
  state.order.splice(position, 0, item.id);
  renderRows(state.order);
  if (state.total !== null) state.total += 1;
  updateHeader();
  updateFooter();
}

function viewLabel() {
  const view = activeView(state.filters);
  return VIEWS.find((entry) => entry.id === view)?.label || "全部收藏";
}

export function updateHeader() {
  els.title.textContent = viewLabel();
  const stale = displayedQuery && displayedQuery !== apiParams(state.filters, state.search, { limit: PAGE_SIZE }).toString();
  els.count.textContent = refreshing ? (state.search ? searchPreview ? `已找到 ${state.order.length} 条 · 全文搜索中…` : "正在搜索全文…" : "正在筛选…") : searchPreview ? `已找到 ${state.order.length} 条 · 全文搜索未完成` : stale && state.listError ? "上次结果" : state.total === null ? "" : `${state.total} 条`;
  els.count.hidden = !refreshing && state.total === null;
  els.search.placeholder = `在「${viewLabel()}」中搜索`;
  renderActiveFilters();
}

function filterChip(label, onRemove) {
  return h("button.filter-chip", { type: "button", onclick: onRemove, title: "移除这个筛选条件" },
    h("span", label), icon("x", 12));
}

function renderActiveFilters() {
  const holder = els.activeFilters;
  holder.replaceChildren();
  const filters = state.filters;
  const chips = [];
  const labels = { topics: "主题", topic_refinements: "进一步筛选", resource_kinds: "资源类型", custom_tags: "自定义", content_functions: "内容功能", carriers: "载体", affordances: "潜在用途", entity_state: "实体" };
  for (const key of ["topics", "topic_refinements", "resource_kinds", "custom_tags", "content_functions", "carriers", "affordances", "entity_state"]) {
    for (const value of splitList(filters[key])) {
      const label = key === "entity_state" ? (hooks.entityStateLabel?.(value) || value) : termLabel(key, value);
      chips.push(filterChip(`${labels[key]}：${label}`, () => hooks.toggleFilter(key, value)));
    }
  }
  if (filters.form) chips.push(filterChip(`形态：${termLabel("form", filters.form)}`, () => hooks.toggleFilter("form", filters.form)));
  if (filters.use) chips.push(filterChip(`用途：${termLabel("use", filters.use)}`, () => hooks.toggleFilter("use", filters.use)));
  if (filters.source) chips.push(filterChip(`来源：${sourceLabels[filters.source] || filters.source}`, () => hooks.toggleFilter("source", filters.source)));
  if (filters.since) chips.push(filterChip(`时间：${hooks.sinceLabel?.(filters.since) || filters.since}`, () => hooks.toggleFilter("since", filters.since)));
  if (filters.uncertain === "true" && activeView(filters) !== "uncertain") chips.push(filterChip("仅待确认", () => hooks.toggleFilter("uncertain", "true")));
  if (chips.length) {
    chips.push(h("button.link-btn", { type: "button", onclick: () => hooks.clearFacets() }, "清除筛选"));
  }
  holder.append(...chips);
  holder.hidden = chips.length === 0;
}

function skeleton() {
  return Array.from({ length: 7 }, () => h("li.row.skeleton", { "aria-hidden": "true" },
    h("div.row-main", h("div.row-body", h("div.sk.sk-title"), h("div.sk.sk-line"), h("div.sk.sk-line.short")))));
}

function updateFooter() {
  const empty = !state.loading && !state.listError && state.order.length === 0;
  els.empty.hidden = !empty;
  if (empty) renderEmpty();
  els.tail.hidden = state.loading || Boolean(state.nextBeforeID) || state.order.length === 0;
  els.more.hidden = !(state.loading && !refreshing && state.order.length > 0);
}

function renderEmpty() {
  const view = activeView(state.filters);
  const facets = facetFilterCount(state.filters);
  let title = "这里还没有收藏";
  let hint = "";
  const actions = [];
  if (state.search) {
    title = `没有找到「${state.search}」`;
    hint = "试试更短的关键词，或换一种说法。";
    if (view !== "all") actions.push(h("button.btn", { type: "button", onclick: () => hooks.searchEverywhere() }, icon("search", 14), "在全部收藏中搜索"));
  } else if (facets > 0) {
    title = "暂无匹配收藏";
  } else if (view === "inbox") {
    title = "收件箱已清空";
    hint = "新收藏会自动出现在这里。";
  } else if (view === "uncertain") {
    title = "暂无待确认分类";
  }
  if (facets > 0) actions.push(h("button.btn", { type: "button", onclick: () => hooks.clearFacets() }, "清除筛选"));
  clear(els.empty, h("div.empty-icon", icon(view === "inbox" && !facets && !state.search ? "check" : "search", 22)),
    h("p.empty-title", title), hint ? h("p.empty-hint", hint) : null, actions.length ? h("div.empty-actions", actions) : null);
}

function showError(error) {
  state.listError = error;
  const code = error?.message;
  const unsupported = code === "unsupported_filter_contract";
  clear(els.notice,
    icon("alert", 16),
    h("span#load-error-text", unsupported ? errorLabel(code) : `读取收藏失败：${errorLabel(code)}`),
    h("button.btn.btn-sm#retry-load", { type: "button", onclick: () => (unsupported ? hooks.clearFacets() : reload()) },
      unsupported ? "清除筛选" : "重试"));
  els.notice.hidden = false;
}

// showNewItems offers to load bookmarks that arrived after the list was read,
// without yanking the list away from what the user is looking at.
export function showNewItems(count) {
  if (!count || count < 1 || state.search || state.loading) {
    els.fresh.hidden = true;
    return;
  }
  clear(els.fresh, icon("arrowUp", 14), `有 ${count} 条新收藏，点击载入`);
  els.fresh.hidden = false;
}

// --- Loading ----------------------------------------------------------------

export async function reload({ keepSelection = true, silent = false, reuse = false } = {}) {
  if (!reuse) invalidateQueryReads();
  controller?.abort();
  controller = new AbortController();
  const version = ++requestVersion;
  prioritizeSearch(Boolean(state.search));
  const params = apiParams(state.filters, state.search, { limit: PAGE_SIZE });
  const filters = { ...state.filters };
  refreshing = !silent;
  searchPreview = false;
  state.loading = true;
  els.pane.dataset.loading = "true";
  els.pane.setAttribute("aria-busy", "true");
  els.fresh.hidden = true;
  state.listError = null;
  els.notice.hidden = true;
  if (!silent) {
    if (!state.order.length) els.rows.replaceChildren(...skeleton());
    syncChecks();
  }
  if (!silent && reuse && state.search) {
    const preview = previewSearch(params, { params: displayedQuery, items: state.order.map(getItem) });
    if (preview?.length) {
      for (const item of preview) mergeItem(item);
      state.order = preview.map(item => item.id);
      state.nextBeforeID = null;
      searchPreview = true;
      renderRows(state.order);
      els.scroll.scrollTop = 0;
    }
  }
  updateHeader();
  updateFooter();
  try {
    const page = await api.list(params, controller.signal, { reuse });
    if (version !== requestVersion) return;
    if (needsFilterContract(filters) && page.filter_contract_version !== 1) {
      throw Object.assign(new Error("unsupported_filter_contract"), { status: 409 });
    }
    const snapshot = JSON.stringify(page);
    if (silent && snapshot === firstPageSnapshot) return;
    firstPageSnapshot = snapshot;
    displayedQuery = params.toString();
    searchPreview = false;
    const items = Array.isArray(page.items) ? page.items : [];
    for (const item of items) mergeItem(item);
    state.order = items.map((item) => item.id);
    state.nextBeforeID = page.next_before_id ?? null;
    state.total = page.counts?.total ?? items.length;
    state.counts = page.counts || null;
    renderRows(state.order, { reuseImages: reuse || silent });
    if (!silent) els.scroll.scrollTop = 0;
    updateHeader();
    emit("list:loaded", { silent, keepSelection });
    if (!silent) void api.primeFilters(params, controller.signal);
  } catch (error) {
    if (version !== requestVersion || error.name === "AbortError") return;
    if (!silent) {
      if (!state.order.length) els.rows.replaceChildren();
      showError(error);
    }
  } finally {
    if (version === requestVersion) {
      prioritizeSearch(false);
      state.loading = false;
      refreshing = false;
      els.pane.dataset.loading = "false";
      els.pane.setAttribute("aria-busy", "false");
      updateHeader();
      updateFooter();
      renderBatchBar();
    }
  }
}

export async function loadMore() {
  if (state.loading || !state.nextBeforeID || state.listError) return false;
  controller?.abort();
  controller = new AbortController();
  const version = ++requestVersion;
  state.loading = true;
  els.pane.dataset.loading = "true";
  els.pane.setAttribute("aria-busy", "true");
  updateFooter();
  try {
    const params = apiParams(state.filters, state.search, { limit: PAGE_SIZE, beforeId: state.nextBeforeID });
    params.set("counts", "0");
    const page = await api.list(params, controller.signal);
    if (version !== requestVersion) return false;
    if (needsFilterContract(state.filters) && page.filter_contract_version !== 1) throw new Error("unsupported_filter_contract");
    const items = (Array.isArray(page.items) ? page.items : []).filter((item) => !state.order.includes(item.id));
    for (const item of items) mergeItem(item);
    state.order.push(...items.map((item) => item.id));
    state.nextBeforeID = page.next_before_id ?? null;
    renderRows(items.map((item) => item.id), { append: true });
    emit("list:more");
    return items.length > 0;
  } catch (error) {
    if (version !== requestVersion || error.name === "AbortError") return false;
    showError(error);
    return false;
  } finally {
    if (version === requestVersion) {
      state.loading = false;
      els.pane.dataset.loading = "false";
      els.pane.setAttribute("aria-busy", "false");
      updateFooter();
    }
  }
}

// pollFirstPage refreshes rows in place while something near the top is still
// being processed, without disturbing scroll position or selection.
async function pollFirstPage() {
  if (document.hidden || pollBusy || state.loading || state.listError || state.search || state.route.name === "backstage" ||
      (state.layout === "narrow" && state.route.name === "bookmark") || !els.pane.getClientRects().length) return;
  const head = state.order.slice(0, PAGE_SIZE).map(getItem);
  if (!head.some(isWorking)) return;
  const version = requestVersion;
  pollBusy = true;
  try {
    const params = apiParams(state.filters, state.search, { limit: PAGE_SIZE });
    params.set("counts", "0");
    const page = await api.list(params);
    if (version !== requestVersion || state.loading) return;
    for (const item of page.items || []) {
      if (!state.order.includes(item.id)) continue;
      const before = JSON.stringify(getItem(item.id));
      mergeItem(item);
      if (JSON.stringify(getItem(item.id)) !== before) {
        invalidateQueryReads();
        updateRow(item.id);
        emit("item", item.id);
      }
    }
  } catch {
    // Background refresh is best effort; the next tick retries.
  } finally { pollBusy = false; }
}

// --- Selection & batch --------------------------------------------------------

export function markSelected(id, { scroll = true } = {}) {
  for (const row of els.rows.querySelectorAll("li.row.selected")) {
    row.classList.remove("selected");
    row.querySelector("a.row-main")?.removeAttribute("aria-current");
  }
  const row = rowElement(id);
  if (!row) return;
  row.classList.add("selected");
  row.querySelector("a.row-main")?.setAttribute("aria-current", "true");
  if (scroll) row.scrollIntoView({ block: "nearest" });
}

export function toggleChecked(id, { range = false } = {}) {
  if (range && anchorId && state.order.includes(anchorId)) {
    const [from, to] = [state.order.indexOf(anchorId), state.order.indexOf(id)].sort((a, b) => a - b);
    const target = !state.checked.has(id);
    for (const rangeId of state.order.slice(from, to + 1)) {
      if (target) state.checked.add(rangeId);
      else state.checked.delete(rangeId);
    }
  } else if (state.checked.has(id)) state.checked.delete(id);
  else state.checked.add(id);
  anchorId = id;
  syncChecks();
}

export function checkAllLoaded() {
  const all = state.order.every((id) => state.checked.has(id));
  if (all) state.checked.clear();
  else for (const id of state.order) state.checked.add(id);
  syncChecks();
}

export function clearChecked() {
  state.checked.clear();
  syncChecks();
}

function syncChecks() {
  for (const row of els.rows.querySelectorAll("li.row")) {
    const checked = state.checked.has(Number(row.dataset.id));
    row.classList.toggle("checked", checked);
    row.querySelector(".row-check")?.setAttribute("aria-checked", String(checked));
  }
  els.pane.classList.toggle("selecting", state.checked.size > 0);
  renderBatchBar();
}

function renderBatchBar() {
  const count = state.checked.size;
  els.batch.hidden = count === 0;
  els.head.classList.toggle("batching", count > 0);
  if (!count) return;
  const statusButton = (status, iconName, label) => h("button.btn.btn-sm", {
    type: "button", title: `把所选收藏设为「${label}」`, dataset: { batchStatus: status },
    onclick: () => hooks.batchStatus([...state.checked], status)
  }, icon(iconName, 14), h("span", label));
  const allChecked = state.order.length > 0 && state.order.every((id) => state.checked.has(id));
  clear(els.batch,
    h("div.batch-top",
      h("button.icon-btn", { type: "button", "aria-label": "取消选择", title: "取消选择 (Esc)", onclick: clearChecked }, icon("x", 16)),
      h("span.batch-count", `已选 ${count} 条`),
      h("button.link-btn", { type: "button", onclick: checkAllLoaded }, allChecked ? "全不选" : "全选已加载"),
      h("span.batch-spacer"),
      h("button.icon-btn", {
        type: "button", "aria-label": "更多批量操作", title: "确认标签、导出、重新读取", "aria-haspopup": "menu",
        onclick: (event) => hooks.batchMenu(event.currentTarget, [...state.checked])
      }, icon("more", 18))),
    h("div.batch-actions",
      statusButton("inbox", "inbox", "收件箱"),
      statusButton("kept", "star", "精选"),
      statusButton("compiled", "book", "已编入"),
      statusButton("drop", "archive", "搁置")));
}

// --- Wiring -------------------------------------------------------------------

export function initList(options) {
  hooks = options;
  Object.assign(els, {
    pane: byId("list-pane"), head: byId("list-head"), title: byId("list-title"), count: byId("list-count"),
    search: byId("search"), activeFilters: byId("active-filters"), batch: byId("batch-bar"),
    scroll: byId("list-scroll"), notice: byId("list-notice"), rows: byId("rows"), empty: byId("empty"),
    tail: byId("tail"), more: byId("loading-more"), sentinel: byId("sentinel"), fresh: byId("new-items")
  });
  els.fresh.addEventListener("click", () => reload({ silent: false }));

  els.rows.addEventListener("click", (event) => {
    const row = event.target.closest("li.row");
    if (!row || row.classList.contains("skeleton")) return;
    const id = Number(row.dataset.id);
    if (event.target.closest(".row-check")) {
      event.preventDefault();
      toggleChecked(id, { range: event.shiftKey });
      return;
    }
    const link = event.target.closest("a.row-main");
    if (!link) return;
    // Let the browser open a new tab or window for modified clicks.
    if (event.metaKey || event.ctrlKey || event.button !== 0) return;
    event.preventDefault();
    // Shift-click extends a multi-selection like a file manager.
    if (event.shiftKey) {
      toggleChecked(id, { range: true });
      return;
    }
    hooks.select(id, { fromList: true });
  });

  if (typeof IntersectionObserver === "function") {
    new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) loadMore();
    }, { root: els.scroll, rootMargin: "800px 0px" }).observe(els.sentinel);
  }

  setInterval(pollFirstPage, POLL_INTERVAL);
  on("taxonomy", () => {
    for (const id of state.order) updateRow(id);
    updateHeader();
  });
  on("item", (id) => {
    if (state.order.includes(id)) updateRow(id);
  });
}
