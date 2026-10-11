import { openSettingsPage, leaveSettingsPage } from "./workspace-helper.mjs";
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
    // Personal uses stay human-only on the server, whatever a client sends.
    let archived = await api("/api/tag-catalog");
    const forbidden = await fetch(base + "/api/tag-catalog/operations", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ operation_key: crypto.randomUUID(), expected_revision: archived.revision, dimension: "uses", id: "contra", type: "edit", definition: { ai_enabled: true } }),
    });
    assert.equal(forbidden.status, 400);
    assert.equal((await forbidden.json()).error, "personal_use_human_only");

    // 设置 · 标签库: create a topic. Losing the response keeps a durable
    // receipt in the page header; 重试 replays the same operation key.
    await openSettingsPage(page, "tags");
    const lib = page.locator("#sp-body");
    await page.locator("#sp-actions").getByRole("button", { name: "新建标签", exact: true }).click();
    await lib.getByLabel("新标签名称").fill("LoRA");
    await lib.locator("#new-desc").fill("LoRA 低秩适配器的训练和使用");
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
    await lib.getByRole("button", { name: "创建", exact: true }).click();
    const receipt = page.locator("#sp-actions .sp-note-warn");
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

    // Archive with a lost response: same identity on retry, audited once; then restore.
    await openTag(page, "LoRA");
    const archiveKeys = [];
    let loseArchive = true;
    await page.route("**/api/tag-catalog/operations", async (route) => {
      const body = route.request().postDataJSON();
      if (body.id !== term.id || body.type !== "archive") return route.continue();
      archiveKeys.push(body.operation_key);
      if (loseArchive) {
        loseArchive = false;
        const response = await route.fetch();
        assert.equal(response.status(), 200, await response.text());
        await route.abort("failed");
      } else await route.continue();
    });
    await page.locator("#lib-detail-foot").getByRole("button", { name: "归档", exact: true }).click();
    await receipt.waitFor();
    await receipt.getByRole("button", { name: "重试", exact: true }).click();
    await receipt.waitFor({ state: "detached" });
    assert.equal(archiveKeys.length, 2);
    assert.equal(archiveKeys[0], archiveKeys[1]);
    await page.unroute("**/api/tag-catalog/operations");
    archived = await api("/api/tag-catalog");
    assert.equal(archived.catalog.topics.find((t) => t.id === term.id).active, false);
    assert.equal((await api(`/api/tag-catalog/history?dimension=topics&id=${term.id}`)).items.filter((i) => i.type === "archive").length, 1);
    await page.locator("#lib-detail-foot").getByRole("button", { name: "恢复", exact: true }).click();
    await wait(async () => (await api("/api/tag-catalog")).catalog.topics.find((t) => t.id === term.id).active);

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
    // Rename in settings; the workspace picks it up without a reload.
    await openSettingsPage(page, "tags");
    await openTag(page, "LoRA");
    await rename(page, "LoRA 微调");
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
    await leaveSettingsPage(page);
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
    await openSettingsPage(page, "tags", "topics:ai_coding");
    await page.locator("#td-name").waitFor();
    assert.equal(await page.locator("#td-name").inputValue(), existing.label);
    await rename(page, "AI 编程实践");
    await wait(async () => (await api("/api/tag-catalog")).catalog.topics.find((t) => t.id === "ai_coding").label === "AI 编程实践");
    await leaveSettingsPage(page);
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
    // 设置 · 合集规则: the first hit tag switches the rule on.
    await closeInspector(page);
    await openSettingsPage(page, "rules", collectionID);
    await page.locator(".rd-titles h4", { hasText: "AIGC 自动收录" }).waitFor();
    await page.locator(".rd").getByRole("button", { name: "添加标签", exact: true }).click();
    const pick = page.locator(".sp-pop");
    await pick.getByPlaceholder("搜索标签或别名").fill("LoRA");
    await pick.locator(".tf-chip", { hasText: "LoRA 微调" }).click();
    await pick.locator('.tf-chip[aria-pressed="true"]', { hasText: "LoRA 微调" }).waitFor();
    await pick.getByRole("button", { name: "完成", exact: true }).click();
    await pick.waitFor({ state: "detached" });
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
    // Phone: the settings page fits 390px and the AI switch writes immediately.
    await page.setViewportSize({ width: 390, height: 844 });
    await openSettingsPage(page, "tags", "topics:" + term.id);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    await page.locator("#sp-body").getByRole("switch", { name: "参与 AI 自动打标" }).click();
    await wait(async () => (await api("/api/tag-catalog")).catalog.topics.find((t) => t.id === term.id).ai_enabled === false);
    data = await api("/api/tag-catalog");
    assert.equal(
      data.catalog.topics.find((t) => t.id === term.id).ai_enabled,
      false,
    );
    assert.deepEqual(errors, []);
    process.stdout.write(
      "ok managed tags (settings page): real D1 directory, HTTP UUID, uncertain response replay (create + archive), rename hot reload, multi-tag rules, manual exclusion, phone layout\n",
    );
  } finally {
    await page.close();
  }
}

// Search the library, select the row, then rename on Enter.
async function openTag(page, label) {
  await page.locator("#lib-search").fill(label);
  await page.locator("#lib-list .lib-row").filter({ hasText: label }).first().click();
  await page.locator("#td-name").waitFor();
  assert.equal(await page.locator("#td-name").inputValue(), label);
}
async function rename(page, label) {
  const name = page.locator("#td-name");
  await name.fill(label);
  await name.press("Enter");
}
