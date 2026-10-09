import {openFilters,closeFilters} from "../../tests/browser/workspace-helper.mjs";
// Real embedded UI; all data and requests stay in the synthetic local fixture.
import assert from "node:assert/strict";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright";
import { createFixtureState, startFixtureServer } from "../../tests/browser/fixture-server.mjs";
import { taxonomyV2 } from "../../tests/browser/fixture-data.mjs";

const fixture = createFixtureState();
const broad = (id, label) => ({ id, label, active: true, granularity: "broad", navigation: true, aliases: [] });
const specific = (id, label, aliases = []) => ({ id, label, active: true, granularity: "specific", navigation: false, aliases });
const catalog = { ...taxonomyV2(), topics: [broad("image_creation", "图像生成"), broad("design", "设计"),
  specific("portrait", "写真", ["个人写真"]), specific("avatar", "头像")], resource_kinds: [{ id: "skill", label: "Skill", active: true }] };
for (const [index, item] of fixture.items.entries()) {
  item.status = "completed";
  item.ai_title = index < 2 ? `AI 写真 Skill：${index ? "自然光人像" : "生成真实的人像照片"}` : `图像工作流示例 ${index + 1}`;
  item.summary = "从照片选取、光线设置到成片检查，整理可复用的人像制作方法与提示词。";
  item.original_text = "写真制作方法\n\n先选择适合的人像照片，再设置自然光与背景，最后检查细节并保存可复用的提示词。";
  item.translated_text = item.original_text;
  item.images = [];
  item.classification = { ...item.classification, topics: [index % 2 ? "design" : "image_creation", index < 2 ? "portrait" : "avatar"], resource_kinds: ["skill"] };
  if (item.v2) item.v2.selection = { ...item.v2.selection, ...item.classification };
}
const { server, url } = await startFixtureServer({ state: fixture });
const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || "/usr/bin/google-chrome", args: ["--no-sandbox"] });
const context = await browser.newContext({ viewport: { width: 1204, height: 900 } });
const page = await context.newPage();
const errors = [], requests = [];
let scope = "b".repeat(64), acknowledge = true, tagAcknowledged = true;
let heldList = null;
page.on("pageerror", (error) => errors.push(error.message));
page.on("request", (request) => requests.push({ url: request.url(), method: request.method() }));
const send = (route, body, extra = {}) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body),
  headers: { "X-Cairn-Offline-Scope": scope, ...(acknowledge ? { "X-Cairn-Topic-Granularity": "1", ...(tagAcknowledged ? { "X-Cairn-Tag-System": "1" } : {}) } : {}), ...extra } });
