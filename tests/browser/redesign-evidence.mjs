// Offline screenshots of the real frontend. All API content is synthetic.
import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright";
import { createFixtureState, startFixtureServer } from "./fixture-server.mjs";
const output = process.env.EVIDENCE_DIR || "/tmp/cairn-redesign-evidence";
await mkdir(output, { recursive: true });
const { server, url } = await startFixtureServer({
  state: createFixtureState(),
});
const browser = await chromium.launch({
  executablePath: process.env.CHROME_PATH || "/usr/bin/google-chrome",
  args: ["--no-sandbox"],
});
try {
  const page = await browser.newPage({
    viewport: { width: 1600, height: 1000 },
  });
  const collection = {
    id: "design",
    name: "设计与 AIGC",
    description: "设计流程与创作参考",
    item_count: 3,
    pinned: true,
    revision: 1,
    rule_tags: "[]",
    selected_count: 0,
  };
  await page.route("**/api/collections**", (route) => {
    const path = new URL(route.request().url()).pathname;
    return route.fulfill({
      json:
        path === "/api/collections/organizing"
          ? { items: [] }
          : path === "/api/collections"
            ? { items: [collection] }
            : { collection, items: [] },
    });
  });
  await page.route("**/api/tag-catalog", async (route) => {
    const taxonomy = (await import("./fixture-data.mjs")).taxonomyV2();
    await route.fulfill({
      json: { revision: 1, catalog: taxonomy, counts: [], custom_tags: [] },
    });
  });
  const shot = async (name) => {
    await page.screenshot({ path: join(output, name + ".png") });
  };
  await page.goto(url);
  await page.waitForFunction(
    () => document.querySelector("#body-loading").hidden,
  );
  await page.locator(".chip").first().waitFor();
  await shot("reading-desktop");
  await page.evaluate(() => (document.documentElement.dataset.theme = "dark"));
  await page.waitForTimeout(250);
  await shot("reading-dark");
  await page.evaluate(() => (document.documentElement.dataset.theme = "light"));
  await page.locator("#browse-tags").click();
  await page
    .locator(".tag-manager-row")
    .first()
    .getByRole("button", { name: "管理", exact: true })
    .click();
  await page.getByLabel("标签名称", { exact: true }).waitFor();
  await shot("tags-desktop");
  await page.locator("#browse-collections").click();
  await page.locator(".collection-title").click();
  await page.getByLabel("合集名称", { exact: true }).waitFor();
  await shot("collections-desktop");
  await page.locator("#organize-button").click();
  await page
    .getByRole("button", { name: "开始整理现有收藏", exact: true })
    .waitFor();
  await shot("organizing-desktop");
  await page.locator("#settings-button").click();
  await shot("settings-desktop");
  await page.locator("#service-link").click();
  await page.locator(".stats").waitFor();
  await shot("service-desktop");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(url);
  await page.locator(".row-main").first().waitFor();
  await shot("library-mobile");
  await page.locator(".row-main").first().click();
  await page.waitForFunction(
    () => document.querySelector("#body-loading").hidden,
  );
  await shot("reading-mobile");
  await page.locator("#detail-back").click();
  await page.locator('#tabbar [data-page="tags"]').click();
  await page.locator(".tag-manager-row").first().waitFor();
  await shot("tags-mobile");
  console.log(output);
} finally {
  await browser.close();
  server.close();
}
