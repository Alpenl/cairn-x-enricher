// Navigation: library views with live counts, facet filters and the service
// status line. On narrower screens the same element becomes a drawer.
import { api } from "./api.js";
import { byId, clear, h } from "./dom.js";
import { icon } from "./icons.js";
import {
  apiParams, activeView, dateInputFromSince, facetFilterCount, sinceDaysAgo, sinceFromDateInput, sinceLabel, splitList, VIEWS
} from "./query.js";
import { on, state } from "./store.js";
import { ENTITY_STATES, V2_DIMENSIONS, vocab } from "./taxonomy.js";
import { offlineScopeVersion, preferenceScope } from "./offline.js";
import { topicSections } from "./topic-presentation.js";

const OPEN_KEY = "cairn.facets.open.v2";
const els = {};
let hooks = {};
let openGroups = new Set();
let countsSignature = "";
let tagCounts = {};
let countsEpoch = 0;
let countsTimer = 0;
let countsController = null;
let pinnedTopics = new Set();
let preferenceKey = "";
let topicSearch = "";
let allTopics = false;
let refreshTopicCounts = null;
let topicView = null;
let moreView = null;
const vocabularyViews = new Map();

// Keep the count slot even while the next query is pending. Removing it
// changes the available label width and makes wrapped labels jump twice.
function updateChipCount(chip, count) {
  let slot = chip.querySelector(".facet-chip-count");
  if (!slot) { slot = h("span.facet-chip-count"); chip.append(slot); }
  const text = Number.isFinite(count) ? String(count) : "";
  if (slot.textContent !== text) slot.textContent = text;
}

function reconcileChildren(parent, children) {
  const keep = new Set(children);
  for (const child of [...parent.children]) if (!keep.has(child)) child.remove();
  let cursor = parent.firstChild;
  for (const child of children) {
    if (child !== cursor) parent.insertBefore(child, cursor);
    cursor = child.nextSibling;
  }
}

// isEqualNode compares markup, not live input values/checked properties.
function sameControls(left, right) {
  if (!left?.isEqualNode(right)) return false;
  const a = [...left.querySelectorAll("input, select, textarea")];
  const b = [...right.querySelectorAll("input, select, textarea")];
  return a.length === b.length && a.every((node, i) => node.value === b[i].value && node.checked === b[i].checked);
}

function updateGroup(details, label, count) {
  const summary = details.querySelector(":scope > summary");
  let badge = summary.querySelector(".facet-badge");
  if (!count) { badge?.remove(); return; }
  if (!badge) { badge = h("span.facet-badge"); summary.append(badge); }
  badge.title = `${label}已有 ${count} 项筛选`;
  const text = `${count} 已选`;
  if (badge.textContent !== text) badge.textContent = text;
}

function updateMode(body, key, label, count) {
  let mode = body.querySelector(":scope > .facet-mode");
  if (count < 2 || vocab.tagSystemAvailable === false) { mode?.remove(); return; }
  if (!mode) {
    mode = h("select.facet-mode", { "aria-label": `${label}匹配方式` },
      h("option", { value: "any" }, "匹配任一"), h("option", { value: "all" }, "全部匹配"));
    mode.addEventListener("change", () => hooks.setFilter(key, mode.value === "all" ? "all" : ""));
    body.append(mode);
  }
  mode.value = state.filters[key] || "any";
}

// A count response must not replace the button under the pointer or keyboard
// focus, reset open groups, or rebuild unrelated filter controls.
function renderCounts() {
  const counts = Object.fromEntries(Object.entries(tagCounts).filter(([, value]) => Array.isArray(value))
    .map(([key, values]) => [key, new Map(values.map(entry => [entry.id, entry.count]))]));
  for (const chip of els.facets.querySelectorAll("button[data-facet]")) {
    const field = chip.dataset.facet === "topic_refinements" ? "topics" : chip.dataset.facet;
    if (["topics", "resource_kinds", "content_functions", "custom_tags"].includes(field)) {
      updateChipCount(chip, counts[field]?.get(chip.dataset.value));
    }
  }
  refreshTopicCounts?.();
}

