// Same-origin API client. The browser never holds a Worker token or model key:
// every call goes to the Go service, which forwards it with its own credentials.
import { emit, getItem, on } from "./store.js";

const ERROR_LABELS = Object.freeze({
  job_busy: "这条正在处理中",
  not_found: "这条收藏不存在",
  backend_error: "Cloudflare 后端暂时不可用",
  queue_full: "本机处理队列已满，请稍后再试",
  manual_queue_full: "待执行的人工请求已满，请稍后重试",
  invalid_operation_key: "请求编号无效，请重试",
  shutting_down: "服务正在重启，请稍后再试",
  invalid_ids: "所选收藏无效",
  invalid_source: "原文不能为空或过长",
  input_changed: "这条收藏已更新，请核对原文后重试",
  operation_conflict: "这次提交与先前请求不一致，请重新提交",
  source_unsupported: "服务尚未支持安全保存原文，请稍后重试",
  invalid_curation: "整理内容无效，请检查标签和收藏原因",
  invalid_id: "收藏编号无效",
  invalid_query: "筛选条件无效",
  invalid_json: "请求内容格式不对，请重试",
  invalid_content_type: "请求类型不受支持，请刷新页面重试",
  invalid_override: "这个标签操作无效",
  tag_system_unsupported: "后端尚未启用新版标签编辑",
  duplicate_tag: "已有同名标签，请使用已有项",
  custom_tag_conflict: "这个标记已更新，请刷新后重试",
  tag_in_use: "这个标记仍有关联收藏，请先核对影响范围",
  revision_conflict: "已被其他客户端更新",
  snapshot_conflict: "已被其他客户端更新",
  lease_conflict: "已有抓取任务在进行中",
  provider_result_unknown: "上次模型调用结果尚未核对，已暂停重试；可粘贴新的原文或更换来源。",
  unsupported_filter_contract: "服务暂不支持完整筛选，请更新服务或清除筛选后浏览。",
  v2_unsupported: "后端不支持这个操作",
  no_replayable_run: "还没有可重算的分类记录",
  network_error: "网络连接失败，请检查服务是否在线"
});

// Unknown codes are surfaced verbatim rather than hidden, so a new backend
// error is visible instead of being replaced by a generic message.
export function errorLabel(code) {
  return ERROR_LABELS[code] || code || "请求失败，请重试";
}

export const errorCodes = Object.freeze(Object.keys(ERROR_LABELS));

export class APIError extends Error {
  constructor(code, status, payload = {}) {
    super(code);
    this.name = "APIError";
    this.status = status;
    this.payload = payload;
    // The server's current revision travels with a CAS conflict so the UI can
    // explain it and let the user re-apply instead of showing a generic error.
    if (typeof payload.revision === "number") this.revision = payload.revision;
  }
}

export async function fetchJSON(path, options = {}) {
  let response;
  try {
    response = await fetch(path, { cache: "no-store", ...options });
  } catch (error) {
    if (error?.name === "AbortError") throw error;
    throw new APIError("network_error", 0);
  }
  if (!response.ok) {
    const payload = await response.json().catch(() => ({}));
    throw new APIError(payload.error || `HTTP ${response.status}`, response.status, payload);
  }
  return response.json();
}

