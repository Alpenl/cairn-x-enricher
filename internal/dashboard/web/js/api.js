// Same-origin API client. The browser never holds a Worker token or model key:
// every call goes to the Go service, which forwards it with its own credentials.

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
  revision_conflict: "已被其他客户端更新",
  snapshot_conflict: "已被其他客户端更新",
  lease_conflict: "已有抓取任务在进行中",
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

export const api = {
  list: (params, signal) => fetchJSON(`/api/bookmarks?${params}`, { signal }),
  detail: (id, signal) => fetchJSON(`/api/bookmarks/${id}`, { signal }),
  overview: () => fetchJSON("/api/overview"),
  backstage: () => fetchJSON("/api/backstage"),
  taxonomy: () => cached("taxonomy", () => fetchJSON("/api/taxonomy")),
  taxonomyV2: () => cached("taxonomy-v2", () => fetchJSON("/api/v2-taxonomy")),
  curation: (id, body) => fetchJSON(`/api/bookmarks/${id}/curation`, jsonBody("PATCH", body)),
  v2Selection: (id) => fetchJSON(`/api/bookmarks/${id}/v2-selection`),
  v2Override: (id, body) => fetchJSON(`/api/bookmarks/${id}/v2-override`, jsonBody("POST", body)),
  evidence: (id) => fetchJSON(`/api/bookmarks/${id}/evidence`),
  classificationStatus: (id) => fetchJSON(`/api/bookmarks/${id}/classification-status`),
  entities: (id) => fetchJSON(`/api/bookmarks/${id}/entities`),
  correctEntity: (id, body) => fetchJSON(`/api/bookmarks/${id}/entities`, jsonBody("POST", body)),
  retryClassification: (id) => fetchJSON(`/api/bookmarks/${id}/retry-classification`, { method: "POST" }),
  refreshSource: refreshSourceRequest,
  replayPolicy: (id, commit) => fetchJSON(`/api/bookmarks/${id}/replay-policy`, jsonBody("POST", commit ? { commit: true } : {})),
  process: processRequest,
  submitSource: (id, submission) => processingRequest(`/api/bookmarks/${id}/source`, {
    original_text: submission.text, operation_key: submission.operation_key,
    expected_revision: submission.expected_revision
  })
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
