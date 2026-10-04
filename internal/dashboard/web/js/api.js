// Same-origin API client. The browser never holds a Worker token or model key:
// every call goes to the Go service, which forwards it with its own credentials.
import { emit, getItem, on, state } from "./store.js";
import { beginOfflineMutation, forgetOffline, observeOfflineScope, offlineScopeVersion } from "./offline.js";
import { localFilterResult } from "./local-filters.js";

let filterCatalog = null;

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
  network_error: "网络连接失败，请检查服务是否在线",
  account_changed: "连接的账号已变更，正在刷新页面"
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
  const taxonomyRequest = /^\/api\/(?:v2-)?taxonomy$/.test(path);
  const refinementRequest = new URLSearchParams(path.split("?")[1] || "").has("topic_refinements");
  if (taxonomyRequest || refinementRequest) options = { ...options, headers: { ...options.headers,
    "X-Cairn-Tag-System": "1", "X-Cairn-Topic-Granularity": "1" } };
  const scopeVersion = offlineScopeVersion();
  try {
    response = await fetch(path, { cache: "no-store", ...options });
  } catch (error) {
    if (error?.name === "AbortError") throw error;
    throw new APIError("network_error", 0);
  }
  checkResponseScope(response, scopeVersion);
  const acceptedScopeVersion = offlineScopeVersion();
  if (!response.ok) {
    const payload = await response.json().catch(() => ({}));
    assertCurrentScope(acceptedScopeVersion);
    throw new APIError(payload.error || `HTTP ${response.status}`, response.status, payload);
  }
  const payload = await response.json().catch((error) => {
    assertCurrentScope(acceptedScopeVersion);
    throw error;
  });
  assertCurrentScope(acceptedScopeVersion);
  const granularity = response.headers?.get("X-Cairn-Topic-Granularity") === "1" && response.headers?.get("X-Cairn-Tag-System") === "1";
  if (refinementRequest && !granularity) throw new APIError("unsupported_filter_contract", 409);
  if (taxonomyRequest && !granularity && Array.isArray(payload.topics)) {
    payload.topics = payload.topics.map(({ granularity, navigation, ...term }) => term);
  }
  if (path === "/api/v2-taxonomy") filterCatalog = granularity ? payload : null;
  return payload;
}

