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
      TYPESAFE_MODEL: "jev-latest",
      POLL_INTERVAL: "2s",
      MAX_JOBS_PER_RUN: "5",
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
        policy_version: "jev-policy-v2", requested_model: "jev-latest", protocol: "v2"
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
    check("the scheduler produced a v2 run with the mock model", run.resolved_model === "jev-mock-1.0", JSON.stringify(run).slice(0, 300));
    check("the provider usage was preserved", run.usage?.input_tokens === 111, JSON.stringify(run.usage));
    const job = await jsonFetch(`${workerURL}/api/enrichment/classifications/${id}`, { headers: auth(enricherToken) });
    check("the classification job completed against the target", job.payload.status === "completed", JSON.stringify(job.payload).slice(0, 200));

    const selection = await jsonFetch(`${workerURL}/api/v2/links/${id}/selection`, { headers: auth(enricherToken) });
    const topics = selection.payload.selection?.topics || [];
    check("the effective view is derived from the real decision", selection.payload.provenance?.source === "decision", JSON.stringify(selection.payload).slice(0, 300));
    check("the multidimensional topics are present", topics.length > 3, JSON.stringify(topics));

    // 4. The real browser loads the real Go proxy over the real Worker.
    const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || undefined, args: ["--no-sandbox"] });
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

    check("no page errors during the real browser session", pageErrors.length === 0, pageErrors.join("; "));
    await browser.close();
  } finally {
    await teardown();
  }
  process.stdout.write(`\n${checks - failures}/${checks} real end-to-end checks passed\n`);
  process.exit(failures === 0 ? 0 : 1);
}

main().catch((error) => {
  process.stderr.write(`real end-to-end harness failed: ${error?.stack || error}\n`);
  process.exit(1);
});
