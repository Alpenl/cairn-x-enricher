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
  return merged;
}

export function getItem(id) {
  return state.items.get(id) || null;
}

export function indexOf(id) {
  return state.order.indexOf(id);
}
