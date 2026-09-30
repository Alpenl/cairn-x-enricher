// Application controller: routing, layout, selection and the curation actions
// that span several components (status changes with auto-advance and undo,
// batch operations, export).
import { api, errorLabel } from "./api.js";
import * as backstage from "./backstage.js";
import * as curation from "./curation.js";
import * as detail from "./detail.js";
import * as diagnostics from "./diagnostics.js";
import { byId } from "./dom.js";
import { exportItems, exportServer } from "./export.js";
import { curationLabels, needsReview } from "./format.js";
import * as list from "./list.js";
import {
  apiParams, buildQuery, clearFacets, matchesStatusView, parseQuery, sinceLabel, toggleValue, withView
} from "./query.js";
import { initShortcuts, showHelp } from "./shortcuts.js";
import * as sidebar from "./sidebar.js";
import { emit, getItem, mergeItem, on, state } from "./store.js";
import { ENTITY_STATES, loadV1, loadV2, vocab } from "./taxonomy.js";
import { initTheme } from "./theme.js";
import { confirmAction, openMenu, runLastToastAction, toast } from "./ui.js";

const OVERVIEW_INTERVAL = 30000;
const OVERVIEW_EDIT_DELAY = 5000;
const app = byId("app");
let bootDeepLink = 0;
let overviewTimer = 0;
let overviewGeneration = 0;
let pendingCuration = 0;
let healthState = "ok";
let overviewState = "ok";
let prefetchTimer = 0;
let prefetchIdle = 0;

// --- URLs & routing ----------------------------------------------------------------

function querySuffix() {
  return buildQuery(state.filters, state.search);
}

function bookmarkURL(id) {
  return `/bookmarks/${id}${querySuffix()}`;
}

function libraryURL() {
  return `/${querySuffix()}`;
}

function viewHref(view) {
  return `/${buildQuery(withView(state.filters, view), state.search)}`;
}

function parseLocation() {
  const path = window.location.pathname;
  const match = path.match(/^\/bookmarks\/([1-9][0-9]*)$/);
  const { filters, search } = parseQuery(window.location.search);
  state.filters = filters;
  state.search = search;
  if (path === "/backstage") state.route = { name: "backstage", id: 0 };
  else if (match) state.route = { name: "bookmark", id: Number(match[1]) };
  else state.route = { name: "library", id: 0 };
}

function applyRouteClasses() {
  app.classList.toggle("route-backstage", state.route.name === "backstage");
  app.classList.toggle("detail-open", state.route.name === "bookmark");
  byId("backstage-view").hidden = state.route.name !== "backstage";
}

// --- Layout --------------------------------------------------------------------------

const wideQuery = matchMedia("(min-width: 1180px)");
const mediumQuery = matchMedia("(min-width: 760px)");

function updateLayout() {
  state.layout = wideQuery.matches ? "wide" : mediumQuery.matches ? "medium" : "narrow";
  app.dataset.layout = state.layout;
  if (state.layout === "wide") setSidebarOpen(false);
}

function setSidebarOpen(open) {
  app.classList.toggle("sidebar-open", open);
  byId("sidebar-backdrop").hidden = !open;
  for (const button of document.querySelectorAll("[data-open-sidebar]")) button.setAttribute("aria-expanded", String(open));
  if (open) byId("sidebar").querySelector("a, button")?.focus({ preventScroll: true });
}

function isFocusMode() {
  return app.classList.contains("focus-mode");
}

function toggleFocus() {
  app.classList.toggle("focus-mode");
  byId("focus-toggle").setAttribute("aria-pressed", String(isFocusMode()));
}

// --- Selection -------------------------------------------------------------------------

function cancelPrefetch() {
  clearTimeout(prefetchTimer);
  if (prefetchIdle && window.cancelIdleCallback) window.cancelIdleCallback(prefetchIdle);
  prefetchTimer = 0;
  prefetchIdle = 0;
}

