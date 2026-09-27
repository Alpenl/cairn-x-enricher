// Navigation: library views with live counts, facet filters and the service
// status line. On narrower screens the same element becomes a drawer.
import { byId, clear, h } from "./dom.js";
import { icon } from "./icons.js";
import {
  activeView, dateInputFromSince, sinceDaysAgo, sinceFromDateInput, sinceLabel, splitList, VIEWS
} from "./query.js";
import { on, state } from "./store.js";
import { ENTITY_STATES, V2_DIMENSIONS, vocab } from "./taxonomy.js";

const OPEN_KEY = "cairn.facets.open";
const els = {};
let hooks = {};
let openGroups = new Set(["topics", "content_functions"]);

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
  els.views.replaceChildren(...VIEWS.map((view) => {
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
    return link;
  }));
}

// --- Facets -------------------------------------------------------------------------

function facetChip(key, value, label, selected, { title } = {}) {
  return h("button.facet-chip", {
    type: "button", dataset: { facet: key, value }, "aria-pressed": String(selected), title: title || label,
    onclick: () => hooks.toggleFilter(key, value)
  }, label);
}

function group(id, label, content, { selectedCount = 0, hint } = {}) {
  const open = openGroups.has(id) || selectedCount > 0;
  const details = h("details.facet-group", { dataset: { group: id }, open });
  const summary = h("summary.facet-summary", icon("chevronRight", 14, "facet-caret"), h("span", label),
    selectedCount ? h("span.facet-badge", String(selectedCount)) : null);
  details.append(summary, h("div.facet-body", hint ? h("p.facet-hint", hint) : null, content));
  details.addEventListener("toggle", () => {
    if (details.open) openGroups.add(id);
    else openGroups.delete(id);
    persistOpen();
  });
  return details;
}

function vocabularyGroup(key, label, terms) {
  const selected = new Set(splitList(state.filters[key]));
  const chips = h("div.facet-chips");
  const known = new Set();
  for (const term of terms) {
    known.add(term.id);
    if ((term.active === false || term.deprecated) && !selected.has(term.id)) continue;
    chips.append(facetChip(key, term.id, `${term.label || term.id}${term.active === false || term.deprecated ? "（已停用）" : ""}`, selected.has(term.id)));
  }
  // A saved URL never silently loses an unknown requested ID.
  for (const id of selected) if (!known.has(id)) chips.append(facetChip(key, id, `${id}（词表不可用）`, true));
  return group(key, label, chips, { selectedCount: selected.size });
}

function singleGroup(key, label, options) {
  const chips = h("div.facet-chips");
  for (const [value, text] of options) chips.append(facetChip(key, value, text, state.filters[key] === value));
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
  const body = h("div", chips, h("label.facet-date-row", h("span", "起始日期"), input));
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
  const groups = [];
  if (vocab.v2Available && vocab.v2) {
    for (const dimension of V2_DIMENSIONS) groups.push(vocabularyGroup(dimension.key, dimension.label, vocab.v2[dimension.key] || []));
  }
  groups.push(singleGroup("source", "来源", [["x", "X"], ["wechat", "公众号"], ["other", "其他网页"]]));
  groups.push(timeGroup());
  if (vocab.v2Available) {
    groups.push(vocabularyGroup("entity_state", "实体状态", ENTITY_STATES.map((entry) => ({ id: entry.id, label: entry.label, active: true }))));
  }
  if (vocab.v1 && vocab.v2Available) {
    const legacy = h("div.facet-legacy",
      h("p.facet-hint", "旧版单选分类，仅在需要兼容旧标签时使用。"),
      h("div.facet-subhead", "形态"), h("div.facet-chips", (vocab.v1.forms || []).map((term) => facetChip("form", term.id, term.label, state.filters.form === term.id))),
      h("div.facet-subhead", "用途"), h("div.facet-chips", (vocab.v1.uses || []).map((term) => facetChip("use", term.id, term.label, state.filters.use === term.id))));
    groups.push(group("legacy", "旧版形态 / 用途", legacy, { selectedCount: (state.filters.form ? 1 : 0) + (state.filters.use ? 1 : 0) }));
  }
  const notices = [];
  if (vocab.v2Available === false) {
    notices.push(h("p.facet-notice#filter-capability", icon("alert", 14), "多维词表暂不可用：服务端未启用多维分类，只能按状态、来源和时间筛选。"));
  }
  clear(els.facets, uncertainToggle(), ...notices, ...groups);
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

export function renderSidebar() {
  renderViews();
  renderFacets();
  const count = [...els.facets.querySelectorAll(".facet-chip[aria-pressed='true']")].length + (state.filters.uncertain === "true" && activeView(state.filters) !== "uncertain" ? 1 : 0);
  els.clear.hidden = count === 0;
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
  on("taxonomy", renderSidebar);
}
