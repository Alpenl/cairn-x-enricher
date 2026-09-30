// Pure tag-system helpers. UI labels never invent a human confirmation from a
// bookmark-wide reviewed flag; the per-term state is the source of truth.
export const PRIMARY_TAG_FIELDS = Object.freeze([
  { key: "topics", label: "主题" }, { key: "resource_kinds", label: "资源类型" },
  { key: "content_functions", label: "内容特征" }
]);

export function tagRef(field, id) { return `system/${field}/${id}`; }
export function parseTagRef(ref) {
  const parts = String(ref || "").split("/");
  return parts[0] === "system" && parts.length === 3
    ? { field: parts[1], term: parts[2] }
    : parts[0] === "custom" && parts.length === 3 ? { field: "custom_tags", term: parts[2] } : null;
}
export function tagFieldState(payload, field) { return payload?.state?.fields?.[field] || {}; }
export function tagOrigin(payload, field, term) {
  const state = tagFieldState(payload, field);
  const value = (state.values || []).find((entry) => entry.term === term);
  if (value?.origin === "human") return value.human_action === "confirm" || value.confirmed ? "你已确认" : "你添加";
  return value?.origin === "automatic" ? "自动标签" : "历史来源未知";
}
export function reviewCandidates(payload, field) {
  return (tagFieldState(payload, field).candidates || []).filter((entry) => entry.verdict === "abstained");
}
export function rejectedTags(payload, field) {
  return (tagFieldState(payload, field).actions || []).filter((entry) => entry.action === "reject").map((entry) => entry.term);
}
export function normalizeTagName(name) { return String(name || "").normalize("NFKC").trim().replace(/\s+/g, " ").toLowerCase(); }
export function findTagName(name, catalog) {
  const normalized = normalizeTagName(name);
  return catalog.find((entry) => [entry.label, ...(entry.aliases || [])].some((value) => normalizeTagName(value) === normalized));
}

// A local draft is only a visual preview. The server resolves group clears,
// term reset/readmit, versions and undo atomically and returns authoritative state.
export function previewTagActions(payload, actions, customCatalog = []) {
  const next = structuredClone(payload);
  next.selection ||= {};
  next.state ||= { fields: {} };
  next.state.fields ||= {};
  next.custom_tags ||= [];
  for (const action of actions) {
    if (action.action === "replace") {
      const replacement = previewTagActions(next, [
        { action: "reject", tag_ref: action.from_tag_ref }, { action: "accept", tag_ref: action.to_tag_ref }
      ], customCatalog);
      Object.assign(next, replacement);
      continue;
    }
    if (action.dimension) {
      const field = action.dimension;
      next.selection[field] = action.action === "reset_group" ? [...(next.automatic?.[field] || [])] : [];
      next.state.fields[field] = { ...(next.state.fields[field] || {}), values: [],
        cleared_automatic: action.action === "set_empty" ? { origin: "human" } : null, actions: [] };
      continue;
    }
    const parsed = parseTagRef(action.tag_ref);
    if (!parsed) continue;
    const { field, term } = parsed;
    if (field === "custom_tags") {
      next.custom_tags = next.custom_tags.filter((entry) => entry.id !== term);
      if (action.action === "attach") {
        const entry = customCatalog.find((tag) => tag.id === term);
        if (entry) next.custom_tags.push(entry);
      }
      continue;
    }
    let selected = (next.selection[field] || []).filter((id) => id !== term);
    if (["accept", "confirm"].includes(action.action) ||
        action.action === "reset" && (next.automatic?.[field] || []).includes(term)) selected.push(term);
    next.selection[field] = selected;
    const state = next.state.fields[field] ||= {};
    state.values = (state.values || []).filter((entry) => entry.term !== term);
    if (selected.includes(term)) state.values.push({ term, origin: action.action === "reset" ? "automatic" : "human", confirmed: action.action === "confirm" });
    state.actions = (state.actions || []).filter((entry) => entry.term !== term);
    if (action.action !== "reset") state.actions.push({ term, action: action.action === "reject" ? "reject" : "accept", origin: "human", confirmed: action.action === "confirm" });
  }
  return next;
}

export function tagStatusText(payload, field) {
  const state = tagFieldState(payload, field);
  if (state.cleared_automatic || state.empty?.origin === "human") return "已关闭本组自动新增";
  if (state.incomplete) return "依据内容不完整，标签可继续编辑";
  return ({ not_run: "标签尚未判断", failed: "标签暂未更新，可重试", stale: "内容或定义已变化，人工决定保留",
    abstained: "有其他建议可选", completed_empty: "暂无匹配标签" })[state.status] || "";
}