function prefetch(id) {
  const item = getItem(id);
  if (!item || item.content_loaded !== false) return;
  const selected = state.selectedId;
  const query = querySuffix();
  const load = () => {
    prefetchIdle = 0;
    if (state.loading || selected !== state.selectedId || query !== querySuffix() || !state.order.includes(id) || !api.prefetchAvailable()) return;
    api.prefetchDetail(id).then(mergeItem).catch(() => {});
  };
  prefetchTimer = setTimeout(() => {
    prefetchTimer = 0;
    if (window.requestIdleCallback) prefetchIdle = window.requestIdleCallback(load, { timeout: 1000 });
    else load();
  }, 300);
}

function select(id, { fromList = false, scroll = true, prefetchNext = true } = {}) {
  if (!id) return;
  cancelPrefetch();
  const narrow = state.layout === "narrow";
  state.selectedId = id;
  list.markSelected(id, { scroll });
  detail.showItem(id);
  const url = bookmarkURL(id);
  if (narrow && state.route.name !== "bookmark") history.pushState({ detail: true }, "", url);
  else history.replaceState(history.state, "", url);
  state.route = { name: "bookmark", id };
  applyRouteClasses();
  const index = state.order.indexOf(id);
  if (prefetchNext && index >= 0 && state.order[index + 1]) prefetch(state.order[index + 1]);
  if (fromList && !narrow) byId("detail-scroll").scrollTop = 0;
}

async function step(delta) {
  if (state.route.name === "backstage") return;
  if (!state.order.length) return;
  let index = state.order.indexOf(state.selectedId);
  if (index === -1) {
    select(state.order[0]);
    return;
  }
  index += delta;
  if (index >= state.order.length && state.nextBeforeID) {
    await list.loadMore();
  }
  if (index < 0 || index >= state.order.length) return;
  select(state.order[index]);
}

function closeDetail() {
  if (state.layout !== "narrow") return;
  if (history.state?.detail) history.back();
  else {
    history.replaceState(null, "", libraryURL());
    state.route = { name: "library", id: 0 };
    applyRouteClasses();
  }
}

// --- Filters -----------------------------------------------------------------------------

function setFilters(filters, search = state.search, { push = false } = {}) {
  cancelPrefetch();
  state.filters = filters;
  state.search = search;
  if (byId("search").value.trim() !== search) byId("search").value = search;
  const keepBookmark = state.route.name === "bookmark" && state.layout !== "narrow";
  const url = keepBookmark ? bookmarkURL(state.selectedId) : libraryURL();
  if (state.route.name === "backstage") state.route = { name: "library", id: 0 };
  history[push ? "pushState" : "replaceState"](null, "", url);
  applyRouteClasses();
  backstage.showBackstage(false);
  state.checked.clear();
  sidebar.renderSidebar();
  list.reload({ reuse: true });
  if (state.layout !== "wide") setSidebarOpen(false);
}

const hooksForFilters = {
  toggleFilter: (key, value) => setFilters(toggleValue(state.filters, key, value)),
  setFilter: (key, value) => setFilters({ ...state.filters, [key]: value }),
  clearFacets: () => setFilters(clearFacets(state.filters)),
  setView: (view) => setFilters(withView(state.filters, view), state.search, { push: true }),
  sinceLabel,
  entityStateLabel: (id) => ENTITY_STATES.find((entry) => entry.id === id)?.label || id,
  searchEverywhere: () => setFilters(withView(state.filters, "all"))
};

let searchTimer = 0;
function wireSearch() {
  const input = byId("search");
  input.value = state.search;
  input.addEventListener("input", () => {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(() => {
      const next = input.value.trim();
      if (next !== state.search) setFilters(state.filters, next);
    }, 260);
  });
  byId("search-form").addEventListener("submit", (event) => {
    event.preventDefault();
    clearTimeout(searchTimer);
    const next = input.value.trim();
    if (next !== state.search) setFilters(state.filters, next);
    else if (state.order[0] && state.layout !== "narrow") select(state.order[0]);
    input.blur();
  });
  input.addEventListener("keydown", (event) => {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      input.blur();
      step(state.selectedId ? 1 : 0);
    }
    if (event.key !== "Escape") return;
    event.preventDefault();
    clearTimeout(searchTimer);
    if (input.value) {
      input.value = "";
      if (state.search) setFilters(state.filters, "");
    } else input.blur();
  });
  byId("search-clear").addEventListener("click", () => {
    input.value = "";
    if (state.search) setFilters(state.filters, "");
    input.focus();
  });
  const syncClear = () => { byId("search-clear").hidden = !input.value; };
  input.addEventListener("input", syncClear);
  on("list:loaded", syncClear);
}

