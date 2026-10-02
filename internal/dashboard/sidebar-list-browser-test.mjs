// Focused navigation regression with real assets and synthetic APIs only.
// Run: node internal/dashboard/sidebar-list-browser-test.mjs
import assert from "node:assert/strict";
import { chromium } from "playwright";
import { createFixtureState, startFixtureServer } from "../../tests/browser/fixture-server.mjs";
import { taxonomyV2 } from "../../tests/browser/fixture-data.mjs";

const fixture = createFixtureState();
const tag = (id, label) => ({ id, label, active: true, aliases: [] });
const catalog = { ...taxonomyV2(), topics: [tag("image_creation", "图像生成"), tag("ai_coding", "AI编程"), tag("video_creation", "视频制作")],
  resource_kinds: [tag("skill", "Skill"), tag("prompt", "提示词"), tag("reference", "参考资料")] };
const custom = { id: "123e4567-e89b-12d3-a456-426614174000", label: "我的项目", status: "active", revision: 1,
  owner_id: "default", tag_ref: "custom/default/123e4567-e89b-12d3-a456-426614174000" };
for (const item of fixture.items) {
  item.classification = { ...item.classification, topics: ["image_creation", "ai_coding"], resource_kinds: ["skill", "prompt", "reference"] };
  item.custom_tags = [custom];
  if (item.v2) item.v2.selection = { ...item.v2.selection, topics: [...item.classification.topics], resource_kinds: [...item.classification.resource_kinds] };
}
const { server, url } = await startFixtureServer({ state: fixture });
const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || "/usr/bin/google-chrome", args: ["--no-sandbox"] });
const context = await browser.newContext({ viewport: { width: 1280, height: 860 } });
const page = await context.newPage();
const errors = [];
page.on("pageerror", (error) => errors.push(error.message));
const send = (route, body) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
await page.route("**/api/v2-taxonomy", (route) => send(route, catalog));
await page.route("**/api/custom-tags", (route) => send(route, { tags: [custom] }));
await page.route("**/api/bookmarks/*/tags", (route) => {
  const id = Number(new URL(route.request().url()).pathname.split("/")[3]);
  const item = fixture.items.find((entry) => entry.id === id);
  return send(route, { id, revision: 1, content_revision: 1, decision_id: 1, custom_tags: item.custom_tags,
    selection: { topics: item.classification.topics, resource_kinds: item.classification.resource_kinds },
    automatic: item.classification, state: { fields: {} } });
});
await page.route("**/api/tag-counts?*", (route) => send(route, { total: 7,
  topics: catalog.topics.map(({ id }) => ({ id, count: 7 })), resource_kinds: catalog.resource_kinds.map(({ id }) => ({ id, count: 7 })),
  custom_tags: [{ id: custom.id, count: 7 }] }));
