import {openFilters,chooseTag} from "../../tests/browser/workspace-helper.mjs";
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
    if (new URL(route.request().url()).searchParams.has("tags")) filteredReads++;
    const response = await route.fetch();
    const payload = await response.json();
    await route.fulfill({ response, json: { ...payload, local_filter_version: 1 } });
  });
  await page.route("**/api/tag-counts?*", route => { countReads++; return route.continue(); });
  await page.goto(`${url}/?curation_status=all`);
  await page.waitForFunction(() => document.querySelector("#list-pane")?.dataset.loading === "false");
  await openFilters(page);
  const chip = (id) => page.locator(`#tf-pop .tf-chip[data-ref="system/topics/${id}"]`);
  await chip("llm").waitFor();
  await page.waitForFunction(() => !document.querySelector('#tf-pop .tf-chip[data-ref="system/topics/llm"]')?.disabled);
  // "any" first: chip numbers are scope counts, the list is the union.
  await page.locator('#tf-mode [data-mode="any"]').click();
  await page.waitForTimeout(250);
  const before = { listReads };
  const started = await chip("llm").evaluate(node => { const time = performance.now(); node.focus(); node.click(); return time; });
  await page.waitForFunction(() => document.querySelector("#list-pane").dataset.loading === "false");
  const elapsed = await page.evaluate(time => Math.round(performance.now() - time), started);
  const expected = fixture.items.filter(item => item.classification.topics.includes("llm")).map(item => String(item.id));
  assert.deepEqual(await page.locator("#rows li.row[data-id]").evaluateAll(rows => rows.map(row => row.dataset.id)), expected);
  assert.equal(new URL(page.url()).searchParams.get("tags"), "system/topics/llm");
  assert.equal(new URL(page.url()).searchParams.get("tags_mode"), "any");
  await page.waitForTimeout(300);
  assert.equal(await chip("llm").evaluate(node => node === document.activeElement && node.classList.contains("on")), true, "the chosen chip keeps focus");
  assert.equal(await page.locator("#tf-tokens .tf-token").count(), 1, "the selection shows as a token in the search field");
  assert.deepEqual({ listReads }, before, "the complete local snapshot answers the filter");
  await chip("design").click();
  await page.waitForFunction(() => document.querySelector("#list-pane").dataset.loading === "false");
  assert.equal(await page.locator("#rows li.row[data-id]").count(), 12);
  await page.locator('#tf-mode [data-mode="all"]').click();
  await page.waitForFunction(() => document.querySelector("#list-pane").dataset.loading === "false");
  assert.equal(await page.locator("#rows li.row[data-id]").count(), 0);
  assert.equal(listReads, before.listReads);
  await page.evaluate(async () => { const { invalidateQueryReads } = await import("/assets/js/api.js"); invalidateQueryReads(); });
  await page.locator('#tf-mode [data-mode="any"]').click();
  await page.waitForFunction(() => document.querySelector("#list-pane").dataset.loading === "false");
  assert.equal(filteredReads, 1, "invalidation forces the new filtered query to the server");
  assert.ok(listReads <= before.listReads + 2, "at most one bounded background view read follows it");
  // Backspace in the empty field removes the last token.
  await page.locator("#search").focus();
  await page.keyboard.press("Backspace");
  await page.waitForFunction(() => new URL(location.href).searchParams.get("tags") === "system/topics/llm");
  void countReads; void chooseTag;
  assert.deepEqual(errors, []);
  console.log(`Tag popover over the complete snapshot: first chip ${elapsed}ms, zero list reads, any/all, token, Backspace and invalidation passed`);
} finally { await browser.close(); await new Promise(resolve => server.close(resolve)); }
