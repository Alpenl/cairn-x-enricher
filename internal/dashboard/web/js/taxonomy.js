// Controlled vocabularies. v1 (topics/forms/uses) is always present; the
// multidimensional v2 vocabulary is optional and its absence switches the UI
// to the v1 editor and hides filters the backend cannot evaluate.
import { api } from "./api.js";
import { emit, on } from "./store.js";

export const V2_DIMENSIONS = Object.freeze([
  { key: "topics", label: "主题", multi: true, max: 64 },
  { key: "resource_kinds", label: "资源类型", multi: true, max: 64, optional: true },
  { key: "content_functions", label: "内容特征", multi: true, max: 8 },
  { key: "carriers", label: "载体", multi: false, max: 1 },
  { key: "affordances", label: "潜在用途", multi: true, max: 8 }
]);

export const V1_DIMENSIONS = Object.freeze([
  { key: "topics", label: "主题", multi: true, max: 3, vocabulary: "topics" },
  { key: "form", label: "形态", multi: false, max: 1, vocabulary: "forms" },
  { key: "use", label: "用途", multi: false, max: 1, vocabulary: "uses" }
]);

export const ENTITY_STATES = Object.freeze([
  { id: "completed_nonempty", label: "已完成，有实体" },
  { id: "completed_empty", label: "已完成，无实体" },
  { id: "not_run", label: "未运行" },
  { id: "failed", label: "失败" },
  { id: "stale", label: "来源已变化" }
]);

export const vocab = {
  custom: [],
  tagSystemAvailable: null,
  v1: null,
  v2: null,
  v2Available: null,
  labels: new Map()
};

function index(dimension, terms) {
  for (const term of terms || []) vocab.labels.set(`${dimension}:${term.id}`, term);
}

export async function loadV1() {
  const catalog = await api.taxonomy();
  vocab.v1 = catalog;
  // A legacy response must not replace the managed names in a modern catalog.
  if (!vocab.v2) index("topics", catalog.topics);
  if (!vocab.v2?.forms) index("forms", catalog.forms);
  if (!vocab.v2?.uses) index("uses", catalog.uses);
  return catalog;
}

export async function loadV2() {
  try {
    const catalog = await api.taxonomyV2();
    const valid = catalog && catalog.available !== false
      && V2_DIMENSIONS.every(({ key, optional }) => optional || Array.isArray(catalog[key]));
    vocab.v2Available = Boolean(valid);
    if (!valid) return null;
    vocab.v2 = catalog;
    if (Array.isArray(catalog.resource_kinds)) loadCustomTags().catch(() => {});
    for (const { key } of V2_DIMENSIONS) index(key, catalog[key]);
    index("forms", catalog.forms);
    index("uses", catalog.uses);
    return catalog;
  } catch (error) {
    vocab.v2Available = false;
    throw error;
  }
}

// termLabel resolves a controlled ID to its display name, degrading to the raw
// ID before the vocabulary loads or for a retired term.
export function termLabel(dimension, id, fallback = "") {
  const key = dimension === "topic_refinements" ? "topics" : dimension === "form" ? "forms" : dimension === "use" ? "uses" : dimension;
  const term = vocab.labels.get(`${key}:${id}`);
  return term?.label || fallback || id;
}

export function termActive(dimension, id) {
  const key = dimension === "form" ? "forms" : dimension === "use" ? "uses" : dimension;
  const term = vocab.labels.get(`${key}:${id}`);
  return term ? term.active !== false && !term.deprecated : false;
}

export function terms(dimension) {
  if (dimension === "custom_tags") return vocab.custom;
  if (dimension === "form") return vocab.v2?.forms || vocab.v1?.forms || [];
  if (dimension === "use") return vocab.v2?.uses || vocab.v1?.uses || [];
  if (V2_DIMENSIONS.some(({ key }) => key === dimension) && vocab.v2) return vocab.v2[dimension] || [];
  if (dimension === "topics") return vocab.v1?.topics || [];
  return [];
}

let customFlight = null, customUntil = 0, customEpoch = 0;
function invalidateCustomTags() { customEpoch++; customUntil = 0; customFlight = null; }
on("custom-tags:changed", invalidateCustomTags);
on("account:changed", () => { invalidateCustomTags(); vocab.custom = [];
  for (const key of vocab.labels.keys()) if (key.startsWith("custom_tags:")) vocab.labels.delete(key);
});
export function loadCustomTags() {
  if (customUntil > Date.now()) return Promise.resolve(vocab.custom);
  if (customFlight) return customFlight;
  const epoch = customEpoch;
  const flight = api.customTags().then((payload) => {
    if (epoch !== customEpoch) return loadCustomTags();
    if (payload.available === false) { customUntil = Date.now() + 60_000; return []; }
    const next = (payload.tags || []).map((tag) => ({ ...tag, active: tag.status === "active" }));
    const changed = JSON.stringify(next) !== JSON.stringify(vocab.custom);
    vocab.custom = next;
    customUntil = Date.now() + 60_000;
    for (const key of vocab.labels.keys()) if (key.startsWith("custom_tags:")) vocab.labels.delete(key);
    index("custom_tags", next);
    if (changed) emit("taxonomy");
    return next;
  }).finally(() => { if (customFlight === flight) customFlight = null; });
  customFlight = flight;
  return flight;
}

export function visibleDimensions() {
  return V2_DIMENSIONS.filter(({ key, optional }) => !optional || Array.isArray(vocab.v2?.[key]));
}