// --- Overview counts -------------------------------------------------------------------------

function renderServiceState() {
  sidebar.setServiceState(overviewState === "ok" ? healthState : overviewState);
}

async function refreshStatus() {
  try {
    const response = await fetch("/status", { cache: "no-store" });
    if (!response.ok) throw new Error("status_unavailable");
    const health = await response.json();
    healthState = health.ready === false ? "not-ready" : "ok";
  } catch {
    healthState = "offline";
  }
  renderServiceState();
}

async function refreshOverview() {
  if (pendingCuration) return;
  const generation = overviewGeneration;
  try {
    const overview = await api.overview();
    // A read started before a local edit may contain the old counts. Let
    // the edit's trailing refresh fetch a new snapshot after it is saved.
    if (generation !== overviewGeneration || pendingCuration) return;
    state.overview = overview;
    overviewState = overview?.stale ? "backend" : "ok";
    renderServiceState();
    emit("overview", overview);
  } catch (error) {
    if (generation !== overviewGeneration) return;
    overviewState = error?.message === "network_error" ? "offline" : "backend";
    renderServiceState();
  }
}

function scheduleOverview(delay = OVERVIEW_EDIT_DELAY) {
  overviewGeneration++;
  clearTimeout(overviewTimer);
  overviewTimer = setTimeout(() => { overviewTimer = 0; refreshOverview(); }, delay);
}

function adjustOverviewCounts(changes, reverse = false) {
  if (!state.overview?.views) return;
  const views = { ...state.overview.views };
  for (const { previous, next } of changes) {
    if (!previous || !next || previous === next) continue;
    const from = reverse ? next : previous;
    const to = reverse ? previous : next;
    if (Number.isFinite(views[from])) views[from] = Math.max(0, views[from] - 1);
    if (Number.isFinite(views[to])) views[to]++;
  }
  for (const { uncertainBefore, uncertainAfter } of changes) {
    if (typeof uncertainBefore !== "boolean" || typeof uncertainAfter !== "boolean") continue;
    if (Number.isFinite(views.uncertain)) views.uncertain = Math.max(0,
      views.uncertain + (reverse ? Number(uncertainBefore) - Number(uncertainAfter) : Number(uncertainAfter) - Number(uncertainBefore)));
  }
  overviewGeneration++;
  state.overview = { ...state.overview, views };
  emit("overview", state.overview);
}

// --- Curation actions ----------------------------------------------------------------------------

function afterRemoval(removedSelected, successor) {
  if (!removedSelected) return;
  if (successor) select(successor, { scroll: true });
  else {
    state.selectedId = 0;
    detail.showItem(0);
    if (state.layout === "narrow") closeDetail();
    else history.replaceState(null, "", libraryURL());
    state.route = state.layout === "narrow" ? state.route : { name: "library", id: 0 };
  }
}

async function undoStatus(changes) {
  const countChanges = changes.map(({ id, previous }) => ({
    id, previous: getItem(id)?.curation_status, next: previous,
    previousReviewed: getItem(id)?.classification_reviewed,
    uncertainBefore: getItem(id) ? needsReview(getItem(id)) : false, uncertainAfter: false
  }));
  pendingCuration++;
  for (const { id, previous } of changes) {
    const item = getItem(id);
    if (!item) continue;
    mergeItem({ ...item, curation_status: previous, classification_reviewed: true });
    if (matchesStatusView(getItem(id), state.filters)) list.restoreRow(getItem(id));
    emit("item", id);
  }
  adjustOverviewCounts(countChanges);
  if (changes.length === 1 && state.order.includes(changes[0].id)) select(changes[0].id);
  const failures = [];
  await Promise.all(changes.map(async ({ id, previous }) => {
    try {
      mergeItem(await api.curation(id, { curation_status: previous }));
      emit("item", id);
    } catch (error) {
      failures.push({ id, error });
    }
  }));
  const failed = countChanges.filter(({ id }) => failures.some((entry) => entry.id === id));
  adjustOverviewCounts(failed, true);
  const leaving = [];
  for (const { id, previous, previousReviewed } of failed) {
    const item = getItem(id);
    if (!item || !previous) continue;
    mergeItem({ ...item, curation_status: previous, classification_reviewed: previousReviewed });
    if (matchesStatusView(getItem(id), state.filters)) list.restoreRow(getItem(id));
    else if (state.order.includes(id)) leaving.push(id);
    emit("item", id);
  }
  if (leaving.length) {
    const successor = list.removeRows(leaving);
    afterRemoval(leaving.includes(state.selectedId), successor);
  }
  pendingCuration--;
  scheduleOverview();
  if (failures.length) toast(`撤销没有全部完成：${errorLabel(failures[0].error.message)}`, { tone: "error" });
  else toast("已撤销", { tone: "ok" });
}

