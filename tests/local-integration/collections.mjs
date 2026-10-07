import { closeWorkspace, setCheckbox } from "./workspace-helper.mjs";
import { closeInspector, openCuration } from "../browser/workspace-helper.mjs";
import assert from "node:assert/strict";

// Real browser -> Go -> Worker -> D1. Only transport failures are injected.
export async function verifyCollections(browser, base, bookmarkID) {
  const page = await browser.newPage({
    viewport: { width: 1280, height: 900 },
  });
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const api = async (path, body) => {
    const response = await fetch(
      base + "/api/collections" + path,
      body
        ? {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(body),
          }
        : {},
    );
    assert.equal(response.status, 200, await response.clone().text());
    return response.json();
  };
  const waitAPI = async (check, timeout = 60000) => {
    const end = Date.now() + timeout;
    while (Date.now() < end) {
      if (await check()) return;
      await new Promise((r) => setTimeout(r, 200));
    }
    throw Error("timeout waiting for collection state");
  };
  try {
    // Plain HTTP NAS pages do not expose randomUUID.
    await page.addInitScript(() =>
      Object.defineProperty(crypto, "randomUUID", { value: undefined }),
    );
    await page.goto(`${base}/bookmarks/${bookmarkID}`);
    await closeInspector(page);
    await page.locator("#browse-collections").click();
    await page
      .locator("#management-page")
      .getByRole("button", { name: "新建", exact: true })
      .click();
    const create = page.locator('wa-dialog[label="新建合集"]');
    await create.getByLabel("合集名称").fill("项目资料");
    await create.getByLabel("合集说明").fill("为个人网站准备的参考");
    await create.getByRole("button", { name: "创建", exact: true }).click();
    await create.waitFor({ state: "detached" });
    let catalog = await api("");
    const id = catalog.items.find((c) => c.name === "项目资料").id;
    await closeWorkspace(page);
    await openCuration(page);
    await page.locator("#inspector-add-collection").click();
    const picker = page.locator('wa-dialog[label="加入合集"]');
    await setCheckbox(picker, "项目资料", true);
    await picker.getByRole("button", { name: "保存", exact: true }).click();
    await picker.waitFor({ state: "detached" });
    assert.deepEqual(
      (await api(`/${id}`)).items.map((i) => i.link_id),
      [bookmarkID],
    );

    await closeInspector(page);
    await page.locator("#browse-collections").click();
    await page
      .locator("#management-page .collection-row")
      .filter({ hasText: "项目资料" })
      .getByRole("button", { name: "阅读", exact: true })
      .click();
    await page.waitForURL(
      (url) => url.searchParams.get("collection_id") === id,
    );
    await page.waitForFunction(
      () => document.querySelector("#list-title")?.textContent === "项目资料",
    );
    await page
      .locator("#collection-context")
      .getByRole("button", { name: "管理" })
      .click();
    let manager = page.locator('wa-dialog[label="管理合集"]');
    await manager.getByRole("button", { name: "备注", exact: true }).click();
    const note = page.locator('wa-dialog[label="合集内备注"]');
    await note
      .getByRole("textbox", { name: "合集内备注", exact: true })
      .fill("引用其中的方法");
    await note.getByRole("button", { name: "保存", exact: true }).click();
    await note.waitFor({ state: "detached" });
    await manager.getByText("引用其中的方法", { exact: true }).waitFor();

    // Concurrent edits never silently replace an already committed remote version.
    let current = (await api(`/${id}`)).collection;
    await api(`/${id}/operations`, {
      operation_key: crypto.randomUUID(),
      expected_revision: current.revision,
      type: "edit",
      name: "网页另一端",
    });
    await manager.getByLabel("合集名称").fill("我的项目资料");
    await manager.getByRole("button", { name: "保存", exact: true }).click();
    const conflict = page.locator('wa-dialog[label="合集已在其他设备更新"]');
    await conflict.getByRole("button", { name: "取消", exact: true }).click();
    assert.equal(
      await manager.getByLabel("合集名称").inputValue(),
      "我的项目资料",
    );
    assert.equal((await api(`/${id}`)).collection.name, "网页另一端");
    await manager.getByRole("button", { name: "保存", exact: true }).click();
    await conflict
      .getByRole("button", { name: "重新提交", exact: true })
      .click();
    await manager.waitFor({ state: "detached" });
    assert.equal((await api(`/${id}`)).collection.name, "我的项目资料");

    // A response lost AFTER commit reuses the exact operation identity.
    await page
      .locator("#collection-context")
      .getByRole("button", { name: "管理" })
      .click();
    manager = page.locator('wa-dialog[label="管理合集"]');
    const keys = [];
    let lose = true;
    await page.route(`**/api/collections/${id}/operations`, async (route) => {
      keys.push(route.request().postDataJSON().operation_key);
      if (lose) {
        lose = false;
        await route.fetch();
        await route.abort("failed");
      } else await route.continue();
    });
    await manager.getByLabel("合集说明").fill("响应丢失也能恢复");
    await manager.getByRole("button", { name: "保存", exact: true }).click();
    await page.waitForFunction(
      () =>
        !document.querySelector(
          'wa-dialog[label="管理合集"] wa-button[variant=brand]',
        )?.disabled,
    );
    await manager.getByRole("button", { name: "保存", exact: true }).click();
    await manager.waitFor({ state: "detached" });
    assert.equal(keys.length, 2);
    assert.equal(keys[0], keys[1]);
    await page.unroute(`**/api/collections/${id}/operations`);

    await page.setViewportSize({ width: 390, height: 844 });
    await page.locator("#detail-back").click();
    await page
      .locator("#collection-context")
      .getByRole("button", { name: "管理" })
      .click();
    manager = page.locator('wa-dialog[label="管理合集"]');
    assert.equal(
      await manager.evaluate((n) => n.scrollWidth <= n.clientWidth),
      true,
    );
    await manager
      .getByRole("button", { name: "删除合集", exact: true })
      .click();
    await page
      .locator('wa-dialog[label="删除这个合集？"]')
      .getByRole("button", { name: "删除合集", exact: true })
      .click();
    await manager.waitFor({ state: "detached" });
    assert.equal((await api(`/${id}`)).collection.deleted, 1);
    await page.getByRole("button", { name: "撤销", exact: true }).click();
    await waitAPI(async () => !(await api(`/${id}`)).collection.deleted);
    assert.equal((await api(`/${id}`)).items[0].note, "引用其中的方法");
    await page.setViewportSize({ width: 1280, height: 900 });
    const target = crypto.randomUUID();
    await api(`/${target}/operations`, {
      operation_key: crypto.randomUUID(),
      expected_revision: 0,
      type: "create",
      name: "整理测试合集",
      description: "LLM实践参考",
    });
    await page.reload();
    await page.locator("#browse-collections").click();
    await page
      .locator("#management-page")
      .getByRole("button", { name: "自动整理", exact: true })
      .click();
    let organizer = page.locator("#management-page");
    assert.equal(
      await organizer.locator("wa-select").evaluate((n) => n.value),
      "review",
    );
    await organizer
      .getByRole("button", { name: "开始整理现有收藏", exact: true })
      .click();
    let results = page.locator("#management-page");
    await results
      .locator(".collection-review-item")
      .first()
      .waitFor({ timeout: 60000 });
    assert.deepEqual(
      (await api(`/${target}`)).items,
      [],
      "review mode must not write memberships",
    );
    const item = results.locator(".collection-review-item").first();
    await item.getByRole("button", { name: "确认加入", exact: true }).click();
    await waitAPI(async () => (await api(`/${target}`)).items.length > 0);
    await closeWorkspace(page);
    const direct = crypto.randomUUID();
    await api(`/${direct}/operations`, {
      operation_key: crypto.randomUUID(),
      expected_revision: 0,
      type: "create",
      name: "直接应用测试",
      description: "LLM实践参考",
    });
    // Entity extraction already reserved one per-item call on the first bookmark.
    // Use a distinct, normally archived source for direct mode; never clear the
    // ledger or raise limits to force two workflows through an exhausted item.
    const captureResponse = await fetch(
      process.env.CAIRN_WORKER_URL + "/api/captures",
      {
        method: "POST",
        headers: {
          Authorization: "Bearer " + process.env.CAIRN_APP_TOKEN,
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          url: "https://example.com/direct-organizing",
          note: "",
          client_id: crypto.randomUUID(),
          capture: {
            title: "独立整理材料",
            language: "zh",
            text: "# 网站设计\n\n用于网站改版的LLM实践参考与界面设计方法。",
            images: [],
          },
        }),
      },
    );
    assert.equal(
      captureResponse.status,
      201,
      await captureResponse.clone().text(),
    );
    const fresh = await captureResponse.json();
    const directRun = await api("/organizing", {
      operation_key: crypto.randomUUID(),
      collection_ids: [direct],
      link_ids: [fresh.id],
      mode: "apply",
    });
    await waitAPI(async () => {
      const d = await api("/organizing/" + directRun.run.id);
      return (
        d.run.auto_finished === 1 &&
        d.actions.some((a) => a.actor === "direct" && a.status === "applied")
      );
    });
    assert.deepEqual(
      (await api(`/${direct}`)).items.map((i) => i.link_id),
      [fresh.id],
    );
    await page.reload();
    await page.locator("#browse-collections").click();
    await page
      .locator("#management-page")
      .getByRole("button", { name: "自动整理", exact: true })
      .click();
    organizer = page.locator("#management-page");
    await organizer
      .locator(".collection-row")
      .filter({ hasText: "直接应用" })
      .getByRole("button")
      .first()
      .click();
    results = page.locator("#management-page");
    await results
      .getByText(/直接应用 ·/)
      .first()
      .waitFor();
    assert.equal((await api(`/${direct}`)).collection.name, "直接应用测试");
    await page.setViewportSize({ width: 390, height: 844 });
    assert.equal(
      await results.evaluate((n) => n.scrollWidth <= n.clientWidth),
      true,
    );
    await closeWorkspace(page);
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.locator("#browse-collections").click();
    await page
      .locator("#management-page")
      .getByRole("button", { name: "自动整理", exact: true })
      .click();
    organizer = page.locator("#management-page");
    await organizer.getByLabel("自动整理模式").click();
    await page
      .getByRole("option", { name: "直接应用预选，无需审核", exact: true })
      .click();
    await organizer
      .getByRole("button", { name: "开始整理现有收藏", exact: true })
      .click();
    const before = (await api(`/${direct}`)).items;
    await waitAPI(async () =>
      Boolean((await api("/organizing")).items[0].next_attempt_at),
    );
    results = page.locator("#management-page");
    await results.getByText(/当日预算已用完/).waitFor({ timeout: 15000 });
    assert.deepEqual((await api(`/${direct}`)).items, before);
    await closeWorkspace(page);
    console.log(
      "ok   Collection organizing: actual Jev mock, review default with no writes, explicit approval, scoped direct application, preserved definitions, budget deferral and mobile layout",
    );
    const response = await fetch(`${base}/api/bookmarks/${bookmarkID}`);
    assert.equal(
      response.status,
      200,
      "collection deletion never deletes a bookmark",
    );
    assert.deepEqual(errors, []);
    console.log(
      "ok   Collections: HTTP NAS UUID, create/add, contextual note, conflict draft, lost response, mobile and delete/undo through real Go/Worker/D1",
    );
  } catch (error) {
    const runs = await api("/organizing");
    console.error(
      "Organizing diagnostics",
      JSON.stringify({
        dialogs: await page.locator("dialog").allTextContents(),
        runs,
      }),
    );
    if (runs.items?.length)
      console.error(
        "Latest organizing run",
        JSON.stringify(await api("/organizing/" + runs.items[0].id)),
      );
    throw error;
  } finally {
    await page.close();
  }
}
