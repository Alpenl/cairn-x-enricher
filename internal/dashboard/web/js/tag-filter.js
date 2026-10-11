// Tag-chip filter popover opened from the list search field (prototype
// "tf-pop"). Selections become the unified `tags` / `tags_mode` query, so
// system and personal tags mix in one any/all condition; counts come from
// /api/tag-counts for the current scope, answered from the cached snapshot
// whenever possible (local-filters.js).
import { api } from "./api.js";
import { byId, clear, h } from "./dom.js";
import { icon } from "./icons.js";
import { apiParams, splitList } from "./query.js";
import { on, state } from "./store.js";
import { loadCustomTags, termLabel, terms, vocab } from "./taxonomy.js";

const GROUPS = [
  { field: "topics", label: "主题", dot: "topic" },
  { field: "resource_kinds", label: "资源类型", dot: "resource" },
  { field: "content_functions", label: "内容特征", dot: "feature" },
  { field: "custom_tags", label: "个人标签", dot: "custom" }
];
// Kept filterable but folded away, as agreed: smaller legacy dimensions.
const MORE = [
  { field: "carriers", label: "载体" },
  { field: "affordances", label: "潜在用途" }
];
const COUNTED = new Set(GROUPS.map((g) => g.field));

let hooks = {};
let open = false;
let suppressOpen = false;
let roving = "";
let counts = null;
let countsKey = "";
let countsTimer = 0;
let countsAbort = null;
let moreOpen = false;
const els = {};

export const tagRef = (field, id) => field === "custom_tags" ? `custom/default/${id}` : `system/${field}/${id}`;
export function parseRef(ref) {
  const [kind, field, id] = String(ref).split("/");
  if (kind === "custom" && field === "default" && id) return { field: "custom_tags", id };
  if (kind === "system" && field && id) return { field, id };
  return null;
}
export function selectedRefs(filters = state.filters) { return splitList(filters.tags); }
// A new selection starts as "all" (prototype default); a link that carries
// tags without a mode is read the way the server reads it ("any").
export function tagMode(filters = state.filters) {
  if (filters.tags_mode === "all" || filters.tags_mode === "any") return filters.tags_mode;
  return filters.tags ? "any" : "all";
}

function refLabel(ref) {
  const parsed = parseRef(ref);
  return parsed ? termLabel(parsed.field, parsed.id, parsed.id) : ref;
}

// Next filters for a selection; tags_mode is only sent with tags.
export function withTags(filters, refs, mode = tagMode(filters)) {
  const unique = [...new Set(refs)].slice(0, 32);
  return { ...filters, tags: unique.join(","), tags_mode: unique.length ? mode : "" };
}

export function toggleTag(ref, force) {
  const refs = selectedRefs();
  const has = refs.includes(ref);
  const on = force ?? !has;
  if (on === has) return false;
  hooks.setFilters(withTags(state.filters, on ? [...refs, ref] : refs.filter((r) => r !== ref)));
  return true;
}

function setMode(mode) {
  if (tagMode() === mode) return;
  const refs = selectedRefs();
  // With nothing selected the mode is remembered locally until the first chip.
  if (!refs.length) { pendingMode = mode; render(); return; }
  hooks.setFilters(withTags(state.filters, refs, mode));
}
let pendingMode = "all";
const effectiveMode = () => selectedRefs().length ? tagMode() : pendingMode;

// --- counts -----------------------------------------------------------------

function scheduleCounts() {
  if (!open) return;
  const mode = effectiveMode();
  // "all": a chip's number is the result size after adding it, i.e. its count
  // within the current result. "any": how many in scope carry the tag.
  const filters = mode === "any" ? { ...state.filters, tags: "", tags_mode: "" } : state.filters;
  const params = apiParams(filters, state.search);
  const key = params.toString();
  if (key === countsKey) return;
  countsKey = key;
  clearTimeout(countsTimer);
  countsAbort?.abort();
  countsTimer = setTimeout(() => {
    const controller = new AbortController();
    countsAbort = controller;
    api.tagCounts(params, controller.signal).then((value) => {
      if (countsKey !== key) return;
      counts = value?.available === false ? null : value;
      render();
    }).catch(() => { if (countsKey === key) countsKey = ""; })
      .finally(() => { if (countsAbort === controller) countsAbort = null; });
  }, 120);
}

function countOf(field, id) {
  if (!counts || !COUNTED.has(field) || !Array.isArray(counts[field])) return null;
  return counts[field].find((entry) => entry.id === id)?.count ?? 0;
}