// setStatuses applies one curation status to several bookmarks. Rows that
// leave the current view disappear immediately and, when the open bookmark
// leaves, the next one opens: the core of inbox triage.
async function setStatuses(ids, status, { advance = true } = {}) {
  const changes = ids
    .map((id) => ({
      id, previous: getItem(id)?.curation_status || "inbox",
      previousReviewed: getItem(id)?.classification_reviewed,
      uncertainBefore: needsReview(getItem(id))
    }))
    .filter((change) => getItem(change.id) && change.previous !== status);
  if (!changes.length) return;
  pendingCuration++;
  const leaving = [];
  for (const { id } of changes) {
    const next = mergeItem({ ...getItem(id), curation_status: status, classification_reviewed: true });
    if (state.order.includes(id) && !matchesStatusView(next, state.filters)) leaving.push(id);
    else list.updateRow(id);
    emit("item", id);
  }
  const removedSelected = leaving.includes(state.selectedId);
  const successor = leaving.length ? list.removeRows(leaving) : 0;
  if (advance) afterRemoval(removedSelected, successor);
  else if (removedSelected) detail.showItem(state.selectedId);
  adjustOverviewCounts(changes.map(({ previous, uncertainBefore }) => ({ previous, next: status, uncertainBefore, uncertainAfter: false })));

  const failed = [];
  const queue = changes.slice();
  const worker = async () => {
    while (queue.length) {
      const change = queue.shift();
      try {
        // Only the status travels: a status change never confirms AI tags.
        mergeItem(await api.curation(change.id, { curation_status: status }));
        emit("item", change.id);
      } catch (error) {
        failed.push({ ...change, error });
      }
    }
  };
  await Promise.all(Array.from({ length: Math.min(4, changes.length) }, worker));
  if (failed.length) {
    adjustOverviewCounts(failed.map(({ previous, uncertainBefore }) => ({ previous, next: status, uncertainBefore, uncertainAfter: false })), true);
    for (const { id, previous, previousReviewed } of failed) {
      mergeItem({ ...getItem(id), curation_status: previous, classification_reviewed: previousReviewed });
      if (matchesStatusView(getItem(id), state.filters)) list.restoreRow(getItem(id));
      emit("item", id);
    }
    toast(`${failed.length} 条没有保存：${errorLabel(failed[0].error?.message)}`, { tone: "error" });
  }
  pendingCuration--;
  scheduleOverview();
  const saved = changes.filter((change) => !failed.some((entry) => entry.id === change.id));
  if (!saved.length) return;
  const label = curationLabels[status];
  toast(saved.length === 1 ? `已移到「${label}」` : `已把 ${saved.length} 条移到「${label}」`, {
    tone: "ok", action: { label: "撤销", key: "Z", run: () => undoStatus(saved) }
  });
}

async function confirmOne(id) {
  const item = getItem(id);
  if (!item) return;
  if (item.classification_reviewed && !vocab.tagSystemAvailable) {
    toast("这条的标签已经确认过了");
    return;
  }
  try {
    if (!await curation.confirmTags(id)) return;
  } catch {
    return;
  }
  if (!vocab.tagSystemAvailable) toast("已确认 AI 标签", { tone: "ok" });
  scheduleOverview();
  const updated = getItem(id);
  if (state.order.includes(id) && !matchesStatusView(updated, state.filters)) {
    const removedSelected = id === state.selectedId;
    const successor = list.removeRows([id]);
    afterRemoval(removedSelected, successor);
  } else list.updateRow(id);
}

