// E4: with "建议需人工确认后生效" on, a fresh real classification leaves its AI
// tags pending (dashed) until the reader confirms them; dismissing one keeps it out.
import assert from "node:assert/strict";
import { openCuration, closeInspector } from "../browser/workspace-helper.mjs";

export async function verifyAiConfirm(browser, base, workerURL, internalToken) {
  const appToken = process.env.CAIRN_APP_TOKEN || "app";
  const page = await browser.newPage({ viewport: { width: 1204, height: 900 } }), errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const api = async (path, body) => {
    const r = await fetch(base + path, body ? { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) } : {});
    assert.equal(r.status, 200, await r.clone().text());
    return r.json();
  };
  const setPrefs = async (patch) => {
    const current = await api("/api/preferences");
    return api("/api/preferences/operations", { operation_key: crypto.randomUUID(), expected_revision: current.revision, patch });
  };
  const wait = async (what, check) => {
    const end = Date.now() + 60000;
    while (Date.now() < end) { if (await check()) return; await new Promise((r) => setTimeout(r, 150)); }
    throw Error("ai confirm timeout: " + what);
  };
  try {
    await setPrefs({ ai_confirm_before_apply: true });
    const url = "https://x.com/test/status/2095999999999999777";
    const created = await fetch(workerURL + "/api/links", { method: "POST",
      headers: { Authorization: "Bearer " + appToken, "Content-Type": "application/json" },
      body: JSON.stringify({ url, note: "确认后生效验证" }) });
    assert.equal(created.status, 201, await created.clone().text());
    const id = (await created.json()).id;
    const captured = await fetch(workerURL + "/api/captures", { method: "POST",
      headers: { Authorization: "Bearer " + appToken, "Content-Type": "application/json" },
      body: JSON.stringify({ url, note: "", client_id: crypto.randomUUID(), capture: { title: "确认后生效验证", language: "en", images: [],
        text: "BrowserEntity provides a practical guide to LoRA fine-tuning, evaluating large language models, methods, tools and data." } }) });
    assert.equal(captured.status, 201, await captured.clone().text());
    await wait("classification", async () => {
      const r = await fetch(workerURL + "/api/enrichment/classifications/" + id, { headers: { Authorization: "Bearer " + internalToken } });
      return r.status === 200 && (await r.json()).status === "completed";
    });
    let tags = await api("/api/bookmarks/" + id + "/tags");
    const pending = tags.state.ai_pending || [];
    assert.ok(pending.length >= 1, "AI suggestions are held for confirmation: " + JSON.stringify(tags.state.ai_pending));
    assert.ok(pending.every((p) => p.reason === "confirm"));
    for (const p of pending) assert.ok(!(tags.selection[p.field] || []).includes(p.term), "a held suggestion is not a tag yet");
    assert.ok(tags.automatic.topics.length >= 1, "the decision itself still records the AI output");

    await page.goto(base + "/bookmarks/" + id);
    await openCuration(page);
    const chips = page.locator("#tag-rows .tag-system-chip.pending");
    await chips.first().waitFor();
    assert.equal(await chips.count(), pending.length);
    await page.locator("#tag-rows .tag-pending-note").waitFor();
    if (process.env.CAIRN_E2E_SHOTS) {
      await page.waitForTimeout(300);
      await page.screenshot({ path: `${process.env.CAIRN_E2E_SHOTS}/13-ai-pending.png` });
    }
    const first = pending[0];
    await page.locator(`#tag-rows .tag-system-chip.pending[data-term="${first.term}"] .tag-name`).click();
    await wait("confirm", async () => {
      tags = await api("/api/bookmarks/" + id + "/tags");
      return (tags.selection[first.field] || []).includes(first.term) && !(tags.state.ai_pending || []).some((p) => p.term === first.term);
    });
    const value = tags.state.fields[first.field].values.find((v) => v.term === first.term);
    assert.equal(value.origin, "human");
    assert.equal(value.confirmed, true, "confirming a held suggestion records a confirmation, not a manual add");
    if (pending.length > 1) {
      const second = pending[1];
      await page.locator(`#tag-rows .tag-system-chip.pending[data-term="${second.term}"] .tag-remove`).click();
      await wait("dismiss", async () => {
        tags = await api("/api/bookmarks/" + id + "/tags");
        return !(tags.state.ai_pending || []).some((p) => p.term === second.term);
      });
      assert.ok(!(tags.selection[second.field] || []).includes(second.term), "a dismissed suggestion stays out");
    }
    await page.locator(`#tag-rows .tag-system-chip:not(.pending) .tag-name`, { hasText: /./ }).first().waitFor();
    await closeInspector(page);
    assert.deepEqual(errors, []);
    process.stdout.write(`ok   E4 confirm-before-apply: ${pending.length} held, confirmed and dismissed in the reader\n`);
  } finally {
    await setPrefs({ ai_suggestion_limit: 5, ai_confirm_before_apply: false, ai_low_confidence_review: false }).catch(() => {});
    await page.close();
  }
}
