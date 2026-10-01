// Recent full-text copies are private to this browser and backend credential.
// A portable, script-free HTML export also works on the NAS's HTTP LAN origin,
// where a service worker cannot provide an offline application shell.
const MAX_ITEMS = 20;
const MAX_BYTES = 8 * 1024 * 1024;
const MAX_AGE = 7 * 24 * 60 * 60 * 1000;
let database;
let scopePromise;
let epoch = 0;
let currentScope;
let accountEpoch = 0;
let mutations = 0;

export function offlineScopeVersion() { return accountEpoch; }

// Responses from a request predating an observed account switch cannot switch
// the page back. A current response identifies the scope without another GET.
export function observeOfflineScope(value, version = accountEpoch) {
  if (!/^[a-f0-9]{64}$/.test(value || "")) return { accepted: true, changed: false };
  if (value === currentScope) return { accepted: true, changed: false };
  if (version !== accountEpoch) return { accepted: false, changed: false };
  const changed = !!currentScope;
  currentScope = value;
  accountEpoch++;
  if (changed) epoch++;
  scopePromise = Promise.resolve(value);
  return { accepted: true, changed };
}

function scope() {
  const version = accountEpoch;
  if (!scopePromise) scopePromise = fetch("/api/offline-scope", { cache: "no-store", signal: AbortSignal.timeout(5000) })
    .then(async (response) => {
      const body = await response.json();
      if (!response.ok) throw new Error("offline_unsupported");
      const value = body.scope;
      if (!/^[a-f0-9]{64}$/.test(value)) throw new Error("invalid_scope");
      return observeOfflineScope(value, version).accepted ? value : null;
    }).catch(() => null);
  return scopePromise;
}

function openDatabase() {
  if (!globalThis.indexedDB) return Promise.resolve(null);
  if (!database) database = new Promise((resolve) => {
    const request = indexedDB.open("cairn-offline-v1", 1);
    request.onupgradeneeded = () => request.result.createObjectStore("articles", { keyPath: "id" });
    request.onsuccess = () => {
      request.result.onversionchange = () => { request.result.close(); database = null; };
      resolve(request.result);
    };
    request.onerror = request.onblocked = () => resolve(null);
  });
  return database;
}

// Each operation validates and prunes the whole bounded store in one IDB
// transaction, so parallel tabs cannot exceed limits or mix account scopes.
async function transact(change) {
  if (!globalThis.indexedDB) return [];
  const [db, account] = await Promise.all([openDatabase(), scope()]);
  if (!db || !account || account !== currentScope) return [];
  const version = accountEpoch;
  return new Promise((resolve) => {
    let result = [];
    const transaction = db.transaction("articles", "readwrite");
    const store = transaction.objectStore("articles");
    const request = store.getAll();
    request.onsuccess = () => {
      if (version !== accountEpoch) { transaction.abort(); return; }
      const now = Date.now();
      let rows = request.result.filter((row) => row.scope === account &&
        Number.isSafeInteger(row.id) && row.id > 0 && row.savedAt <= now &&
        now - row.savedAt < MAX_AGE && row.bytes > 0 && row.bytes <= MAX_BYTES);
      rows = change(rows, account, now).sort((a, b) => b.savedAt - a.savedAt);
      let bytes = 0;
      rows = rows.filter((row, index) => index < MAX_ITEMS && (bytes += row.bytes) <= MAX_BYTES);
      const keep = new Set(rows.map((row) => row.id));
      for (const old of request.result) if (!keep.has(old.id)) store.delete(old.id);
      for (const row of rows) store.put(row);
      result = rows;
    };
    transaction.oncomplete = () => resolve(version === accountEpoch ? result : []);
    transaction.onerror = transaction.onabort = () => resolve([]);
  }).catch(() => []);
}