async function confirmMany(ids) {
  const candidates = ids.filter((id) => {
    const item = getItem(id);
    return item && (vocab.tagSystemAvailable || !item.classification_reviewed) && (item.classification?.topics?.length || item.classification?.resource_kinds?.length || item.classification?.form || item.classification?.use);
  });
  if (!candidates.length) {
    toast("所选收藏没有待确认的 AI 标签");
    return;
  }
  let done = 0;
  for (const id of candidates) {
    try {
      if (await curation.confirmTags(id, { quiet: true })) done++;
    } catch {
      // Reported in the summary below.
    }
  }
  const leaving = candidates.filter((id) => state.order.includes(id) && !matchesStatusView(getItem(id), state.filters));
  const removedSelected = leaving.includes(state.selectedId);
  const successor = leaving.length ? list.removeRows(leaving) : 0;
  afterRemoval(removedSelected, successor);
  for (const id of candidates) list.updateRow(id);
  scheduleOverview();
  toast(done === candidates.length ? `已确认 ${done} 条的 AI 标签` : `已确认 ${done} 条，${candidates.length - done} 条失败`, { tone: done ? "ok" : "error" });
}

async function processMany(ids) {
  const items = ids.map(getItem).filter((item) => item && item.processable !== false && item.status !== "unsupported");
  if (!items.length) {
    toast("所选收藏都不能自动读取（只有 X 链接可以）", { tone: "error" });
    return;
  }
  const rewrite = items.filter((item) => item.ai_title || item.translated_text).length;
  const confirmed = await confirmAction({
    title: `处理 ${items.length} 条收藏？`,
    message: `会为每条调用模型读取原帖${rewrite ? `，其中 ${rewrite} 条已有内容会被覆盖` : ""}。人工整理不受影响。`,
    confirmLabel: "开始处理"
  });
  if (!confirmed) return;
  let accepted = 0;
  const rejected = [];
  for (let index = 0; index < items.length; index += 10) {
    try {
      const result = await api.process(items.slice(index, index + 10).map((item) => item.id));
      accepted += result.accepted.length;
      rejected.push(...result.rejected);
      for (const id of result.accepted) {
        mergeItem({ ...getItem(id), status: "processing" });
        list.updateRow(id);
        emit("item", id);
      }
    } catch (error) {
      rejected.push({ error: error.message });
    }
  }
  scheduleOverview();
  toast(rejected.length ? `已提交 ${accepted} 条，${rejected.length} 条未提交：${errorLabel(rejected[0].error)}` : `已提交 ${accepted} 条处理请求`,
    { tone: rejected.length && !accepted ? "error" : "ok" });
}

async function exportIds(ids) {
  const items = ids.map(getItem).filter(Boolean);
  if (!items.length) return;
  const close = toast(`正在导出 ${items.length} 条…`, { duration: 60000 });
  try {
    await exportItems(items, { scope: items.length === 1 ? "单条收藏" : "所选收藏" });
    close?.();
    toast(`已导出 ${items.length} 条`, { tone: "ok" });
  } catch {
    close?.();
    toast("导出失败，请重试", { tone: "error" });
  }
}

async function exportCurrentList() {
  const limit = Math.min(Math.max(state.total ?? state.order.length, 1), 500);
  const params = apiParams(state.filters, state.search, { limit, summary: false });
  params.delete("view");
  const close = toast("正在准备导出…", { duration: 60000 });
  try {
    await exportServer(params);
    close?.();
  } catch (error) {
    close?.();
    if (error?.message === "export_unsupported") {
      await exportItems(state.order.map(getItem).filter(Boolean), { hasMore: Boolean(state.nextBeforeID), scope: "当前已加载的收藏" })
        .catch(() => toast("导出失败，请重试", { tone: "error" }));
    } else toast("导出失败，请重试", { tone: "error" });
  }
}