// --- rendering --------------------------------------------------------------

function activeTerms(field) {
  return (terms(field) || []).filter((t) => t && t.active !== false && !t.deprecated && t.status !== "archived");
}

function chip(field, term, selected, query) {
  const ref = tagRef(field, term.id);
  const label = termLabel(field, term.id, term.label || term.id);
  if (query && !label.toLowerCase().includes(query) && !(term.aliases || []).some((a) => String(a).toLowerCase().includes(query))) return null;
  const on = selected.has(ref);
  const n = countOf(field, term.id);
  const disabled = !on && n === 0;
  const node = h("button.tf-chip", {
    type: "button", tabindex: "-1", "aria-pressed": String(on), disabled: disabled || null,
    dataset: { ref }, class: on ? "on" : "",
    title: disabled ? (effectiveMode() === "all" && selected.size ? "与已选标签同时满足的收藏为 0" : "当前范围内没有带此标签的收藏") : on ? "点击取消" : null
  }, on ? icon("check", 12, "ck") : null, h("span.nm", label), n === null ? null : h("span.n", String(n)));
  return { node, n: n ?? 0, label };
}

function group({ field, label, dot }, selected, query) {
  const list = activeTerms(field).map((t) => chip(field, t, selected, query)).filter(Boolean);
  if (!list.length) return null;
  // Most used first (selected ones stay where they are by count).
  list.sort((a, b) => b.n - a.n || a.label.localeCompare(b.label, "zh-CN"));
  return h("div.tf-group", { role: "group", "aria-label": label },
    h("div.tf-group-label", dot ? h(`span.type-dot.type-${dot}`) : null, label, h("span.n", String(list.length))),
    h("div.tf-chips", list.map((c) => c.node)));
}

function render() {
  if (!els.pop) return;
  renderTokens();
  if (!open) return;
  const focusRef = document.activeElement?.closest?.(".tf-chip")?.dataset.ref;
  const selected = new Set(selectedRefs());
  const query = (els.input.value || "").trim().toLowerCase();
  const groups = vocab.v2 ? GROUPS.map((g) => group(g, selected, query)).filter(Boolean) : [];
  const more = vocab.v2 ? MORE.map((g) => group(g, selected, query)).filter(Boolean) : [];
  const moreSelected = MORE.reduce((n, g) => n + [...selected].filter((r) => parseRef(r)?.field === g.field).length, 0);
  if (more.length) {
    const details = h("details.tf-more-types", { open: moreOpen || moreSelected > 0 || Boolean(query) || null },
      h("summary", "更多类型", moreSelected ? h("span.n", `已选 ${moreSelected}`) : null), more);
    details.addEventListener("toggle", () => { moreOpen = details.open; });
    groups.push(details);
  }
  if (vocab.v2Available === false) clear(els.body, h("div.tf-none", "服务端暂未提供标签词表，只能搜索正文。"));
  else clear(els.body, groups.length ? groups : h("div.tf-none", query ? ["没有匹配的标签，按 ", h("span.kbd", "Enter"), " 搜索正文"] : "暂无可筛选的标签"));
  const chips = [...els.body.querySelectorAll(".tf-chip:not(:disabled)")];
  const current = chips.find((c) => c.dataset.ref === roving) || chips.find((c) => c.classList.contains("on")) || chips[0];
  if (current) { current.tabIndex = 0; roving = current.dataset.ref; }
  const mode = effectiveMode();
  for (const button of els.mode.querySelectorAll("[data-mode]")) {
    const active = button.dataset.mode === mode;
    button.classList.toggle("active", active);
    button.setAttribute("aria-checked", String(active));
    button.tabIndex = active ? 0 : -1;
  }
  const scope = byId("list-title")?.textContent || "全部收藏";
  clear(els.desc, mode === "all"
    ? ["在", h("b", `「${scope}」`), "中，只显示", h("b", "同时带有"), "所选全部标签的收藏。数字为加选该标签后的条数。"]
    : ["在", h("b", `「${scope}」`), "中，带有", h("b", "任一"), "所选标签的收藏都会显示。数字为带有该标签的条数。"]);
  const total = Number.isFinite(state.total) ? state.total : null;
  els.apply.textContent = total === null ? "显示结果" : `显示 ${total} 条`;
  els.clear.disabled = !selected.size;
  els.hint.textContent = selected.size ? `已选 ${selected.size} 个标签` : "";
  if (focusRef) {
    const node = [...els.body.querySelectorAll(".tf-chip")].find((c) => c.dataset.ref === focusRef);
    if (node && !node.disabled) { setRoving(node); node.focus({ preventScroll: true }); }
  }
}

