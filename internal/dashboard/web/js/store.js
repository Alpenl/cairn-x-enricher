// Application state and a tiny event bus. Components render from this state
// and announce changes through events instead of reaching into each other.

const listeners = new Map();

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
  const sameContent = previous && previous.enriched_at === incoming.enriched_at && previous.status === incoming.status;
  if (sameContent && incoming.content_loaded === false && previous.content_loaded !== false) {
    merged = {
      ...previous, ...incoming, content_loaded: previous.content_loaded,
      original_text: previous.original_text, translated_text: previous.translated_text,
      related_links: previous.related_links, images: incoming.images ?? previous.images
    };
  }
  state.items.set(incoming.id, merged);
  const identityChanged = previous?.cache_identity && merged.cache_identity &&
    ["content_revision", "personal_revision", "latest_decision_id", "latest_entity_revision"].some((key) =>
      previous.cache_identity[key] !== merged.cache_identity[key]);
  // Loading full text alone does not change a filtered list. New tags, human
  // curation or server revisions do, including changes found by detail polling.
  if (previous && (identityChanged || libraryFacts(previous) !== libraryFacts(merged))) emit("library:changed", incoming.id);
  return merged;
}

export function getItem(id) {
  return state.items.get(id) || null;
}

export function indexOf(id) {
  return state.order.indexOf(id);
}