function openListMenu(anchor) {
  openMenu(anchor, [
    { label: "导出当前结果（Markdown）", icon: "download", hint: state.total ? `${Math.min(state.total, 500)} 条，不调用模型` : "不调用模型", run: exportCurrentList },
    { label: state.checked.size ? "取消全部选择" : "选择全部已加载", icon: "checkSquare", kbd: "X", run: list.checkAllLoaded },
    { label: "刷新列表", icon: "refresh", run: () => list.reload() },
    "separator",
    { label: "键盘快捷键", icon: "keyboard", kbd: "?", run: showHelp }
  ]);
}

function openBatchMenu(anchor, ids) {
  openMenu(anchor, [
    { label: "确认 AI 标签", icon: "sparkles", hint: "只确认有 AI 建议且未确认的", run: () => confirmMany(ids) },
    { label: "导出所选（Markdown）", icon: "download", run: () => exportIds(ids) },
    { label: "重新读取原帖", icon: "refresh", hint: "会调用模型", run: () => processMany(ids) }
  ]);
}

// --- Backstage ----------------------------------------------------------------------------------

function openBackstage() {
  if (state.route.name === "backstage") return;
  state.route = { name: "backstage", id: 0 };
  history.pushState({ backstage: true }, "", `/backstage${querySuffix()}`);
  applyRouteClasses();
  sidebar.renderSidebar();
  backstage.showBackstage(true);
  if (state.layout !== "wide") setSidebarOpen(false);
}

// --- Boot ------------------------------------------------------------------------------------------

function onPopState() {
  const previousQuery = querySuffix();
  parseLocation();
  applyRouteClasses();
  byId("search").value = state.search;
  if (state.route.name === "backstage") {
    backstage.showBackstage(true);
    sidebar.renderSidebar();
    return;
  }
  backstage.showBackstage(false);
  sidebar.renderSidebar();
  if (querySuffix() !== previousQuery) { cancelPrefetch(); list.reload({ reuse: true }); }
  if (state.route.name === "bookmark") {
    state.selectedId = state.route.id;
    list.markSelected(state.selectedId);
    detail.showItem(state.selectedId);
  } else if (state.layout === "narrow") {
    list.markSelected(state.selectedId, { scroll: false });
  }
}

function escape() {
  if (state.checked.size) { list.clearChecked(); return true; }
  if (app.classList.contains("sidebar-open")) { setSidebarOpen(false); return true; }
  if (isFocusMode()) { toggleFocus(); return true; }
  if (state.layout === "narrow" && state.route.name === "bookmark") { closeDetail(); return true; }
  if (state.route.name === "backstage") { setFilters(state.filters); return true; }
  return false;
}

