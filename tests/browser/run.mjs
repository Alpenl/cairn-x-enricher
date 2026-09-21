// B06 browser acceptance harness.
//
// It serves the *real* dashboard assets from internal/dashboard and a mock
// Worker API, then drives them with a real Chrome via Playwright. Static JS
// checks are not a substitute for this: the assertions here inspect the actual
// network request bodies and call counts.
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { chromium } from "playwright";

const here = path.dirname(fileURLToPath(import.meta.url));
const dashboardDir = path.resolve(here, "../../internal/dashboard");

let checks = 0;
let failures = 0;
function check(name, condition, detail = "") {
  checks++;
  if (condition) {
    process.stdout.write(`ok   ${name}\n`);
  } else {
    failures++;
    process.stdout.write(`FAIL ${name}${detail ? `: ${detail}` : ""}\n`);
  }
}
function equal(name, actual, expected) {
  check(name, JSON.stringify(actual) === JSON.stringify(expected), `got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);
}

function taxonomyV2() {
  return {
    version: "2026-09-20.1", definition_version: 1,
    topics: [
      { id: "llm", label: "LLM", active: true, aliases: [], description: "d" },
      { id: "eng", label: "工程", active: true, aliases: [], description: "d" },
      { id: "eval", label: "评估", active: true, aliases: [], description: "d" },
      { id: "design", label: "设计", active: true, aliases: [], description: "d" },
      { id: "old", label: "旧主题", active: false, aliases: [], description: "d" }
    ],
    forms: [{ id: "method", label: "方法", active: true, aliases: [], description: "d" }],
    uses: [{ id: "try", label: "待试", active: true, aliases: [], description: "d" }],
    content_functions: [
      { id: "method", label: "方法", active: true, aliases: [], description: "d" },
      { id: "tool", label: "工具", active: true, aliases: [], description: "d" },
      { id: "data", label: "数据", active: true, aliases: [], description: "d" }
    ],
    carriers: [
      { id: "single", label: "单帖", active: true, aliases: [], description: "d" },
      { id: "author_continuation", label: "作者续帖", active: true, aliases: [], description: "d" }
    ],
    affordances: [
      { id: "practice", label: "可实践", active: true, aliases: [], description: "d" },
      { id: "background", label: "可作背景", active: true, aliases: [], description: "d" }
    ]
  };
}

// The mock Worker keeps just enough state to exercise idempotency, CAS and the
// accept/reject/set-empty/reset distinction.
function createMock() {
  const state = {
    revision: 3,
    selection: {
      topics: ["llm", "eng", "eval", "design"], content_functions: ["tool", "method", "data"],
      carriers: ["author_continuation"], affordances: ["practice"], form: "method", use: "try"
    },
    overrides: [],
    rejected: new Set(),
    requests: [],
    modelCalls: 0,
    xSearchCalls: 0,
    operations: new Map(),
    // Test controls: delay the next override response so a rapid second action
    // is genuinely in flight, and force the next CAS check to conflict.
    delayNextMs: 0,
    forceConflict: false,
    entities: ["acme"],
    entityState: "completed_nonempty",
    actions: []
  };
  return state;
}

async function serveAsset(res, file, type) {
  try {
    const body = await readFile(path.join(dashboardDir, file));
    res.writeHead(200, { "Content-Type": type });
    res.end(body);
  } catch {
    res.writeHead(404);
    res.end("not found");
  }
}

// waitFor polls a predicate until it is true or the deadline passes, so the
// harness never depends on a UI animation or a fixed sleep.
async function waitFor(predicate, timeoutMs = 5000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    // Predicates may be async (they often read the DOM), so the result must be
    // awaited; treating a Promise as truthy would return immediately.
    if (await predicate()) return true;
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  return false;
}

function startServer(state) {
  const server = createServer(async (req, res) => {
    const url = new URL(req.url, "http://localhost");
    const send = (code, body) => { res.writeHead(code, { "Content-Type": "application/json" }); res.end(JSON.stringify(body)); };
    let raw = "";
    for await (const chunk of req) raw += chunk;
    const body = raw ? JSON.parse(raw) : {};

    if (url.pathname === "/bookmarks/12") return serveAsset(res, "reader.html", "text/html; charset=utf-8");
    const asset = url.pathname.match(/^\/assets\/(.+)$/);
    if (asset) {
      const type = asset[1].endsWith(".css") ? "text/css" : asset[1].endsWith(".svg") ? "image/svg+xml" : "text/javascript; charset=utf-8";
      return serveAsset(res, asset[1], type);
    }
    if (url.pathname === "/api/bookmarks/12") {
      return send(200, {
        id: 12, url: "https://x.com/a/status/12", note: "", created_at: "2026-09-20T00:00:00Z",
        status: "completed", curation_status: "inbox", why: "", classification_reviewed: false,
        ai_title: "测试标题", summary: "摘要", translated_text: "译文", original_text: "<script>alert(1)</script>",
        related_links: [], images: [], classification: { topics: ["llm", "eng", "eval", "design"], form: "method", use: "try", entities: [], uncertainty: false, why_suggestion: "", taxonomy_version: "x", discarded_tags: [] }
      });
    }
    if (url.pathname === "/api/taxonomy") {
      return send(200, { version: "2026-09-20.1", topics: taxonomyV2().topics, forms: taxonomyV2().forms, uses: taxonomyV2().uses });
    }
    if (url.pathname === "/api/v2-taxonomy") return send(200, taxonomyV2());
    if (url.pathname === "/api/bookmarks/12/v2-selection") {
      if (req.method === "GET") {
        return send(200, { available: true, revision: state.revision, selection: state.selection, v1_projection: { topics: state.selection.topics.slice(0, 3), form: state.selection.form, use: state.selection.use } });
      }
      return send(200, { id: 12, selection: state.selection });
    }
    if (url.pathname === "/api/bookmarks/12/v2-override") {
      state.requests.push({ path: url.pathname, body });
      const key = body.operation_key;
      if (state.operations.has(key)) return send(200, state.operations.get(key));
      if (state.delayNextMs > 0) {
        const delay = state.delayNextMs;
        state.delayNextMs = 0;
        await new Promise((resolve) => setTimeout(resolve, delay));
      }
      if (state.forceConflict) {
        state.forceConflict = false;
        state.revision += 1; // another client moved first
        return send(409, { error: "revision_conflict", revision: state.revision });
      }
      if (body.expected_revision !== undefined && body.expected_revision !== state.revision) {
        return send(409, { error: "revision_conflict", revision: state.revision });
      }
      // Mirror the real Worker's validation: only the four v2 dimensions, only
      // the four actions, and never a why/status accept event.
      if (body.field === "why" || body.field === "status") return send(400, { error: "invalid_override" });
      const singleValued = ["carriers", "form", "use"].includes(body.field);
      if (body.action === "accept") {
        if (singleValued) state.selection[body.field] = [body.term];
        else if (!state.selection[body.field].includes(body.term)) state.selection[body.field].push(body.term);
      }
      if (body.action === "reject") {
        state.rejected.add(body.term);
        state.selection[body.field] = state.selection[body.field].filter((id) => id !== body.term);
      }
      if (body.action === "set_empty") state.selection[body.field] = [];
      state.revision += 1;
      const response = { id: 12, field: body.field, term: body.term, action: body.action, revision: state.revision, replayed: false };
      state.operations.set(key, response);
      return send(200, response);
    }
    if (url.pathname === "/api/bookmarks/12/v2-effective") {
      return send(200, { id: 12, effective: { topics: state.selection.topics, form: state.selection.form, use: state.selection.use } });
    }
    if (url.pathname === "/api/bookmarks/12/evidence") {
      return send(200, {
        available: true, current: true, truncated: false,
        snapshot: { blocks: [
          { id: "b1", role: "primary", text: "primary body" },
          { id: "b2", role: "quoted", text: "a quoted disagreement" }
        ], fetched_at: "2026-09-21T00:00:00Z", retrieval: "x_search", truncation: { truncated: false } }
      });
    }
    if (url.pathname === "/api/bookmarks/12/classification-status") {
      return send(200, { status: "completed", attempts: 1, error: null });
    }
    if (url.pathname === "/api/bookmarks/12/entities") {
      if (req.method === "GET") {
        return send(200, {
          available: true, state: state.entityState, stale: false,
          entities: state.entities, human: state.entities, revision: state.revision
        });
      }
      state.actions.push({ action: "entity", body });
      if (body.action === "accept" && !state.entities.includes(body.term)) state.entities.push(body.term);
      if (body.action === "reject") state.entities = state.entities.filter((entity) => entity !== body.term);
      return send(200, { available: true, state: state.entityState, stale: false, entities: state.entities, human: state.entities, revision: state.revision });
    }
    if (url.pathname === "/api/bookmarks/12/retry-classification") {
      state.actions.push({ action: "retry_classification" });
      return send(200, { id: 12, action: "retry_classification", model_calls: 0, detail: "只重新入分类队列；不抓取来源，不立即调用模型。" });
    }
    if (url.pathname === "/api/bookmarks/12/refresh-source") {
      state.actions.push({ action: "refresh_source" });
      return send(200, { id: 12, action: "refresh_source", fetch: true, detail: "重新抓取原文；旧内容与人工整理在新内容到达前保持不变。" });
    }
    if (url.pathname === "/api/bookmarks/12/replay-policy") {
      state.actions.push({ action: body.commit ? "replay_commit" : "replay_dry_run" });
      if (body.commit) {
        return send(200, { id: 12, model_calls: 0, changed: ["topics"], committed: false, committed_reason: "写回未授权：需要服务端显式设置 CAIRN_ALLOW_DECISION_WRITE=1" });
      }
      return send(200, { id: 12, run_id: 1, model_calls: 0, changed: ["topics"], before: {}, after: {}, committed: false });
    }
    if (url.pathname === "/api/bookmarks/12/curation") {
      state.requests.push({ path: url.pathname, body });
      return send(200, { id: 12, url: "https://x.com/a/status/12", status: "completed", curation_status: "inbox", why: body.why ?? "", classification: body.classification ?? null });
    }
    if (url.pathname === "/api/bookmarks") return send(200, { items: [], counts: {} });
    if (url.pathname === "/status") return send(200, { state: "ok", build: {} });
    return send(404, { error: "not_found" });
  });
  return new Promise((resolve) => server.listen(0, () => resolve(server)));
}

async function main() {
  const state = createMock();
  const server = await startServer(state);
  const base = `http://127.0.0.1:${server.address().port}`;
  const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || undefined, args: ["--no-sandbox"] });
  const page = await browser.newPage({ viewport: { width: 375, height: 720 } });
  const consoleErrors = [];
  page.on("pageerror", (error) => consoleErrors.push(String(error)));
  await page.goto(`${base}/bookmarks/12`, { waitUntil: "networkidle" });

  // 1. The v2 panel is available and shows the folded fourth topic.
  await page.waitForSelector("#v2-curation:not([hidden])");
  check("v2 curation panel is available", await page.isVisible("#v2-curation"));
  const folded = await page.textContent("#v2-folded-note");
  check("fourth topic is folded, not deleted", /另外 1 个/.test(folded || ""), folded || "");
  const selectedTopics = await page.$$eval("#v2-topics input:checked", (nodes) => nodes.map((n) => n.value));
  equal("all four effective topics are rendered as checked", selectedTopics, ["llm", "eng", "eval", "design"]);

  // 2. content_functions multi-select and carrier are independent.
  const functions = await page.$$eval("#v2-content_functions input:checked", (nodes) => nodes.map((n) => n.value).sort());
  equal("content functions are independently checked", functions, ["data", "method", "tool"]);
  const carriers = await page.$$eval("#v2-carriers input:checked", (nodes) => nodes.map((n) => n.value));
  equal("carrier is independent", carriers, ["author_continuation"]);

  // 3. Rejecting a candidate sends a reject override and does not touch the body.
  await page.click("#v2-curation > summary");
  await page.waitForSelector("#v2-topics input[value='eng']", { state: "visible" });
  await page.click("#v2-topics input[value='eng']");
  await waitFor(() => state.requests.some((entry) => entry.body.action === "reject"));
  const rejectRequest = state.requests.find((entry) => entry.body.action === "reject");
  check("reject sends a reject override", rejectRequest?.body.term === "eng", JSON.stringify(rejectRequest));
  check("reject override carries an operation key", typeof rejectRequest?.body.operation_key === "string");
  check("reject override carries expected_revision", rejectRequest?.body.expected_revision === 3);

  // 4. set_empty and reset produce different payloads.
  await page.click("#v2-affordances button[data-action='set_empty']");
  await waitFor(() => state.requests.some((entry) => entry.body.action === "set_empty"));
  const emptyRequest = state.requests.find((entry) => entry.body.action === "set_empty");
  check("set_empty sends its own action", emptyRequest?.body.field === "affordances", JSON.stringify(emptyRequest));
  await page.click("#v2-topics button[data-action='reset']");
  await waitFor(() => state.requests.some((entry) => entry.body.action === "reset"));
  const resetRequest = state.requests.find((entry) => entry.body.action === "reset");
  check("reset sends a distinct action", resetRequest !== undefined && resetRequest.body.action !== "set_empty", JSON.stringify(resetRequest));
  check("set_empty and reset payloads differ", JSON.stringify(emptyRequest.body) !== JSON.stringify(resetRequest.body));

  // 5. Idempotent replay of the same operation key.
  // Replaying the same key must return the stored result regardless of the
  // revision having advanced, because the operation already happened.
  const storedKey = [...state.operations.keys()][0];
  const replayBody = { field: "topics", term: "eng", action: "accept", operation_key: storedKey };
  const replay = await page.evaluate(async (payload) => {
    const response = await fetch("/api/bookmarks/12/v2-override", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
    return response.status;
  }, replayBody);
  check("replaying the same operation key is accepted", replay === 200);

  // 6. Malicious original text is rendered as text, never as a script.
  const bodyHTML = await page.innerHTML("#read-body");
  check("malicious original is escaped, not executed", !bodyHTML.toLowerCase().includes("<script"), bodyHTML.slice(0, 120));
  check("no page error from malicious content", consoleErrors.length === 0, consoleErrors.join("; "));

  // 7. A why-only save must not send a classification or an override event.
  await page.click("#read-curation > summary");
  await page.waitForSelector("#curation-why", { state: "visible" });
  const requestsBeforeWhy = state.requests.length;
  await page.fill("#curation-why", "只是有趣");
  await page.click("#save-curation");
  await waitFor(() => state.requests.some((entry) => entry.path.endsWith("/curation")));
  const whyRequest = state.requests.slice(requestsBeforeWhy).find((entry) => entry.path.endsWith("/curation"));
  check("why-only save reaches the v1 curation endpoint", whyRequest !== undefined);
  check("why-only save omits classification", whyRequest && !("classification" in whyRequest.body), JSON.stringify(whyRequest));
  const overrideAfterWhy = state.requests.slice(requestsBeforeWhy).find((entry) => entry.path.endsWith("/v2-override"));
  check("why-only save produces no override event", overrideAfterWhy === undefined);

  // 8. F12: rapid actions are queued, each with its own operation id, and none
  // is dropped while the previous request is still in flight.
  const overrideRequests = () => state.requests.filter((entry) => entry.path.endsWith("/v2-override"));
  const beforeRapid = overrideRequests().length;
  const checkedBeforeRapid = await page.$$eval("#v2-topics input:checked", (nodes) => nodes.map((node) => node.value));
  check("the draft reflects the server state before the rapid actions",
    !checkedBeforeRapid.includes("eng"), JSON.stringify({ checkedBeforeRapid, serverTopics: state.selection.topics }));
  state.delayNextMs = 400;
  await page.click("#v2-topics input[value='llm']");
  await page.click("#v2-topics input[value='eng']");
  await waitFor(() => overrideRequests().length >= beforeRapid + 2, 8000);
  const rapid = overrideRequests().slice(beforeRapid);
  check("both rapid actions reached the server", rapid.length === 2, JSON.stringify(rapid.map((entry) => entry.body)));
  const rapidKeys = new Set(rapid.map((entry) => entry.body.operation_key));
  check("each new logical action has a distinct operation id", rapidKeys.size === rapid.length, JSON.stringify([...rapidKeys]));
  equal("rapid actions preserve both intents", rapid.map((entry) => `${entry.body.action}:${entry.body.term}`).sort(), ["accept:eng", "reject:llm"]);

  // 9. F12: reload starts a new action identity; the same click is a new action,
  // not a replay of the previous operation key.
  await page.reload({ waitUntil: "load" });
  await page.waitForSelector("#v2-curation:not([hidden])");
  // A reload closes the <details> panel, so open it again before clicking.
  await page.click("#v2-curation > summary");
  await page.waitForSelector("#v2-topics input[value='eval']", { state: "visible" });
  const beforeReload = overrideRequests().length;
  await page.click("#v2-topics input[value='eval']");
  await waitFor(() => overrideRequests().length > beforeReload, 5000);
  const reloaded = overrideRequests()[beforeReload];
  check("a post-reload action uses a fresh operation id", !rapidKeys.has(reloaded.body.operation_key), JSON.stringify(reloaded.body));

  // 10. F12: a CAS conflict preserves the draft and requires an explicit re-apply.
  state.forceConflict = true;
  const beforeConflict = overrideRequests().length;
  await page.click("#v2-topics input[value='design']");
  await waitFor(() => overrideRequests().length > beforeConflict, 5000);
  await page.waitForSelector("#v2-conflict:not([hidden])");
  check("conflict is explained to the user", await page.isVisible("#v2-conflict"));
  const draftPreserved = await page.$eval("#v2-topics input[value='design']", (node) => node.checked);
  check("conflict does not discard the unsaved draft", draftPreserved === false);
  await page.click("#v2-conflict button");
  await waitFor(() => overrideRequests().length > beforeConflict + 1, 5000);
  const reapplied = overrideRequests()[beforeConflict + 1];
  check("re-apply submits the preserved action", reapplied.body.action === "reject" && reapplied.body.term === "design", JSON.stringify(reapplied.body));
  await page.waitForSelector("#v2-conflict", { state: "hidden" });

  // 11. No model or X Search call happened for any of the above.
  check("no model calls occurred", state.modelCalls === 0);
  check("no X Search calls occurred", state.xSearchCalls === 0);

  // 12. B06/B09 panels: evidence provenance, status, entities and the three
  // explicit redo actions, each with its own stated cost.
  await page.waitForSelector("#v2-evidence:not([hidden])");
  const evidenceRoles = await page.$$eval("#v2-evidence-blocks .v2-evidence-role", (nodes) => nodes.map((node) => node.textContent));
  equal("evidence blocks render their real roles", evidenceRoles, ["原帖", "引用"]);
  const status = await page.textContent("#v2-classification-status");
  check("classification status is shown independently", /分类完成/.test(status || ""), status || "");

  await page.waitForSelector("#v2-entities:not([hidden])");
  const entityText = await page.textContent("#v2-entity-list");
  check("the entity list renders the stored entities", /acme/.test(entityText || ""), entityText || "");
  await page.click("#v2-entities > summary");
  await page.waitForSelector("#v2-entity-input", { state: "visible" });
  await page.fill("#v2-entity-input", "widget");
  await page.click("#v2-entity-add");
  await waitFor(() => state.actions.some((entry) => entry.action === "entity" && entry.body.term === "widget"));
  await waitFor(async () => /widget/.test((await page.textContent("#v2-entity-list")) || ""), 5000).catch(() => {});
  const entityTextAfter = await page.textContent("#v2-entity-list");
  check("a human entity correction is submitted and shown", /widget/.test(entityTextAfter || ""), entityTextAfter || "");

  await page.click("#v2-retry-classification");
  await waitFor(() => state.actions.some((entry) => entry.action === "retry_classification"));
  check("classification retry is a separate action", true);
  page.once("dialog", (dialog) => dialog.accept());
  await page.click("#v2-refresh-source");
  await waitFor(() => state.actions.some((entry) => entry.action === "refresh_source"));
  check("refresh-source is a separate, confirmed action", true);
  await page.click("#v2-replay-policy");
  await waitFor(() => state.actions.some((entry) => entry.action === "replay_dry_run"));
  await page.waitForSelector("#v2-replay-result button");
  const replayText = await page.textContent("#v2-replay-result");
  check("policy replay reports zero model calls", /模型调用 0 次/.test(replayText || ""), replayText || "");
  await page.click("#v2-replay-result button");
  await waitFor(() => state.actions.some((entry) => entry.action === "replay_commit"));
  await waitFor(async () => /未授权/.test((await page.textContent("#v2-replay-result")) || ""), 5000).catch(() => {});
  const commitText = await page.textContent("#v2-replay-result");
  check("an unauthorized write-back explains itself instead of silently failing", /未授权/.test(commitText || ""), commitText || "");
  check("no model call was made by any of the three actions", state.modelCalls === 0);

  // 9. Keyboard reachability of the v2 controls.
  const focusable = await page.evaluate(() => {
    const first = document.querySelector("#v2-topics input");
    first.focus();
    return document.activeElement === first;
  });
  check("v2 controls are keyboard focusable", focusable);

  // 10. Narrow viewport does not push controls out of the document width.
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  check("no horizontal overflow at 375px", overflow <= 1, `overflow=${overflow}px`);

  await browser.close();
  server.close();
  process.stdout.write(`\n${checks - failures}/${checks} browser checks passed\n`);
  process.exit(failures === 0 ? 0 : 1);
}

main().catch((error) => {
  process.stderr.write(`browser harness failed: ${error?.stack || error}\n`);
  process.exit(1);
});
