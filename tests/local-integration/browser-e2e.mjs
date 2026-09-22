// Real browser end-to-end: real Go HTTP service (serve) + real Worker/D1/R2 +
// real Chrome. Only the two paid model boundaries are local mocks.
//
// Expects CAIRN_WORKER_URL and GO_BIN; the shell wrapper starts the Worker and
// builds the binary. It creates a normal bookmark, lets the real scheduler
// retrieve, read and classify it, then drives the actual dashboard page.
import { spawn } from "node:child_process";
import { chromium } from "playwright";
import { startMockModel } from "./mock-model.mjs";

const workerURL = process.env.CAIRN_WORKER_URL;
const goBin = process.env.GO_BIN;
const enricherToken = process.env.CAIRN_ENRICHER_TOKEN || "internal";
const appToken = process.env.CAIRN_APP_TOKEN || "app";
// Explicit current production policy: the personal-use guard changes semantics.
const policyVersion = "jev-policy-v3";
if (!workerURL || !goBin) {
  process.stderr.write("CAIRN_WORKER_URL and GO_BIN are required\n");
  process.exit(1);
}

let checks = 0;
let failures = 0;
function check(name, condition, detail = "") {
  checks++;
  if (condition) process.stdout.write(`ok   ${name}\n`);
  else { failures++; process.stdout.write(`FAIL ${name}${detail ? `: ${detail}` : ""}\n`); }
}