try {
  const saved = JSON.parse(localStorage.getItem(OPEN_KEY) || "null");
  if (Array.isArray(saved)) openGroups = new Set(saved);
} catch {
  // Storage can be unavailable (private mode); the defaults still work.
}

function persistOpen() {
  try { localStorage.setItem(OPEN_KEY, JSON.stringify([...openGroups])); } catch { /* optional */ }
}

// --- Views ------------------------------------------------------------------------

function renderViews() {
  const current = state.route.name === "backstage" ? "" : activeView(state.filters);
  const counts = state.overview?.views || {};
  const previous = new Map([...els.views.children].map(node => [node.dataset.view, node]));
  const links = VIEWS.map((view) => {
    const count = counts[view.id];
    const link = h("a.nav-item", {
      href: hooks.viewHref(view.id), dataset: { view: view.id }, "aria-current": view.id === current ? "page" : null,
      title: `${view.label}（快捷键 ${view.key}）`
    }, icon(view.icon, 16), h("span.nav-label", view.label),
    Number.isFinite(count) ? h("span.nav-count", { class: view.id === "inbox" && count > 0 ? "strong" : "" }, String(count)) : null);
    link.addEventListener("click", (event) => {
      if (event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) return;
      event.preventDefault();
      hooks.setView(view.id);
    });
    const old = previous.get(view.id);
    return old?.isEqualNode(link) ? old : link;
  });
  reconcileChildren(els.views, links);
}

// --- Facets -------------------------------------------------------------------------

function facetChip(key, value, label, selected, { title, count } = {}) {
  return h("button.facet-chip", {
    type: "button", dataset: { facet: key, value }, "aria-pressed": String(selected), title: title || label,
    onclick: (event) => hooks.toggleFilter(event.currentTarget.dataset.facet, value)
  }, h("span.facet-chip-label", label), h("span.facet-chip-count", Number.isFinite(count) ? String(count) : ""));
}

function group(id, label, content, { selectedCount = 0, hint } = {}) {
  const open = openGroups.has(id);
  const details = h("details.facet-group", { dataset: { group: id }, open });
  const summary = h("summary.facet-summary", icon("chevronRight", 14, "facet-caret"), h("span", label),
    selectedCount ? h("span.facet-badge", { title: `${label}已有 ${selectedCount} 项筛选` }, `${selectedCount} 已选`) : null);
  details.append(summary, h("div.facet-body", hint ? h("p.facet-hint", hint) : null, content));
  const remember = () => {
    if (details.open) openGroups.add(id);
    else openGroups.delete(id);
    persistOpen();
    syncFacetVisibility();
  };
  summary.addEventListener("click", (event) => {
    event.preventDefault();
    details.open = !details.open;
    // Native toggle events are deferred; keep a click followed by navigation
    // from losing the user's choice before the asynchronous event is delivered.
    remember();
  });
  details.addEventListener("toggle", () => {
    if (details.isConnected) remember();
  });
  return details;
}

