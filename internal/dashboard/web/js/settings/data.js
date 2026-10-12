// Settings data: tag catalog + personal tags + usage, collections with their
// rules, and the cross-device AI preferences. Every write goes through the
// receipt-backed send() and then refreshes what it touched.
import { request, send, uuid, collator, DIMENSIONS, MORE_DIMENSIONS } from "./core.js";
import { emit } from "../store.js";
import { invalidateTaxonomyReads, invalidateQueryReads } from "../api.js";

export const db = {
  catalog: null, // raw /api/tag-catalog
  tags: [], // normalized, see normalize()
  usage: new Map(), // tag_ref → {count, last_used_at, rules:[{collection_id,name,enabled,role}]}
  merges: [],
  collections: [],
  prefs: null, // {value, revision}
  prefsSupported: true,
  usageSupported: true,
};
const countField = (d) => (d === "forms" ? "form" : d === "uses" ? "use" : d);
const humanOnly = (d, id) => d === "uses" && id === "contra";

function normalize() {
  const data = db.catalog;
  const counts = new Map((data.counts || []).map((c) => [`${c.field}:${c.term}`, c.n]));
  const out = [];
  for (const d of [...DIMENSIONS, ...MORE_DIMENSIONS].map((x) => x.key).filter((k) => k !== "custom"))
    for (const t of data.catalog?.[d] || []) {
      const ref = `system/${d}/${t.id}`;
      const broader = (t.relations || []).find((r) => r.kind === "broader")?.id || null;
      const synonym = (t.relations || []).find((r) => r.kind === "synonym")?.id || null;
      out.push({
        kind: "system", d, id: t.id, ref, raw: t,
        label: t.label, aliases: t.aliases || [], description: t.description || "",
        includes: t.includes || [], excludes: t.excludes || [],
        ai: t.ai_enabled !== false && !humanOnly(d, t.id), humanOnly: humanOnly(d, t.id),
        archived: !t.active || Boolean(t.deprecated), parent: d === "topics" ? broader : null, mergedInto: synonym,
        granularity: t.granularity || "specific", more: !["topics", "resource_kinds", "content_functions"].includes(d),
        count: db.usage.get(ref)?.count ?? counts.get(`${countField(d)}:${t.id}`) ?? 0,
      });
    }
  for (const t of data.custom_tags || [])
    out.push({
      kind: "custom", d: "custom", id: t.id, ref: t.tag_ref, raw: t, revision: t.revision,
      label: t.label, aliases: [], description: "", includes: [], excludes: [], ai: false, humanOnly: true,
      archived: t.status !== "active", parent: null, more: false, count: db.usage.get(t.tag_ref)?.count ?? t.link_count ?? 0,
    });
  db.tags = out.sort((a, b) => collator.compare(a.label, b.label));
}
export const tagByRef = (ref) => db.tags.find((t) => t.ref === ref) || null;
export const topicById = (id) => db.tags.find((t) => t.d === "topics" && t.id === id) || null;
export const childrenOf = (t) => (t.d === "topics" ? db.tags.filter((x) => x.d === "topics" && x.parent === t.id && !x.archived) : []);
export const rulesUsing = (ref) => db.usage.get(ref)?.rules || db.collections.filter((c) => !c.deleted && (refsOf(c, "rule_tags").includes(ref) || refsOf(c, "rule_exclude_tags").includes(ref))).map((c) => ({ collection_id: c.id, name: c.name, enabled: Boolean(c.rule_enabled), role: refsOf(c, "rule_tags").includes(ref) ? "include" : "exclude" }));
export function refsOf(c, field = "rule_tags") {
  const v = c?.[field];
  if (Array.isArray(v)) return v;
  try {
    return JSON.parse(v || "[]");
  } catch {
    return [];
  }
}
/** Display label for a rule ref, including refs whose tag has gone. */
export function refLabel(ref) {
  const t = tagByRef(ref);
  if (t) return t.label;
  const m = db.merges.find((x) => x.status === "active" && ref.endsWith(`/${x.from}`));
  return m ? m.from : ref.split("/").pop();
}

export async function loadTags() {
  const [catalog, usage] = await Promise.all([
    request("/api/tag-catalog"),
    request("/api/tag-catalog/usage").catch((e) => { if (e.status === 404 || e.status === 409) db.usageSupported = false; return null; }),
  ]);
  db.catalog = catalog;
  if (usage) {
    db.usageSupported = true;
    db.usage = new Map((usage.items || []).map((u) => [u.tag_ref, u]));
    db.merges = usage.merges || [];
  }
  normalize();
  return db.tags;
}
export async function loadCollectionsData(force = true) {
  const { loadCollections } = await import("../collections.js");
  db.collections = (await loadCollections(force)) || [];
  return db.collections;
}
export async function loadPrefs() {
  try {
    db.prefs = await request("/api/preferences");
    db.prefsSupported = true;
  } catch (e) {
    if (e.status === 404 || e.status === 405) db.prefsSupported = false;
    db.prefs = null;
    throw e;
  }
  return db.prefs;
}

async function tagsChanged() {
  await loadTags();
  invalidateTaxonomyReads();
  emit("custom-tags:changed");
  try {
    const { loadV1, loadV2, loadCustomTags } = await import("../taxonomy.js");
    await Promise.all([loadV1(), loadV2(), loadCustomTags()]);
  } catch {}
  emit("taxonomy");
  emit("tags:catalog-changed");
}