export async function rememberOffline(item) {
  if (!Number.isSafeInteger(item?.id) || item.id <= 0 || item.content_loaded === false ||
      mutations || item.offline_cached_at || (!item.original_text && !item.translated_text)) return;
  const generation = epoch;
  const copy = structuredClone(item);
  // Images remain governed by the online proxy. No remote image cache is kept.
  copy.images = [];
  const bytes = JSON.stringify(copy).length * 2;
  await transact((rows, account, now) => generation !== epoch || mutations ? rows : [
    ...rows.filter((row) => row.id !== copy.id),
    ...(bytes <= MAX_BYTES ? [{ id: copy.id, item: copy, scope: account, bytes, savedAt: now }] : [])
  ]);
}

export async function offlineItem(id) {
  const rows = await transact((current) => current);
  const row = rows.find((entry) => entry.id === id);
  return row ? { ...row.item, offline_cached_at: row.savedAt, content_loaded: true } : null;
}

export async function forgetOffline(id) {
  epoch++;
  await transact((rows) => id ? rows.filter((row) => row.id !== id) : []);
}

// Mutations keep writes paused until a second invalidation completes. Reads
// overlapping a write cannot recreate a stale copy after the first deletion.
export function beginOfflineMutation(id) {
  mutations++;
  epoch++;
  let finished = false;
  return async () => {
    if (finished) return;
    finished = true;
    try { await forgetOffline(id); }
    finally { mutations--; }
  };
}

export async function recentOffline() {
  return (await transact((rows) => rows)).map((row) => ({ ...row.item, offline_cached_at: row.savedAt }));
}

export function offlineHTML(items) {
  const escape = (value) => String(value ?? "").replace(/[&<>"']/g, (char) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[char]);
  const title = (item) => escape(item.ai_title || item.url || `收藏 ${item.id}`);
  const safeURL = (value) => {
    try { const url = new URL(value); return ["https:", "http:"].includes(url.protocol) ? escape(url.href) : ""; }
    catch { return ""; }
  };
  const sections = items.map((item, index) => {
    const link = safeURL(item.url);
    const time = new Date(item.offline_cached_at).toISOString();
    return `<article id="article-${index}"><h2>${title(item)}</h2><p class="meta">副本保存于 ${escape(time)}${link ? ` · <a href="${link}" target="_blank" rel="noopener noreferrer">打开原帖 ↗</a>` : ""}</p>` +
      [["摘要", item.summary], ["收藏原因", item.why], ["备注", item.note], ["中文全文", item.translated_text], ["原文", item.original_text]]
        .filter(([, value]) => value).map(([label, value]) => `<h3>${label}</h3><p class="text">${escape(value)}</p>`).join("") +
      '<p><a href="#top">回到目录 ↑</a></p></article>';
  }).join("");
  return `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'"><title>Cairn 离线阅读</title><style>body{max-width:760px;margin:auto;padding:32px 22px;color:#29313b;background:#fafaf8;font:17px/1.8 system-ui,sans-serif}h1,h2{line-height:1.35}h2{margin-top:0}a{color:#385b75}article{padding:36px 0;border-top:1px solid #dce1e4}.meta{font-size:13px;color:#6b7280}.text{white-space:pre-wrap;overflow-wrap:anywhere}li{margin:10px 0}h3{font-size:14px;color:#6b7280;margin-bottom:8px}</style><body id="top"><h1>Cairn 离线阅读</h1><p class="meta">${items.length} 条最近阅读的文字副本。内容可能已更新；图片与编辑需要联网。此文件保存在你的设备上，服务器删除收藏不会删除已下载的文件。</p><nav><ol>${items.map((item, index) => `<li><a href="#article-${index}">${title(item)}</a></li>`).join("")}</ol></nav>${sections}</body></html>`;
}

export async function downloadOffline() {
  const items = await recentOffline();
  if (!items.length) return false;
  const url = URL.createObjectURL(new Blob([offlineHTML(items)], { type: "text/html;charset=utf-8" }));
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = `cairn-offline-${new Date().toISOString().slice(0, 10)}.html`;
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
  return true;
}