function renderTokens() {
  const refs = selectedRefs();
  const max = innerWidth < 1100 ? 1 : 2;
  const shown = refs.slice(-max), hidden = refs.slice(0, refs.length - shown.length);
  clear(els.tokens,
    hidden.length ? h("button.tf-more", { type: "button", tabindex: "-1", dataset: { act: "more" }, title: hidden.map(refLabel).join("、"), "aria-label": `另有 ${hidden.length} 个标签` }, `+${hidden.length}`) : null,
    shown.map((ref) => h("span.tf-token", { dataset: { ref }, title: refLabel(ref) }, h("span.nm", refLabel(ref)),
      h("button.tf-x", { type: "button", tabindex: "-1", dataset: { act: "remove", ref }, "aria-label": `移除标签筛选 ${refLabel(ref)}` }, icon("x", 10)))));
  els.tokens.hidden = !refs.length;
  els.input.placeholder = refs.length ? "" : "搜索标题、正文、标签、收藏原因";
  syncChrome();
}

function syncChrome() {
  const n = selectedRefs().length;
  els.input.setAttribute("aria-expanded", String(open));
  els.toggle.setAttribute("aria-expanded", String(open));
  els.toggle.classList.toggle("has", n > 0);
  els.toggle.title = n ? `按标签筛选 · 已选 ${n} 个` : "按标签筛选";
  byId("search-clear").hidden = !els.input.value && !n;
  if (els.kbd) els.kbd.hidden = Boolean(els.input.value) || n > 0 || open || document.activeElement === els.input;
}

function setRoving(node) {
  for (const c of els.body.querySelectorAll(".tf-chip")) c.tabIndex = c === node ? 0 : -1;
  roving = node.dataset.ref;
}

export function openTagFilter() {
  if (open || !els.pop) return;
  open = true;
  els.pop.hidden = false;
  roving = "";
  countsKey = "";
  render();
  els.body.scrollTop = 0;
  scheduleCounts();
}
export function closeTagFilter(returnFocus = false) {
  if (!open) return;
  open = false;
  els.pop.hidden = true;
  clearTimeout(countsTimer);
  countsAbort?.abort();
  syncChrome();
  if (returnFocus) focusField();
}
export const tagFilterOpen = () => open;
function focusField() { suppressOpen = true; els.input.focus({ preventScroll: true }); suppressOpen = false; }

function nav(current, key) {
  const chips = [...els.body.querySelectorAll(".tf-chip:not(:disabled)")];
  if (!chips.length) return;
  const i = chips.indexOf(current);
  let next = null;
  if (key === "ArrowRight") next = chips[(i + 1) % chips.length];
  else if (key === "ArrowLeft") next = chips[(i - 1 + chips.length) % chips.length];
  else if (key === "Home") next = chips[0];
  else if (key === "End") next = chips[chips.length - 1];
  else {
    const r = current.getBoundingClientRect(), cx = r.left + r.width / 2, down = key === "ArrowDown";
    const candidates = chips.map((c) => [c, c.getBoundingClientRect()]).filter(([, cr]) => down ? cr.top > r.top + 4 : cr.top < r.top - 4);
    if (!candidates.length) { if (!down) focusField(); return; }
    const top = down ? Math.min(...candidates.map(([, cr]) => cr.top)) : Math.max(...candidates.map(([, cr]) => cr.top));
    const row = candidates.filter(([, cr]) => Math.abs(cr.top - top) < 4);
    row.sort((a, b) => Math.abs(a[1].left + a[1].width / 2 - cx) - Math.abs(b[1].left + b[1].width / 2 - cx));
    next = row[0][0];
  }
  if (next) { setRoving(next); next.focus({ preventScroll: true }); next.scrollIntoView({ block: "nearest" }); }
}

