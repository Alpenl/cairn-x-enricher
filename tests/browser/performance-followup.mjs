// Real Go handlers and embedded modules. No request interception or paid calls.
import assert from "node:assert/strict";
import { chromium } from "playwright";
const base = process.env.CAIRN_FOLLOWUP_BASE;
assert.ok(base?.startsWith("http://127.0.0.1:"));
const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || undefined, args: ["--no-sandbox"] });
let checks = 0;
const check = (name, actual, expected) => { assert.deepEqual(actual, expected, name); console.log(`ok ${++checks} ${name}`); };
try {
  const page = await browser.newPage();
  const errors = []; page.on("pageerror", error => errors.push(error.message));
  await page.goto(base + "/followup-probe");
  await page.evaluate(async () => {
    window.apiModule = await import("/assets/js/api.js");
    window.storeModule = await import("/assets/js/store.js");
    for (const path of ["/api/taxonomy", "/api/v2-taxonomy"]) {
      for (let i = 0; i < 3; i++) await apiModule.fetchJSON(path);
    }
  });
  const stats = async () => (await fetch(base + "/followup-stats")).json();
  const initial = await stats();
  check("real Chrome no-store sends no-cache on every ordinary taxonomy read", initial.no_cache_requests, 6);
  check("ordinary reads reuse both Go catalog caches", [initial.legacy, initial.modern], [1, 1]);
  await page.evaluate(() => apiModule.fetchJSON("/api/v2-taxonomy?refresh=1"));
  check("explicit refresh still reloads the modern catalog", (await stats()).modern, 2);
  await page.reload();
  await page.evaluate(async () => { window.apiModule = await import("/assets/js/api.js"); window.storeModule = await import("/assets/js/store.js"); await apiModule.fetchJSON("/api/v2-taxonomy"); });
  check("a new document also reuses the Go catalog", (await stats()).modern, 2);

  const body = await page.evaluate(async () => {
    const { api, fetchJSON } = apiModule, { mergeItem, getItem } = storeModule;
    const legacy = await fetchJSON("/api/bookmarks?view=summary");
    const detail = await fetchJSON("/api/bookmarks/1");
    mergeItem(detail);
    const summary = await api.list(new URLSearchParams("view=summary"));
    mergeItem(summary.items[0]);
    return { legacyIdentity: Boolean(legacy.items[0].cache_identity), summaryIdentity: summary.items[0].cache_identity,
      summaryHasBody: Boolean(summary.items[0].original_text), retained: getItem(1).original_text === detail.original_text,
      loaded: getItem(1).content_loaded !== false };
  });
  check("legacy list response keeps its strict shape", body.legacyIdentity, false);
  check("modern Web negotiates summary identity through real Go", (await stats()).identity_requested, true);
  check("summary keeps the body version without transferring text", [body.summaryIdentity.body_revision, body.summaryHasBody], [1, false]);
  check("list refresh retains the matching loaded private body", [body.loaded, body.retained], [true, true]);

  const changed = await page.evaluate(async () => {
    const { mergeItem, getItem } = storeModule;
    const detail = await apiModule.fetchJSON("/api/bookmarks/1");
    mergeItem(detail);
    mergeItem({ ...detail, content_loaded: false, original_text: undefined, translated_text: undefined,
      why: "new personal reason", cache_identity: { ...detail.cache_identity, personal_revision: 2 } });
    const personal = getItem(1).why === "new personal reason" && getItem(1).original_text === detail.original_text;
    mergeItem({ ...detail, content_loaded: false, original_text: undefined, translated_text: undefined,
      cache_identity: { ...detail.cache_identity, body_revision: 2 } });
    return { personal, changedBody: getItem(1).content_loaded === false && !getItem(1).original_text };
  });
  check("personal edits update metadata while changed body versions invalidate text", changed, { personal: true, changedBody: true });
  await page.evaluate(async () => {
    const { api } = apiModule; await api.list(new URLSearchParams("view=summary"));
    assertCacheNotEmpty(); await api.curation(1, { why: "fixture reason" });
    function assertCacheNotEmpty() { if (!api.cacheStats().query_items) throw Error("expected populated list cache"); }
  });
  check("a curation write invalidates reusable list snapshots", await page.evaluate(() => apiModule.api.cacheStats().query_items), 0);

  await fetch(base + "/followup-control?legacy=1", { method: "POST" });
  const legacyBody = await page.evaluate(async () => {
    const { mergeItem, getItem } = storeModule;
    mergeItem(await apiModule.fetchJSON("/api/bookmarks/1"));
    mergeItem((await apiModule.api.list(new URLSearchParams("view=summary"))).items[0]);
    return getItem(1).content_loaded === false && !getItem(1).original_text;
  });
  check("an old backend without versions cannot reuse an unverified body", legacyBody, true);
  await fetch(base + "/followup-control?account=2", { method: "POST" });
  const account = await page.evaluate(async () => {
    try { await apiModule.api.list(new URLSearchParams("view=summary")); } catch (error) {
      return { error: error.message, items: storeModule.state.items.size, queries: apiModule.api.cacheStats().query_items };
    }
  });
  check("account change rejects the response and clears bodies and queries", account, { error: "account_changed", items: 0, queries: 0 });
  check("no page errors", errors, []);
  console.log(`PASS: ${checks} real Chrome/Go follow-up checks`);
} finally { await browser.close(); }
