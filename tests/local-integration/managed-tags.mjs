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
    // Reproduce a pending legacy uses/contra archive from the real UI. Losing
    // its response must keep the original operation identity and audit once.
    await manager.getByLabel("搜索或新建标签", { exact: true }).fill("反对");
    const opposition = manager.locator('.tl-group[data-dim="uses"] .tl-row').filter({ hasText: "反对" });
    await opposition.locator(".tl-main").click();
    const archiveKeys = [];
    let loseArchive = true;
    await page.route("**/api/tag-catalog/operations", async (route) => {
      const body = route.request().postDataJSON();
      if (body.dimension !== "uses" || body.id !== "contra" || body.type !== "archive") return route.continue();
      archiveKeys.push(body.operation_key);
      if (loseArchive) {
        loseArchive = false;
        const response = await route.fetch();
        assert.equal(response.status(), 200, await response.text());
        await route.abort("failed");
      } else await route.continue();
    });
    await manager.locator(".tl-panel").getByRole("button", { name: "停用", exact: true }).click();
    await manager.locator(".tl-receipt").getByRole("button", { name: "重试", exact: true }).click();
    await manager.locator(".tl-receipt").waitFor({ state: "detached" });
    assert.equal(archiveKeys.length, 2);
    assert.equal(archiveKeys[0], archiveKeys[1]);
    await page.unroute("**/api/tag-catalog/operations");
    let archived = await api("/api/tag-catalog");
    assert.equal(archived.catalog.uses.find((t) => t.id === "contra").active, false);
    assert.equal((await api("/api/tag-catalog/history?dimension=uses&id=contra")).items.length, 1);
    await manager.locator(".tl-row.is-off").filter({ hasText: "反对" }).getByRole("button", { name: "恢复", exact: true }).click();
    await wait(async () => (await api("/api/tag-catalog")).catalog.uses.find((t) => t.id === "contra").active);
    archived = await api("/api/tag-catalog");
    assert.equal(archived.catalog.uses.find((t) => t.id === "contra").ai_enabled, false);
    const forbidden = await fetch(base + "/api/tag-catalog/operations", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ operation_key: crypto.randomUUID(), expected_revision: archived.revision, dimension: "uses", id: "contra", type: "edit", definition: { ai_enabled: true } }),
    });
    assert.equal(forbidden.status, 400);
    assert.equal((await forbidden.json()).error, "personal_use_human_only");
    await manager.getByLabel("搜索或新建标签", { exact: true }).fill("");
    await manager
      .getByRole("button", { name: "新建标签", exact: true })
      .click();
    const add = manager.locator('.tl-add[data-dim="topics"]');
    await add.getByLabel("新标签名称").fill("LoRA");
    await add.getByLabel("新标签说明").fill("LoRA 低秩适配器的训练和使用");
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
    await add.getByRole("button", { name: "创建", exact: true }).click();
    // A lost response keeps a durable receipt; retrying reuses the same key.
    const receipt = manager.locator(".tl-receipt");
    await receipt.waitFor();
    await receipt.getByRole("button", { name: "重试", exact: true }).click();
    await receipt.waitFor({ state: "detached" });
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
    await openTag(manager, "LoRA");
    await renameOpen(manager, "LoRA 微调");
    await wait(async () => (await api("/api/tag-catalog")).catalog.topics.find((t) => t.id === term.id).label === "LoRA 微调");
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
    await openTag(manager, existing.label);
    await renameOpen(manager, "AI 编程实践");
    await wait(async () => (await api("/api/tag-catalog")).catalog.topics.find((t) => t.id === "ai_coding").label === "AI 编程实践");
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
    await collections
      .locator(".collection-row")
      .filter({ hasText: "AIGC 自动收录" })
      .click();
    await collections.locator(".cl-hooks .tl-chip-add").click();
    const rule = page.locator('wa-dialog[label="给「AIGC 自动收录」挂标签"]');
    await rule.getByLabel("查找标签").fill("LoRA");
    await rule.locator(".tag-pick", { hasText: "LoRA 微调" }).click();
    await rule.getByRole("button", { name: "保存", exact: true }).click();
    await rule.waitFor({ state: "detached" });
    await wait(async () => (await api("/api/collections/" + collectionID)).collection.rule_enabled);
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
    const captured = await fetch(workerURL + "/api/captures", {
      method: "POST", headers: {
        Authorization: "Bearer " + (process.env.CAIRN_APP_TOKEN || "app"),
        "Content-Type": "application/json"
      }, body: JSON.stringify({
        url: "https://x.com/test/status/2095999999999999888", note: "", client_id: crypto.randomUUID(),
        capture: { title: "LoRA 自动归属验证", language: "en", images: [],
          text: "BrowserEntity provides a practical guide to LoRA fine-tuning, evaluating large language models, methods, tools and data." }
      })
    });
    assert.equal(captured.status, 201, await captured.clone().text());
    assert.equal((await captured.json()).id, newID);
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
    await manager.getByLabel("搜索或新建标签").fill("LoRA");
    await manager
      .locator(".tl-row")
      .filter({ hasText: "LoRA 微调" })
      .locator(".tl-ai")
      .click();
    await wait(async () => (await api("/api/tag-catalog")).catalog.topics.find((t) => t.id === term.id).ai_enabled === false);
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

// Search, open the matching row in place, and rename it on blur.
async function openTag(manager, label) {
  await manager.getByLabel("搜索或新建标签").fill(label);
  await manager.locator(".tl-row").filter({ hasText: label }).first().locator(".tl-main").click();
  await manager.locator(".tl-panel").waitFor();
}
async function renameOpen(manager, label) {
  const name = manager.locator(".tl-panel").getByLabel("名称", { exact: true });
  await name.fill(label);
  await name.press("Enter");
}
