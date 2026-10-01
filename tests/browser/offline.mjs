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
  const page = await browser.newPage();
  await page.goto(`http://127.0.0.1:${server.address().port}/`);
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
      const open = indexedDB.open("cairn-offline-v1", 1);
      open.onsuccess = () => {
        const db = open.result;
        const transaction = db.transaction("articles", "readwrite");
        const store = transaction.objectStore("articles");
        const request = store.get(1);
        request.onsuccess = () => store.put({ ...request.result, savedAt: Date.now() - 8 * 86400000 });
        transaction.oncomplete = () => { db.close(); resolve(); };
      };
    });
  });
  assert.equal(await page.evaluate(async () => await cache.offlineItem(1)), null);
  console.log("ok   expired copies are removed when the store is read");
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