await page.route("**/api/**", async (route) => {
  const response = await route.fetch();
  return route.fulfill({ response, headers: { ...response.headers(), "X-Cairn-Offline-Scope": scope } });
});
await page.route("**/api/v2-taxonomy", (route) => {
  assert.equal(route.request().headers()["x-cairn-topic-granularity"], "1");
  return send(route, catalog);
});
await page.route("**/api/offline-scope", (route) => send(route, { scope }));
await page.route("**/api/custom-tags", (route) => send(route, { tags: [] }));
await page.route("**/api/tag-counts?*", (route) => {
  const params = new URL(route.request().url()).searchParams;
  let items = fixture.items;
  const selected = (params.get("topics") || "").split(",").filter(Boolean);
  const refine = (params.get("topic_refinements") || "").split(",").filter(Boolean);
  if (selected.length) items = items.filter((item) => selected.some((id) => item.classification.topics.includes(id)));
  items = items.filter((item) => refine.every((id) => item.classification.topics.includes(id)));
  return send(route, { total: items.length, topics: catalog.topics.map((term) => ({ id: term.id,
    count: items.filter((item) => item.classification.topics.includes(term.id)).length })), resource_kinds: [{ id: "skill", count: items.length }] });
});
await page.route("**/api/bookmarks?*", async (route) => {
  const response = await route.fetch();
  const body = await response.json();
  const params = new URL(route.request().url()).searchParams;
  const refine = (params.get("topic_refinements") || "").split(",").filter(Boolean);
  body.items = body.items.filter((item) => refine.every((id) => item.classification?.topics.includes(id)));
  if (body.counts) body.counts.total = body.items.length;
  if (heldList && params.get("topics") === "portrait") {
    await heldList.released;
  }
  return send(route, body);
});
const topics = () => page.locator('details[data-group="topics"]');
const openTopics = async () => { await openFilters(page); if (!await topics().evaluate((node) => node.open)) await topics().locator(":scope > summary").click(); };
try {
  await page.goto(`${url}/?curation_status=all`);
  await openFilters(page);
  await page.locator('.row-meta [data-tag-id="portrait"]').first().waitFor();
  assert.equal(await page.locator('.row-meta').first().locator('.tag').first().textContent(), "写真");
  await openTopics();
  assert.equal(await topics().locator('[data-value="portrait"]').count(), 0);
  await page.locator("#topic-search").fill("个人写真");
  assert.equal(await topics().locator('[data-value="portrait"]').count(), 1);
  const pin = topics().getByRole("button", { name: "固定写真", exact: true });
  await pin.waitFor();
  await pin.click();
  await page.locator("#topic-search").fill("");
  assert.equal(await topics().locator('[data-topic-section="pinned"] [data-value="portrait"]').count(), 1);
  await page.reload(); await openFilters(page); await topics().waitFor(); await openTopics();
  await topics().locator('[data-topic-section="pinned"] [data-value="portrait"]').waitFor();
  await topics().getByRole("button", { name: "取消固定写真", exact: true }).click();

  await page.goto(`${url}/?curation_status=all&topics=image_creation,design`);
  await openFilters(page);
  await openTopics();
  const refine = topics().locator('[data-facet="topic_refinements"][data-value="portrait"]');
  await refine.waitFor(); await refine.click();
  await page.waitForURL((value) => value.searchParams.get("topic_refinements") === "portrait");
  await page.waitForFunction(() => document.querySelector('#list-pane')?.dataset.loading === "false");
  assert.equal(new URL(page.url()).searchParams.get("topics"), "image_creation,design");
  assert.equal(new URL(page.url()).searchParams.get("topics_mode"), null);
  assert.equal(await page.locator('.row[data-id]').count(), 2);
  assert.match(await page.locator('#active-filters').textContent(), /进一步筛选：写真/);
  await closeFilters(page);
  await page.locator('#active-filters .filter-chip').filter({ hasText: "进一步筛选" }).click();
  await page.waitForURL((value) => !value.searchParams.has("topic_refinements"));
  assert.equal(new URL(page.url()).searchParams.get("topics"), "image_creation,design");
  await page.reload(); await openFilters(page);
  await page.waitForFunction(() => document.querySelector('#list-pane')?.dataset.loading === "false");
  assert.equal(new URL(page.url()).searchParams.get("topics"), "image_creation,design");

  await page.goto(`${url}/?curation_status=all`);
  await openFilters(page);
  await page.waitForFunction(() => document.querySelector('.row.selected'));
  const previousPath = new URL(page.url()).pathname;
  let release;
  const listRequested = page.waitForRequest(request => {
    const value = new URL(request.url());
    return value.pathname === "/api/bookmarks" && value.searchParams.get("topics") === "portrait";
  });
  heldList = { released: new Promise(resolve => { release = resolve; }), release: () => release() };
  await page.locator('.row-meta [data-tag-id="portrait"]').first().click();
  await page.waitForURL((value) => value.searchParams.get("topics") === "portrait");
  assert.equal(new URL(page.url()).pathname, previousPath);
  await listRequested;
  // URL updates before the filtered rows arrive. Keep this response pending
  // until focus is on an old row, so the former CI race happens every time.
  await page.locator('.row-meta [data-tag-id="portrait"]').first().focus();
  const focusedRow = await page.evaluate(() => {
    window.focusedBeforeListRefresh = document.activeElement;
    return Number(document.activeElement.closest('.row').dataset.id);
  });
  heldList.release();
  await page.waitForFunction(() => document.querySelector('#list-pane')?.dataset.loading === "false");
  assert.equal(await page.evaluate(() => window.focusedBeforeListRefresh.isConnected), true, "filter response retains an unchanged row and its focused control");
  assert.deepEqual(await page.evaluate(() => ({ row: Number(document.activeElement.closest('.row')?.dataset.id),
    field: document.activeElement.dataset.tagField, term: document.activeElement.dataset.tagId })),
  { row: focusedRow, field: "topics", term: "portrait" }, "filter refresh must retain the focused tag");
  heldList = null;
  await page.keyboard.press("Enter");
  await page.waitForURL((value) => !value.searchParams.has("topics"));
  await page.waitForFunction(() => document.querySelector('#list-pane')?.dataset.loading === "false");

  const focusUpdates = await page.evaluate(async () => {
    const { emit } = await import("/assets/js/store.js");
    const results = [];
    for (const selector of ['.tag-filter[data-tag-id="portrait"]', '.row-main', '.row-check']) {
      const control = document.querySelector(`.row ${selector}`);
      const id = Number(control.closest('.row').dataset.id);
      control.focus();
      for (const event of ['item', 'taxonomy']) {
        emit(event, id);
        results.push(document.activeElement === document.querySelector(`.row[data-id="${id}"] ${selector}`));
      }
    }
    const search = document.querySelector('#search');
    search.focus();
    emit('item', Number(document.querySelector('.row').dataset.id));
    emit('taxonomy');
    results.push(document.activeElement === search);
    return results;
  });
  assert.deepEqual(focusUpdates, Array(7).fill(true), "background row updates preserve controls without taking focus from search");

  await openTopics(); await page.locator('#topic-search').fill("写真");
  await topics().getByRole("button", { name: "固定写真", exact: true }).click();
  scope = "c".repeat(64); await page.reload(); await openFilters(page); await topics().waitFor(); await openTopics();
  assert.equal(await topics().locator('[data-topic-section="pinned"]').count(), 0);
  assert.equal(await page.evaluate(() => JSON.parse(localStorage.getItem(`cairn.topic-pins.v1:${"b".repeat(64)}`)).includes("portrait")), true);

  const artifacts = process.env.TOPIC_EVIDENCE_DIR;
  if (artifacts) {
    mkdirSync(artifacts, { recursive: true, mode: 0o700 });
    await page.evaluate(() => localStorage.setItem("cairn.theme", "dark"));
    await page.goto(`${url}/?curation_status=all&topics=image_creation,design&topic_refinements=portrait`);
  await openFilters(page);
    await page.waitForFunction(() => document.querySelector('#list-pane')?.dataset.loading === "false");
    await page.waitForFunction(() => document.querySelector('#body-loading')?.hidden === true);
    await openTopics();
    await topics().getByRole("button", { name: "固定写真", exact: true }).click();
    await page.screenshot({ path: join(artifacts, "topic-navigation-1204-dark.png") });
    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto(`${url}/?curation_status=all`);
  await openFilters(page);
    await page.locator('.row-meta [data-tag-id="portrait"]').first().waitFor();
    await page.locator('.row-meta [data-tag-id="portrait"]').first().click();
    await page.waitForURL((value) => value.searchParams.get("topics") === "portrait");
    await page.waitForFunction(() => document.querySelector('#list-pane')?.dataset.loading === "false");
    assert.equal(new URL(page.url()).pathname, "/");
    assert.equal(await page.locator('#detail-pane').isVisible(), false);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    await page.screenshot({ path: join(artifacts, "topic-navigation-375-dark.png") });
    await page.setViewportSize({ width: 1204, height: 900 });
  }

  acknowledge = false;
  await page.goto(`${url}/?curation_status=all&topics=image_creation,design&topic_refinements=portrait`);
  await openFilters(page);
  await page.locator('#load-error-text').waitFor();
  assert.match(await page.locator('#load-error-text').textContent(), /筛选|后端|服务/);
  assert.equal(new URL(page.url()).searchParams.get("topic_refinements"), "portrait");
  await page.locator('#clear-filters').click();
  await page.waitForFunction(() => document.querySelectorAll('.row[data-id]').length > 0 && document.querySelector('#list-pane')?.dataset.loading === "false");
  acknowledge = true; tagAcknowledged = false;
  await page.goto(`${url}/?curation_status=all&topics=image_creation,design&topic_refinements=portrait`);
  await openFilters(page);
  await page.locator('#load-error-text').waitFor();
  assert.equal(new URL(page.url()).searchParams.get("topic_refinements"), "portrait");
  assert.deepEqual(errors, []);
  assert.equal(requests.filter((request) => !["GET", "HEAD"].includes(request.method)).length, 0);
  if (artifacts) writeFileSync(join(artifacts, "topic-navigation-proof.json"), JSON.stringify({ viewport: [1204, 375],
    independent_any_refinement: true, account_scoped_pins: true, mouse_and_keyboard: true, mobile_tag_does_not_open_reader: true,
    requests: requests.length, writes: 0, page_errors: errors, old_backend_fails_closed: true }, null, 2), { mode: 0o600 });
  console.log("Topic navigation browser checks passed: specificity, alias, pin/reload/account isolation, independent ANY refinement/history, tag mouse/keyboard, delayed list/background focus preservation, old backend, zero writes");
} finally {
  heldList?.release();
  await page.unrouteAll({ behavior: "wait" });
  await context.close(); await browser.close(); await new Promise((resolve) => server.close(resolve));
}