function vocabularyGroup(key, label, terms) {
  const signature = JSON.stringify(terms);
  let view = vocabularyViews.get(key);
  if (!view || view.signature !== signature) {
    const chips = h("div.facet-chips"), body = h("div", chips);
    view = { signature, chips, body, rows: new Map(), node: group(key, label, body) };
    vocabularyViews.set(key, view);
  }
  const selected = new Set(splitList(state.filters[key]));
  const choices = terms.filter(term => (term.active !== false && !term.deprecated) || selected.has(term.id));
  for (const id of selected) if (!terms.some(term => term.id === id)) choices.push({ id, label: `${id}（词表不可用）` });
  const rows = choices.map(term => {
    let chip = view.rows.get(term.id);
    if (!chip) {
      chip = facetChip(key, term.id, `${term.label || term.id}${term.active === false || term.deprecated ? "（已停用）" : ""}`, false);
      view.rows.set(term.id, chip);
    }
    chip.setAttribute("aria-pressed", String(selected.has(term.id)));
    updateChipCount(chip, (tagCounts[key] || []).find(entry => entry.id === term.id)?.count);
    return chip;
  });
  reconcileChildren(view.chips, rows);
  for (const [id, chip] of view.rows) if (!rows.includes(chip)) view.rows.delete(id);
  const modeKey = ({ topics: "topics_mode", resource_kinds: "resource_mode", custom_tags: "custom_mode", content_functions: "functions_mode" })[key];
  if (modeKey) updateMode(view.body, modeKey, label, selected.size);
  updateGroup(view.node, label, selected.size);
  return view.node;
}

function topicsGroup(terms) {
  if (topicView?.terms !== terms) topicView = createTopicView(terms);
  topicView.update();
  refreshTopicCounts = topicView.update;
  return topicView.node;
}

function createTopicView(terms) {
  const choices = h("div.topic-choices");
  const rows = new Map(), sectionsById = new Map(), unknown = new Map();
  const search = h("input.facet-search#topic-search", { type: "search", placeholder: "查找全部主题", "aria-label": "查找全部主题", value: topicSearch });
  const empty = h("p.facet-hint", "没有匹配的主题");
  const showAll = h("button.link-btn.topic-show-all", { type: "button", onclick: () => {
    allTopics = !allTopics; showAll.textContent = allTopics ? "收起全部主题" : "浏览全部主题"; update();
  } }, allTopics ? "收起全部主题" : "浏览全部主题");
  const body = h("div", search, choices, showAll);
  const node = group("topics", "主题", body);
  search.addEventListener("input", () => { topicSearch = search.value; update(); });
  function update() {
    // Read current state, not the selection captured when a button was made.
    const selected = new Set(splitList(state.filters.topics));
    const refinements = new Set(splitList(state.filters.topic_refinements));
    const current = new Set([...selected, ...refinements]);
    const counts = new Map((tagCounts.topics || []).map(entry => [entry.id, entry.count]));
    const sections = topicSections(terms, current, pinnedTopics, counts, topicSearch, allTopics);
    const next = sections.map(section => {
      let element = sectionsById.get(section.id);
      if (!element) {
        element = h("section.topic-section", { dataset: { topicSection: section.id } },
          h("p.facet-section-label", section.id === "specific" ? "进一步筛选" : section.label), h("div.facet-chips"));
        sectionsById.set(section.id, element);
      }
      const children = section.terms.map(term => {
        let row = rows.get(term.id);
        if (!row) {
          const chip = facetChip("topics", term.id, term.label || term.id, false);
          const pin = h("button.facet-pin", { type: "button", title: "仅固定此设备的常用入口", onclick: () => {
            if (!preferenceKey) return;
            if (pinnedTopics.has(term.id)) pinnedTopics.delete(term.id); else pinnedTopics.add(term.id);
            try { localStorage.setItem(preferenceKey, JSON.stringify([...pinnedTopics])); } catch { /* optional */ }
            update();
          } }, icon("star", 12));
          row = h("div.facet-topic-row", chip, pin);
          rows.set(term.id, row);
        }
        const chip = row.firstElementChild, pin = row.lastElementChild;
        const refine = refinements.has(term.id) || (current.size > 0 && term.granularity === "specific" && !selected.has(term.id));
        chip.dataset.facet = refine ? "topic_refinements" : "topics";
        chip.setAttribute("aria-pressed", String(refine ? refinements.has(term.id) : selected.has(term.id)));
        chip.title = refine ? `进一步筛选：${term.label}（同时满足原有条件）` : (term.label || term.id);
        updateChipCount(chip, counts.get(term.id));
        pin.setAttribute("aria-pressed", String(pinnedTopics.has(term.id)));
        pin.setAttribute("aria-label", `${pinnedTopics.has(term.id) ? "取消固定" : "固定"}${term.label}`);
        pin.disabled = !preferenceKey;
        return row;
      });
      reconcileChildren(element.lastElementChild, children);
      return element;
    });
    if (!sections.some(section => section.terms.length)) next.push(empty);
    for (const id of current) if (!terms.some(term => term.id === id)) {
      let chip = unknown.get(id);
      if (!chip) { chip = facetChip("topics", id, `${id}（词表不可用）`, true); unknown.set(id, chip); }
      chip.dataset.facet = refinements.has(id) ? "topic_refinements" : "topics";
      next.push(chip);
    }
    for (const [id, chip] of unknown) if (!next.includes(chip)) unknown.delete(id);
    reconcileChildren(choices, next);
    updateGroup(node, "主题", current.size);
    updateMode(body, "topics_mode", "主题", selected.size);
  }
  return { terms, node, update };
}

