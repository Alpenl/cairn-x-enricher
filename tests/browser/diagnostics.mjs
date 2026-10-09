import assert from "node:assert/strict";
import { chromium } from "playwright";
import { createFixtureState, startFixtureServer } from "./fixture-server.mjs";

const fixture = createFixtureState();
const { server, url } = await startFixtureServer({ state: fixture });
const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || undefined, args: ["--no-sandbox"] });
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  let pages = 0, details = 0, qualities = 0;
  const answer = (route, json) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(json) });
  await page.route("**/api/bookmarks/*/runs**", (route) => {
    const path = new URL(route.request().url());
    if (/\/runs\/\d+$/.test(path.pathname)) {
      details++;
      return answer(route, { id: 22, answers: { text: "<img src=x onerror=alert(1)>" }, usage: { tokens: 3 } });
    }
    pages++;
    return answer(route, { runs: [{ id: path.searchParams.has("after_id") ? 21 : 22, status: "succeeded", created_at: "2026-10-01T01:00:00Z", resolved_model: "fixture", archived: true }], next_after_id: path.searchParams.has("after_id") ? null : 22 });
  });
  await page.route("**/api/tag-quality", (route) => {
    qualities++;
    return answer(route, { version: 1, coverage: { human_operations: 2, human_facts: 4, unknown_facts: 1 },
      terms: [{ label: "AI编程", term_id: "ai_coding", tag_ref: "topic:ai_coding", current_count: 10, additions: 1, rejections: 2, confirmations: 3 }],
      confusion_pairs: [], retrieval: { total_links: 45, dimensions: [{ dimension: "topics", tagged_links: 30, distinct_terms: 12, largest_term_count: 10 }] } });
  });
  await page.goto(url, { waitUntil: "networkidle" });
  assert.equal(pages, 0);
  assert.equal(details, 0);
  await page.evaluate(async()=>(await import("/assets/js/workspace.js")).showInspector("processing"));
  assert.equal(pages, 0);
  await page.locator("#inspector-tabs").getByRole("tab",{name:"记录",exact:true}).click();
  await page.locator(".run-record").waitFor();
  assert.equal(pages, 1);
  assert.equal(details, 0);
  await page.locator(".run-record > summary").click();
  await page.waitForFunction(() => document.querySelector(".run-record-body")?.textContent.includes("answers"));
  assert.equal(details, 1);
  assert.equal(await page.locator(".run-record-body img").count(), 0);
  await page.locator("#run-history-more").click();
  await page.waitForFunction(() => document.querySelectorAll(".run-record").length === 2);
  assert.equal(pages, 2);
  assert.equal(details, 1);
  assert.equal(await page.locator("#run-history-more").isHidden(), true);
  console.log("ok   history is nested/lazy, paginated, and fetches one safe detail only when opened");
  await page.goto(url + "/backstage", { waitUntil: "networkidle" });
  assert.equal(qualities, 0);
  await page.locator("#tag-quality > summary").click();
  await page.locator(".quality-table tbody tr").waitFor();
  assert.match(await page.locator("#tag-quality-report").innerText(), /10 \/ 45/);
  assert.equal(qualities, 1);
  await page.locator("#tag-quality-refresh").click();
  await page.waitForFunction(() => !document.querySelector("#tag-quality-refresh").disabled);
  assert.equal(qualities, 2);
  console.log("ok   quality report is collapsed until requested and separates recorded feedback from accuracy");
} finally {
  await browser.close();
  server.closeAllConnections();
  await new Promise((resolve) => server.close(resolve));
}
