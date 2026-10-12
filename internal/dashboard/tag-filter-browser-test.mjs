// 2026-10 redesign: the tag-chip popover under the list search replaces the
// facet panel (and the stable-facets / topic-navigation browser tests that
// covered it). Real embedded UI over the synthetic fixture; GET-only.
import assert from "node:assert/strict";
import { chromium } from "playwright";
import { createFixtureState, startFixtureServer } from "../../tests/browser/fixture-server.mjs";
import { taxonomyV2 } from "../../tests/browser/fixture-data.mjs";

const fixture = createFixtureState({ count: 24 });
const catalog = { ...taxonomyV2(), resource_kinds: [{ id: "reference", label: "参考资料", active: true }, { id: "software", label: "软件与服务", active: true }] };
for (const item of fixture.items) {
  item.status = "completed";
  const topics = item.id % 3 === 0 ? ["llm", "design"] : item.id % 3 === 1 ? ["llm"] : ["design"];
  item.classification = { ...item.classification, topics, resource_kinds: [item.id % 2 ? "reference" : "software"], content_functions: ["method"] };
  if (item.v2) item.v2.selection = { ...item.v2.selection, ...item.classification };
}
const { server, url } = await startFixtureServer({ state: fixture });
const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || "/usr/bin/google-chrome", args: ["--no-sandbox"] });
let checks = 0;
const ok = (message) => { checks++; console.log(`ok   ${message}`); };
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  const errors = [], writes = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("request", (request) => { if (!["GET", "HEAD"].includes(request.method())) writes.push(`${request.method()} ${request.url()}`); });
  await page.route("**/api/v2-taxonomy", (route) => route.fulfill({ json: catalog, headers: { "X-Cairn-Tag-System": "1", "X-Cairn-Topic-Granularity": "1" } }));
  const idle = () => page.waitForFunction(() => document.querySelector("#list-pane")?.dataset.loading === "false");
  const rows = () => page.locator("#rows li.row[data-id]").count();
  const chip = (ref) => page.locator(`#tf-pop .tf-chip[data-ref="${ref}"]`);
  const LLM = "system/topics/llm", DESIGN = "system/topics/design", REF = "system/resource_kinds/reference";
  const has = (item, field, id) => item.classification[field].includes(id);

  await page.goto(`${url}/`);
  await idle();
  // Clicking the field opens the popover with grouped, counted chips.
  await page.locator("#search").click();
  await page.locator("#tf-pop").waitFor();
  await page.waitForFunction(() => document.querySelector('#tf-pop .tf-chip[data-ref="system/topics/llm"] .n'));
  assert.equal(await page.locator("#search").getAttribute("aria-expanded"), "true");
  assert.deepEqual(await page.locator("#tf-pop .tf-group-label").allTextContents().then((labels) => labels.map((label) => label.replace(/\d+$/, ""))).then((labels) => labels.slice(0, 3)), ["主题", "资源类型", "内容特征"]);
  assert.equal(await chip(LLM).locator(".n").textContent(), String(fixture.items.filter((item) => has(item, "topics", "llm")).length));
  ok("clicking the search field opens grouped chips with counts");

  // Count updates keep the same chip nodes and the keyboard focus.
  const handle = await chip(DESIGN).elementHandle();
  await handle.focus();
  await chip(LLM).click();
  await idle();
  await page.waitForTimeout(400);
  assert.equal(await handle.evaluate((node) => node.isConnected), true);
  assert.equal(new URL(page.url()).searchParams.get("tags"), LLM);
  assert.equal(await rows(), fixture.items.filter((item) => has(item, "topics", "llm")).length);
  ok("selecting a chip filters immediately and keeps chip nodes mounted through count updates");

  // "all" (default): numbers are the result size after adding the chip.
  assert.equal(await page.locator('#tf-mode [data-mode="all"]').getAttribute("aria-checked"), "true");
  assert.equal(await chip(DESIGN).locator(".n").textContent(), String(fixture.items.filter((item) => has(item, "topics", "llm") && has(item, "topics", "design")).length));
  await chip(DESIGN).click(); await idle();
  assert.equal(await rows(), fixture.items.filter((item) => has(item, "topics", "llm") && has(item, "topics", "design")).length);
  await page.locator('#tf-mode [data-mode="any"]').click(); await idle();
  assert.equal(new URL(page.url()).searchParams.get("tags_mode"), "any");
  assert.equal(await rows(), fixture.items.filter((item) => has(item, "topics", "llm") || has(item, "topics", "design")).length);
  ok("全部满足 / 任一满足 switch the result and are kept in the URL");

  // Typed text narrows the chips; choosing one consumes the typed text.
  await page.locator("#search").focus();
  await page.keyboard.type("参考");
  await page.waitForFunction(() => document.querySelectorAll("#tf-pop .tf-chip").length === 1);
  await chip(REF).click(); await idle();
  assert.equal(await page.locator("#search").inputValue(), "");
  assert.equal(new URL(page.url()).searchParams.get("q"), null);
  assert.equal(new URL(page.url()).searchParams.get("tags"), [LLM, DESIGN, REF].join(","));
  assert.equal(await page.locator("#tf-tokens .tf-more").textContent(), "+1");
  ok("typing finds a chip, choosing it consumes the text; overflow tokens collapse into +N");

  // Keyboard: ArrowDown enters the chips, arrows move, Esc returns to the field.
  await page.locator("#search").focus();
  await page.keyboard.press("ArrowDown");
  assert.equal(await page.evaluate(() => document.activeElement?.classList.contains("tf-chip")), true);
  const first = await page.evaluate(() => document.activeElement.dataset.ref);
  await page.keyboard.press("ArrowRight");
  assert.notEqual(await page.evaluate(() => document.activeElement.dataset.ref), first);
  await page.keyboard.press("Escape");
  await page.locator("#tf-pop").waitFor({ state: "hidden" });
  assert.equal(await page.evaluate(() => document.activeElement?.id), "search");
  ok("ArrowDown / arrows / Esc navigate the chips and return focus to the field");

  // Backspace in the empty field removes the newest token; × removes one.
  await page.keyboard.press("Backspace"); await idle();
  assert.equal(new URL(page.url()).searchParams.get("tags"), [LLM, DESIGN].join(","));
  await page.locator(`#tf-tokens .tf-x[data-ref="${DESIGN}"]`).click(); await idle();
  assert.equal(new URL(page.url()).searchParams.get("tags"), LLM);
  ok("Backspace and the token × remove tags");

  // A tag pill in a list row toggles the same unified condition.
  await page.locator("#tf-pop").waitFor({ state: "hidden" }).catch(() => {});
  await page.keyboard.press("Escape");
  const pill = page.locator(`.row-meta [data-tag-field="topics"][data-tag-id="llm"]`).first();
  assert.equal(await pill.evaluate((node) => node.classList.contains("hit")), true, "pills in the active filter are highlighted");
  await pill.click(); await idle();
  assert.equal(new URL(page.url()).searchParams.get("tags"), null);
  await page.locator(`.row-meta [data-tag-field="topics"][data-tag-id="design"]`).first().click(); await idle();
  assert.equal(new URL(page.url()).searchParams.get("tags"), DESIGN);
  ok("row tag pills add and remove the same tag condition");

  // The field clear button drops text and tags together; the URL is bare again.
  await page.locator("#search").fill("评估"); await page.waitForTimeout(450); await idle();
  await page.locator("#search-clear").click(); await idle();
  assert.equal(new URL(page.url()).search, "");
  assert.equal(await page.locator("#tf-tokens .tf-token").count(), 0);
  ok("the clear button removes the search text and every tag");

  // A selection with no result explains itself, and old facet links still load.
  await page.goto(`${url}/?tags=${encodeURIComponent([LLM, "system/resource_kinds/software", "system/topics/design"].join(","))}&tags_mode=all&topics=llm`);
  await idle();
  assert.equal(await rows(), fixture.items.filter((item) => has(item, "topics", "llm") && has(item, "topics", "design") && has(item, "resource_kinds", "software")).length);
  assert.equal(await page.locator("#tf-tokens .tf-token").count(), 2);
  ok("a shared URL restores tag tokens alongside a legacy facet");

  // Narrow widths: the popover fits the list column and nothing overflows.
  for (const width of [1024, 390]) {
    await page.setViewportSize({ width, height: 800 });
    if (width < 760 && await page.evaluate(() => document.getElementById("app").classList.contains("detail-open"))) await page.locator("#detail-back").click();
    await page.locator("#tf-toggle").click();
    await page.locator("#tf-pop").waitFor();
    const box = await page.locator("#tf-pop").boundingBox();
    assert.ok(box.x >= 0 && box.x + box.width <= width + 1, `popover inside the viewport at ${width}px`);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    await page.keyboard.press("Escape");
  }
  ok("popover fits at 1024px and 390px without horizontal overflow");

  assert.deepEqual(errors, []);
  assert.deepEqual(writes, []);
  console.log(`${checks}/${checks} tag filter browser checks passed`);
} finally {
  await browser.close();
  await new Promise((resolve) => server.close(resolve));
}