function singleGroup(key, label, options) {
  const chips = h("div.facet-chips");
  for (const [value, text] of options) chips.append(facetChip(key, value, text, state.filters[key] === value));
  const selected = state.filters[key];
  if (selected && !options.some(([value]) => value === selected)) chips.append(facetChip(key, selected, `${selected}（不可用）`, true));
  return group(key, label, chips, { selectedCount: state.filters[key] ? 1 : 0 });
}

function timeGroup() {
  const since = state.filters.since;
  const chips = h("div.facet-chips");
  const presets = [[0, "今天"], [7, "近 7 天"], [30, "近 30 天"], [90, "近 90 天"]];
  const current = sinceLabel(since);
  for (const [days, label] of presets) {
    const value = sinceDaysAgo(days);
    const selected = Boolean(since) && current === label;
    chips.append(h("button.facet-chip", {
      type: "button", dataset: { facet: "since", value: String(days) }, "aria-pressed": String(selected),
      onclick: () => hooks.setFilter("since", selected ? "" : value)
    }, label));
  }
  const input = h("input.facet-date#filter-since", { type: "date", value: dateInputFromSince(since), "aria-label": "收藏起始日期" });
  input.addEventListener("change", () => hooks.setFilter("since", sinceFromDateInput(input.value)));
  const body = h("div", chips, h("label.facet-date-row", h("span", "起始日期"), input),
    since ? h("button.link-btn", { type: "button", onclick: () => hooks.setFilter("since", "") }, "清除日期") : null);
  return group("since", "收藏时间", body, { selectedCount: since ? 1 : 0 });
}

function uncertainToggle() {
  if (activeView(state.filters) === "uncertain") return null;
  const checked = state.filters.uncertain === "true";
  const input = h("input#filter-uncertain", { type: "checkbox", checked });
  input.addEventListener("change", () => hooks.setFilter("uncertain", input.checked ? "true" : ""));
  return h("label.facet-switch", input, h("span", "只看待确认分类"));
}

