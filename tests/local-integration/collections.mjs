import assert from "node:assert/strict";

// Real browser -> Go -> Worker -> D1. Only transport failures are injected.
export async function verifyCollections(browser, base, bookmarkID) {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  const errors = [];
  page.on("pageerror", e => errors.push(e.message));
  const api = async (path, body) => {
    const response = await fetch(base + "/api/collections" + path, body ? {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body)
    } : {});
    assert.equal(response.status, 200, await response.clone().text());
    return response.json();
  };
  try {
    // Plain HTTP NAS pages do not expose randomUUID.
    await page.addInitScript(() => Object.defineProperty(crypto, "randomUUID", { value: undefined }));
    await page.goto(`${base}/bookmarks/${bookmarkID}`);
    await page.locator("#browse-collections").click();
    await page.getByRole("dialog", { name: "合集", exact: true }).getByRole("button", { name: "新建", exact: true }).click();
    const create = page.getByRole("dialog", { name: "新建合集", exact: true });
    await create.getByLabel("合集名称").fill("项目资料");
    await create.getByLabel("合集说明").fill("为个人网站准备的参考");
    await create.getByRole("button", { name: "创建", exact: true }).click();
    await create.waitFor({ state: "detached" });
    let catalog = await api("");
    const id = catalog.items.find(c => c.name === "项目资料").id;
    await page.getByRole("dialog", { name: "合集", exact: true }).getByLabel("关闭").click();
    await page.locator("#add-to-collection").click();
    const picker = page.getByRole("dialog", { name: "加入合集", exact: true });
    await picker.getByRole("checkbox", { name: "项目资料" }).check();
    await picker.getByRole("button", { name: "保存", exact: true }).click();
    await picker.waitFor({ state: "detached" });
    assert.deepEqual((await api(`/${id}`)).items.map(i => i.link_id), [bookmarkID]);

    await page.locator("#browse-collections").click();
    await page.getByRole("button", { name: "项目资料 · 1", exact: true }).click();
    await page.waitForURL(url => url.searchParams.get("collection_id") === id);
    await page.waitForFunction(() => document.querySelector("#list-title")?.textContent === "项目资料");
    await page.locator("#collection-context").getByRole("button", { name: "管理" }).click();
    let manager = page.getByRole("dialog", { name: "管理合集", exact: true });
    await manager.getByRole("button", { name: "备注", exact: true }).click();
    const note = page.getByRole("dialog", { name: "合集内备注", exact: true });
    await note.getByLabel("合集内备注").fill("引用其中的方法");
    await note.getByRole("button", { name: "保存", exact: true }).click();
    await note.waitFor({ state: "detached" });
    await manager.getByText("引用其中的方法", { exact: true }).waitFor();

    // Concurrent edits never silently replace an already committed remote version.
    let current = (await api(`/${id}`)).collection;
    await api(`/${id}/operations`, { operation_key: crypto.randomUUID(), expected_revision: current.revision,
      type: "edit", name: "网页另一端" });
    await manager.getByLabel("合集名称").fill("我的项目资料");
    await manager.getByRole("button", { name: "保存", exact: true }).click();
    const conflict = page.getByRole("dialog", { name: "合集已在其他设备更新" });
    await conflict.getByRole("button", { name: "取消", exact: true }).click();
    assert.equal(await manager.getByLabel("合集名称").inputValue(), "我的项目资料");
    assert.equal((await api(`/${id}`)).collection.name, "网页另一端");
    await manager.getByRole("button", { name: "保存", exact: true }).click();
    await conflict.getByRole("button", { name: "重新提交", exact: true }).click();
    await manager.waitFor({ state: "detached" });
    assert.equal((await api(`/${id}`)).collection.name, "我的项目资料");

    // A response lost AFTER commit reuses the exact operation identity.
    await page.locator("#collection-context").getByRole("button", { name: "管理" }).click();
    manager = page.getByRole("dialog", { name: "管理合集", exact: true });
    const keys = [];
    let lose = true;
    await page.route(`**/api/collections/${id}/operations`, async route => {
      keys.push(route.request().postDataJSON().operation_key);
      if (lose) { lose = false; await route.fetch(); await route.abort("failed"); }
      else await route.continue();
    });
    await manager.getByLabel("合集说明").fill("响应丢失也能恢复");
    await manager.getByRole("button", { name: "保存", exact: true }).click();
    await page.waitForFunction(() => !document.querySelector('dialog[aria-label="管理合集"] .btn-primary')?.disabled);
    await manager.getByRole("button", { name: "保存", exact: true }).click();
    await manager.waitFor({ state: "detached" });
    assert.equal(keys.length, 2); assert.equal(keys[0], keys[1]);
    await page.unroute(`**/api/collections/${id}/operations`);

    await page.setViewportSize({ width: 390, height: 844 });
    await page.locator("#detail-back").click();
    await page.locator("#collection-context").getByRole("button", { name: "管理" }).click();
    manager = page.getByRole("dialog", { name: "管理合集", exact: true });
    assert.equal(await manager.evaluate(n => n.scrollWidth <= n.clientWidth), true);
    await manager.getByRole("button", { name: "删除合集", exact: true }).click();
    await page.getByRole("dialog", { name: "删除这个合集？", exact: true }).getByRole("button", { name: "删除合集", exact: true }).click();
    await manager.waitFor({ state: "detached" });
    assert.equal((await api(`/${id}`)).collection.deleted, 1);
    await page.getByRole("button", { name: "撤销", exact: true }).click();
    await page.waitForFunction(async cid => !(await (await fetch(`/api/collections/${cid}`)).json()).collection.deleted, id);
    assert.equal((await api(`/${id}`)).items[0].note, "引用其中的方法");
    const response = await fetch(`${base}/api/bookmarks/${bookmarkID}`);
    assert.equal(response.status, 200, "collection deletion never deletes a bookmark");
    assert.deepEqual(errors, []);
    console.log("ok   Collections: HTTP NAS UUID, create/add, contextual note, conflict draft, lost response, mobile and delete/undo through real Go/Worker/D1");
  } finally { await page.close(); }
}
