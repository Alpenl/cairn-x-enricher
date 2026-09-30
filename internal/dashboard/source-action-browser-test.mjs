// Original-post action regression with real reading UI and synthetic APIs.
import assert from "node:assert/strict";
import { chromium } from "playwright";
import { createFixtureState, startFixtureServer } from "../../tests/browser/fixture-server.mjs";

const fixture = createFixtureState({ count: 45 });
const item = fixture.items.find((entry) => entry.status === "completed" && entry.curation_status === "inbox");
item.ai_title = "打开原帖：从收藏内容直接回到作者发布的完整讨论";
item.url = "https://x.com/source_author/status/123456789?ref=collection#discussion";
item.note = "以后复盘时查看原作者讨论。" + "LongNoteWithoutSpaces".repeat(10);
item.classification.topics = ["llm", "eval", "eng", "design", "product"];
if (item.v2) {
  item.v2.selection.topics = [...item.classification.topics];
  item.v2.selection.content_functions = [];
}
const { server, url } = await startFixtureServer({ state: fixture });
const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || "/usr/bin/google-chrome", args: ["--no-sandbox"] });
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
await context.route("https://x.com/**", (route) => route.fulfill({ status: 200, contentType: "text/html", body: "<!doctype html><title>Synthetic original post</title>Original post fixture" }));
await context.addInitScript(() => {
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async (value) => { window.__copied = value; } } });
});
const page = await context.newPage();
const errors = [];
page.on("pageerror", (error) => errors.push(error.message));
let checks = 0;
const checked = (label) => { checks++; process.stdout.write(`ok   ${label}\n`); };
try {
  await page.goto(`${url}/bookmarks/${item.id}?curation_status=all`);
  const source = page.locator("#open-source");
  await source.waitFor();
  await page.waitForFunction(() => document.querySelector("#body-loading")?.hidden === true);
  await page.waitForFunction(() => document.querySelectorAll(".curate-summary-tag").length === 5);
  assert.equal(await source.textContent().then((value) => value.trim()), "打开原帖");
  assert.equal(await source.getAttribute("href"), item.url);
  assert.equal(await source.getAttribute("target"), "_blank");
  assert.equal(await source.getAttribute("rel"), "noopener noreferrer");
  assert.equal(await source.locator("svg").count(), 1);
  assert.equal(await page.locator(".detail-tools #open-source").count(), 0);
  assert.equal(await page.locator("#open-source").count(), 1);
  checked("a single labelled external action sits in the reading header and targets the exact saved source URL");

  for (const width of [1440, 1204, 375, 320]) for (const theme of ["light", "dark"]) {
    await page.setViewportSize({ width, height: 900 });
    await page.evaluate((value) => { document.documentElement.dataset.theme = value; }, theme);
    await page.waitForTimeout(300); // Let the existing drawer/reader transitions finish before measuring visibility.
    const layout = await page.evaluate(() => {
      const link = document.querySelector("#open-source");
      const bounds = link.getBoundingClientRect();
      const title = document.querySelector("#detail-title").getBoundingClientRect();
      const scroll = document.querySelector("#detail-scroll");
      return { x: bounds.x, right: bounds.right, y: bounds.y, bottom: bounds.bottom, height: bounds.height,
        titleBottom: title.bottom, viewport: innerWidth, pageWidth: document.documentElement.scrollWidth,
        scrollWidth: scroll.scrollWidth, scrollClient: scroll.clientWidth,
        background: getComputedStyle(link).backgroundColor, color: getComputedStyle(link).color };
    });
    assert.ok(layout.x >= 0 && layout.right <= width + 1);
    assert.ok(layout.y >= layout.titleBottom && layout.bottom <= 900);
    assert.ok(layout.height >= 40);
    assert.ok(layout.pageWidth <= layout.viewport + 1);
    assert.ok(layout.scrollWidth <= layout.scrollClient + 1, JSON.stringify(layout));
    assert.notEqual(layout.background, "rgba(0, 0, 0, 0)");
    assert.notEqual(layout.background, layout.color);
    assert.equal(await page.locator(".curate-summary-tag").count(), 5);
    assert.equal(await page.locator(".curate-summary-more").count(), 0);
    assert.equal(await page.locator("#curate").evaluate((node) => node.open), false);
    if (width === 1440 || width === 320) await page.screenshot({ path: `/tmp/cairn-source-action-${width}-${theme}.png` });
    checked(`${width}px ${theme}: original action remains visible and long metadata wraps without overflow`);
  }

  const popupPromise = page.waitForEvent("popup");
  await source.click();
  const popup = await popupPromise;
  await popup.waitForLoadState();
  assert.equal(popup.url(), item.url);
  assert.equal(await popup.evaluate(() => window.opener === null), true);
  await popup.close();
  checked("the action opens a new tab with no opener using a synthetic original-post response");

  await page.evaluate(() => {
    window.__opened = [];
    window.open = (...args) => { window.__opened.push(args); return null; };
  });
  await page.locator("#detail-scroll").focus();
  await page.keyboard.press("v");
  assert.deepEqual(await page.evaluate(() => window.__opened), [[item.url, "_blank", "noopener,noreferrer"]]);
  await page.locator("#detail-menu").click();
  await page.getByRole("menuitem", { name: "复制原帖链接", exact: true }).click();
  assert.equal(await page.evaluate(() => window.__copied), item.url);
  checked("V keeps opening the current original post and the menu still copies its complete URL");

  await page.evaluate(async (id) => {
    const { getItem, mergeItem, emit } = await import("/assets/js/store.js");
    mergeItem({ ...getItem(id), url: "http://example.com/article?view=original" }); emit("item", id);
  }, item.id);
  assert.equal(await source.getAttribute("href"), "http://example.com/article?view=original");
  for (const invalid of ["javascript:alert(1)", "data:text/html,unsafe", "file:///tmp/source", "invalid source"]) {
    await page.evaluate(async ({ id, invalid }) => {
      const { getItem, mergeItem, emit } = await import("/assets/js/store.js");
      mergeItem({ ...getItem(id), url: invalid }); emit("item", id);
    }, { id: item.id, invalid });
    assert.equal(await source.isVisible(), false);
    assert.equal(await source.getAttribute("href"), null);
    const before = await page.evaluate(() => window.__opened.length);
    await page.locator("#detail-scroll").focus(); await page.keyboard.press("v");
    assert.equal(await page.evaluate(() => window.__opened.length), before);
    await page.locator("#detail-menu").click();
    assert.equal(await page.getByRole("menuitem", { name: /^打开原帖(?:\s+V)?$/ }).isDisabled(), true);
    await page.keyboard.press("Escape");
  }
  checked("source changes refresh the link; unsafe schemes disable the visible action, menu and shortcut");
  assert.deepEqual(errors, []);
  assert.equal(fixture.requests.some((request) => request.method !== "GET"), false);
  assert.equal(fixture.modelCalls, 0);
  process.stdout.write(`${checks} source-action browser checks passed\n`);
} finally {
  await context.close(); await browser.close(); await new Promise((resolve) => server.close(resolve));
}
