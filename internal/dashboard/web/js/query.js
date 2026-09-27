// URL <-> library state. The query string is the single source of truth for
// what the list shows, so every view, filter and search is linkable and the
// browser history restores it. Pure module: no DOM access.

export const MULTI_KEYS = Object.freeze(["topics", "content_functions", "carriers", "affordances", "entity_state"]);
export const SINGLE_KEYS = Object.freeze(["curation_status", "form", "use", "source", "since", "uncertain"]);
export const FILTER_KEYS = Object.freeze([...SINGLE_KEYS, ...MULTI_KEYS]);

// Dimensions whose values only the multidimensional (v2) Worker can evaluate.
// Sending them requires the explicit filter contract so an old backend fails
// loudly instead of silently ignoring a condition.
const CONTRACT_KEYS = Object.freeze([...MULTI_KEYS, "form", "use"]);

export const VIEWS = Object.freeze([
  { id: "inbox", label: "收件箱", icon: "inbox", key: "g i" },
  { id: "kept", label: "精选", icon: "star", key: "g s" },
  { id: "compiled", label: "已编入笔记", icon: "book", key: "g c" },
  { id: "drop", label: "搁置", icon: "archive", key: "g d" },
  { id: "all", label: "全部收藏", icon: "layers", key: "g a" },
  { id: "uncertain", label: "待确认分类", icon: "help", key: "g u" }
]);

export const STATUSES = Object.freeze(["inbox", "kept", "compiled", "drop"]);

export function emptyFilters() {
  return Object.fromEntries(FILTER_KEYS.map((key) => [key, ""]));
}

export function splitList(value) {
  return String(value || "").split(",").filter(Boolean);
}

export function parseQuery(search) {
  const params = new URLSearchParams(search);
  const filters = emptyFilters();
  for (const key of FILTER_KEYS) filters[key] = params.get(key)?.trim() || "";
  // The bare landing page is the inbox. Any other link keeps its historical
  // meaning, where an absent status means every status.
  if (![...params.keys()].length) filters.curation_status = "inbox";
  else if (!filters.curation_status) filters.curation_status = "all";
  // Shared URLs from before multi-select used a single `topic`; it becomes
  // one value of the full topic filter instead of being silently dropped.
  const legacyTopic = params.get("topic")?.trim();
  if (legacyTopic) filters.topics = [...new Set([...splitList(filters.topics), legacyTopic])].join(",");
  return { filters, search: params.get("q")?.trim() || "" };
}

export function isDefault(filters, search) {
  return !search && FILTER_KEYS.every((key) => (key === "curation_status" ? filters[key] === "inbox" : !filters[key]));
}

export function buildQuery(filters, search) {
  if (isDefault(filters, search)) return "";
  const params = new URLSearchParams();
  if (search) params.set("q", search);
  for (const key of FILTER_KEYS) if (filters[key]) params.set(key, filters[key]);
  if (!params.has("curation_status")) params.set("curation_status", "all");
  return `?${params}`;
}

export function needsFilterContract(filters) {
  return CONTRACT_KEYS.some((key) => filters[key]);
}

export function apiParams(filters, search, { limit = 40, beforeId = null, summary = !search } = {}) {
  const params = new URLSearchParams({ limit: String(limit) });
  // Search results show a matching excerpt from the full text, so they need
  // the full rows; plain browsing only needs the summary representation.
  if (summary) params.set("view", "summary");
  if (search) params.set("q", search);
  for (const key of FILTER_KEYS) {
    const value = filters[key];
    if (!value || (key === "curation_status" && value === "all")) continue;
    params.set(key, value);
  }
  if (needsFilterContract(filters)) params.set("filter_contract_version", "1");
  if (beforeId) params.set("before_id", String(beforeId));
  return params;
}

export function activeView(filters) {
  const status = filters.curation_status || "all";
  if (filters.uncertain === "true" && status === "all") return "uncertain";
  return status;
}

export function withView(filters, view) {
  const next = { ...filters };
  if (view === "uncertain") {
    next.curation_status = "all";
    next.uncertain = "true";
  } else {
    next.curation_status = view;
    if (activeView(filters) === "uncertain") next.uncertain = "";
  }
  return next;
}

export function toggleValue(filters, key, value) {
  const next = { ...filters };
  if (MULTI_KEYS.includes(key)) {
    const values = new Set(splitList(filters[key]));
    if (values.has(value)) values.delete(value);
    else values.add(value);
    next[key] = [...values].join(",");
  } else {
    next[key] = filters[key] === value ? "" : value;
  }
  return next;
}

// facetFilterCount counts conditions beyond the view itself.
export function facetFilterCount(filters) {
  let count = 0;
  for (const key of FILTER_KEYS) {
    if (key === "curation_status") continue;
    if (key === "uncertain" && activeView(filters) === "uncertain") continue;
    count += MULTI_KEYS.includes(key) ? splitList(filters[key]).length : filters[key] ? 1 : 0;
  }
  return count;
}

export function clearFacets(filters) {
  const next = emptyFilters();
  next.curation_status = filters.curation_status || "all";
  if (activeView(filters) === "uncertain") next.uncertain = "true";
  return next;
}

// matchesStatusView reports whether an item still belongs to the current view
// after a curation change, so the list can drop it and advance.
export function matchesStatusView(item, filters) {
  const status = filters.curation_status;
  if (status && status !== "all" && item.curation_status !== status) return false;
  if (filters.uncertain === "true" && item.classification_reviewed) return false;
  return true;
}

// Local midnight N days ago, as the RFC 3339 instant the API expects.
export function sinceDaysAgo(days, now = new Date()) {
  const date = new Date(now.getFullYear(), now.getMonth(), now.getDate() - days);
  return date.toISOString();
}

export function sinceFromDateInput(value) {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value || "")) return "";
  const date = new Date(`${value}T00:00:00`);
  return Number.isFinite(date.getTime()) ? date.toISOString() : "";
}

export function dateInputFromSince(value) {
  const date = new Date(value);
  if (!value || !Number.isFinite(date.getTime())) return "";
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
}

export function sinceLabel(value, now = new Date()) {
  const date = new Date(value);
  if (!value || !Number.isFinite(date.getTime())) return "";
  const days = Math.round((new Date(now.getFullYear(), now.getMonth(), now.getDate()) - new Date(date.getFullYear(), date.getMonth(), date.getDate())) / 86400000);
  if (days === 0) return "今天";
  if ([7, 30, 90].includes(days)) return `近 ${days} 天`;
  return `${date.getMonth() + 1}月${date.getDate()}日起`;
}
