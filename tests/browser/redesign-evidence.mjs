// Offline screenshots of the real frontend (2026-10 Claude redesign) for
// side-by-side comparison with the approved prototype. All API content is
// synthetic; collection writes are answered in-page and never leave the box.
//
//   CHROME_PATH=/usr/bin/google-chrome node tests/browser/redesign-evidence.mjs
import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright";
import { createFixtureState, startFixtureServer } from "./fixture-server.mjs";
import { taxonomyV2 } from "./fixture-data.mjs";

const output = process.env.EVIDENCE_DIR || "/tmp/cairn-redesign-evidence";
await mkdir(output, { recursive: true });
const fixture = createFixtureState({ count: 36 });
const catalog = { ...taxonomyV2(), resource_kinds: [{ id: "reference", label: "参考资料", active: true }, { id: "software", label: "软件与服务", active: true }, { id: "open_source", label: "开源", active: true }] };
for (const item of fixture.items) {
  const kinds = [["reference"], ["software"], ["open_source", "software"]][item.id % 3];
  item.classification = { ...item.classification, resource_kinds: kinds };
  if (item.v2) item.v2.selection = { ...item.v2.selection, resource_kinds: kinds };
}
const { server, url } = await startFixtureServer({ state: fixture });
const firstId = fixture.items[0].id;
const collections = [
  { id: "aigc", name: "AIGC 生图与视频", item_count: 2, pinned: true, revision: 1, rule_tags: "[]" },
  { id: "ui", name: "界面设计参考", item_count: 4, pinned: true, revision: 1, rule_tags: "[]" },
  { id: "share", name: "下一次分享", item_count: 5, pinned: false, revision: 1, rule_tags: "[]" },
  { id: "local", name: "本地模型实验", item_count: 2, pinned: false, revision: 1, rule_tags: "[]" },
];
const members = new Map([["ui", new Set([firstId])], ["share", new Set([firstId])]]);
const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || "/usr/bin/google-chrome", args: ["--no-sandbox"] });
const errors = [];
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  page.on("pageerror", (error) => errors.push(error.message));
  await page.route("**/api/v2-taxonomy", (route) => route.fulfill({ json: catalog, headers: { "X-Cairn-Tag-System": "1", "X-Cairn-Topic-Granularity": "1" } }));
  await page.route("**/api/collections**", async (route) => {
    const request = route.request();
    const u = new URL(request.url());
    const ids = (u.searchParams.get("link_ids") || "").split(",").filter(Boolean).map(Number);
    const view = (c) => ({ ...c, selected_count: ids.filter((id) => members.get(c.id)?.has(id)).length });
    if (request.method() === "POST") {
      const id = u.pathname.split("/")[3];
      const body = request.postDataJSON();
      const c = collections.find((x) => x.id === id);
      const set = members.get(id) || new Set();
      members.set(id, set);
      for (const link of body.link_ids || []) body.type === "add" ? set.add(link) : set.delete(link);
      c.revision++;
      c.item_count = Math.max(0, c.item_count + (body.type === "add" ? 1 : -1));
      return route.fulfill({ json: { collection: c } });
    }
    if (u.pathname === "/api/collections") return route.fulfill({ json: { items: collections.map(view) }, headers: { "X-Cairn-Collections": "2" } });
    if (u.pathname === "/api/collections/organizing") return route.fulfill({ json: { items: [] } });
    if (u.pathname === "/api/collections/review-summary") return route.fulfill({ json: { items: [{ collection_id: "share", pending: 1 }] } });
    const c = collections.find((x) => x.id === u.pathname.split("/")[3]) || collections[0];
    return route.fulfill({ json: { collection: c, items: [] } });
  });
  const shot = async (name) => { await page.waitForTimeout(250); await page.screenshot({ path: join(output, name + ".png") }); };
  const idle = () => page.waitForFunction(() => document.querySelector("#list-pane")?.dataset.loading === "false");
  const reading = () => page.waitForFunction(() => document.querySelector("#body-loading")?.hidden);

  await page.goto(url);
  await idle(); await reading();
  await page.locator("#inspector-memberships .coll-row").first().waitFor();
  await shot("01-library-wide");

  await page.locator("#search").click();
  await page.waitForFunction(() => document.querySelector("#tf-pop .tf-chip .n"));
  await shot("02-tagfilter-open");
  await page.locator("#tf-pop .tf-chip").nth(0).click(); await idle();
  await page.locator('#tf-pop .tf-group[data-field="resource_kinds"] .tf-chip').first().click(); await idle();
  await shot("03-tagfilter-selected-open");
  await page.keyboard.press("Escape");
  await shot("04-tagfilter-selected");
  await page.locator("#search-clear").click().catch(() => {});
  await idle();

  await page.locator("#toggle-formatted").click().catch(() => {});
  await shot("05-reader-original");
  await page.locator("#reading-version").click().catch(() => {});
  await page.locator("#reason-block").scrollIntoViewIfNeeded().catch(() => {});

  await page.locator("#command-button").click();
  await shot("06-command");
  await page.keyboard.press("Escape");

  await page.evaluate(() => (document.documentElement.dataset.theme = "dark"));
  await shot("07-library-dark");
  await page.evaluate(() => (document.documentElement.dataset.theme = "light"));

  for (const [w, h, name] of [[1100, 800, "08-narrow-1100"], [900, 760, "09-narrow-900"]]) {
    await page.setViewportSize({ width: w, height: h });
    await shot(name);
  }
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(url);
  await idle();
  await shot("10-phone-list");
  await page.locator("#rows li.row[data-id] .row-main").first().click();
  await reading();
  await shot("11-phone-reader");
  console.log(output);
  if (errors.length) { console.error("page errors:", errors); process.exitCode = 1; }
} finally {
  await browser.close();
  server.close();
}