await page.addInitScript(() => localStorage.setItem("cairn.facets.open", JSON.stringify(["topics", "resource_kinds", "advanced"])));
let checks = 0;
const checked = (label) => { checks++; process.stdout.write(`ok   ${label}\n`); };
const group = (id) => page.locator(`details.facet-group[data-group="${id}"]`);
async function open(id) {
  const selected = group(id);
  await selected.evaluate((node) => {
    for (let parent = node.parentElement; parent; parent = parent.parentElement) if (parent instanceof HTMLDetailsElement) parent.open = true;
  });
  if (!await selected.evaluate((node) => node.open)) await selected.locator(":scope > summary").click();
}
try {
  await page.goto(url);
  await group("custom_tags").waitFor({ state: "attached" });
  assert.equal(await page.locator("details.facet-group[open]").count(), 0);
  assert.deepEqual(await page.locator("#facets > details").evaluateAll((nodes) => nodes.map((node) => node.dataset.group)), ["topics", "resource_kinds", "content_functions", "custom_tags", "more"]);
  for (const id of ["source", "since", "entity_state", "legacy"]) assert.equal(await group(id).evaluate((node) => node.closest('details[data-group="more"]') !== null), true);
  checked("all groups start collapsed and low-frequency filters share one More group");

  await open("topics");
  await page.locator(".facet-chip-count").first().waitFor({ state: "attached" });
  const first = page.locator('.facet-chip[data-facet="topics"][data-value="image_creation"]');
  assert.equal(await first.locator(".facet-chip-label").textContent(), "图像生成");
  assert.equal(await first.locator(".facet-chip-count").textContent(), "7");
  assert.equal(await group("topics").locator(".facet-mode").count(), 0);
  assert.equal(await group("topics").locator(".facet-hint").count(), 0);
  await first.click();
  await page.waitForURL(/topics=image_creation/);
  await group("topics").locator(":scope > summary").click();
  await page.waitForFunction(() => !document.querySelector('details[data-group="topics"]').open);
  await page.reload();
  await group("topics").waitFor({ state: "attached" });
  assert.equal(await group("topics").evaluate((node) => node.open), false);
  assert.match(await group("topics").locator(":scope > summary").textContent(), /1 已选/);
  checked("separate count spans and selected summary preserve a user's collapsed group");

  await open("topics");
  await page.locator('.facet-chip[data-facet="topics"][data-value="ai_coding"]').click();
  await group("topics").locator(".facet-mode").waitFor();
  await group("topics").locator(".facet-mode").selectOption("all");
  await page.waitForURL(/topics_mode=all/);
  await page.locator('.facet-chip[data-facet="topics"][data-value="ai_coding"]').click();
  await page.waitForFunction(() => !document.querySelector('details[data-group="topics"] .facet-mode'));
  assert.equal(new URL(page.url()).searchParams.get("topics_mode"), "all");
  assert.equal(new URL(page.url()).searchParams.get("topics"), "image_creation");
  await page.reload();
  await group("topics").waitFor({ state: "attached" });
  assert.equal(await group("topics").evaluate((node) => node.open), true);
  checked("ANY/ALL appears only for multiple selections and hiding it preserves URL semantics and manual expansion");

  await page.evaluate(() => localStorage.removeItem("cairn.facets.open.v2"));
  await page.goto(`${url}/?topics=retired_unknown&source=wechat&since=2026-01-01T00%3A00%3A00Z&form=legacy_unknown&entity_state=failed`);
  await page.locator('.facet-chip[data-facet="form"][data-value="legacy_unknown"]').waitFor({ state: "attached" });
  assert.equal(await page.locator("details.facet-group[open]").count(), 0);
  assert.match(await group("more").locator(":scope > summary").textContent(), /4 已选/);
  assert.equal(await page.locator("#active-filters .filter-chip").count(), 5);
  await open("topics");
  await page.locator('.facet-chip[data-value="retired_unknown"]').click();
  await page.waitForURL((value) => !value.searchParams.has("topics"));
  await open("form");
  await page.locator('.facet-chip[data-facet="form"][data-value="legacy_unknown"]').click();
  await page.waitForURL((value) => !value.searchParams.has("form"));
  await page.locator("#clear-filters").click();
  await page.waitForFunction(() => [...document.querySelectorAll('.facet-chip[aria-pressed="true"]')].length === 0);
  checked("saved unknown and low-frequency filters stay discoverable and can be cleared");

  const card = await page.evaluate(async () => {
    const { renderRow } = await import("/assets/js/list.js");
    const item = { id: 99, url: "https://example.com/source", created_at: "2026-09-30T00:00:00Z", status: "failed", source: "wechat", curation_status: "inbox",
      classification: { topics: ["image_creation", "ai_coding"], resource_kinds: ["skill", "prompt", "reference"], uncertainty: true },
      custom_tags: [{ id: "mine", label: "我的项目" }] };
    const row = renderRow(item);
    return { tags: [...row.querySelectorAll(".tag")].map((node) => node.textContent), more: row.querySelector(".tag-more")?.textContent,
      moreTitle: row.querySelector(".tag-more")?.title, failed: row.querySelector(".badge-danger")?.textContent,
      source: row.querySelector(".row-source")?.textContent, waitingSummary: row.querySelector(".row-summary") !== null,
      inboxBadge: row.querySelector(".status-badge") !== null, link: row.querySelector(".row-main")?.getAttribute("href") };
  });
  assert.deepEqual(card.tags, ["图像生成", "AI编程", "Skill", "提示词", "参考资料"]);
  assert.equal(card.more, "+1");
  assert.match(card.moreTitle, /我的项目/);
  assert.equal(card.failed, "读取失败");
  assert.equal(card.source, "公众号");
  assert.equal(card.waitingSummary, false);
  assert.equal(card.inboxBadge, false);
  assert.match(card.link, /^\/bookmarks\/99/);
  assert.deepEqual(errors, []);
  checked("list previews five labels and overflow count while preserving failures and source navigation");
  process.stdout.write(`${checks} sidebar/list browser checks passed\n`);
} finally {
  await context.close(); await browser.close(); await new Promise((resolve) => server.close(resolve));
}