function jsonBody(method, body) {
  return { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
}

// Processing endpoints answer 409 with a per-ID rejection list; that is a
// normal result, not a transport failure.
async function processingRequest(path, body) {
  let response;
  const scopeVersion = offlineScopeVersion();
  try {
    response = await fetch(path, jsonBody("POST", body));
  } catch {
    throw new APIError("network_error", 0);
  }
  checkResponseScope(response, scopeVersion);
  const acceptedScopeVersion = offlineScopeVersion();
  const payload = await response.json().catch(() => ({}));
  assertCurrentScope(acceptedScopeVersion);
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
let primeUntil = 0;

export function invalidateQueryReads() {
  queryGeneration++;
  queryCache.clear();
  queryBytes = 0;
  primeUntil = 0;
}
on("tags:changed", invalidateQueryReads);
on("library:changed", invalidateQueryReads);
on("account:changed", () => { filterCatalog = null; });

function forgetQuery(key) {
  queryBytes -= queryCache.get(key)?.bytes || 0;
  queryCache.delete(key);
}

function queryRead(path, params, signal, { reuse = false, priority } = {}) {
  const stable = new URLSearchParams(params);
  // Opt in without changing the response shape for older NAS consumers.
  if (path === "/api/bookmarks" && stable.get("view") === "summary") {
    stable.set("include_cache_identity", "1");
    stable.set("local_filter", "1");
  }
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
  if (reuse) {
    for (const [sourceKey, source] of queryCache) {
      if (source.until <= Date.now()) { forgetQuery(sourceKey); continue; }
      if (!sourceKey.startsWith("/api/bookmarks?")) continue;
      const local = localFilterResult(path, stable, new URLSearchParams(sourceKey.split("?")[1]), source.value, filterCatalog);
      // Derived reads share the source's original expiry; they never renew it
      // or evict the complete snapshot by filling the exact-query LRU.
      if (local) return Promise.resolve(local);
    }
  }
  const generation = queryGeneration;
  return fetchJSON(key, { signal, priority: priority || (path === "/api/bookmarks" ? "high" : "low") }).then((value) => {
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

// A fast, explicitly partial preview. Only fields also searched by the server
// are used; full-text matches and the authoritative total still come from it.
export function previewSearch(params, previous) {
  const text = params.get("q")?.trim();
  if (!text || text.length > 200 || text.includes("\0")) return null;
  const fold = value => String(value || "").replace(/[A-Z]/g, c => c.toLowerCase());
  const terms = text.split(/\s+/).map(fold);
  if (terms.length > 10) return null;
  const matching = items => items.filter(item => {
    const fields = [item.url, item.note, item.ai_title, item.summary, item.why].map(fold);
    return terms.every(term => fields.some(field => field.includes(term)));
  });
  const filters = new URLSearchParams(params); filters.delete("q");
  for (const [key, entry] of queryCache) {
    if (entry.until <= Date.now() || !key.startsWith("/api/bookmarks?")) continue;
    const page = localFilterResult("/api/bookmarks", filters, new URLSearchParams(key.split("?")[1]), entry.value, filterCatalog);
    if (!page) continue;
    return matching(page.items);
  }
  // Already displayed rows can also provide a partial preview when only q
  // changes. This does not renew cache TTL or skip the authoritative request.
  if (previous?.params) {
    const old = new URLSearchParams(previous.params); old.delete("q"); old.sort(); filters.sort();
    if (old.toString() === filters.toString()) return matching(previous.items.filter(Boolean));
  }
  return null;
}

// A deep link initially reads only a filtered subset. After that foreground
// read, warm one bounded page of its view when the overview says it can fit.
// Larger libraries, search and rapid navigation do not launch a library crawl.
async function primeFilters(params, signal) {
  const view = params.get("curation_status") || "all";
  const size = state.overview?.views?.[view];
  if (!filterCatalog || !Number.isInteger(size) || size > 60 || params.has("q") || signal?.aborted || primeUntil > Date.now()) return;
  const base = new URLSearchParams({ limit: "60", view: "summary" });
  if (view !== "all") base.set("curation_status", view);
  // An unfiltered foreground page has already populated this exact query.
  const filters = [...params.keys()].filter(key => !["limit", "view", "include_cache_identity", "filter_contract_version", "curation_status"].includes(key));
  if (!filters.length) return;
  primeUntil = Date.now() + QUERY_TTL_MS;
  try { await queryRead("/api/bookmarks", base, signal, { reuse: true, priority: "low" }); }
  catch { /* Optional prefetch never changes the displayed result or error. */ }
}

async function mutate(load, id) {
  invalidateQueryReads();
  const finishOffline = beginOfflineMutation(id);
  invalidateDetails(id);
  try {
    await forgetOffline(id);
    return await load();
  }
  finally {
    // Reads that overlapped a write may still contain the pre-write snapshot.
    invalidateQueryReads();
    invalidateDetails(id);
    await finishOffline();
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

function readAuxDirect(kind, id, identity, { fresh = false, signal } = {}) {
  if (signal?.aborted) return Promise.reject(new DOMException("Request aborted", "AbortError"));
  const path = `/api/bookmarks/${id}/${kind}`;
  const key = auxKey(kind, id, identity);
  if (!key) return fetchJSON(path, { signal });
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
  if (!fresh && current?.epoch === epoch && !current.invalidated && !current.controller.signal.aborted) return consumeDetail(current, signal);
  const flight = { epoch, invalidated: false, promise: null, controller: new AbortController(), consumers: new Set() };
  auxActive.set(id, (auxActive.get(id) || 0) + 1);
  flight.promise = fetchJSON(path, { signal: flight.controller.signal }).then((value) => {
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
  return consumeDetail(flight, signal);
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
  return readDetail(id, { signal: options.signal }).then((item) => readAuxDirect(kind, id, item?.cache_identity || identity, options));
}

async function fetchReadingDetail(id, { prefetch = false, signal } = {}) {
  const options = { priority: prefetch ? "low" : "high", signal };
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
      reading.detail.formatted_content = current.formatted_content;
      reading.detail.formatting_status = current.formatting_status;
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
  recentReads.delete(id);
  invalidateAux(id);
  const state = detailStates.get(id) || { generation: 0, active: 0 };
  state.generation++;
  forgetPrefetch(id);
  if (state.active > 0) detailStates.set(id, state);
  else detailStates.delete(id);
}

function invalidateDetails(id) {
  if (id) invalidateDetail(id);
  else for (const key of new Set([...detailStates.keys(), ...prefetchedDetails.keys(), ...auxActive.keys(), ...auxCache.values()].map((value) => typeof value === "object" ? value.id : value))) invalidateDetail(key);
}

function checkResponseScope(response, version) {
  const observed = observeOfflineScope(response.headers?.get("X-Cairn-Offline-Scope"), version);
  if (observed.changed) {
    void forgetOffline();
    invalidateQueryReads();
    invalidateDetails();
    once.clear();
    readingSupported = null;
    state.items.clear();
    state.order = [];
    state.checked.clear();
    state.counts = state.overview = null;
    emit("account:changed");
  }
  if (!observed.accepted || observed.changed) throw new APIError("account_changed", 409);
}

function assertCurrentScope(version) {
  if (version !== offlineScopeVersion()) throw new APIError("account_changed", 409);
}

function consumeDetail(flight, signal) {
  const consumer = {};
  flight.consumers.add(consumer);
  return new Promise((resolve, reject) => {
    const release = () => { flight.consumers.delete(consumer); signal?.removeEventListener("abort", abort); };
    const abort = () => {
      release();
      if (!flight.consumers.size) flight.controller.abort();
      reject(new DOMException("Request aborted", "AbortError"));
    };
    if (signal?.aborted) { abort(); return; }
    signal?.addEventListener("abort", abort, { once: true });
    flight.promise.then(value => { release(); resolve(value); }, error => { release(); reject(error); });
  });
}

const recentReads = new Map();
const RECENT_READ_MS = 5 * 60_000;
on('account:changed',()=>recentReads.clear());
on('library:changed',()=>recentReads.clear());
function readDetail(id, { prefetch = false, fresh = false, signal } = {}) {
  if (signal?.aborted) return Promise.reject(new DOMException("Request aborted", "AbortError"));
  const known = getItem(id), recent = recentReads.get(id);
  if (!fresh && known?.content_loaded !== false && known?.cache_identity && recent &&
      recent.until > Date.now() && recent.identity === JSON.stringify(known.cache_identity)) {
    return Promise.resolve(structuredClone(known));
  }
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
  if (!fresh && current?.generation === state.generation && !current.controller.signal.aborted) {
    if (!prefetch) { current.used = true; forgetPrefetch(id); }
    return consumeDetail(current, signal);
  }
  const generation = state.generation;
  const flight = { used: !prefetch, generation, promise: null, consumers: new Set(), controller: new AbortController() };
  detailStates.set(id, state);
  state.active++;
  flight.promise = fetchReadingDetail(id, { prefetch, signal: flight.controller.signal }).then(({ detail: item, selection, entities }) => {
    if (state.generation !== generation) return null;
    if (selection && entities) {
      invalidateAux(id);
      rememberAux("v2-selection", id, item.cache_identity, selection);
      rememberAux("entities", id, item.cache_identity, entities);
    }
    if (!flight.used) rememberPrefetch(id, item);
    if (item.cache_identity) {
      recentReads.delete(id);
      recentReads.set(id, { until: Date.now() + RECENT_READ_MS, identity: JSON.stringify(item.cache_identity) });
      while (recentReads.size > 40) recentReads.delete(recentReads.keys().next().value);
    }
    return item;
  }).finally(() => {
    if (detailFlights.get(id) === flight) detailFlights.delete(id);
    state.active--;
    if (state.active === 0 && !prefetchedDetails.has(id)) detailStates.delete(id);
  });
  detailFlights.set(id, flight);
  return consumeDetail(flight, signal);
}

export const api = {
  cacheStats: () => ({ prefetch_items: prefetchedDetails.size, prefetch_bytes: prefetchBytes,
    auxiliary_items: auxCache.size, auxiliary_bytes: auxBytes, query_items: queryCache.size, query_bytes: queryBytes }),
  list: (params, signal, options) => queryRead("/api/bookmarks", params, signal, options),
  primeFilters,
  detail: (id, options) => readDetail(id, options),
  detailFresh: (id, options) => readDetail(id, { ...options, fresh: true }),
  prefetchAvailable: () => readingSupported === true,
  prefetchDetail: (id, options) => readDetail(id, { ...options, prefetch: true }),
  identity: (id) => fetchJSON(`/api/bookmarks/${id}/identity`),
  overview: () => fetchJSON("/api/overview"),
  backstage: () => fetchJSON("/api/backstage"),
  recoverService: () => fetchJSON("/api/service/recover", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({}) }),
  taxonomy: () => cached("taxonomy", () => fetchJSON("/api/taxonomy")),
  taxonomyV2: () => cached("taxonomy-v2", () => fetchJSON("/api/v2-taxonomy")),
  curation: (id, body) => mutate(() => fetchJSON(`/api/bookmarks/${id}/curation`, jsonBody("PATCH", body)), id),
  v2Selection: (id, identity, options) => readAux("v2-selection", id, identity, options),
  v2Override: (id, body) => mutate(() => fetchJSON(`/api/bookmarks/${id}/v2-override`, jsonBody("POST", body)), id),
  tags: (id, options) => fetchJSON(`/api/bookmarks/${id}/tags`, options),
  editTags: (id, body) => mutate(() => fetchJSON(`/api/bookmarks/${id}/tags`, jsonBody("POST", body)), id),
  tagHistory: (id, beforeId) => fetchJSON(`/api/bookmarks/${id}/tag-history?limit=30${beforeId ? `&before_id=${beforeId}` : ""}`),
  customTags: () => fetchJSON("/api/custom-tags"),
  createCustomTag: (body) => mutate(() => fetchJSON("/api/custom-tags", jsonBody("POST", body))).then(customTagsChanged),
  renameCustomTag: (id, body) => mutate(() => fetchJSON(`/api/custom-tags/${encodeURIComponent(id)}`, jsonBody("PATCH", body))).then(customTagsChanged),
  archiveCustomTag: (id, body) => mutate(() => fetchJSON(`/api/custom-tags/${encodeURIComponent(id)}`, jsonBody("DELETE", body))).then(customTagsChanged),
  tagCounts: (params, signal) => queryRead("/api/tag-counts", params, signal, { reuse: true }),
  tagQuality: () => fetchJSON("/api/tag-quality"),
  evidence: (id) => fetchJSON(`/api/bookmarks/${id}/evidence`),
  classificationStatus: (id) => fetchJSON(`/api/bookmarks/${id}/classification-status`),
  runHistory: (id, cursor) => fetchJSON(`/api/bookmarks/${id}/runs${cursor ? `?after_id=${cursor}` : ""}`),
  runDetail: (id, runID) => fetchJSON(`/api/bookmarks/${id}/runs/${runID}`),
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

function customTagsChanged(value) { emit("custom-tags:changed"); return value; }

export function imagePath(key, { size } = {}) {
  // A new URL namespace avoids reusing responses cached by older releases.
  return "/api/images/" + String(key).split("/").map(encodeURIComponent).join("/") + "?privacy=1" + (size === 160 ? "&size=160" : "");
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
