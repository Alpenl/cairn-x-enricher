// Navigation: the whole library plus collections (collections.js renders the
// list under it) and the service status line. Filtering moved to the search
// popover (tag-filter.js). On narrower screens the element becomes a drawer.
import { byId, clear, h } from "./dom.js";
import { icon } from "./icons.js";
import { activeView, VIEWS } from "./query.js";
import { on, state } from "./store.js";

const els = {};
let hooks = {};

function reconcileChildren(parent, children) {
  const keep = new Set(children);
  for (const child of [...parent.children]) if (!keep.has(child)) child.remove();
  let cursor = parent.firstChild;
  for (const child of children) {
    if (child !== cursor) parent.insertBefore(child, cursor);
    cursor = child.nextSibling;
  }
}

// --- Views ------------------------------------------------------------------------

function renderViews() {
  const current = state.route.name === "backstage" || state.filters.collection_id ? "" : activeView(state.filters);
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
  else if (status === "processing-paused") { text = "自动处理暂停"; tone = "warn"; }
  else if (status === "not-ready") { text = "服务未就绪"; tone = "danger"; }
  else if (status === "backend") { text = "后端暂时不可用"; tone = "danger"; }
  else if (attention > 0) { text = `${attention} 条需要处理`; tone = "warn"; }
  else if (queued > 0) { text = `正在处理 ${queued} 条`; tone = "work"; }
  else text = "服务正常";
  clear(link, h("span.status-dot", { dataset: { tone } }), h("span.service-text", text), icon("chevronRight", 14, "service-caret"));
  link.setAttribute("aria-current", state.route.name === "backstage" ? "page" : "false");
}

// Kept for callers that used to refresh facet counts on visibility changes.
export function syncFacetVisibility() {}

export function renderSidebar() {
  renderViews();
}

export function initSidebar(options) {
  hooks = options;
  Object.assign(els, { root: byId("sidebar"), views: byId("nav-views"), service: byId("service-link") });
  els.service.addEventListener("click", (event) => {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) return;
    event.preventDefault();
    hooks.openBackstage();
  });
  on("overview", () => { renderViews(); renderService(); });
  on("collections:loaded", renderViews);
}
