// Reproducible frontend timings and regressions; synthetic API responses only.
// Run with BASELINE=1 to record the old behavior without improvement assertions.
import assert from "node:assert/strict";
import { chromium } from "playwright";
import { createFixtureState, startFixtureServer } from "../../tests/browser/fixture-server.mjs";
import { taxonomyV2 } from "../../tests/browser/fixture-data.mjs";

const fixture = createFixtureState({ count: 96 });
const catalog = { ...taxonomyV2(), resource_kinds: [{ id: "reference", label: "参考资料", active: true }] };
const { server, url } = await startFixtureServer({ state: fixture });
const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || "/usr/bin/google-chrome", args: ["--no-sandbox"] });
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
const page = await context.newPage();
const errors = [];
const lists = [], counts = [];
let listDelay = 200;
let countDelay = 500;
let countValue = 7;
let listGate = null;
let failList = false;
page.on("pageerror", (error) => errors.push(error.message));
const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const send = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
await page.route("**/api/v2-taxonomy", (route) => send(route, catalog));
await page.route("**/api/custom-tags", (route) => send(route, { tags: [] }));
await page.route("**/api/tag-counts?*", async (route) => {
  const params = new URL(route.request().url()).searchParams;
  const entry = { query: params.toString(), started: Date.now(), value: countValue };
  counts.push(entry);
  await delay(countDelay);
  await send(route, { total: entry.value, topics: catalog.topics.map(({ id }) => ({ id, count: entry.value })), resource_kinds: [{ id: "reference", count: entry.value }], custom_tags: [] }).catch(() => {});
  entry.completed = Date.now();
});
await page.route("**/api/bookmarks?*", async (route) => {
  const params = new URL(route.request().url()).searchParams;
  const entry = { query: params.toString(), started: Date.now() };
  lists.push(entry);
  const shouldFail = failList;
  const gate = listGate;
  const response = await route.fetch();
  const body = await response.json();
  if (gate) await gate.promise;
  await delay(listDelay);
  await send(route, shouldFail ? { error: "backend_error" } : body, shouldFail ? 503 : 200).catch(() => {});
  entry.completed = Date.now();
});
const checked = (label) => process.stdout.write(`ok   ${label}\n`);
async function ready() {
  await page.waitForFunction(() => document.querySelector("#list-pane")?.dataset.loading === "false" && document.querySelector('.facet-chip[data-facet="topics"][data-value="llm"]'));
}
async function toggle(term) {
  return page.evaluate((value) => {
    const started = performance.now();
    document.querySelector(`.facet-chip[data-facet="topics"][data-value="${value}"]`).click();
    return { started, rows: document.querySelectorAll("#rows li.row:not(.skeleton)").length,
      busy: document.querySelector("#list-pane").getAttribute("aria-busy"), count: document.querySelector("#list-count").textContent };
  }, term);
}
async function elapsed(started) {
  await ready();
  return page.evaluate((begin) => performance.now() - begin, started);
}
try {
  await page.goto(`${url}/?curation_status=all`);
  await ready();
  await page.locator(".facet-chip-count").first().waitFor({ state: "attached" });
  await page.locator("#rows .row-thumb img").evaluateAll((images) => images.forEach((image) => { image.dataset.originalNode = image.closest("li.row").dataset.id; }));
  const before = lists.length;
  const first = await toggle("llm");
  const firstMs = await elapsed(first.started);
  const reusedImages = await page.locator("#rows .row-thumb img[data-original-node]").count();
  await toggle("llm"); await ready();
  const beforeRepeat = lists.length;
  const repeat = await toggle("llm");
  const repeatMs = await elapsed(repeat.started);
  await delay(750);
  const stats = { first_filter_ms: Math.round(firstMs), repeat_filter_ms: Math.round(repeatMs), visible_rows_while_loading: first.rows,
    repeat_list_requests: lists.length - beforeRepeat, list_requests_during_three_filters: lists.length - before,
    counts_requests_total: counts.length };
  process.stdout.write(`${process.env.BASELINE ? "baseline" : "optimized"} ${JSON.stringify(stats)}\n`);
  if (process.env.BASELINE) process.exitCode = 0;
  else {
    assert.ok(first.rows > 0, "filtering keeps the previous list visible");
    assert.equal(first.busy, "true");
    assert.match(first.count, /正在筛选/);
    assert.ok(reusedImages > 0, "unchanged thumbnails retain their original DOM nodes without changing image cache policy");
    assert.equal(stats.repeat_list_requests, 0, "repeated complete query uses a short-lived snapshot");
    checked("previous rows and immediate busy feedback remain visible; exact repeat avoids a list request");

    const rapidCounts = counts.length;
    await toggle("eng"); await toggle("design"); await toggle("eng"); await ready();
    await delay(750);
    assert.equal(counts.length - rapidCounts, 1);
    assert.match(counts.at(-1).query, /topics=llm%2Cdesign/);
    checked("rapid selections coalesce facet counts into the final complete query");

    // Ignore an old response even when a transport/test double ignores abort.
    listGate = { promise: null, release: null };
    listGate.promise = new Promise((resolve) => { listGate.release = resolve; });
    const slowRequest = page.waitForRequest((request) => request.url().includes("/api/bookmarks?") && new URL(request.url()).searchParams.get("topics")?.includes("health"));
    await toggle("health");
    await slowRequest;
    const release = listGate.release; listGate = null;
    await toggle("health"); await ready();
    const finalIds = await page.locator("#rows li.row[data-id]").evaluateAll((rows) => rows.map((row) => row.dataset.id));
    release(); await delay(350);
    assert.deepEqual(await page.locator("#rows li.row[data-id]").evaluateAll((rows) => rows.map((row) => row.dataset.id)), finalIds);
    checked("a cancelled slow response cannot replace the newest filter result");

    const requestsBeforeWrite = lists.length;
    await page.evaluate(async () => {
      const { api } = await import("/assets/js/api.js");
      const { state } = await import("/assets/js/store.js");
      await api.curation(state.order[0], { curation_status: "kept" });
      const { reload } = await import("/assets/js/list.js");
      await reload({ reuse: true });
    });
    assert.equal(lists.length, requestsBeforeWrite + 1);
    checked("curation writes invalidate query snapshots before the next filter read");

    const beforeFresh = lists.length;
    await page.evaluate(async () => (await import("/assets/js/list.js")).reload());
    assert.equal(lists.length, beforeFresh + 1);
    checked("explicit refresh bypasses a reusable snapshot");

    const oldRows = await page.locator("#rows li.row[data-id]").evaluateAll((rows) => rows.map((row) => row.dataset.id));
    failList = true;
    await toggle("invest"); await ready();
    assert.deepEqual(await page.locator("#rows li.row[data-id]").evaluateAll((rows) => rows.map((row) => row.dataset.id)), oldRows);
    assert.equal(await page.locator("#list-notice").isVisible(), true);
    assert.equal(await page.locator("#list-count").textContent(), "上次结果");
    assert.equal(await page.locator("#list-pane").getAttribute("aria-busy"), "false");
    checked("a failed refresh retains readable rows with an explicit error and clears the busy state");
    assert.deepEqual(errors, []);
    process.stdout.write("6 filter performance browser checks passed\n");
  }
} finally {
  await context.close(); await browser.close(); await new Promise((resolve) => server.close(resolve));
}