export function renderFacets() {
  const active = document.activeElement;
  const focused = active?.id === "topic-search";
  const facet = active?.dataset.facet, value = active?.dataset.value;
  const selection = focused ? active.selectionStart : null;
  refreshTopicCounts = null;
  const groups = [];
  const more = [];
  let moreSelected = 0;
  if (vocab.v2Available && vocab.v2) {
    for (const dimension of V2_DIMENSIONS.filter((entry) => ["topics", "resource_kinds", "content_functions"].includes(entry.key))) {
      if (Array.isArray(vocab.v2[dimension.key])) groups.push(dimension.key === "topics" ? topicsGroup(vocab.v2.topics) : vocabularyGroup(dimension.key, dimension.label, vocab.v2[dimension.key]));
    }
    if (vocab.custom.length || state.filters.custom_tags) groups.push(vocabularyGroup("custom_tags", "自定义标记", vocab.custom));
    const secondary = V2_DIMENSIONS.filter((entry) => !["topics", "resource_kinds", "content_functions"].includes(entry.key));
    more.push(...secondary.map((dimension) => vocabularyGroup(dimension.key, dimension.label, vocab.v2[dimension.key] || [])));
    moreSelected += secondary.reduce((count, entry) => count + splitList(state.filters[entry.key]).length, 0);
  }
  more.push(singleGroup("source", "来源", [["x", "X"], ["wechat", "公众号"], ["other", "其他网页"]]), timeGroup());
  moreSelected += (state.filters.source ? 1 : 0) + (state.filters.since ? 1 : 0);
  if (vocab.v2Available) {
    more.push(vocabularyGroup("entity_state", "实体状态", ENTITY_STATES.map((entry) => ({ id: entry.id, label: entry.label, active: true }))));
    moreSelected += splitList(state.filters.entity_state).length;
  }
  if (vocab.v1 && vocab.v2Available) {
    const legacy = h("div.facet-legacy",
      h("p.facet-hint", "旧版单选分类，仅在需要兼容旧标签时使用。"),
      singleGroup("form", "形态", (vocab.v1.forms || []).map((term) => [term.id, term.label])),
      singleGroup("use", "用途", (vocab.v1.uses || []).map((term) => [term.id, term.label])));
    const selectedCount = (state.filters.form ? 1 : 0) + (state.filters.use ? 1 : 0);
    more.push(group("legacy", "旧版形态 / 用途", legacy, { selectedCount }));
    moreSelected += selectedCount;
  }
  const uncertain = uncertainToggle();
  if (uncertain) more.push(uncertain);
  if (state.filters.uncertain === "true" && activeView(state.filters) !== "uncertain") moreSelected++;
  if (!moreView) {
    const body = h("div.facet-secondary");
    moreView = { body, node: group("more", "更多筛选", body) };
  }
  const previousMore = new Map([...moreView.body.children].map(node => [node.dataset.group || node.className, node]));
  reconcileChildren(moreView.body, more.map(node => {
    const old = previousMore.get(node.dataset.group || node.className);
    return old && old !== node && sameControls(old, node) ? old : node;
  }));
  updateGroup(moreView.node, "更多筛选", moreSelected);
  groups.push(moreView.node);
  const notices = [];
  if (vocab.v2Available === false) {
    notices.push(h("p.facet-notice#filter-capability", icon("alert", 14), "多维词表暂不可用：服务端未启用多维分类，只能按状态、来源和时间筛选。"));
  }
  // Keep mounted groups in place. Unchanged secondary controls retain their
  // own event handlers; vocabulary/topic controls update their state in place.
  const previous = new Map([...els.facets.children].map(node => [node.dataset.group || node.id, node]));
  const children = [...notices, ...groups].map(node => {
    const old = previous.get(node.dataset.group || node.id);
    return old && old !== node && sameControls(old, node) ? old : node;
  });
  reconcileChildren(els.facets, children);
  if (focused) { const input = byId("topic-search"); input?.focus(); if (selection !== null) input?.setSelectionRange(selection, selection); }
  else if (facet) [...els.facets.querySelectorAll("button[data-facet]")]
    .find(node => node.dataset.facet === facet && node.dataset.value === value)?.focus({ preventScroll: true });
}

// --- Service status ---------------------------------------------------------------------

let serviceState = "ok";

export function setServiceState(value) {
  serviceState = value;
  renderService();
}

function renderService() {
  const status = serviceState;
  const overview = state.overview;
  const attention = overview?.attention ?? 0;
  const queued = overview?.queued ?? 0;
  const link = els.service;
  link.classList.toggle("attention", attention > 0);
  link.classList.toggle("offline", status === "offline" || status === "backend");
  let text;
  let tone = "ok";
  if (status === "offline") { text = "无法连接服务"; tone = "danger"; }
  else if (status === "not-ready") { text = "服务未就绪"; tone = "danger"; }
  else if (status === "backend") { text = "后端暂时不可用"; tone = "danger"; }
  else if (attention > 0) { text = `${attention} 条需要处理`; tone = "warn"; }
  else if (queued > 0) { text = `正在处理 ${queued} 条`; tone = "work"; }
  else text = "服务正常";
  clear(link, h("span.status-dot", { dataset: { tone } }), h("span.service-text", text), icon("chevronRight", 14, "service-caret"));
  link.setAttribute("aria-current", state.route.name === "backstage" ? "page" : "false");
}