/* ---------- tag writes ---------- */
export async function editTag(t, definition) {
  if (t.kind === "custom") {
    return customWrite(t, { label: definition.label ?? t.label });
  }
  const body = { operation_key: uuid(), expected_revision: db.catalog.revision, dimension: t.d, id: t.id, type: "edit", definition };
  try {
    return await send("/api/tag-catalog/operations", body);
  } finally {
    await tagsChanged().catch(() => {});
  }
}
async function customWrite(t, patch, method = "PATCH") {
  const body = { operation_key: uuid(), expected_revision: t.revision, ...(method === "PATCH" ? { label: t.label } : {}), ...patch };
  try {
    return await send(`/api/custom-tags/${encodeURIComponent(t.id)}`, body, method);
  } finally {
    await tagsChanged().catch(() => {});
  }
}
export async function setArchived(t, archived) {
  if (t.kind === "custom") return customWrite(t, { archived });
  const body = { operation_key: uuid(), expected_revision: db.catalog.revision, dimension: t.d, id: t.id, type: archived ? "archive" : "restore" };
  try {
    return await send("/api/tag-catalog/operations", body);
  } finally {
    await tagsChanged().catch(() => {});
  }
}
/** Personal tags only: removes it from every bookmark and retires it. */
export const deleteCustom = (t) => customWrite(t, { detach_all: true }, "DELETE");
export async function createTag(dimension, label, description = "") {
  let result;
  try {
    result = dimension === "custom"
      ? await send("/api/custom-tags", { operation_key: uuid(), label })
      : await send("/api/tag-catalog/operations", { operation_key: uuid(), expected_revision: db.catalog.revision, dimension: "topics", type: "create",
          definition: { label, description, aliases: [], includes: [], excludes: [], ai_enabled: true, granularity: "specific" } });
  } finally {
    await tagsChanged().catch(() => {});
  }
  if (dimension === "custom") return tagByRef(result?.tag?.tag_ref) || db.tags.find((t) => t.d === "custom" && t.label === label);
  return db.tags.find((t) => t.d === "topics" && t.label === label) || null;
}
export async function mergeTag(src, dst) {
  const body = src.kind === "custom"
    ? { operation_key: uuid(), kind: "custom", from: src.id, into: dst.id }
    : { operation_key: uuid(), expected_revision: db.catalog.revision, kind: "system", dimension: "topics", from: src.id, into: dst.id };
  try {
    return await send("/api/tag-catalog/merges", body);
  } finally {
    await Promise.all([tagsChanged(), loadCollectionsData()]).catch(() => {});
  }
}
export async function undoMerge(merge) {
  // System merges are catalog writes: re-read the revision right before undoing.
  const body = { operation_key: uuid() };
  if (merge.kind === "system") body.expected_revision = (await request("/api/tag-catalog")).revision;
  try {
    return await send(`/api/tag-catalog/merges/${merge.merge_key}/undo`, body);
  } finally {
    await Promise.all([tagsChanged(), loadCollectionsData()]).catch(() => {});
  }
}

/* ---------- collection writes ---------- */
export const collById = (id) => db.collections.find((c) => c.id === id) || null;
export async function collectionOp(c, type, fields = {}) {
  const body = { operation_key: uuid(), expected_revision: c.revision, type, ...fields };
  try {
    const result = await send(`/api/collections/${encodeURIComponent(c.id)}/operations`, body);
    invalidateQueryReads();
    return result.collection;
  } finally {
    await loadCollectionsData().catch(() => {});
    emit("collections:changed", c.id);
  }
}
export function ruleOf(c) {
  return {
    enabled: Boolean(c.rule_enabled), mode: c.rule_mode === "all" ? "all" : "any",
    tags: refsOf(c, "rule_tags"), exclude: refsOf(c, "rule_exclude_tags"),
    admission: c.rule_admission === "review" ? "review" : "direct", confirmedOnly: Boolean(c.rule_confirmed_only),
  };
}
/** Save the whole rule (v2 fields included so an older value is never left behind). */
export function saveRule(c, next) {
  const r = { ...ruleOf(c), ...next };
  return collectionOp(c, "rule", {
    enabled: r.enabled && r.tags.length > 0, mode: r.mode, tag_refs: r.tags,
    exclude_tag_refs: r.exclude.filter((x) => !r.tags.includes(x)), admission: r.admission, confirmed_only: r.confirmedOnly,
  });
}
export const saveCollectionMeta = (c, patch) => collectionOp(c, "edit", { name: c.name, description: c.description || "", pinned: Boolean(c.pinned), archived: Boolean(c.archived), ...patch });
export const rulePreview = (c) => request(`/api/collections/${encodeURIComponent(c.id)}/rules/preview`);
export const ruleCandidates = (c, status = "pending") => request(`/api/collections/${encodeURIComponent(c.id)}/rules/candidates?status=${status}&limit=100`);

/* ---------- preferences ---------- */
export const PREF_DEFAULTS = { ai_suggestion_limit: 3, ai_confirm_before_apply: false, ai_low_confidence_review: true };
export async function savePrefs(patch) {
  const body = { operation_key: uuid(), expected_revision: db.prefs?.revision ?? 0, patch };
  try {
    const saved = await send("/api/preferences/operations", body);
    db.prefs = { value: saved.value, revision: saved.revision };
    return db.prefs;
  } catch (e) {
    if (e.code === "revision_conflict" && e.payload?.value) db.prefs = { value: e.payload.value, revision: e.payload.revision };
    throw e;
  }
}