export function initTagFilter(options) {
  hooks = options;
  Object.assign(els, {
    wrap: byId("search-wrap"), field: byId("search-form"), input: byId("search"), tokens: byId("tf-tokens"),
    toggle: byId("tf-toggle"), kbd: byId("search-kbd"), pop: byId("tf-pop"), body: byId("tf-body"), mode: byId("tf-mode"),
    desc: byId("tf-desc"), clear: byId("tf-clear"), hint: byId("tf-hint"), apply: byId("tf-apply")
  });
  if (!els.pop) return;
  void loadCustomTags?.().then(() => render()).catch(() => {});
  els.input.addEventListener("focus", () => { syncChrome(); if (!suppressOpen) openTagFilter(); });
  els.input.addEventListener("blur", syncChrome);
  els.input.addEventListener("click", () => openTagFilter());
  els.input.addEventListener("input", () => { openTagFilter(); render(); });
  els.input.addEventListener("keydown", (event) => {
    if (event.isComposing) return;
    if (event.key === "Escape" && open) { event.preventDefault(); event.stopImmediatePropagation(); closeTagFilter(false); return; }
    if (event.key === "Backspace" && !els.input.value && selectedRefs().length) {
      event.preventDefault();
      const refs = selectedRefs();
      toggleTag(refs[refs.length - 1], false);
      return;
    }
    if (event.key === "ArrowDown" && open) {
      const c = els.body.querySelector(".tf-chip[tabindex='0']");
      if (c) { event.preventDefault(); event.stopImmediatePropagation(); c.focus({ preventScroll: true }); c.scrollIntoView({ block: "nearest" }); }
      return;
    }
    if (event.key === "Enter") closeTagFilter(false);
  }, true);
  els.field.addEventListener("mousedown", (event) => {
    if (event.target === els.input) return;
    event.preventDefault();
    if (!event.target.closest("button")) els.input.focus();
  });
  els.field.addEventListener("click", (event) => {
    const button = event.target.closest("button");
    if (!button) return;
    if (button === els.toggle) { if (open) closeTagFilter(false); else { focusField(); openTagFilter(); } }
    else if (button.dataset.act === "remove") toggleTag(button.dataset.ref, false);
    else if (button.dataset.act === "more") { focusField(); openTagFilter(); }
  });
  els.pop.addEventListener("mousedown", (event) => { if (!event.target.closest("summary")) event.preventDefault(); });
  els.pop.addEventListener("click", (event) => {
    const c = event.target.closest(".tf-chip");
    if (c) {
      if (c.disabled) return;
      const turningOn = !selectedRefs().includes(c.dataset.ref);
      // Typed text was used to find this tag; consume it so it doesn't also narrow the list.
      const consumed = turningOn && els.input.value.trim();
      if (consumed) els.input.value = "";
      setRoving(c);
      const refs = selectedRefs();
      const next = withTags(state.filters, turningOn ? [...refs, c.dataset.ref] : refs.filter((r) => r !== c.dataset.ref), effectiveMode());
      hooks.setFilters(next, consumed ? "" : state.search);
      return;
    }
    const m = event.target.closest("[data-mode]");
    if (m) { setMode(m.dataset.mode); return; }
    if (event.target.closest("#tf-clear")) {
      const focused = document.activeElement === els.clear;
      hooks.setFilters(withTags(state.filters, []));
      if (focused) focusField();
      return;
    }
    if (event.target.closest("#tf-apply")) { closeTagFilter(false); hooks.focusResults?.(); }
  });
  els.pop.addEventListener("keydown", (event) => {
    if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); closeTagFilter(true); return; }
    const c = event.target.closest(".tf-chip");
    if (c && ["ArrowRight", "ArrowLeft", "ArrowUp", "ArrowDown", "Home", "End"].includes(event.key)) { event.preventDefault(); event.stopPropagation(); nav(c, event.key); return; }
    if (event.target.closest("#tf-mode") && (event.key === "ArrowLeft" || event.key === "ArrowRight")) {
      event.preventDefault();
      setMode(effectiveMode() === "all" ? "any" : "all");
      els.mode.querySelector(`[data-mode="${effectiveMode()}"]`)?.focus();
    }
  });
  els.wrap.addEventListener("focusout", (event) => {
    if (open && event.relatedTarget && !els.wrap.contains(event.relatedTarget)) closeTagFilter(false);
  });
  document.addEventListener("mousedown", (event) => { if (open && !els.wrap.contains(event.target)) closeTagFilter(false); }, true);
  addEventListener("resize", renderTokens);
  on("list:loaded", () => { render(); scheduleCounts(); });
  on("taxonomy", render);
  on("custom-tags:changed", () => { void loadCustomTags?.().then(render).catch(() => {}); });
  renderTokens();
}

// Called after every filter change so tokens, counts and chip states follow.
export function syncTagFilter() {
  if (!els.pop) return;
  if (selectedRefs().length) pendingMode = tagMode();
  render();
  scheduleCounts();
}
