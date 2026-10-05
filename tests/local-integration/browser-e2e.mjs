// Real browser end-to-end: real Go HTTP service (serve) + real Worker/D1/R2 +
// real Chrome. Only the two paid model boundaries are local mocks.
//
// Expects CAIRN_WORKER_URL and GO_BIN; the shell wrapper starts the Worker and
// builds the binary. It creates a normal bookmark, lets the real scheduler
// retrieve, read and classify it, then drives the actual dashboard page.
import { verifyCollections } from "./collections.mjs";
import { verifyLocalFilters } from "./local-filters.mjs";
import { spawn } from "node:child_process";
import { chromium } from "playwright";
import { startMockModel } from "./mock-model.mjs";

const workerURL = process.env.CAIRN_WORKER_URL;
const goBin = process.env.GO_BIN;
const enricherToken = process.env.CAIRN_ENRICHER_TOKEN || "internal";
const appToken = process.env.CAIRN_APP_TOKEN || "app";
// Match the current production policy; a stale target must not be consumed.
const policyVersion = "jev-policy-v5";
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

const auth = (token) => ({ Authorization: `Bearer ${token}`, "Content-Type": "application/json", "X-Cairn-Tag-System": "1", "X-Cairn-Content-Functions": "1" });
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

async function openTagEditor(page, { secondary = false } = {}) {
  // Reading supplies the compact summary first; the editor fetches only when
  // its disclosure opens. Click its label, clear of the tag-filter buttons.
  await page.locator("#curate-summary-tags .tag").first().waitFor({ state: "visible", timeout: 30000 });
  if (!await page.locator("#curate").evaluate(node => node.open)) {
    await page.locator("#curate > summary .curate-summary-label").click();
  }
  await page.locator('.tag-system-row[data-dimension="topics"]').waitFor({ state: "visible", timeout: 30000 });
  if (secondary && !await page.locator(".tag-secondary").evaluate(node => node.open)) {
    await page.locator(".tag-secondary > summary").click();
  }
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
    const classificationRequests = mock.requests.filter(request => request.path === "/v1/systemone" &&
      !Object.hasOwn(request.body.questions || {}, "entity_0"));
    const expectedQuestions = (spec.spec || spec.payload).questions.map(question => question.id).sort();
    const askedQuestions = classificationRequests.flatMap(request => Object.keys(request.body.questions)).sort();
    check("bounded provider batches cover the entire compiled catalog exactly once",
      classificationRequests.every(request => Object.keys(request.body.questions).length <= 32) &&
      JSON.stringify(askedQuestions) === JSON.stringify(expectedQuestions));
    check("the provider usage was preserved", run.usage?.input_tokens === 111 * classificationRequests.length, JSON.stringify(run.usage));
    const job = await jsonFetch(`${workerURL}/api/enrichment/classifications/${id}`, { headers: auth(enricherToken) });
    check("the classification job completed against the target", job.payload.status === "completed", JSON.stringify(job.payload).slice(0, 200));

    let selection = await jsonFetch(`${workerURL}/api/v2/links/${id}/selection`, { headers: auth(enricherToken) });
    check("the effective view is derived from the real decision", selection.payload.provenance?.source === "decision", JSON.stringify(selection.payload).slice(0, 300));
    const automaticCount = ["topics", "resource_kinds", "content_functions"].reduce((count, key) => count + (selection.payload.selection?.[key]?.length || 0), 0);
    check("the current policy keeps two to five supported automatic tags", automaticCount >= 2 && automaticCount <= 5, String(automaticCount));

    // Source checkpoints can be classified before reading finishes. Wait for
    // the final source and its matching run before replaying its policy; a
    // revision change during this setup is legitimate, not a replay failure.
    await waitFor("source and reading completion", async () => {
      const source = await jsonFetch(`${workerURL}/api/enrichment/jobs/${id}`, { headers: auth(enricherToken) });
      return source.payload.status === "completed";
    });
    const replayRun = await waitFor("classification of the completed source", async () => {
      const latestSelection = await jsonFetch(`${workerURL}/api/v2/links/${id}/selection?include_state=1`, { headers: auth(enricherToken) });
      const latestRuns = await jsonFetch(`${workerURL}/api/v2/links/${id}/runs`, { headers: auth(enricherToken) });
      const matching = latestRuns.payload.runs?.find(candidate => candidate.content_revision === latestSelection.payload.state?.content_revision);
      if (matching) { selection = latestSelection; return matching; }
      return null;
    });

    // Rebuild two identical policy projections from the actual production run.
    // AI-only display values must never become legacy or human source data.
    for (let pass = 0; pass < 2; pass++) {
      const replay = await jsonFetch(`${workerURL}/api/v2/links/${id}/decisions`, {
        method: "POST", headers: auth(enricherToken), body: JSON.stringify({
          operation_key: `browser-ai-rebuild-${pass}`, run_ids: [replayRun.id], policy_version: policyVersion,
          spec_id: spec.spec_id, requested_model: "jev-1.13.0", content_revision: replayRun.content_revision,
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

    // Explicit human additions exercise full membership beyond the small
    // automatic budget and the legacy three-topic summary. They are fixtures,
    // not simulated model output or evidence of model quality.
    const catalog = (await jsonFetch(`http://127.0.0.1:${goPort}/api/v2-taxonomy`)).payload;
    const addedTopics = catalog.topics.filter(term => term.active !== false).slice(0, 4);
    const labelFor = id => catalog.topics.find(term => term.id === id)?.label || id;
    for (const [field, term] of [...addedTopics.map(term => ["topics", term.id]), ["content_functions", "method"]]) {
      const current = await jsonFetch(`${workerURL}/api/v2/links/${id}/selection`, { headers: auth(enricherToken) });
      const added = await jsonFetch(`${workerURL}/api/v2/links/${id}/overrides`, { method: "POST", headers: auth(enricherToken),
        body: JSON.stringify({ field, term, action: "accept", operation_key: `browser-human-${field}-${term}`, expected_revision: current.payload.revision }) });
      if (added.status !== 200) throw new Error(`human fixture rejected: ${JSON.stringify(added.payload)}`);
    }
    selection = await jsonFetch(`${workerURL}/api/v2/links/${id}/selection`, { headers: auth(enricherToken) });
    const topics = selection.payload.selection.topics;
    check("explicit human labels survive beyond the automatic display budget", topics.length >= 4);

    // 4. The real browser loads the real Go proxy over the real Worker.
    browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || undefined, args: ["--no-sandbox"] });
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, acceptDownloads: true });
    const pageErrors = [];
    page.on("pageerror", (error) => pageErrors.push(String(error)));
    await page.goto(`http://127.0.0.1:${goPort}/bookmarks/${id}`, { waitUntil: "load" });
    await page.waitForSelector("#curate-summary-tags .tag", { state: "visible", timeout: 30000 });
    check("collapsed editor has no editor rows before opening", await page.locator(".tag-system-row").count() === 0);
    check("the real tag editor starts collapsed", !await page.locator("#curate").evaluate(node => node.open));
    // Exercise actual Worker projections through NAS and the real list merge.
    // A synthetic summary that retained identity unconditionally hid this bug.
    await page.evaluate(async () => { window.cairnTestStore = await import("/assets/js/store.js"); });
    const loadedBody = await page.waitForFunction(id => {
      const item = window.cairnTestStore.getItem(id);
      return document.querySelector("#list-pane")?.dataset.loading === "false" &&
        document.querySelector("#body-loading")?.hidden && Boolean(item?.original_text) &&
        item.content_loaded !== false ? structuredClone(item) : false;
    }, id);
    const beforeBody = await loadedBody.jsonValue();
    await loadedBody.dispose();
    const bodyReuse = await page.evaluate(async ({ id, before }) => {
      const { getItem } = await import("/assets/js/store.js");
      const { api, fetchJSON } = await import("/assets/js/api.js");
      const legacy = await fetchJSON("/api/bookmarks?view=summary");
      const modern = await api.list(new URLSearchParams("view=summary"));
      await (await import("/assets/js/list.js")).reload();
      const merged = getItem(id);
      const refreshed = await api.detailFresh(id);
      return { legacyIdentity: Boolean(legacy.items.find(item => item.id === id)?.cache_identity),
        beforeIdentity: before.cache_identity, mergedIdentity: merged.cache_identity, refreshedIdentity: refreshed.cache_identity,
        bodyLengths: [before, merged, refreshed].map(item => [item.original_text?.length, item.translated_text?.length, item.content_loaded, item.enriched_at, item.status]),
        modernIdentity: modern.items.find(item => item.id === id)?.cache_identity,
        preserved: Boolean(before.original_text) && merged.content_loaded !== false && merged.original_text === before.original_text,
        refreshed: refreshed.original_text === before.original_text && refreshed.translated_text === before.translated_text };
    }, { id, before: beforeBody });
    check("default summaries preserve the previous strict contract", !bodyReuse.legacyIdentity);
    check("Web summaries explicitly receive valid body identity", bodyReuse.modernIdentity?.schema_version === 1 && Number.isSafeInteger(bodyReuse.modernIdentity.body_revision));
    check("a real list reload preserves the loaded body and its versioned refresh", bodyReuse.preserved && bodyReuse.refreshed, JSON.stringify(bodyReuse));
    // The summary also contains independent tag-filter buttons. Its center
    // can hit one of those; click the disclosure label to open the editor.
    await page.locator("#curate > summary .curate-summary-label").click();
    await page.locator('.tag-system-row[data-dimension="topics"]').waitFor({ state: "visible" });
    check("the modern editor loads through the real proxy", await page.isVisible('.tag-system-row[data-dimension="topics"]'));
    const effectiveTopics = async () => (await page.locator('.tag-system-row[data-dimension="topics"] .tag-name').allTextContents()).map(label => catalog.topics.find(term => term.label === label)?.id || label);
    const checked = await effectiveTopics();
    check("the browser shows the same effective topics as the Worker", JSON.stringify([...checked].sort()) === JSON.stringify([...topics].sort()), JSON.stringify({ checked, topics }));
    check("the editor retains every topic while the compact summary stays bounded", await page.locator(".curate-summary-tag").count() <= 5 && checked.length >= 4);

    // A blank-keyword library query must use the full effective dimensions,
    // even when the v1 summary omits the fourth topic. Nothing is intercepted.
    const library = await browser.newPage({ viewport: { width: 375, height: 812 } });
    library.on("pageerror", error => pageErrors.push(String(error)));
    library.on("response", async response => {
      if (response.url().includes("/api/bookmarks?") && response.status() !== 200) {
        process.stdout.write(`library HTTP ${response.status()}: ${await response.text()}\n`);
      }
    });
    const libraryIdle = () => library.waitForFunction(() => document.querySelector("#list-pane")?.dataset.loading === "false");
    // Facets live in the drawer on a phone; open it and the group, then toggle.
    const toggleFacet = async (key, value) => {
      if (!await library.evaluate(() => document.getElementById("app").classList.contains("sidebar-open"))) {
        await library.click("#list-pane [data-open-sidebar]");
      }
      if (["carriers", "affordances", "entity_state"].includes(key)) {
        const more = library.locator("details[data-group='more']");
        if (!await more.evaluate(node => node.open)) await more.locator(":scope > summary").click();
      }
      const group = library.locator(`details[data-group='${key}']`);
      if (!await group.evaluate((node) => node.open)) await group.locator("summary").click();
      await library.click(`[data-facet='${key}'][data-value='${value}']`);
      await libraryIdle();
    };
    await library.goto(`http://127.0.0.1:${goPort}/?topics=${encodeURIComponent(topics[3])}`, { waitUntil: "load" });
    await library.waitForSelector(`[data-facet='topics'][data-value='${topics[3]}']`, { state: "attached" });
    await library.waitForSelector(`#rows a.row-main[href^='/bookmarks/${id}?']`).catch(async error => {
      process.stdout.write(`library diagnostic: ${JSON.stringify({
        errors: pageErrors, status: await library.locator("#list-notice").innerText(),
        items: await library.locator("#rows").innerText(),
        direct: await jsonFetch(`http://127.0.0.1:${goPort}/api/bookmarks?topics=${encodeURIComponent(topics[3])}&view=summary&filter_contract_version=1`)
      })}\n`);
      throw error;
    });
    check("real blank-query library finds the fourth effective topic", await library.locator("#rows li.row").count() === 1);
    const selectedTopics = await library.$$eval("[data-facet='topics'][aria-pressed='true']", nodes => nodes.map(node => node.dataset.value));
    check("real library restores a saved multi-topic filter", JSON.stringify(selectedTopics) === JSON.stringify([topics[3]]));
    for (const dimension of ["content_functions", "carriers", "affordances"]) {
      const terms = selection.payload.selection[dimension];
      if (!terms?.length) throw new Error(`fixture has no ${dimension}`);
      await toggleFacet(dimension, terms[0]);
    }
    await toggleFacet("entity_state", "completed_nonempty");
    await library.waitForFunction(() => document.querySelector("#list-pane").dataset.loading === "false" && document.querySelectorAll("#rows li.row").length === 1);
    const filteredURL = new URL(library.url());
    const filtered = await jsonFetch(`http://127.0.0.1:${goPort}/api/bookmarks?${filteredURL.searchParams}&filter_contract_version=1`);
    check("real multidimensional query returns confirmed membership and filtered counts", filtered.status === 200 && filtered.payload.filter_contract_version === 1 && filtered.payload.counts?.total === 1 && filtered.payload.items?.[0]?.id === id, JSON.stringify(filtered.payload).slice(0, 200));
    check("real narrow library filters fit the viewport", await library.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth));
    const filteredExport = await fetch(`http://127.0.0.1:${goPort}/api/export?${filteredURL.searchParams}&filter_contract_version=1`).then(response => response.text());
    check("real filtered export carries the matching full dimensions", filteredExport.includes(`收藏 ID：${id}`) && filteredExport.includes(topics[3]) && filteredExport.includes("内容功能："));
    await library.goto(`http://127.0.0.1:${goPort}/?topics=${encodeURIComponent(checked[0])}`, { waitUntil: "load" });
    await library.waitForSelector(`#rows a.row-main[href^='/bookmarks/${id}?']`);

    // 5. A human reject reaches the real Worker and survives a refresh.
    const rejected = checked[0];
    await page.locator('.tag-system-row[data-dimension="topics"]').getByRole("button", { name: `移除${labelFor(rejected)}`, exact: true }).click();
    await waitFor("the override to reach the real Worker", async () => {
      const overrides = await jsonFetch(`${workerURL}/api/v2/links/${id}/overrides`, { headers: auth(enricherToken) });
      return overrides.payload.overrides?.some((entry) => entry.field === "topics" && entry.action === "reject" && entry.term === rejected);
    }, 30000);
    check("the human reject is stored by the real Worker", true);
    const afterReject = await jsonFetch(`${workerURL}/api/v2/links/${id}/selection`, { headers: auth(enricherToken) });
    check("the effective view no longer contains the rejected topic", !(afterReject.payload.selection.topics || []).includes(rejected), JSON.stringify(afterReject.payload.selection.topics));
    await page.reload({ waitUntil: "load" });
    await openTagEditor(page);
    await page.waitForSelector('.tag-system-row[data-dimension="topics"] .tag-name', { state: "visible", timeout: 30000 });
    check("the refreshed UI shows the human decision", !(await effectiveTopics()).includes(rejected));
    await library.reload({ waitUntil: "load" });
    await library.waitForSelector("#empty:not([hidden])");
    check("real filtered library removes a confirmed human rejection", await library.locator("#rows li.row").count() === 0 && !await library.isVisible("#list-notice"));
    await library.waitForSelector(`[data-facet='topics'][data-value='${rejected}']`, { state: "attached" });
    await toggleFacet("topics", afterReject.payload.selection.topics[0]);
    await library.waitForSelector(`#rows a.row-main[href^='/bookmarks/${id}?']`);
    check("real multi-topic OR includes the remaining accepted topic", await library.locator("#rows li.row").count() === 1);
    await library.close();

    // 6. Re-selecting an earlier single-valued option must use action order,
    // including after the next request reconstructs the view from D1 rows.
    if (!await page.locator("#curate").evaluate(node => node.open)) await page.locator("#curate > summary .curate-summary-label").click();
    if (!await page.locator(".tag-secondary").evaluate(node => node.open)) await page.locator(".tag-secondary > summary").click();
    await page.click("#v2-carriers [data-edit='carriers']");
    const carrierOptions = await page.$$eval("#v2-carriers [data-field='carriers'][data-term]:not([data-term=''])", (nodes) => nodes.map((node) => ({ value: node.dataset.term, checked: node.classList.contains("on") })));
    const firstCarrier = carrierOptions.find((option) => !option.checked)?.value;
    const secondCarrier = carrierOptions.find((option) => option.value !== firstCarrier && !option.checked)?.value
      || carrierOptions.find((option) => option.value !== firstCarrier)?.value;
    if (!firstCarrier || !secondCarrier) throw new Error(`expected two carrier choices in the real UI: ${JSON.stringify(carrierOptions)}`);
    for (const [index, term] of [firstCarrier, secondCarrier, firstCarrier].entries()) {
      await page.click(`#v2-carriers [data-field='carriers'][data-term='${term}'][data-action='accept']`);
      await waitFor(`carrier choice ${index + 1} to persist`, async () => {
        const current = await jsonFetch(`${workerURL}/api/v2/links/${id}/selection`, { headers: auth(enricherToken) });
        return JSON.stringify(current.payload.selection?.carriers) === JSON.stringify([term]);
      }, 30000);
      await page.waitForFunction(() => document.querySelector("#curate")?.getAttribute("aria-busy") !== "true");
      check(`carrier choice ${index + 1} is the last selected value`, true);
    }
    await page.reload({ waitUntil: "load" });
    await openTagEditor(page, { secondary: true });
    await page.waitForSelector("#v2-carriers .chip.on", { state: "visible", timeout: 30000 });
    check("carrier A survives refresh after A-B-A", await page.$eval("#v2-carriers .chip.on", (node) => node.dataset.term) === firstCarrier);
    const otherPage = await browser.newPage();
    otherPage.on("pageerror", error => pageErrors.push(String(error)));
    await otherPage.goto(`http://127.0.0.1:${goPort}/bookmarks/${id}`, { waitUntil: "load" });
    await openTagEditor(otherPage, { secondary: true });
    await otherPage.waitForSelector("#v2-carriers .chip.on", { state: "visible", timeout: 30000 }).catch(async error => {
      process.stdout.write(`second page carrier diagnostic: ${JSON.stringify({ errors: pageErrors,
        url: otherPage.url(), tags: await otherPage.locator("#tag-rows").innerHTML(),
        detail: await otherPage.locator("#detail-pane").innerText(),
        selection: await jsonFetch(`http://127.0.0.1:${goPort}/api/bookmarks/${id}/v2-selection`),
        reading: (await jsonFetch(`http://127.0.0.1:${goPort}/api/bookmarks/${id}/reading`)).payload.selection
      })}\n`);
      throw error;
    });
    check("a second browser page reads the final carrier A", await otherPage.$eval("#v2-carriers .chip.on", (node) => node.dataset.term) === firstCarrier);
    await otherPage.close();

    // Entity processing uses the actual opt-in extension and model client.
    if (!await page.locator("#curate").evaluate(node => node.open)) await page.locator("#curate > summary .curate-summary-label").click();
    if (!await page.locator(".tag-secondary").evaluate(node => node.open)) await page.locator(".tag-secondary > summary").click();
    await page.waitForFunction(() => document.querySelector("#v2-entity-list")?.textContent.includes("BrowserEntity"));
    check("the entity row shows the production entity", (await page.textContent("#v2-entity-list")).includes("BrowserEntity"));
    await page.click("#diagnostics > summary");
    await page.waitForSelector("#v2-entity-observations li");
    check("real entity observations expose source occurrences and explicit unknown identity", (await page.textContent("#v2-entity-observations")).includes("原文「BrowserEntity」") && (await page.textContent("#v2-entity-observations")).includes("身份未确认"));
    await page.locator(".v2-entity", { hasText: "BrowserEntity" }).getByRole("button", { name: "移除" }).click();
    await page.waitForFunction(() => !document.querySelector("#v2-entity-list")?.textContent.includes("BrowserEntity"));
    check("rejecting an entity removes it from the entity row", !(await page.textContent("#v2-entity-list")).includes("BrowserEntity"));
    await page.waitForFunction(() => [...document.querySelectorAll("#v2-entity-observations li")].some((node) => node.textContent.includes("非当前有效结果")));
    check("rejected entity provenance remains an explicit historical judgment", (await page.locator("#v2-entity-observations li", { hasText: "BrowserEntity" }).first().textContent()).includes("非当前有效结果"));
    const serverExport = await fetch(`http://127.0.0.1:${goPort}/api/export`).then(response => response.text());
    check("server export excludes the rejected entity", !serverExport.includes("实体：BrowserEntity"));
    const downloadPromise = page.waitForEvent("download");
    await page.click("#detail-export");
    const download = await downloadPromise;
    let exported = "";
    for await (const chunk of await download.createReadStream()) exported += chunk;
    check("the real export button excludes the rejected entity", exported.startsWith("# Cairn 收藏摘录") && !exported.includes("实体：BrowserEntity"));
    const currentEntities = await jsonFetch(`http://127.0.0.1:${goPort}/api/bookmarks/${id}/entities`);
    const restoredEntities = await jsonFetch(`http://127.0.0.1:${goPort}/api/bookmarks/${id}/entities`, {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({
        operation_key: "browser-entity-reset", action: "reset", term: "BrowserEntity", expected_revision: currentEntities.payload.revision
      })
    });
    check("entity reset reaches the real Worker through Go", restoredEntities.status === 200 && restoredEntities.payload.entities?.includes("BrowserEntity"));
    await page.reload({ waitUntil: "load" });
    await openTagEditor(page, { secondary: true });
    await page.waitForFunction(() => document.querySelector("#v2-entity-list")?.textContent.includes("BrowserEntity"));
    check("a page reload restores the automatic entity after reset", (await page.textContent("#v2-entity-list")).includes("BrowserEntity"));
    const restoredExport = await fetch(`http://127.0.0.1:${goPort}/api/export`).then(response => response.text());
    check("server export includes the restored entity", restoredExport.includes("实体：BrowserEntity"));

    // 7. Navigation counts come from the real Worker through the Go overview.
    const overview = await jsonFetch(`http://127.0.0.1:${goPort}/api/overview`);
    check("the real overview reports every navigation view", overview.status === 200 && ["all", "inbox", "kept", "compiled", "drop", "uncertain"].every((view) => Number.isInteger(overview.payload.views?.[view])), JSON.stringify(overview.payload));
    check("the new bookmark is counted in the inbox", overview.payload.views?.inbox >= 1 && overview.payload.views?.all >= 1);

    await verifyLocalFilters(`http://127.0.0.1:${goPort}`);
    await verifyCollections(browser, `http://127.0.0.1:${goPort}`, id);

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
