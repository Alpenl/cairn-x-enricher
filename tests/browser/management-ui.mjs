// End-to-end workspace acceptance against synthetic read-only APIs.
import assert from "node:assert/strict";
import { chromium } from "playwright";
import { createFixtureState, startFixtureServer } from "./fixture-server.mjs";
import { taxonomyV2 } from "./fixture-data.mjs";
const { server, url } = await startFixtureServer({
  state: createFixtureState(),
});
const browser = await chromium.launch({
  executablePath: process.env.CHROME_PATH || "/usr/bin/google-chrome",
  args: ["--no-sandbox"],
});
const collection = {
  id: "test-pinned",
  name: "AIGC 生图生成视频相关",
  description: "生图与视频参考",
  item_count: 3,
  pinned: true,
  archived: false,
  deleted: false,
  revision: 1,
  rule_enabled: false,
  rule_tags: "[]",
  selected_count: 0,
};
const errors = [],
  writes = [],
  assets = [];
try {
  const page = await browser.newPage({
    viewport: { width: 1600, height: 1000 },
  });
  page.setDefaultTimeout(10000);
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("response", (response) => {
    if (
      response.status() >= 400 &&
      new URL(response.url()).pathname.startsWith("/assets")
    )
      assets.push(response.url());
  });
  await page.route("**/api/**", async (route) => {
    const request = route.request(),
      path = new URL(request.url()).pathname;
    if (request.method() !== "GET") {
      writes.push(path);
      return route.abort();
    }
    let data;
    if (path === "/api/collections") data = { items: [collection] };
    if (path === `/api/collections/${collection.id}`)
      data = { collection, items: [] };
    if (path === "/api/collections/organizing") data = { items: [] };
    if (path === "/api/tag-catalog")
      data = {
        revision: 1,
        catalog: taxonomyV2(),
        counts: [],
        custom_tags: [],
      };
    if (data) return route.fulfill({ json: data });
    return route.continue();
  });
  const overflow = async () =>
    assert.equal(
      await page.evaluate(
        () => document.documentElement.scrollWidth > innerWidth,
      ),
      false,
    );
  // The redesign removed the sidebar shortcuts; pages open from settings / ⌘K.
  const go = async (key) => {
    await page.evaluate(async (key) => (await import("/assets/js/workspace.js")).navigate(key), key);
  };
  await page.goto(url);
  await page.waitForFunction(
    () => document.querySelector("#list-pane").dataset.loading === "false",
  );
  await page.locator("#pinned-collections .nav-item").waitFor();
  const columns = await page
    .locator("#library")
    .evaluate((node) =>
      getComputedStyle(node).gridTemplateColumns.split(" ").map(parseFloat),
    );
  assert.equal(columns.length, 3);
  assert.equal(columns[0], 352); // prototype 232 / 352 / flex / 300
  assert.equal(columns[2], 300);
  await page.locator("#pinned-collections .nav-item").click();
  await page.waitForFunction(() =>
    document.querySelector("#pinned-collections [aria-current]"),
  );
  assert.equal(await page.locator("#nav-views [aria-current]").count(), 0);
  await page.locator('#nav-views [data-view="all"]').click();
  await page.locator("#inspector-toggle").click(); // preserve the user's closed panel across page changes
  for (const width of [1600, 1100, 900, 390, 320]) {
    await page.setViewportSize({ width, height: 1000 });
    if (
      width < 760 &&
      (await page
        .locator("#app")
        .evaluate((node) => node.classList.contains("detail-open")))
    )
      await page.locator("#detail-back").click();
    for (const theme of ["light", "dark"]) {
      await page.evaluate(
        (theme) => (document.documentElement.dataset.theme = theme),
        theme,
      );
      // 设置 · 标签库 is a full page now; the workspace stays mounted under it.
      await go("tags");
      await page.locator("#settings-page:not([hidden])").waitFor();
      if (width > 1100) {
        await page.locator("#lib-search").fill("");
        await page.locator("#lib-list .lib-row").first().waitFor();
        assert.ok((await page.locator("#lib-list .lib-row").count()) > 10);
        await overflow();
        await page.locator("#lib-list .lib-row").nth(1).click();
        await page.locator("#td-name").waitFor();
        await page.locator("#lib-search").fill("无此标签");
        await page.locator(".lib-empty").getByRole("button", { name: "新建「无此标签」" }).click();
        assert.equal(await page.getByLabel("新标签名称", { exact: true }).inputValue(), "无此标签");
        await page.locator("#lib-search").fill("");
      } else {
        // ≤1100px: the list collapses into the 选择标签 dropdown above the editor.
        await page.getByRole("button", { name: "选择标签" }).click();
        await page.locator(".sp-menu [role=menuitem], .sp-menu [role=option]").nth(1).click();
        await page.locator("#td-name").waitFor();
        await page.locator("#sp-actions").getByRole("button", { name: "新建标签" }).click();
        await page.getByLabel("新标签名称", { exact: true }).fill("无此标签");
      }
      await page.locator("#new-desc").fill("低秩适配训练与模型应用");
      await overflow();
      await page.locator("#lib-detail-foot").getByRole("button", { name: "取消" }).click();
      await page.locator("#sp-back").click();
      await page.locator("#settings-page").waitFor({ state: "hidden" });
      await go("collections");
      await page.locator("#management-page .collection-row").first().waitFor();
      await page.getByLabel("查找合集", { exact: true }).fill("AIGC");
      assert.equal(await page.locator("#management-page .collection-row").count(), 1);
      await page.locator("#management-page .collection-row").first().click();
      await page.getByLabel("合集名称", { exact: true }).waitFor();
      await overflow();
      const pin = page.getByRole("checkbox", { name: "置顶", exact: true });
      assert.equal(await pin.isChecked(), true);
      // Rules are edited on 设置 · 合集规则; the collection card links there.
      await page.locator("#management-page .cl-hooks").getByRole("button", { name: "设置规则" }).waitFor();
    }
    await page.evaluate(async () => {
      (await import("/assets/js/workspace.js")).closeWorkspace();
    });
    if (width === 1600)
      await page.locator('#nav-views [data-view="all"]').click();
  }
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.evaluate(async () => {
    (await import("/assets/js/workspace.js")).closeWorkspace();
  });
  await page.locator("#inspector-toggle").click();
  const drawer = page.locator("wa-dialog.inspector-drawer[open]");
  await drawer.waitFor();
  await drawer.locator("#curation-why").waitFor();
  await drawer.getByRole("tab", { name: "处理", exact: true }).click();
  assert.equal(
    await page.locator("#diagnostics").evaluate((node) => node.open),
    true,
  );
  await page.evaluate(async () => {
    const { closeInspector, showInspector } = await import("/assets/js/workspace.js");
    closeInspector();
    showInspector("curation");
  });
  await drawer.locator("#curation-why").waitFor();
  await page.keyboard.press("Escape");
  await drawer.waitFor({ state: "detached" });
  assert.equal(
    await page.locator("#inspector").evaluate((node) => node.parentElement.id),
    "library",
  );
  await page.locator("#tf-toggle").click();
  await page.locator("#tf-pop").waitFor();
  await page.keyboard.press("Escape");
  await page.locator("#tf-pop").waitFor({ state: "hidden" });
  await page.keyboard.press("Control+k");
  const palette = page.locator('wa-dialog[label="搜索或执行命令"]');
  await palette.getByLabel("搜索命令", { exact: true }).waitFor();
  await palette.getByLabel("搜索命令", { exact: true }).fill("标签库");
  await palette.locator(".command-option").first().click();
  await page.getByRole("heading", { name: "标签库", exact: true }).waitFor();
  assert.deepEqual(errors, []);
  assert.deepEqual(writes, []);
  assert.deepEqual(assets, []);
  console.log(
    "ok redesigned workspace: pages, library columns, real forms, drawer, command palette, 320/390/900/1100/1600px, light/dark, read-only",
  );
} finally {
  await browser.close();
  server.close();
}