function jsonBody(method, body) {
  return { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
}

// Processing endpoints answer 409 with a per-ID rejection list; that is a
// normal result, not a transport failure.
async function processingRequest(path, body) {
  let response;
  try {
    response = await fetch(path, jsonBody("POST", body));
  } catch {
    throw new APIError("network_error", 0);
  }
  const payload = await response.json().catch(() => ({}));
  if (!response.ok && !Array.isArray(payload.rejected)) {
    throw new APIError(payload.error || `HTTP ${response.status}`, response.status, payload);
  }
  return {
    accepted: Array.isArray(payload.accepted) ? payload.accepted : [],
    rejected: Array.isArray(payload.rejected) ? payload.rejected : []
  };
}

// Keep the operation key and source revision across a transport retry. A new
// text value is a new action and reads the current revision before submission.
export async function prepareSourceSubmission(id, text, previous, baseRevision) {
  if (previous?.text === text) return previous;
  const revision = Number.isSafeInteger(baseRevision)
    ? baseRevision
    : (await fetchJSON(`/api/bookmarks/${id}`)).cache_identity?.content_revision;
  if (!Number.isSafeInteger(revision) || revision < 0) throw new APIError("source_unsupported", 503);
  return { text, operation_key: newOperationKey(`manual-source-${id}`), expected_revision: revision };
}

const pendingProcessKeys = new Map();
const pendingRefreshKeys = new Map();
function processKey(id) {
  if (pendingProcessKeys.has(id)) return pendingProcessKeys.get(id);
  const storageKey = `cairn:manual-process:${id}`;
  let key;
  try { key = globalThis.sessionStorage?.getItem(storageKey); } catch { /* storage can be disabled */ }
  if (!key) key = newOperationKey(`manual-process-${id}`);
  pendingProcessKeys.set(id, key);
  try { globalThis.sessionStorage?.setItem(storageKey, key); } catch { /* in-memory retry still works */ }
  return key;
}

function clearProcessKey(id) {
  pendingProcessKeys.delete(id);
  try { globalThis.sessionStorage?.removeItem(`cairn:manual-process:${id}`); } catch { /* optional storage */ }
}

async function processRequest(ids) {
  const operation_keys = Object.fromEntries(ids.map(id => [String(id), processKey(id)]));
  const result = await processingRequest("/api/bookmarks/process", { ids, operation_keys });
  for (const id of result.accepted) clearProcessKey(id);
  for (const { id, error } of result.rejected) {
    if (["manual_queue_full", "lease_conflict", "job_busy", "not_found", "invalid_operation_key"].includes(error)) {
      clearProcessKey(id);
    }
  }
  return result;
}

async function refreshSourceRequest(id) {
  let key = pendingRefreshKeys.get(id);
  if (!key) {
    try { key = globalThis.sessionStorage?.getItem(`cairn:refresh-source:${id}`); } catch { /* optional storage */ }
  }
  if (!key) key = newOperationKey(`refresh-source-${id}`);
  pendingRefreshKeys.set(id, key);
  try { globalThis.sessionStorage?.setItem(`cairn:refresh-source:${id}`, key); } catch { /* optional storage */ }
  try {
    const result = await fetchJSON(`/api/bookmarks/${id}/refresh-source`, jsonBody("POST", { operation_key: key }));
    pendingRefreshKeys.delete(id);
    try { globalThis.sessionStorage?.removeItem(`cairn:refresh-source:${id}`); } catch { /* optional storage */ }
    return result;
  } catch (error) {
    if ([409, 429].includes(error?.status)) {
      pendingRefreshKeys.delete(id);
      try { globalThis.sessionStorage?.removeItem(`cairn:refresh-source:${id}`); } catch { /* optional storage */ }
    }
    throw error;
  }
}

const once = new Map();
function cached(key, load) {
  if (!once.has(key)) {
    once.set(key, load().catch((error) => { once.delete(key); throw error; }));
  }
  return once.get(key);
}

// Filter navigation can revisit an identical query while the server is still
// expensive to read. Keep a small, short-lived copy of successful responses;
// explicit refreshes and background polling always go to the server.
const queryCache = new Map();
const QUERY_TTL_MS = 10_000;
const QUERY_MAX_ITEMS = 10;
const QUERY_MAX_BYTES = 4 * 1024 * 1024;
let queryBytes = 0;
let queryGeneration = 0;

export function invalidateQueryReads() {
  queryGeneration++;
  queryCache.clear();
  queryBytes = 0;
}
on("tags:changed", invalidateQueryReads);
on("library:changed", invalidateQueryReads);

function forgetQuery(key) {
  queryBytes -= queryCache.get(key)?.bytes || 0;
  queryCache.delete(key);
}

function queryRead(path, params, signal, { reuse = false } = {}) {
  const stable = new URLSearchParams(params);
  stable.sort();
  const key = `${path}?${stable}`;
  if (signal?.aborted) return Promise.reject(new DOMException("Request aborted", "AbortError"));
  const stored = queryCache.get(key);
  if (stored && stored.until <= Date.now()) forgetQuery(key);
  else if (stored && reuse) {
    queryCache.delete(key);
    queryCache.set(key, stored);
    return Promise.resolve(structuredClone(stored.value));
  }
  const generation = queryGeneration;
  return fetchJSON(key, { signal, priority: path === "/api/bookmarks" ? "high" : "low" }).then((value) => {
    if (generation !== queryGeneration || signal?.aborted) return value;
    const bytes = JSON.stringify(value).length * 2;
    if (bytes <= QUERY_MAX_BYTES) {
      forgetQuery(key);
      queryCache.set(key, { value: structuredClone(value), bytes, until: Date.now() + QUERY_TTL_MS });
      queryBytes += bytes;
      while (queryCache.size > QUERY_MAX_ITEMS || queryBytes > QUERY_MAX_BYTES) forgetQuery(queryCache.keys().next().value);
    }
    return value;
  });
}

async function mutate(load, id) {
  invalidateQueryReads();
  if (id) invalidateDetail(id);
  try { return await load(); }
  finally {
    // Reads that overlapped a write may still contain the pre-write snapshot.
    invalidateQueryReads();
    emit("library:changed");
  }
}

// A nearby row is prefetched only once. A later explicit refresh starts a new
// generation so a slow old prefetch cannot put stale text back into the cache.
const detailFlights = new Map();
const prefetchedDetails = new Map();
let readingSupported = null;
const detailStates = new Map();
const PREFETCH_MAX_ITEMS = 4;
const PREFETCH_MAX_BYTES = 2 * 1024 * 1024;
const PREFETCH_TTL_MS = 15_000;
let prefetchBytes = 0;
const auxCache = new Map();
const auxFlights = new Map();
const auxEpochs = new Map();
const auxActive = new Map();
const AUX_MAX_ITEMS = 64;
const AUX_MAX_BYTES = 2 * 1024 * 1024;
let auxBytes = 0;

function forgetAux(key) {
  const entry = auxCache.get(key);
  if (entry) auxBytes -= entry.bytes;
  auxCache.delete(key);
  if (entry && !auxActive.get(entry.id) &&
      ![...auxCache.values()].some((cached) => cached.id === entry.id)) auxEpochs.delete(entry.id);
}

function compactAux() {
  while (auxCache.size > AUX_MAX_ITEMS || auxBytes > AUX_MAX_BYTES) {
    forgetAux(auxCache.keys().next().value);
  }
}

function invalidateAux(id) {
  auxEpochs.set(id, (auxEpochs.get(id) || 0) + 1);
  for (const [key, entry] of auxCache) if (entry.id === id) forgetAux(key);
  if (!auxActive.get(id)) auxEpochs.delete(id);
}

function auxKey(kind, id, identity) {
  const versions = identity && [identity.content_revision, identity.body_revision,
    identity.personal_revision, identity.latest_decision_id, identity.latest_entity_revision];
  return identity?.schema_version === 1 && versions.every((value) => Number.isSafeInteger(value) && value >= 0)
    ? `${kind}:${id}:${versions.join(":")}` : null;
}

function rememberAux(kind, id, identity, value) {
  const key = auxKey(kind, id, identity);
  if (!key) return;
  const bytes = JSON.stringify(value).length * 2;
  if (bytes > AUX_MAX_BYTES) return;
  forgetAux(key);
  auxCache.set(key, { id, value: structuredClone(value), bytes });
  auxBytes += bytes;
  compactAux();
}

function readAuxDirect(kind, id, identity, { fresh = false } = {}) {
  const path = `/api/bookmarks/${id}/${kind}`;
  const key = auxKey(kind, id, identity);
  if (!key) return fetchJSON(path);
  if (fresh) forgetAux(key);
  else {
    const stored = auxCache.get(key);
    if (stored) {
      auxCache.delete(key);
      auxCache.set(key, stored);
      return Promise.resolve(structuredClone(stored.value));
    }
  }
  const epoch = auxEpochs.get(id) || 0;
  const current = auxFlights.get(key);
  if (fresh && current) current.invalidated = true;
  if (!fresh && current?.epoch === epoch && !current.invalidated) return current.promise;
  const flight = { epoch, invalidated: false, promise: null };
  auxActive.set(id, (auxActive.get(id) || 0) + 1);
  flight.promise = fetchJSON(path).then((value) => {
    if (flight.invalidated || (auxEpochs.get(id) || 0) !== epoch) return null;
    rememberAux(kind, id, identity, value);
    return value;
  }).finally(() => {
    if (auxFlights.get(key) === flight) auxFlights.delete(key);
    const active = (auxActive.get(id) || 1) - 1;
    if (active) auxActive.set(id, active);
    else { auxActive.delete(id); if (![...auxCache.values()].some((entry) => entry.id === id)) auxEpochs.delete(id); }
  });
  auxFlights.set(key, flight);
  return flight.promise;
}

function readAux(kind, id, identity, options = {}) {
  const key = auxKey(kind, id, identity);
  const loaded = getItem(id);
  if (options.fresh || readingSupported === false || (key && auxCache.has(key)) ||
      (loaded?.content_loaded !== false && loaded?.cache_identity &&
        auxKey(kind, id, loaded.cache_identity) === key)) {
    return readAuxDirect(kind, id, identity, options);
  }
  // Opening a detail already needs the article. Let that single-snapshot read
  // supply tags and entities too; older Workers fall back to the old routes.
  return readDetail(id).then((item) => readAuxDirect(kind, id, item?.cache_identity || identity, options));
}

async function fetchReadingDetail(id, { prefetch = false } = {}) {
  const options = { priority: prefetch ? "low" : "high" };
  if (readingSupported === false) return { detail: await fetchJSON(`/api/bookmarks/${id}`, options) };
  try {
    const current = getItem(id);
    const bodyRevision = current?.content_loaded !== false &&
      Number.isSafeInteger(current?.cache_identity?.body_revision) && current.cache_identity.body_revision >= 0
      ? current.cache_identity.body_revision : null;
    const suffix = bodyRevision === null ? "" : `?body_revision=${bodyRevision}`;
    const reading = await fetchJSON(`/api/bookmarks/${id}/reading${suffix}`, options);
    if (reading?.version !== 1 || reading.detail?.id !== id || !reading.detail.cache_identity ||
        reading.selection?.available !== true || !reading.entities) throw new APIError("invalid_reading", 502);
    if (reading.body_unchanged) {
      if (bodyRevision === null || reading.detail.cache_identity.body_revision !== bodyRevision ||
          current?.content_loaded === false) throw new APIError("invalid_reading", 502);
      reading.detail.original_text = current.original_text;
      reading.detail.translated_text = current.translated_text;
      reading.detail.content_loaded = true;
    }
    readingSupported = true;
    return reading;
  } catch (error) {
    if (![404, 405].includes(error?.status) &&
        !(error?.status === 503 && error.message === "reading_unsupported")) throw error;
    const detail = await fetchJSON(`/api/bookmarks/${id}`, options);
    readingSupported = false;
    return { detail };
  }
}

function forgetPrefetch(id) {
  const old = prefetchedDetails.get(id);
  if (old) prefetchBytes -= old.bytes;
  prefetchedDetails.delete(id);
  if (detailStates.get(id)?.active === 0 && !detailFlights.has(id)) detailStates.delete(id);
}

function rememberPrefetch(id, item) {
  const bytes = JSON.stringify(item).length * 2;
  if (bytes > PREFETCH_MAX_BYTES) return;
  forgetPrefetch(id);
  prefetchedDetails.set(id, { item: structuredClone(item), bytes, until: Date.now() + PREFETCH_TTL_MS });
  prefetchBytes += bytes;
  while (prefetchedDetails.size > PREFETCH_MAX_ITEMS || prefetchBytes > PREFETCH_MAX_BYTES) {
    forgetPrefetch(prefetchedDetails.keys().next().value);
  }
}

function invalidateDetail(id) {
  invalidateAux(id);
  const state = detailStates.get(id) || { generation: 0, active: 0 };
  state.generation++;
  forgetPrefetch(id);
  if (state.active > 0) detailStates.set(id, state);
  else detailStates.delete(id);
}

function readDetail(id, { prefetch = false, fresh = false } = {}) {
  let state = detailStates.get(id);
  if (!state) { state = { generation: 0, active: 0 }; detailStates.set(id, state); }
  if (fresh) {
    state.generation++;
    forgetPrefetch(id);
    invalidateAux(id);
  } else if (!prefetch) {
    const ready = prefetchedDetails.get(id);
    if (ready) {
      forgetPrefetch(id);
      if (ready.until > Date.now()) return Promise.resolve(ready.item);
    }
  }
  const current = detailFlights.get(id);
  if (!fresh && current?.generation === state.generation) {
    if (!prefetch) { current.used = true; forgetPrefetch(id); }
    return current.promise;
  }
  const generation = state.generation;
  const flight = { used: !prefetch, generation, promise: null };
  detailStates.set(id, state);
  state.active++;
  flight.promise = fetchReadingDetail(id, { prefetch }).then(({ detail: item, selection, entities }) => {
    if (state.generation !== generation) return null;
    if (selection && entities) {
      invalidateAux(id);
      rememberAux("v2-selection", id, item.cache_identity, selection);
      rememberAux("entities", id, item.cache_identity, entities);
    }
    if (!flight.used) rememberPrefetch(id, item);
    return item;
  }).finally(() => {
    if (detailFlights.get(id) === flight) detailFlights.delete(id);
    state.active--;
    if (state.active === 0 && !prefetchedDetails.has(id)) detailStates.delete(id);
  });
  detailFlights.set(id, flight);
  return flight.promise;
}

export const api = {
  cacheStats: () => ({ prefetch_items: prefetchedDetails.size, prefetch_bytes: prefetchBytes,
    auxiliary_items: auxCache.size, auxiliary_bytes: auxBytes, query_items: queryCache.size, query_bytes: queryBytes }),
  list: (params, signal, options) => queryRead("/api/bookmarks", params, signal, options),
  detail: (id) => readDetail(id),
  detailFresh: (id) => readDetail(id, { fresh: true }),
  prefetchAvailable: () => readingSupported === true,
  prefetchDetail: (id) => readDetail(id, { prefetch: true }),
  identity: (id) => fetchJSON(`/api/bookmarks/${id}/identity`),
  overview: () => fetchJSON("/api/overview"),
  backstage: () => fetchJSON("/api/backstage"),
  taxonomy: () => cached("taxonomy", () => fetchJSON("/api/taxonomy")),
  taxonomyV2: () => cached("taxonomy-v2", () => fetchJSON("/api/v2-taxonomy")),
  curation: (id, body) => mutate(() => fetchJSON(`/api/bookmarks/${id}/curation`, jsonBody("PATCH", body)), id),
  v2Selection: (id, identity, options) => readAux("v2-selection", id, identity, options),
  v2Override: (id, body) => mutate(() => fetchJSON(`/api/bookmarks/${id}/v2-override`, jsonBody("POST", body)), id),
  tags: (id) => fetchJSON(`/api/bookmarks/${id}/tags`),
  editTags: (id, body) => mutate(() => fetchJSON(`/api/bookmarks/${id}/tags`, jsonBody("POST", body)), id),
  tagHistory: (id, beforeId) => fetchJSON(`/api/bookmarks/${id}/tag-history?limit=30${beforeId ? `&before_id=${beforeId}` : ""}`),
  customTags: () => fetchJSON("/api/custom-tags"),
  createCustomTag: (body) => mutate(() => fetchJSON("/api/custom-tags", jsonBody("POST", body))),
  renameCustomTag: (id, body) => mutate(() => fetchJSON(`/api/custom-tags/${encodeURIComponent(id)}`, jsonBody("PATCH", body))),
  archiveCustomTag: (id, body) => mutate(() => fetchJSON(`/api/custom-tags/${encodeURIComponent(id)}`, jsonBody("DELETE", body))),
  tagCounts: (params, signal) => queryRead("/api/tag-counts", params, signal, { reuse: true }),
  evidence: (id) => fetchJSON(`/api/bookmarks/${id}/evidence`),
  classificationStatus: (id) => fetchJSON(`/api/bookmarks/${id}/classification-status`),
  entities: (id, identity, options) => readAux("entities", id, identity, options),
  correctEntity: (id, body) => mutate(() => fetchJSON(`/api/bookmarks/${id}/entities`, jsonBody("POST", body)), id),
  retryClassification: (id) => mutate(() => fetchJSON(`/api/bookmarks/${id}/retry-classification`, { method: "POST" }), id),
  refreshSource: (id) => mutate(() => refreshSourceRequest(id), id),
  replayPolicy: (id, commit) => mutate(() => fetchJSON(`/api/bookmarks/${id}/replay-policy`, jsonBody("POST", commit ? { commit: true } : {})), id),
  process: (ids) => { ids.forEach(invalidateDetail); return mutate(() => processRequest(ids)); },
  submitSource: (id, submission) => mutate(() => processingRequest(`/api/bookmarks/${id}/source`, {
    original_text: submission.text, operation_key: submission.operation_key,
    expected_revision: submission.expected_revision
  }), id)
};

export function imagePath(key) {
  // A new URL namespace avoids reusing responses cached by older releases.
  return "/api/images/" + String(key).split("/").map(encodeURIComponent).join("/") + "?privacy=1";
}

// newOperationKey gives every new logical action its own identity. Only a
// retry of the same action reuses it, so a later identical click can never be
// swallowed as a replay of an earlier stored result.
export function newOperationKey(prefix) {
  const random = globalThis.crypto?.randomUUID
    ? globalThis.crypto.randomUUID()
    : `${Date.now()}-${Math.random().toString(16).slice(2)}-${Math.random().toString(16).slice(2)}`;
  return `${prefix}-${random}`;
}