export function syncFacetVisibility() {
  if (!els.root) return;
  const visible = !document.hidden && state.route.name !== "backstage" &&
    (state.layout === "wide" || byId("app").classList.contains("sidebar-open"));
  const needed = ["topics", "resource_kinds", "content_functions", "custom_tags"].some(key => openGroups.has(key));
  if (!visible || !needed) {
    if (countsTimer || countsController) {
      clearTimeout(countsTimer); countsTimer = 0;
      countsController?.abort(); countsController = null;
      countsEpoch++; countsSignature = "";
    }
    return;
  }
  if (Array.isArray(vocab.v2?.resource_kinds)) {
    const signature = JSON.stringify([state.filters, state.search]);
    if (signature !== countsSignature) {
      countsSignature = signature;
      const epoch = ++countsEpoch;
      clearTimeout(countsTimer);
      countsController?.abort();
      const params = apiParams(state.filters, state.search);
      countsTimer = setTimeout(() => {
        countsTimer = 0;
        if (state.search && state.loading) { countsSignature = ""; return; }
        const controller = new AbortController(); countsController = controller;
        api.tagCounts(params, controller.signal).then((counts) => {
          if (epoch !== countsEpoch || counts.available === false) return;
          tagCounts = counts; renderCounts();
        }).catch(() => { if (epoch === countsEpoch) countsSignature = ""; })
          .finally(() => { if (countsController === controller) countsController = null; });
      }, 160);
    }
  }
}

export function renderSidebar() {
  loadTopicPreferences();
  syncFacetVisibility();
  renderViews();
  renderFacets();
  els.clear.hidden = facetFilterCount(state.filters) === 0;
}

export function initSidebar(options) {
  hooks = options;
  Object.assign(els, {
    root: byId("sidebar"), views: byId("nav-views"), facets: byId("facets"), clear: byId("clear-filters"),
    service: byId("service-link")
  });
  els.clear.addEventListener("click", () => hooks.clearFacets());
  els.service.addEventListener("click", (event) => {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) return;
    event.preventDefault();
    hooks.openBackstage();
  });
  on("overview", () => { renderViews(); renderService(); });
  on("list:loaded", () => { if (state.search) syncFacetVisibility(); });
  on("taxonomy", renderSidebar);
  on("tags:changed", () => { countsSignature = ""; renderSidebar(); });
  on("library:changed", () => { countsSignature = ""; renderSidebar(); });
  loadTopicPreferences();
  on("account:changed", () => {
    preferenceKey = ""; pinnedTopics.clear(); tagCounts = {};
    topicView = null; moreView = null; vocabularyViews.clear();
  });
  document.addEventListener("visibilitychange", syncFacetVisibility);
}

function loadTopicPreferences() {
  const version = offlineScopeVersion();
  preferenceScope().then((scope) => {
    // Reuse authenticated response headers. Do not add a request to the
    // critical first load or guess an account on an older server.
    if (!scope || offlineScopeVersion() !== version || preferenceKey === `cairn.topic-pins.v1:${scope}`) return;
    preferenceKey = `cairn.topic-pins.v1:${scope}`;
    try { const saved = JSON.parse(localStorage.getItem(preferenceKey) || "[]");
      pinnedTopics = new Set(Array.isArray(saved) ? saved.filter((id) => typeof id === "string").slice(0, 64) : []);
    } catch { pinnedTopics = new Set(); }
    renderFacets();
  });
}
