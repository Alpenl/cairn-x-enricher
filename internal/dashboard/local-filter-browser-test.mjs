import {openFilters} from "../../tests/browser/workspace-helper.mjs";
import assert from "node:assert/strict";
import { chromium } from "playwright";
import { createFixtureState, startFixtureServer } from "../../tests/browser/fixture-server.mjs";
import { taxonomyV2 } from "../../tests/browser/fixture-data.mjs";
const fixture = createFixtureState({ count: 12 });
const catalog = { ...taxonomyV2(), resource_kinds: [{ id: "reference", label: "参考资料", active: true }] };
for (const item of fixture.items) {
  item.status = "completed";
  item.classification = { ...item.classification, topics: [item.id % 2 ? "llm" : "design"], resource_kinds: ["reference"], content_functions: ["method"] };
  if (item.v2) item.v2.selection = { ...item.v2.selection, ...item.classification };
}
const { server, url } = await startFixtureServer({ state: fixture });
const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || "/usr/bin/google-chrome", args: ["--no-sandbox"] });
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  let listReads = 0, countReads = 0, filteredReads = 0;
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.route("**/api/v2-taxonomy", route => route.fulfill({ json: catalog, headers: { "X-Cairn-Tag-System": "1", "X-Cairn-Topic-Granularity": "1" } }));
  await page.route("**/api/bookmarks?*", async route => {
    listReads++;
    if (new URL(route.request().url()).searchParams.has("topics")) filteredReads++;
    const response = await route.fetch();
    const payload = await response.json();
    await route.fulfill({ response, json: { ...payload, local_filter_version: 1 } });
  });
  await page.route("**/api/tag-counts?*", route => { countReads++; return route.fulfill({ json: { topics: [], resource_kinds: [], custom_tags: [], content_functions: [] } }); });
  await page.goto(`${url}/?curation_status=all`);
  await openFilters(page);
  await page.waitForFunction(() => document.querySelector("#list-pane")?.dataset.loading === "false");
  await page.locator('[data-group="topics"] > summary').click();
  await page.waitForTimeout(250);
  const before = { listReads, countReads };
  const started = await page.locator('[data-facet="topics"][data-value="llm"]').evaluate(node => { const time = performance.now(); node.focus(); node.click(); return time; });
  await page.waitForFunction(() => document.querySelector("#list-pane").dataset.loading === "false");
  const elapsed = await page.evaluate(time => Math.round(performance.now() - time), started);
  const expected = fixture.items.filter(item => item.classification.topics.includes("llm")).map(item => String(item.id));
  assert.deepEqual(await page.locator("#rows li.row[data-id]").evaluateAll(rows => rows.map(row => row.dataset.id)), expected);
  const control = await page.locator('[data-facet="topics"][data-value="llm"]').elementHandle();
  await page.waitForTimeout(300);
  assert.equal(await control.evaluate(node => node.isConnected && node === document.activeElement), true);
  assert.deepEqual({ listReads, countReads }, before);
  await page.locator('[data-facet="topics"][data-value="design"]').click();
  await page.waitForFunction(() => document.querySelector("#list-pane").dataset.loading === "false");
  assert.equal(await page.locator("#rows li.row[data-id]").count(), 12);
  await page.locator('[aria-label="主题匹配方式"]').selectOption("all");
  await page.waitForFunction(() => document.querySelector("#list-pane").dataset.loading === "false");
  assert.equal(await page.locator("#rows li.row[data-id]").count(), 0);
  assert.equal(listReads, before.listReads);
  await page.evaluate(async () => { const { invalidateQueryReads } = await import("/assets/js/api.js"); invalidateQueryReads(); });
  await page.locator('[aria-label="主题匹配方式"]').selectOption("any");
  await page.waitForFunction(() => document.querySelector("#list-pane").dataset.loading === "false");
  assert.equal(filteredReads, 1, "invalidation forces the new filtered query to the server");
  assert.ok(listReads <= before.listReads + 2, "at most one bounded background view read follows it");
  assert.deepEqual(errors, []);
  console.log(`Complete snapshot: first new filter ${elapsed}ms, zero list/count reads, OR/AND and invalidation passed`);
} finally { await browser.close(); await new Promise(resolve => server.close(resolve)); }
