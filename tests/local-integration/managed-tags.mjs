import { closeWorkspace, setCheckbox } from "./workspace-helper.mjs";
import { openCuration, closeInspector } from "../browser/workspace-helper.mjs";
import assert from "node:assert/strict";
export async function verifyManagedTags(
  browser,
  base,
  workerURL,
  internalToken,
  bookmarkID,
) {
  const page = await browser.newPage({
      viewport: { width: 1204, height: 900 },
    }),
    errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const api = async (path, body) => {
    const r = await fetch(
      base + path,
      body
        ? {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(body),
          }
        : {},
    );
    assert.equal(r.status, 200, await r.clone().text());
    return r.json();
  };
  const internal = async (path, body) => {
    const r = await fetch(workerURL + "/api/enrichment/" + path, {
      method: body ? "POST" : "GET",
      headers: {
        Authorization: "Bearer " + internalToken,
        "Content-Type": "application/json",
      },
      body: body ? JSON.stringify(body) : undefined,
    });
    if (r.status === 404 && path.startsWith("classifications/")) return {};
    assert.equal(r.status, 200, await r.clone().text());
    return r.json();
  };
  const wait = async (check) => {
    const end = Date.now() + 60000;
    while (Date.now() < end) {
      if (await check()) return;
      await new Promise((r) => setTimeout(r, 150));
    }
    throw Error("managed tag timeout");
  };
  try {
    await page.addInitScript(() =>
      Object.defineProperty(crypto, "randomUUID", { value: undefined }),
    );
    await page.goto(base + "/bookmarks/" + bookmarkID);
    await closeInspector(page);
    await page.locator("#browse-tags").click();
    let manager = page.locator("#management-page");
    await manager
      .getByRole("button", { name: "新建标签", exact: true })
      .click();
    let editor = page.locator(".manager-editor");
    await editor.getByLabel("标签名称").fill("LoRA");
    await editor.getByLabel("标签含义").fill("LoRA 低秩适配器的训练和使用");
    assert.equal(await editor.getByRole("checkbox").isChecked(), true);
    const keys = [];
    let lose = true;
    await page.route("**/api/tag-catalog/operations", async (route) => {
      keys.push(route.request().postDataJSON().operation_key);
      if (lose) {
        lose = false;
        await route.fetch();
        await route.abort("failed");
      } else await route.continue();
    });
    await editor.getByRole("button", { name: "保存", exact: true }).click();
    await wait(
      async () =>
        !(await editor
          .getByRole("button", { name: "保存", exact: true })
          .isDisabled()),
    );
    await editor.getByRole("button", { name: "保存", exact: true }).click();
    await editor.getByLabel("标签名称").waitFor({ state: "detached" });
    assert.equal(keys.length, 2);
    assert.equal(keys[0], keys[1]);
    await page.unroute("**/api/tag-catalog/operations");
    let data = await api("/api/tag-catalog"),
      term = data.catalog.topics.find((t) => t.label === "LoRA");
    assert.equal(term.ai_enabled, true);
    const ref = "system/topics/" + term.id;
    const human = await api("/api/bookmarks/" + bookmarkID + "/tags");
    await api("/api/bookmarks/" + bookmarkID + "/tags", {
      operation_key: crypto.randomUUID(),
      expected_revision: human.revision,
      actions: [{ action: "accept", tag_ref: ref }],
    });
    await page.goto(base + "/bookmarks/" + bookmarkID);
    await openCuration(page);
    await page
      .locator('.tag-system-row[data-dimension="topics"]')
      .getByText("LoRA", { exact: true })
      .waitFor({ state: "visible" });
    await closeInspector(page);
    await page.locator("#browse-tags").click();
    manager = page.locator("#management-page");
    await manager.getByLabel("查找标签").fill("LoRA");
    await manager
      .locator(".tag-manager-row")
      .filter({ has: page.getByText("LoRA", { exact: true }) })
      .getByRole("button", { name: "管理", exact: true })
      .click();
    editor = page.locator(".manager-editor");
    await editor.getByLabel("标签名称").fill("LoRA 微调");
    await editor.getByRole("button", { name: "保存", exact: true }).click();
    await editor.getByLabel("标签名称").waitFor({ state: "detached" });
    const renamed = await api("/api/tag-catalog");
    assert.equal(
      renamed.catalog.version,
      data.catalog.version,
      JSON.stringify({
        before: term,
        after: renamed.catalog.topics.find((t) => t.id === term.id),
      }),
    );
    assert.equal(
      renamed.catalog.topics.find((t) => t.id === term.id).label,
      "LoRA 微调",
    );
    await closeWorkspace(page);
    await openCuration(page);
    await page
      .locator('.tag-system-row[data-dimension="topics"]')
      .getByText("LoRA 微调", { exact: true })
      .waitFor({ state: "visible" });
    const existing = renamed.catalog.topics.find((t) => t.id === "ai_coding");
    assert.ok(existing);
    const attached = await api("/api/bookmarks/" + bookmarkID + "/tags");
    await api("/api/bookmarks/" + bookmarkID + "/tags", {
      operation_key: crypto.randomUUID(),
      expected_revision: attached.revision,
      actions: [{ action: "accept", tag_ref: "system/topics/ai_coding" }],
    });
    await page.goto(base + "/bookmarks/" + bookmarkID);
    await openCuration(page);
    await closeInspector(page);
    await page.locator("#browse-tags").click();
    manager = page.locator("#management-page");
    await manager.getByLabel("查找标签").fill(existing.label);
    await manager
      .locator(".tag-manager-row")
      .filter({ has: page.getByText(existing.label, { exact: true }) })
      .getByRole("button", { name: "管理", exact: true })
      .click();
    editor = page.locator(".manager-editor");
    await editor.getByLabel("标签名称").fill("AI 编程实践");
    await editor.getByRole("button", { name: "保存", exact: true }).click();
    await editor.getByLabel("标签名称").waitFor({ state: "detached" });
    await page.evaluate(async () => {
      const { loadV1, loadV2 } = await import("/assets/js/taxonomy.js");
      await loadV2();
      await loadV1();
      const { emit } = await import("/assets/js/store.js");
      emit("taxonomy");
    });
    await closeWorkspace(page);
    await openCuration(page);
    await page
      .locator('.tag-system-row[data-dimension="topics"]')
      .getByText("AI 编程实践", { exact: true })
      .waitFor({ state: "visible" });
    const old = await internal("classifications/" + bookmarkID);
    const collectionID = crypto.randomUUID();
    await api(`/api/collections/${collectionID}/operations`, {
      operation_key: crypto.randomUUID(),
      expected_revision: 0,
      type: "create",
      name: "AIGC 自动收录",
    });
    await closeWorkspace(page);
    await closeInspector(page);
    await page.locator("#browse-collections").click();
    let collections = page.locator("#management-page");
    const row = collections
      .locator(".collection-row")
      .filter({ hasText: "AIGC 自动收录" });
    await row.getByRole("button", { name: /AIGC 自动收录/ }).click();
    const collectionManager = page.locator(".manager-editor");
    await collectionManager
      .getByRole("button", { name: "自动收录标签", exact: true })
      .click();
    let rule = page.locator('wa-dialog[label="合集自动收录"]');
    await setCheckbox(rule, "按标签自动收录新收藏", true);
    await rule.getByLabel("查找自动收录标签").fill("LoRA");
    await setCheckbox(rule, /LoRA 微调/, true);
    await rule.getByRole("button", { name: "保存规则", exact: true }).click();
    await rule.waitFor({ state: "detached" });
    const created = await fetch(workerURL + "/api/links", {
      method: "POST",
      headers: {
        Authorization: "Bearer " + (process.env.CAIRN_APP_TOKEN || "app"),
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        url: "https://x.com/test/status/2095999999999999888",
        note: "LoRA 自动归属验证",
      }),
    });
    assert.equal(created.status, 201, await created.clone().text());
    const newID = (await created.json()).id;
    await wait(async () => {
      const job = await internal("classifications/" + newID);
      return job.status === "completed";
    });
    const snapshot = await api("/api/bookmarks/" + newID + "/tags");
    assert.ok(
      snapshot.automatic.topics.includes(term.id),
      "new tag participates in actual mock Jev classification",
    );
    await internal("collection-rules/drain", {});
    await wait(async () =>
      (await api("/api/collections/" + collectionID)).items.some(
        (i) => i.link_id === newID,
      ),
    );
    let detail = await api("/api/collections/" + collectionID);
    assert.equal(detail.items.find((i) => i.link_id === newID).origin, "rule");
    assert.ok(
      JSON.parse(
        detail.items.find((i) => i.link_id === newID).matched_tags,
      ).includes(ref),
    );
    const unchanged = await internal("classifications/" + bookmarkID);
    assert.equal(unchanged.target_generation, old.target_generation);
    assert.equal(unchanged.attempts, old.attempts);
    await api(`/api/collections/${collectionID}/operations`, {
      operation_key: crypto.randomUUID(),
      expected_revision: detail.collection.revision,
      type: "remove",
      link_ids: [newID],
    });
    await internal("collection-rules/drain", {});
    detail = await api("/api/collections/" + collectionID);
    assert.ok(!detail.items.some((i) => i.link_id === newID));
    await closeWorkspace(page);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.locator("#detail-back").click();
    await page.locator("#tabbar [data-page=tags]").click();
    manager = page.locator("#management-page");
    assert.equal(
      await manager.evaluate((n) => n.scrollWidth <= n.clientWidth),
      true,
    );
    await manager.getByLabel("查找标签").fill("LoRA");
    await manager
      .locator(".tag-manager-row")
      .filter({ has: page.getByText("LoRA 微调", { exact: true }) })
      .getByRole("button", { name: "管理", exact: true })
      .click();
    editor = page.locator(".manager-editor");
    await setCheckbox(editor, "参与 AI 自动打标", false);
    await editor.getByRole("button", { name: "保存", exact: true }).click();
    await editor.getByLabel("标签名称").waitFor({ state: "detached" });
    data = await api("/api/tag-catalog");
    assert.equal(
      data.catalog.topics.find((t) => t.id === term.id).ai_enabled,
      false,
    );
    assert.deepEqual(errors, []);
    process.stdout.write(
      "ok managed tags: real D1 directory, HTTP UUID, uncertain response replay, AI hot reload, no historic rerun, rules, manual exclusion, phone layout\n",
    );
  } finally {
    await page.close();
  }
}
