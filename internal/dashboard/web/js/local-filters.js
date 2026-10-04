// Reuse only a complete, negotiated server snapshot. Search, legacy dimensions,
// entity state, partial pages and unknown query syntax always go to the server.
const modes = { topics: "topics_mode", resource_kinds: "resource_mode", content_functions: "functions_mode", custom_tags: "custom_mode" };
const baseKeys = new Set(["limit", "view", "include_cache_identity", "filter_contract_version", "curation_status"]);
const allowed = new Set([...baseKeys, ...Object.keys(modes), ...Object.values(modes), "topic_refinements"]);
const statuses = ["pending", "processing", "completed", "failed", "exhausted", "unsupported"];
const curations = ["all", "inbox", "kept", "compiled", "drop"];

export function localFilterResult(path, params, sourceParams, page, catalog) {
  if (!["/api/bookmarks", "/api/tag-counts"].includes(path) || !catalog || page.local_filter_version !== 1 ||
      page.next_before_id !== null || !Array.isArray(page.items) || page.counts?.total !== page.items.length ||
      sourceParams.get("view") !== "summary" || [...sourceParams.keys()].some(key => !baseKeys.has(key)) ||
      [...params.keys()].some(key => !allowed.has(key) || params.getAll(key).length !== 1)) return null;
  const sourceView = sourceParams.get("curation_status") || "all";
  const targetView = params.get("curation_status") || "all";
  if (!curations.includes(targetView) || (sourceView !== "all" && sourceView !== targetView) ||
      params.get("view") !== "summary" || (params.has("filter_contract_version") && params.get("filter_contract_version") !== "1")) return null;
  if (params.has("include_cache_identity") && !["0", "1"].includes(params.get("include_cache_identity"))) return null;
  const limit = Number(params.get("limit"));
  if (!/^[0-9]+$/.test(params.get("limit")) || !Number.isInteger(limit) || limit < 1 || limit > 60) return null;
  const groups = [];
  for (const [field, modeKey] of Object.entries(modes)) {
    const mode = params.get(modeKey) || "any";
    if ((params.has(modeKey) && !["any", "all"].includes(params.get(modeKey))) || !["any", "all"].includes(mode)) return null;
    if (!params.has(field)) continue;
    const raw = params.get(field), terms = [...new Set(raw.split(","))];
    if (raw.length > 1024 || raw.split(",").length > 64 || terms.some(term => field === "custom_tags"
      ? !/^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/.test(term) : !catalog[field]?.some(entry => entry.id === term))) return null;
    groups.push({ field, terms, mode });
  }
  if (params.has("topic_refinements")) {
    const raw = params.get("topic_refinements"), terms = raw.split(",");
    if (raw.length > 1024 || raw.split(",").length > 64 || terms.some(id => !catalog.topics?.some(term => term.id === id && term.granularity === "specific"))) return null;
    groups.push({ field: "topics", terms, mode: "all" });
  }
  // Fail closed if a future/legacy summary cannot express the count contract.
  if (new Set(page.items.map(item => item.id)).size !== page.items.length ||
      page.items.some(item => !statuses.includes(item.status) || !curations.includes(item.curation_status))) return null;
  const values = (item, field) => field === "custom_tags" ? (item.custom_tags || []).map(tag => tag.id) : item.classification?.[field] || [];
  const items = page.items.filter(item => (targetView === "all" || item.curation_status === targetView) &&
    groups.every(({ field, terms, mode }) => terms[mode === "all" ? "every" : "some"](term => values(item, field).includes(term))));
  if (path === "/api/tag-counts") {
    const counts = { total: items.length };
    for (const field of Object.keys(modes)) {
      const totals = new Map();
      for (const item of items) for (const term of new Set(values(item, field))) totals.set(term, (totals.get(term) || 0) + 1);
      counts[field] = [...totals].map(([id, count]) => ({ id, count }));
    }
    return counts;
  }
  const counts = Object.fromEntries(statuses.map(status => [status, 0]));
  for (const item of items) counts[item.status]++;
  counts.total = items.length;
  return { items: structuredClone(items.slice(0, limit)), counts, next_before_id: items.length > limit ? items[limit - 1].id : null,
    ...(params.has("filter_contract_version") ? { filter_contract_version: 1 } : {}) };
}
