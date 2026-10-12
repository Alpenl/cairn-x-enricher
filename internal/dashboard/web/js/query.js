// URL <-> library state. The query string is the single source of truth for
// what the list shows, so every view, filter and search is linkable and the
// browser history restores it. Pure module: no DOM access.

export const MULTI_KEYS = Object.freeze(["tags", "topics", "topic_refinements", "resource_kinds", "custom_tags", "content_functions", "carriers", "affordances", "entity_state"]);
export const SINGLE_KEYS = Object.freeze(["collection_id", "tags_mode", "topics_mode", "resource_mode", "custom_mode", "functions_mode", "curation_status", "form", "use", "source", "since", "uncertain"]);
export const FILTER_KEYS = Object.freeze([...SINGLE_KEYS, ...MULTI_KEYS]);

// Dimensions whose values only the multidimensional (v2) Worker can evaluate.
// Sending them requires the explicit filter contract so an old backend fails
// loudly instead of silently ignoring a condition.
const CONTRACT_KEYS = Object.freeze([...MULTI_KEYS, "tags_mode", "topics_mode", "resource_mode", "custom_mode", "functions_mode", "form", "use"]);

// The redesigned workspace has one library view; collections are the only
// other navigation. Curation status stays in the data (old links such as
// ?curation_status=drop keep working) but is no longer a UI concept.
export const VIEWS = Object.freeze([
  { id: "all", label: "全部收藏", icon: "layers", key: "g a" }
]);
// Hidden legacy views: reachable from old URLs, labelled in the list title.
export const LEGACY_VIEWS = Object.freeze([
  { id: "inbox", label: "收件箱" },
  { id: "kept", label: "精选" },
  { id: "compiled", label: "已编入笔记" },
  { id: "drop", label: "搁置" },
  { id: "uncertain", label: "待确认分类" }
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
  // The bare landing page is the whole library; an absent status always
  // means every status.
  if (!filters.curation_status) filters.curation_status = "all";
  // Shared URLs from before multi-select used a single `topic`; it becomes
  // one value of the full topic filter instead of being silently dropped.
  const legacyTopic = params.get("topic")?.trim();
  if (legacyTopic) filters.topics = [...new Set([...splitList(filters.topics), legacyTopic])].join(",");
  return { filters, search: params.get("q")?.trim() || "" };
}

export function isDefault(filters, search) {
  return !search && FILTER_KEYS.every((key) => (key === "curation_status" ? filters[key] === "all" : !filters[key]));
}

export function buildQuery(filters, search) {
  if (isDefault(filters, search)) return "";
  const params = new URLSearchParams();
  if (search) params.set("q", search);
  for (const key of FILTER_KEYS) if (filters[key] && !(key === "curation_status" && filters[key] === "all")) params.set(key, filters[key]);
  return `?${params}`;
}

export function needsFilterContract(filters) {
  return CONTRACT_KEYS.some((key) => filters[key]);
}

export function apiParams(filters, search, { limit = 40, beforeId = null, summary = true } = {}) {
  const params = new URLSearchParams({ limit: String(limit) });
  // Search summaries include a bounded matching excerpt. The Go client
  // negotiates support and falls back once for older Worker versions.
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
  const next = { ...filters, collection_id: "" };
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
    if (key === "curation_status" || key === "collection_id" || key === "tags_mode") continue;
    if (key === "uncertain" && activeView(filters) === "uncertain") continue;
    count += MULTI_KEYS.includes(key) ? splitList(filters[key]).length : filters[key] ? 1 : 0;
  }
  return count;
}

export function clearFacets(filters) {
  const next = emptyFilters();
  next.curation_status = filters.curation_status || "all";
  next.collection_id = filters.collection_id || "";
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
