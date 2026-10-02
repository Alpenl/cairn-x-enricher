import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { chromium } from "playwright";

let scope = "a".repeat(64);
const code = await readFile(new URL("../../internal/dashboard/web/js/offline.js", import.meta.url));
const modules = new Map([["/offline.js", code]]);
for (const name of ["api", "store"]) modules.set(`/${name}.js`, await readFile(new URL(`../../internal/dashboard/web/js/${name}.js`, import.meta.url)));
const gate = () => {
  let release; const promise = new Promise((resolve) => { release = resolve; });
  return { promise, release };
};
let writeGate, readGate, writeStarted, readStarted, bodyGate;
const server = createServer(async (request, response) => {
  const requestScope = scope;
  response.setHeader("Content-Type", modules.has(request.url) ? "text/javascript" : "application/json");
  if (modules.has(request.url)) { response.end(modules.get(request.url)); return; }
  if (request.url.startsWith("/api/")) response.setHeader("X-Cairn-Offline-Scope", requestScope);
  if (request.url === "/api/deferred-body" || request.url === "/api/bookmarks/process") {
    response.flushHeaders();
    await bodyGate.promise;
    response.end(JSON.stringify({ scope: requestScope, accepted: [7], rejected: [] })); return;
  }
  if (request.url === "/api/bookmarks/7/curation") {
    writeStarted?.release();
    if (writeGate) await writeGate.promise;
    response.end("{}"); return;
  }
  if (request.url.startsWith("/api/bookmarks/7/reading")) {
    readStarted?.release();
    if (readGate) await readGate.promise;
    response.end(JSON.stringify({ version: 1, detail: { id: 7, original_text: "before write", content_loaded: true,
      cache_identity: { schema_version: 1, content_revision: 1, body_revision: 1, personal_revision: 0, latest_decision_id: 1, latest_entity_revision: 0 } },
      selection: { available: true }, entities: {} })); return;
  }
  response.end(JSON.stringify({ scope: requestScope }));
});
await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || "/usr/bin/google-chrome", headless: true, args: ["--no-sandbox"] });
try {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto(`http://127.0.0.1:${server.address().port}/`);
  await page.evaluate(async () => {
    await new Promise((resolve, reject) => {
      const open = indexedDB.open("cairn-offline-v1", 1);
      open.onupgradeneeded = () => open.result.createObjectStore("articles", { keyPath: "id" });
      open.onerror = () => reject(open.error);
      open.onsuccess = () => {
        const db = open.result;
        const transaction = db.transaction("articles", "readwrite");
        for (const [id, account, age] of [[90, "a", 0], [91, "a", 8 * 86400000], [92, "b", 0]]) {
          transaction.objectStore("articles").put({ id, scope: account.repeat(64), bytes: 100,
            savedAt: Date.now() - age, item: { id, original_text: "legacy copy" } });
        }
        transaction.oncomplete = () => { db.close(); resolve(); };
      };
    });
    window.cache = await import("/offline.js");
  });
  assert.deepEqual(await page.evaluate(async () => (await cache.recentOffline()).map((item) => item.id)), [90]);
  await page.evaluate(() => cache.forgetOffline());
  console.log("ok   metadata migration preserves valid legacy copies and prunes expired/other-account records");
  await page.evaluate(async () => {
    window.cache = await import("/offline.js");
    for (let id = 1; id <= 25; id++) await cache.rememberOffline({ id, url: `https://example.com/${id}`, original_text: "正文".repeat(1000), cache_identity: { body_revision: 1 } });
  });
  assert.equal(await page.evaluate(async () => (await cache.recentOffline()).length), 20);
  assert.equal(await page.evaluate(async () => await cache.offlineItem(1)), null);
  assert.equal(await page.evaluate(async () => (await cache.offlineItem(25)).original_text.length), 2000);
  await page.reload();
  assert.equal(await page.evaluate(async () => { window.cache = await import("/offline.js"); return (await cache.recentOffline()).length; }), 20);
  console.log("ok   recent text copies survive reload and remain within twenty articles");
  await page.evaluate(async () => {
    await cache.forgetOffline(25);
    await cache.rememberOffline({ id: 30, original_text: "x".repeat(5 * 1024 * 1024) });
  });
  assert.equal(await page.evaluate(async () => await cache.offlineItem(25)), null);
  assert.equal(await page.evaluate(async () => await cache.offlineItem(30)), null);
  scope = "b".repeat(64);
  await page.reload();
  assert.equal(await page.evaluate(async () => { window.cache = await import("/offline.js"); return (await cache.recentOffline()).length; }), 0);
  console.log("ok   deletion, oversized articles and backend/account changes cannot revive stale copies");
  await page.evaluate(async () => {
    await cache.rememberOffline({ id: 1, original_text: "expired" });
    await new Promise((resolve) => {
      const open = indexedDB.open("cairn-offline-v1", 2);
      open.onsuccess = () => {
        const db = open.result;
        const transaction = db.transaction(["articles", "metadata"], "readwrite");
        for (const name of ["articles", "metadata"]) {
          const store = transaction.objectStore(name);
          const request = store.get(1);
          request.onsuccess = () => store.put({ ...request.result, savedAt: Date.now() - 8 * 86400000 });
        }
        transaction.oncomplete = () => { db.close(); resolve(); };
      };
    });
  });
  assert.equal(await page.evaluate(async () => await cache.offlineItem(1)), null);
  console.log("ok   expired copies are removed when the store is read");
  const storage = await page.evaluate(async () => {
    await cache.forgetOffline();
    for (let id = 100; id < 120; id++) await cache.rememberOffline({ id, original_text: "文".repeat(180000) });
    const counts = { get: 0, getAll: 0, put: 0, delete: 0 };
    const originals = {};
    for (const method of Object.keys(counts)) {
      originals[method] = IDBObjectStore.prototype[method];
      IDBObjectStore.prototype[method] = function (...args) {
        if (this.name === "articles") counts[method]++;
        return originals[method].apply(this, args);
      };
    }
    try {
      const before = performance.now();
      const item = await cache.offlineItem(110);
      const read = { ...counts, ms: performance.now() - before, length: item?.original_text.length };
      for (const key of Object.keys(counts)) counts[key] = 0;
      await cache.rememberOffline({ id: 110, original_text: "新".repeat(180000) });
      const write = { ...counts };
      for (const key of Object.keys(counts)) counts[key] = 0;
      await cache.forgetOffline(110);
      return { read, write, remove: { ...counts } };
    } finally {
      for (const [method, original] of Object.entries(originals)) IDBObjectStore.prototype[method] = original;
    }
  });
  assert.equal(storage.read.length, 180000);
  assert.deepEqual(Object.fromEntries(Object.entries(storage.read).filter(([key]) => !["ms", "length"].includes(key))), { get: 1, getAll: 0, put: 0, delete: 0 });
  assert.deepEqual(storage.write, { get: 0, getAll: 0, put: 1, delete: 0 });
  assert.deepEqual(storage.remove, { get: 0, getAll: 0, put: 0, delete: 1 });
  assert.equal(await page.evaluate(async () => (await cache.recentOffline()).length), 19);
  console.log(`ok   near-capacity cache reads only one body (${storage.read.ms.toFixed(1)}ms), writes one body, and deletes one body`);
  await page.evaluate(() => cache.forgetOffline());
  const otherTab = await page.context().newPage();
  await otherTab.goto(`http://127.0.0.1:${server.address().port}/`);
  await otherTab.evaluate(async () => { window.cache = await import("/offline.js"); });
  await Promise.all([page, otherTab].map((tab, index) => tab.evaluate(async (offset) => {
    await Promise.all(Array.from({ length: 15 }, (_, index) => cache.rememberOffline({
      id: offset + index, original_text: "parallel ".repeat(22000)
    })));
  }, 200 + index * 15)));
  assert.equal(await page.evaluate(async () => (await cache.recentOffline()).length), 20);
  await page.evaluate(() => cache.forgetOffline());
  await Promise.all([page, otherTab].map((tab, index) => tab.evaluate(async (offset) => {
    await Promise.all(Array.from({ length: 2 }, (_, index) => cache.rememberOffline({
      id: offset + index, original_text: "文".repeat(1500000)
    })));
  }, 300 + index * 2)));
  assert.equal(await page.evaluate(async () => (await cache.recentOffline()).length), 2);
  await otherTab.close();
  await page.evaluate(() => cache.forgetOffline());
  console.log("ok   concurrent tabs preserve both article and byte limits with atomic metadata updates");
  await page.evaluate(async () => {
    window.apicache = (await import("/api.js")).api;
    await cache.rememberOffline({ id: 7, original_text: "old personal snapshot" });
  });
  writeGate = gate(); writeStarted = gate();
  const writing = page.evaluate(() => apicache.curation(7, { why: "new reason" }));
  await writeStarted.promise;
  assert.equal(await page.evaluate(async () => {
    await cache.rememberOffline({ id: 7, original_text: "read during write" });
    return cache.offlineItem(7);
  }), null);
  readGate = gate(); readStarted = gate();
  const overlappingRead = page.evaluate(() => apicache.detailFresh(7));
  await readStarted.promise;
  writeGate.release(); await writing; writeGate = null;
  readGate.release(); assert.equal(await overlappingRead, null); readGate = null;
  assert.equal(await page.evaluate(() => cache.offlineItem(7)), null);
  await page.evaluate(async () => { const fresh = await apicache.detailFresh(7); await cache.rememberOffline(fresh); });
  assert.equal(await page.evaluate(async () => (await cache.offlineItem(7)).original_text), "before write");
  console.log("ok   mutation pauses copies, discards overlapping detail and permits fresh post-write reads");
  readGate = gate(); readStarted = gate();
  const oldAccountRead = page.evaluate(() => apicache.detailFresh(7).then(() => "accepted", (error) => error.message));
  await readStarted.promise;
  scope = "c".repeat(64);
  assert.equal(await page.evaluate(async () => {
    const { fetchJSON } = await import("/api.js");
    try { await fetchJSON("/api/new-account"); return "accepted"; }
    catch (error) { return error.message; }
  }), "account_changed");
  readGate.release(); assert.equal(await oldAccountRead, "account_changed"); readGate = null;
  assert.equal(await page.evaluate(async () => (await cache.recentOffline()).length), 0);
  console.log("ok   same-page account switch clears copies and cannot be reversed by an old response");
  for (const [kind, nextScope] of [["json", "d"], ["processing", "e"]]) {
    bodyGate = gate();
    await page.evaluate(async (kind) => {
      const { fetchJSON } = await import("/api.js");
      window.bodyDecodeStarted = false;
      window.originalJSONDecode = Response.prototype.json;
      const path = kind === "json" ? "/api/deferred-body" : "/api/bookmarks/process";
      Response.prototype.json = function (...args) {
        if (new URL(this.url).pathname === path) window.bodyDecodeStarted = true;
        return window.originalJSONDecode.apply(this, args);
      };
      window.deferredBodyResult = (kind === "json" ? fetchJSON(path) : apicache.process([7]))
        .then(() => "accepted", (error) => error.message);
    }, kind);
    await page.waitForFunction(() => window.bodyDecodeStarted);
    scope = nextScope.repeat(64);
    assert.equal(await page.evaluate(async () => {
      const { fetchJSON } = await import("/api.js");
      try { await fetchJSON("/api/new-account"); return "accepted"; }
      catch (error) { return error.message; }
    }), "account_changed");
    bodyGate.release();
    assert.equal(await page.evaluate(() => window.deferredBodyResult), "account_changed");
    await page.evaluate(() => { Response.prototype.json = window.originalJSONDecode; });
  }
  console.log("ok   account changes while JSON is decoding reject both reads and processing receipts");
  const html = await page.evaluate(() => cache.offlineHTML([{ id: 1, ai_title: '<script>window.injected=true</script>', url: 'javascript:alert(1)', original_text: '</p><img src=x onerror="window.injected=true">', offline_cached_at: Date.now() }]));
  const offline = await browser.newPage();
  await offline.context().setOffline(true);
  await offline.setContent(html);
  assert.match(await offline.locator("article").innerText(), /window.injected=true/);
  assert.equal(await offline.locator("script,img").count(), 0);
  assert.equal(await offline.locator('a[href^="javascript:"]').count(), 0);
  console.log("ok   portable HTML opens with networking disabled and treats source markup as text");
} finally {
  await browser.close();
  await new Promise((resolve) => server.close(resolve));
}
