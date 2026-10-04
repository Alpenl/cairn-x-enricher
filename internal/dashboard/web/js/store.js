// Application state and a tiny event bus. Components render from this state
// and announce changes through events instead of reaching into each other.

const listeners = new Map();
const bodyEntries = new Map();
const BODY_MAX_ITEMS = 40;
const BODY_MAX_BYTES = 8 * 1024 * 1024;

function bodySize(item) {
  return ((item.original_text?.length || 0) + (item.translated_text?.length || 0) + (item.formatted_content?.length || 0) +
    JSON.stringify(item.related_links || []).length) * 2;
}

function retainBody(id, item) {
  bodyEntries.delete(id);
  if (item.content_loaded !== false && (item.original_text || item.translated_text)) bodyEntries.set(id, bodySize(item));
  for (const key of bodyEntries.keys()) if (!state.items.has(key)) bodyEntries.delete(key);
  let bytes = [...bodyEntries.values()].reduce((sum, size) => sum + size, 0);
  for (const [key, size] of bodyEntries) {
    if (bodyEntries.size <= BODY_MAX_ITEMS && bytes <= BODY_MAX_BYTES) break;
    // The open article remains readable even when it alone exceeds the cache
    // budget. Everything else is reduced to an authoritative summary row.
    if (key === state.selectedId) continue;
    const saved = state.items.get(key);
    if (saved) state.items.set(key, { ...saved, content_loaded: false,
      original_text: undefined, translated_text: undefined, formatted_content: undefined, related_links: undefined });
    bodyEntries.delete(key);
    bytes -= size;
  }
}

export function bodyCacheStats() {
  return { items: bodyEntries.size, bytes: [...bodyEntries.values()].reduce((sum, size) => sum + size, 0),
    max_items: BODY_MAX_ITEMS, max_bytes: BODY_MAX_BYTES };
}

export function on(event, handler) {
  if (!listeners.has(event)) listeners.set(event, new Set());
  listeners.get(event).add(handler);
  return () => listeners.get(event)?.delete(handler);
}

export function emit(event, payload) {
  for (const handler of listeners.get(event) || []) {
    try {
      handler(payload);
    } catch (error) {
      // One broken subscriber must not stop the others from updating.
      console.error(`handler for ${event} failed`, error);
    }
  }
}

export const state = {
  route: { name: "library", id: 0 },
  filters: {},
  search: "",
  // Loaded list rows in display order (newest first) and the latest known copy
  // of every bookmark the page has seen, summary or detail.
  order: [],
  items: new Map(),
  nextBeforeID: null,
  total: null,
  counts: null,
  loading: false,
  listError: null,
  selectedId: 0,
  checked: new Set(),
  overview: null,
  layout: "wide"
};

// mergeItem keeps the richest copy: a summary row never erases full text that
// a detail read already delivered.
function libraryFacts(item) {
  return JSON.stringify([item.status, item.enriched_at, item.curation_status, item.classification_reviewed,
    item.classification, item.custom_tags, item.why, item.note, item.ai_title, item.summary, item.url, item.source]);
}

export function mergeItem(incoming) {
  if (!incoming || !incoming.id) return null;
  const previous = state.items.get(incoming.id);
  let merged = incoming;
  const beforeBody = previous?.cache_identity, nextBody = incoming.cache_identity;
  // Missing/invalid versions from an older backend cannot establish that a
  // stored private body still belongs to the returned summary.
  const sameBody = beforeBody?.schema_version === 1 && nextBody?.schema_version === 1 &&
    Number.isSafeInteger(beforeBody.body_revision) && beforeBody.body_revision >= 0 &&
    beforeBody.body_revision === nextBody.body_revision;
  const sameContent = previous && sameBody && previous.enriched_at === incoming.enriched_at && previous.status === incoming.status &&
    previous.url === incoming.url &&
    Number.isSafeInteger(beforeBody.content_revision) && beforeBody.content_revision >= 0 &&
    beforeBody.content_revision === nextBody.content_revision;
  if (sameContent && incoming.content_loaded === false && previous.content_loaded !== false) {
    merged = {
      ...previous, ...incoming, content_loaded: previous.content_loaded,
      original_text: previous.original_text, translated_text: previous.translated_text, formatted_content:previous.formatted_content, formatting_status:previous.formatting_status,
      related_links: previous.related_links, images: incoming.images ?? previous.images
    };
  }
  state.items.set(incoming.id, merged);
  retainBody(incoming.id, merged);
  const identityChanged = previous?.cache_identity && merged.cache_identity &&
    ["content_revision", "personal_revision", "latest_decision_id", "latest_entity_revision"].some((key) =>
      previous.cache_identity[key] !== merged.cache_identity[key]);
  // Loading full text alone does not change a filtered list. New tags, human
  // curation or server revisions do, including changes found by detail polling.
  if (previous && (identityChanged || libraryFacts(previous) !== libraryFacts(merged))) emit("library:changed", incoming.id);
  return merged;
}

export function getItem(id) {
  const item = state.items.get(id) || null;
  if (bodyEntries.has(id)) {
    const bytes = bodyEntries.get(id);
    bodyEntries.delete(id);
    bodyEntries.set(id, bytes);
  }
  return item;
}

export function indexOf(id) {
  return state.order.indexOf(id);
}
