// Protect the rendered navigation and manager layout, including narrow drawers.
// Synthetic read-only APIs: no changes to a real library or model calls.
import assert from "node:assert/strict";
import { chromium } from "playwright";
import { createFixtureState, startFixtureServer } from "./fixture-server.mjs";
import { taxonomyV2 } from "./fixture-data.mjs";

const { server, url } = await startFixtureServer({ state: createFixtureState() });
const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || "/usr/bin/google-chrome", args: ["--no-sandbox"] });
const collection = { id: "test-pinned", name: "AIGC 生图生成视频相关", description: "生图与视频参考", item_count: 3, pinned: true, archived: false, deleted: false, revision: 1 };
const errors = [], writes = [];
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  page.on("pageerror", error => errors.push(error.message));
  await page.route("**/api/**", async route => {
    const request = route.request(), pathname = new URL(request.url()).pathname;
    if (request.method() !== "GET") { writes.push(pathname); return route.abort(); }
    let data;
    if (pathname === "/api/collections") data = { items: [collection] };
    if (pathname === "/api/tag-catalog") data = { revision: 0, catalog: taxonomyV2(), counts: [], custom_tags: [] };
    if (data) return route.fulfill({ json: data });
    return route.continue();
  });
  const close = async dialog => {
    await dialog.getByLabel("关闭", { exact: true }).click();
    await dialog.waitFor({ state: "detached" });
  };
  const alignedNavigation = async () => {
    const rows = await page.locator("#sidebar .nav-item").evaluateAll(nodes => nodes.map(node => {
      const rect = node.getBoundingClientRect(), style = getComputedStyle(node), label = node.querySelector(".nav-label"), icon = node.querySelector("svg");
      return { x: rect.x, width: rect.width, height: rect.height, labelX: label?.getBoundingClientRect().x,
        icon: !!icon, font: style.font, border: parseFloat(style.borderTopWidth), overflow: node.scrollWidth > node.clientWidth };
    }));
    assert.ok(rows.length >= 9);
    const reference = rows[0];
    for (const row of rows) {
      for (const key of ["x", "width", "height", "labelX"]) assert.ok(Math.abs(row[key] - reference[key]) < 1, `${key}: ${JSON.stringify(row)}`);
      assert.equal(row.font, reference.font);
      assert.equal(row.border, 0);
      assert.ok(row.icon && !row.overflow);
    }
  };
  const toolbarFits = async dialog => {
    const bounds = await dialog.locator(".management-toolbar").evaluate(node => {
      const rect = node.getBoundingClientRect(), search = node.querySelector("input").getBoundingClientRect();
      return { overflow: node.scrollWidth > node.clientWidth, searchWidth: search.width, width: rect.width,
        controlsBelow: [...node.querySelectorAll("select,button")].every(control => control.getBoundingClientRect().top >= search.bottom) };
    });
    assert.ok(!bounds.overflow && bounds.controlsBelow);
    assert.ok(Math.abs(bounds.searchWidth - bounds.width) < 1);
    assert.equal(await dialog.evaluate(node => node.scrollWidth > node.clientWidth), false);
  };
  await page.goto(url);
  await page.locator("#pinned-collections .nav-item").waitFor();
  await page.waitForFunction(() => document.querySelector("#list-pane").dataset.loading === "false");
  await alignedNavigation();
  await page.locator("#pinned-collections .nav-item").click();
  await page.waitForFunction(() => document.querySelector("#pinned-collections [aria-current='page']"));
  assert.equal(await page.locator("#nav-views [aria-current]").count(), 0);
  await page.locator("#nav-views [data-view='inbox']").click();
  await page.waitForFunction(() => !document.querySelector("#pinned-collections [aria-current]"));

  for (const width of [1280, 390, 320]) {
    await page.setViewportSize({ width, height: 900 });
    if (width < 1180) {
      if (await page.locator("#app").evaluate(node => node.classList.contains("detail-open"))) await page.locator("#detail-back").click();
      await page.locator("#list-pane [data-open-sidebar]").click();
    }
    await alignedNavigation();
    for (const theme of ["light", "dark"]) {
      await page.evaluate(theme => document.documentElement.dataset.theme = theme, theme);
      await page.locator("#browse-tags").click();
      const tags = page.getByRole("dialog", { name: "标签管理", exact: true });
      await toolbarFits(tags);
      await tags.getByLabel("查找标签").fill("无此标签");
      await tags.getByText("没有符合条件的标签").waitFor();
      await tags.getByRole("button", { name: "新建标签", exact: true }).click();
      const editor = page.getByRole("dialog", { name: "新建主题标签", exact: true });
      assert.ok(await editor.getByRole("checkbox", { name: "参与 AI 自动打标" }).isChecked());
      assert.equal(await editor.getByLabel("正例").isVisible(), false);
      await editor.getByText("匹配示例与排除条件", { exact: true }).click();
      await editor.getByLabel("正例").fill("LoRA 的训练与应用");
      await editor.getByLabel("反例").fill("无关的图片展示");
      await close(editor); await close(tags);
      await page.locator("#browse-collections").click();
      const collections = page.getByRole("dialog", { name: "合集", exact: true });
      await toolbarFits(collections);
      await collections.getByLabel("查找合集").fill("AIGC");
      assert.equal(await collections.locator(".collection-row").count(), 1);
      await close(collections);
    }
    if (width < 1180) {
      await page.keyboard.press("Escape");
      assert.ok(await page.locator("#list-pane").evaluate(node => !node.inert));
    }
  }
  assert.deepEqual(errors, []); assert.deepEqual(writes, []);
  console.log("ok management UI: aligned navigation, pinned selection, search, advanced fields, 320/390/1280px, light/dark, read-only");
} finally { await browser.close(); server.close(); }