const auth = (token) => ({ Authorization: `Bearer ${token}`, "Content-Type": "application/json" });
async function jsonFetch(url, options = {}) {
  const response = await fetch(url, options);
  const text = await response.text();
  let payload = {};
  try { payload = text ? JSON.parse(text) : {}; } catch { payload = { raw: text }; }
  return { status: response.status, payload };
}
async function waitFor(description, predicate, timeoutMs = 120000) {
  const deadline = Date.now() + timeoutMs;
  let last;
  while (Date.now() < deadline) {
    try {
      last = await predicate();
      if (last) return last;
    } catch (error) { last = error; }
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  throw new Error(`timeout waiting for ${description}: ${last instanceof Error ? last.message : JSON.stringify(last)}`);
}

async function main() {
  const mock = await startMockModel();
  let browser;
  const goPort = 18080 + Math.floor(Math.random() * 1000);
  const go = spawn(goBin, ["serve"], {
    env: {
      ...process.env,
      HTTP_ADDR: `127.0.0.1:${goPort}`,
      CAIRN_API_BASE_URL: workerURL,
      CAIRN_ENRICHER_TOKEN: enricherToken,
      // The Grok client appends /responses, the TypeSafe client /v1/systemone.
      GROK_MODELS_BASE_URL: `${mock.url}/v1`,
      XAI_API_KEY: "local-mock",
      GROK_MODEL: "grok-mock",
      TYPESAFE_BASE_URL: mock.url,
      TYPESAFE_API_KEY: "local-mock",
      TYPESAFE_MODEL: "jev-1.13.0",
      POLL_INTERVAL: "2s",
      MAX_JOBS_PER_RUN: "5",
      CAIRN_EXTENSION_ENTITIES: "true",
      LOG_LEVEL: "warn"
    },
    stdio: ["ignore", "pipe", "pipe"]
  });
  let goLog = "";
  go.stdout.on("data", (chunk) => { goLog += chunk; });
  go.stderr.on("data", (chunk) => { goLog += chunk; });
  const teardown = async () => {
    go.kill("SIGTERM");
    await mock.close();
  };
  try {
    // 1. The real service registers its immutable, content-addressed spec on
    // startup. The id is derived from the semantics, so it is discovered rather
    // than assumed.
    const spec = await waitFor("the Go service to register its question spec", async () => {
      if (go.exitCode !== null) throw new Error(`go serve exited: ${goLog.slice(-2000)}`);
      const list = await jsonFetch(`${workerURL}/api/v2/question-specs`, { headers: auth(enricherToken) });
      const entry = (list.payload.specs ?? []).find((candidate) => String(candidate.spec_id).startsWith("classify-"));
      if (!entry) return null;
      const detail = await jsonFetch(`${workerURL}/api/v2/question-specs/${entry.spec_id}`, { headers: auth(enricherToken) });
      return detail.status === 200 ? detail.payload : null;
    }, 90000);
    check("the real Go service registered the compiled spec", typeof spec.spec_hash === "string" && spec.spec_hash.length === 64, JSON.stringify(spec).slice(0, 200));
    const taxonomyVersion = (spec.spec || spec.payload || {}).taxonomy_version;
    check("the spec carries the multidimensional taxonomy version", typeof taxonomyVersion === "string" && taxonomyVersion.length > 0);

    // 2. The operator activates the v2 target the consumer supports.
    const activated = await jsonFetch(`${workerURL}/api/enrichment/classifications/target`, {
      method: "POST", headers: auth(enricherToken),
      body: JSON.stringify({
        spec_id: spec.spec_id, spec_hash: spec.spec_hash, taxonomy_version: taxonomyVersion,
        policy_version: policyVersion, requested_model: "jev-1.13.0", protocol: "v2"
      })
    });
    check("v2 target activated", activated.status === 200, JSON.stringify(activated.payload));

    // 3. A normal new bookmark goes through the real production path.
    const created = await jsonFetch(`${workerURL}/api/links`, {
      method: "POST", headers: auth(appToken),
      body: JSON.stringify({ url: "https://x.com/local/status/42", note: "" })
    });
    check("new bookmark created", created.status === 200 || created.status === 201, JSON.stringify(created.payload));
    const id = created.payload.id;

    const run = await waitFor("a real v2 classification run", async () => {
      const result = await jsonFetch(`${workerURL}/api/v2/links/${id}/runs`, { headers: auth(enricherToken) });
      return result.status === 200 && result.payload.runs?.length ? result.payload.runs[0] : null;
    }, 180000);
    check("the scheduler produced a v2 run with the mock model", run.resolved_model === "jev-1.13.0", JSON.stringify(run).slice(0, 300));
    check("the provider usage was preserved", run.usage?.input_tokens === 111, JSON.stringify(run.usage));
    const job = await jsonFetch(`${workerURL}/api/enrichment/classifications/${id}`, { headers: auth(enricherToken) });
    check("the classification job completed against the target", job.payload.status === "completed", JSON.stringify(job.payload).slice(0, 200));

    const selection = await jsonFetch(`${workerURL}/api/v2/links/${id}/selection`, { headers: auth(enricherToken) });
    const topics = selection.payload.selection?.topics || [];
    check("the effective view is derived from the real decision", selection.payload.provenance?.source === "decision", JSON.stringify(selection.payload).slice(0, 300));
    check("the multidimensional topics are present", topics.length > 3, JSON.stringify(topics));

    // Rebuild two identical policy projections from the actual production run.
    // AI-only display values must never become legacy or human source data.
    for (let pass = 0; pass < 2; pass++) {
      const replay = await jsonFetch(`${workerURL}/api/v2/links/${id}/decisions`, {
        method: "POST", headers: auth(enricherToken), body: JSON.stringify({
          operation_key: `browser-ai-rebuild-${pass}`, run_ids: [run.id], policy_version: policyVersion,
          spec_id: spec.spec_id, requested_model: "jev-1.13.0", content_revision: run.content_revision,
          automatic: selection.payload.selection
        })
      });
      check(`AI-only projection rebuild ${pass + 1} succeeds`, replay.status === 200, JSON.stringify(replay.payload));
    }
    const aiOverrides = await jsonFetch(`${workerURL}/api/v2/links/${id}/overrides`, { headers: auth(enricherToken) });
    check("AI-only rebuilds create no human or legacy overrides", aiOverrides.payload.overrides?.length === 0);
    const aiLegacy = await jsonFetch(`${workerURL}/api/enrichment/jobs/${id}`, { headers: auth(enricherToken) });
    check("the old endpoint still reports AI-only as unreviewed", aiLegacy.payload.classification_reviewed === false);
    const aiView = await jsonFetch(`${workerURL}/api/v2/links/${id}/effective`, { headers: auth(enricherToken) });
    check("the current endpoint still reports AI-only as unreviewed", aiView.payload.effective?.reviewed === false);

    // 4. The real browser loads the real Go proxy over the real Worker.
    browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || undefined, args: ["--no-sandbox"] });
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    const pageErrors = [];
    page.on("pageerror", (error) => pageErrors.push(String(error)));
    await page.goto(`http://127.0.0.1:${goPort}/bookmarks/${id}`, { waitUntil: "load" });
    await page.waitForSelector("#v2-curation:not([hidden])", { timeout: 30000 });
    check("the v2 panel loads through the real proxy", await page.isVisible("#v2-curation"));
    const checked = await page.$$eval("#v2-topics input:checked", (nodes) => nodes.map((node) => node.value));
    check("the browser shows the same effective topics as the Worker", JSON.stringify(checked.sort()) === JSON.stringify([...topics].sort()), JSON.stringify({ checked, topics }));
    const folded = await page.textContent("#v2-folded-note");
    check("the fourth effective topic is folded, not deleted", /另外 [1-9]/.test(folded || ""), folded || "");

    // A blank-keyword library query must use the full effective dimensions,
    // even when the v1 summary omits the fourth topic. Nothing is intercepted.
    const library = await browser.newPage({ viewport: { width: 375, height: 812 } });
    library.on("pageerror", error => pageErrors.push(String(error)));
    library.on("response", async response => {
      if (response.url().includes("/api/bookmarks?") && response.status() !== 200) {
        process.stdout.write(`library HTTP ${response.status()}: ${await response.text()}\n`);
      }
    });
    await library.goto(`http://127.0.0.1:${goPort}/?topics=${encodeURIComponent(topics[3])}`, { waitUntil: "load" });
    await library.waitForSelector("#filter-topics:not([disabled])");
    await library.waitForSelector(`#stream a[href^='/bookmarks/${id}?']`).catch(async error => {
      process.stdout.write(`library diagnostic: ${JSON.stringify({
        errors: pageErrors, status: await library.locator("#load-error").innerText(),
        items: await library.locator("#stream").innerText(),
        direct: await jsonFetch(`http://127.0.0.1:${goPort}/api/bookmarks?topics=${encodeURIComponent(topics[3])}&view=summary&filter_contract_version=1`)
      })}\n`);
      throw error;
    });
    check("real blank-query library finds the fourth effective topic", await library.locator("#stream a.entry").count() === 1);
    const selectedTopics = await library.$eval("#filter-topics", node => [...node.selectedOptions].map(option => option.value));
    check("real library restores a saved multi-topic filter", JSON.stringify(selectedTopics) === JSON.stringify([topics[3]]));
    for (const dimension of ["content_functions", "carriers", "affordances"]) {
      const terms = selection.payload.selection[dimension];
      if (!terms?.length) throw new Error(`fixture has no ${dimension}`);
      await library.selectOption(`#filter-${dimension}`, terms[0]);
    }
    await library.selectOption("#filter-entity_state", "completed_nonempty");
    await library.waitForFunction(() => document.querySelector("#loading").hidden && document.querySelectorAll("#stream a.entry").length === 1);
    const filteredURL = new URL(library.url());
    const filtered = await jsonFetch(`http://127.0.0.1:${goPort}/api/bookmarks?${filteredURL.searchParams}&filter_contract_version=1`);
    check("real multidimensional query returns confirmed membership and filtered counts", filtered.status === 200 && filtered.payload.filter_contract_version === 1 && filtered.payload.counts?.total === 1 && filtered.payload.items?.[0]?.id === id, JSON.stringify(filtered.payload).slice(0, 200));
    check("real narrow library filters fit the viewport", await library.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth));
    const filteredExport = await fetch(`http://127.0.0.1:${goPort}/api/export?${filteredURL.searchParams}&filter_contract_version=1`).then(response => response.text());
    check("real filtered export carries the matching full dimensions", filteredExport.includes(`收藏 ID：${id}`) && filteredExport.includes(topics[3]) && filteredExport.includes("内容功能："));
    await library.goto(`http://127.0.0.1:${goPort}/?topics=${encodeURIComponent(checked[0])}`, { waitUntil: "load" });
    await library.waitForSelector(`#stream a[href^='/bookmarks/${id}?']`);

    // 5. A human reject reaches the real Worker and survives a refresh.
    const rejected = checked[0];
    await page.click("#v2-curation > summary");
    await page.waitForSelector(`#v2-topics input[value='${rejected}']`, { state: "visible" });
    await page.click(`#v2-topics input[value='${rejected}']`);
    await waitFor("the override to reach the real Worker", async () => {
      const overrides = await jsonFetch(`${workerURL}/api/v2/links/${id}/overrides`, { headers: auth(enricherToken) });
      return overrides.payload.overrides?.some((entry) => entry.field === "topics" && entry.action === "reject" && entry.term === rejected);
    }, 30000);
    check("the human reject is stored by the real Worker", true);
    const afterReject = await jsonFetch(`${workerURL}/api/v2/links/${id}/selection`, { headers: auth(enricherToken) });
    check("the effective view no longer contains the rejected topic", !(afterReject.payload.selection.topics || []).includes(rejected), JSON.stringify(afterReject.payload.selection.topics));
    await page.reload({ waitUntil: "load" });
    await page.waitForSelector("#v2-curation:not([hidden])", { timeout: 30000 });
    await page.click("#v2-curation > summary");
    const afterReload = await page.$eval(`#v2-topics input[value='${rejected}']`, (node) => node.checked);
    check("the refreshed UI shows the human decision", afterReload === false);
    await library.reload({ waitUntil: "load" });
    await library.waitForSelector("#empty:not([hidden])");
    check("real filtered library removes a confirmed human rejection", await library.locator("#stream a.entry").count() === 0 && !await library.isVisible("#load-error"));
    await library.waitForSelector("#filter-topics:not([disabled])");
    await library.selectOption("#filter-topics", [rejected, afterReject.payload.selection.topics[0]]);
    await library.waitForSelector(`#stream a[href^='/bookmarks/${id}?']`);
    check("real multi-topic OR includes the remaining accepted topic", await library.locator("#stream a.entry").count() === 1);
    await library.close();

    // 6. Re-selecting an earlier radio option must use action order, including
    // after the next request reconstructs the effective view from D1 rows.
    const carrierOptions = await page.$$eval("#v2-carriers input", (nodes) => nodes.map((node) => ({ value: node.value, checked: node.checked })));
    const firstCarrier = carrierOptions.find((option) => !option.checked)?.value;
    const secondCarrier = carrierOptions.find((option) => option.value !== firstCarrier)?.value;
    if (!firstCarrier || !secondCarrier) throw new Error("expected two carrier choices in the real UI");
    for (const [index, term] of [firstCarrier, secondCarrier, firstCarrier].entries()) {
      await page.click(`#v2-carriers input[value='${term}']`);
      await waitFor(`carrier choice ${index + 1} to persist`, async () => {
        const current = await jsonFetch(`${workerURL}/api/v2/links/${id}/selection`, { headers: auth(enricherToken) });
        return JSON.stringify(current.payload.selection?.carriers) === JSON.stringify([term]);
      }, 30000);
      check(`carrier choice ${index + 1} is the last selected value`, true);
    }
    await page.reload({ waitUntil: "load" });
    await page.waitForSelector("#v2-carriers input:checked", { state: "attached", timeout: 30000 });
    check("carrier A survives refresh after A-B-A", await page.$eval("#v2-carriers input:checked", (node) => node.value) === firstCarrier);
    const otherPage = await browser.newPage();
    await otherPage.goto(`http://127.0.0.1:${goPort}/bookmarks/${id}`, { waitUntil: "load" });
    await otherPage.waitForSelector("#v2-carriers input:checked", { state: "attached", timeout: 30000 });
    check("a second browser page reads the final carrier A", await otherPage.$eval("#v2-carriers input:checked", (node) => node.value) === firstCarrier);
    await otherPage.close();

    // Entity processing uses the actual opt-in extension and model client.
    await page.waitForFunction(() => document.querySelector("#v2-entity-list")?.textContent.includes("BrowserEntity"));
    check("the entity panel and reading header show the production entity", (await page.textContent("#read-entities")).includes("BrowserEntity"));
    await page.click("#v2-entities > summary");
    await page.locator(".v2-entity", { hasText: "BrowserEntity" }).getByRole("button", { name: "移除" }).click();
    await page.waitForFunction(() => !document.querySelector("#v2-entity-list")?.textContent.includes("BrowserEntity"));
    check("rejecting an entity also clears the reading header", !(await page.textContent("#read-entities")).includes("BrowserEntity"));
    const serverExport = await fetch(`http://127.0.0.1:${goPort}/api/export`).then(response => response.text());
    check("server export excludes the rejected entity", !serverExport.includes("实体：BrowserEntity"));
    const downloadPromise = page.waitForEvent("download");
    await page.click("#read-export");
    const download = await downloadPromise;
    let exported = "";
    for await (const chunk of await download.createReadStream()) exported += chunk;
    check("the real export button excludes the rejected entity", !exported.includes("实体：BrowserEntity"));
    const currentEntities = await jsonFetch(`http://127.0.0.1:${goPort}/api/bookmarks/${id}/entities`);
    const restoredEntities = await jsonFetch(`http://127.0.0.1:${goPort}/api/bookmarks/${id}/entities`, {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({
        operation_key: "browser-entity-reset", action: "reset", term: "BrowserEntity", expected_revision: currentEntities.payload.revision
      })
    });
    check("entity reset reaches the real Worker through Go", restoredEntities.status === 200 && restoredEntities.payload.entities?.includes("BrowserEntity"));
    await page.reload({ waitUntil: "load" });
    await page.waitForFunction(() => document.querySelector("#read-entities")?.textContent.includes("BrowserEntity"));
    check("a page reload restores the automatic entity after reset", (await page.textContent("#v2-entity-list")).includes("BrowserEntity"));
    const restoredExport = await fetch(`http://127.0.0.1:${goPort}/api/export`).then(response => response.text());
    check("server export includes the restored entity", restoredExport.includes("实体：BrowserEntity"));

    check("no page errors during the real browser session", pageErrors.length === 0, pageErrors.join("; "));
  } finally {
    await browser?.close();
    await teardown();
  }
  process.stdout.write(`\n${checks - failures}/${checks} real end-to-end checks passed\n`);
  process.exit(failures === 0 ? 0 : 1);
}

main().catch((error) => {
  process.stderr.write(`real end-to-end harness failed: ${error?.stack || error}\n`);
  process.exit(1);
});