async function boot() {
  initTheme();
  parseLocation();
  updateLayout();
  wideQuery.addEventListener("change", updateLayout);
  mediumQuery.addEventListener("change", updateLayout);
  applyRouteClasses();

  list.initList({
    select: (id, options) => select(id, options),
    querySuffix,
    batchStatus: (ids, status) => { setStatuses(ids, status, { advance: true }); list.clearChecked(); },
    batchMenu: openBatchMenu,
    ...hooksForFilters
  });
  detail.initDetail({
    href: bookmarkURL,
    step,
    closeDetail,
    setStatus: (id, status) => setStatuses([id], status),
    exportItems: exportIds,
    isFocusMode,
    toggleFocus
  });
  curation.initCuration();
  diagnostics.initDiagnostics();
  sidebar.initSidebar({ viewHref, openBackstage, ...hooksForFilters });
  backstage.initBackstage({
    href: bookmarkURL,
    openItem: (id) => {
      state.route = { name: "bookmark", id };
      history.pushState(null, "", bookmarkURL(id));
      applyRouteClasses();
      backstage.showBackstage(false);
      sidebar.renderSidebar();
      state.selectedId = id;
      detail.showItem(id);
      if (!state.order.length) list.reload();
    }
  });
  wireSearch();

  on("tags:changed", () => { scheduleOverview(); list.reload({ silent: true }); });
  on("tag-filter-request", ({ field, term }) => setFilters(toggleValue(state.filters, field, term)));
  on("confirm-request", (id) => confirmOne(id));
  on("select-request", (id) => select(id));
  on("list:loaded", ({ silent } = {}) => {
    if (silent || state.route.name === "backstage") return;
    if (state.selectedId && state.order.includes(state.selectedId)) {
      list.markSelected(state.selectedId);
      bootDeepLink = 0;
      return;
    }
    if (bootDeepLink && state.selectedId === bootDeepLink) {
      bootDeepLink = 0;
      return;
    }
    if (state.layout === "narrow") return;
    if (state.order[0]) select(state.order[0], { scroll: false, prefetchNext: false });
    else {
      state.selectedId = 0;
      detail.showItem(0);
      if (state.route.name === "bookmark") {
        state.route = { name: "library", id: 0 };
        history.replaceState(null, "", libraryURL());
        applyRouteClasses();
      }
    }
  });
  on("backstage", () => scheduleOverview(0));
  on("overview", (overview) => {
    // A plain view whose server count grew has new bookmarks to offer.
    const view = state.filters.uncertain === "true" ? "uncertain" : state.filters.curation_status || "all";
    const plain = !state.search && Object.entries(state.filters).every(([key, value]) => !value || key === "curation_status" || (key === "uncertain" && view === "uncertain"));
    const known = overview?.views?.[view];
    list.showNewItems(plain && Number.isFinite(known) && state.total !== null && !state.loading ? known - state.total : 0);
  });

  for (const button of document.querySelectorAll("[data-open-sidebar]")) {
    button.addEventListener("click", () => setSidebarOpen(!app.classList.contains("sidebar-open")));
  }
  byId("sidebar-backdrop").addEventListener("click", () => setSidebarOpen(false));
  byId("list-menu").addEventListener("click", (event) => openListMenu(event.currentTarget));
  byId("help-button").addEventListener("click", showHelp);
  byId("focus-toggle").addEventListener("click", toggleFocus);
  window.addEventListener("popstate", onPopState);

  initShortcuts({
    step,
    open: () => {
      const active = document.activeElement?.closest?.("li.row");
      if (active) select(Number(active.dataset.id), { fromList: true });
      else if (state.selectedId) select(state.selectedId);
      if (state.layout !== "narrow") byId("detail-scroll").focus({ preventScroll: true });
    },
    search: () => { if (state.layout === "narrow" && state.route.name === "bookmark") closeDetail(); byId("search").focus(); byId("search").select(); },
    status: (status) => { if (state.selectedId) setStatuses([state.selectedId], status); },
    why: () => { if (state.selectedId) curation.focusWhy(); },
    confirm: () => { if (state.checked.size) confirmMany([...state.checked]); else if (state.selectedId) confirmOne(state.selectedId); },
    editTags: () => curation.toggleEditingAll(),
    undo: () => { if (!runLastToastAction()) toast("没有可以撤销的操作"); },
    check: () => { if (state.selectedId) list.toggleChecked(state.selectedId); },
    openSource: () => { const item = getItem(state.selectedId); if (item) window.open(item.url, "_blank", "noopener,noreferrer"); },
    exportCurrent: () => { if (state.checked.size) exportIds([...state.checked]); else if (state.selectedId) exportIds([state.selectedId]); },
    focus: toggleFocus,
    view: (view) => hooksForFilters.setView(view),
    backstage: openBackstage,
    escape,
    scrollDetail: (direction) => {
      const scroller = byId("detail-scroll");
      scroller.scrollBy({ top: direction * scroller.clientHeight * 0.85, behavior: "smooth" });
    }
  });

  sidebar.renderSidebar();
  if (state.route.name === "bookmark") {
    bootDeepLink = state.route.id;
    state.selectedId = state.route.id;
    detail.showItem(state.route.id);
  } else {
    detail.showItem(0);
  }
  if (state.route.name === "backstage") backstage.showBackstage(true);
  list.reload();
  refreshOverview();
  refreshStatus();
  setInterval(() => {
    if (document.hidden) return;
    if (!overviewTimer) refreshOverview();
    refreshStatus();
  }, OVERVIEW_INTERVAL);
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden) { scheduleOverview(0); refreshStatus(); }
  });

  Promise.allSettled([loadV1(), loadV2()]).then(([v1]) => {
    if (v1.status === "rejected") toast("读取标签词表失败，标签将显示原始编号", { tone: "error" });
    emit("taxonomy");
    if (state.selectedId) curation.refresh(state.selectedId);
  });
}

boot();
