// Browser acceptance harness for the dashboard application.
//
// It serves the *real* embedded assets from internal/dashboard/web with mock
// APIs and drives them with a real Chrome via Playwright. Static JS checks are
// not a substitute: the assertions here inspect actual network request bodies
// and call counts. No paid or network call is made.
//
//   Part A: field-level curation invariants against a focused mock that can
//           delay responses and force CAS conflicts.
//   Part B: library triage flows (keyboard, auto-advance, undo, batch, search,
//           phone layout) against the realistic fixture server.
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { chromium } from "playwright";
import { createFixtureState, startFixtureServer } from "./fixture-server.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const webDir = path.resolve(here, "../../internal/dashboard/web");

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

// waitFor polls a predicate until it is true or the deadline passes, so the
// harness never depends on an animation or a fixed sleep.
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

async function openCuration(page) {
  await page.locator("#curate > summary").waitFor();
  if (!await page.locator("#curate").evaluate((node) => node.open)) await page.locator("#curate > summary .curate-summary-label").click();
}

// --- Part A: focused curation mock ------------------------------------------------

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
  return {
    revision: 3,
    selection: {
      topics: ["llm", "eng", "eval", "design"], content_functions: ["tool", "method", "data"],
      carriers: ["author_continuation"], affordances: ["practice"], form: "method", use: "try"
    },
    curation: { why: "", status: "inbox", reviewed: false },
    requests: [],
    modelCalls: 0,
    xSearchCalls: 0,
    operations: new Map(),
    curationOperations: new Map(),
    // Test controls: delay the next override response so a rapid second action
    // is genuinely in flight, and force the next CAS check to conflict.
    delayNextMs: 0,
    forceConflict: false,
    entities: ["acme"],
    entityState: "completed_nonempty",
    actions: [],
    listQueries: [],
    oldFilterBackend: false,
    identityRevision: 1,
    identityReads: 0,
    detailReads: 0,
    combinedReading: false,
    holdReading: false,
    releaseReading: null,
    readingReads: 0,
    readingBodyOmissions: 0,
    selectionReads: 0,
    entityReads: 0,
    remoteTitle: null,
    holdWhy: false,
    releaseWhy: null,
    failNextCuration: false,
    loseNextCurationResponse: false,
    delayNextCurationMs: 0
  };
}

const TYPES = { ".css": "text/css", ".svg": "image/svg+xml", ".js": "text/javascript; charset=utf-8", ".html": "text/html; charset=utf-8" };
async function serveFile(res, file) {
  try {
    const body = await readFile(file);
    res.writeHead(200, { "Content-Type": TYPES[path.extname(file)] || "application/octet-stream" });
    res.end(body);
  } catch {
    res.writeHead(404);
    res.end("not found");
  }
}

const BOOKMARK = {
  id: 12, url: "https://x.com/a/status/12", note: "", created_at: "2026-09-20T00:00:00Z",
  status: "completed", processable: true, curation_status: "inbox", why: "", classification_reviewed: false,
  paid_call_unresolved: false,
  ai_title: "测试标题", summary: "摘要", translated_text: "译文", original_text: "<script>alert(1)</script><img src=x onerror=alert(2)>",
  related_links: [], images: [], classification: { topics: ["llm", "eng", "eval"], form: "method", use: "try", entities: [], uncertainty: false, why_suggestion: "", taxonomy_version: "x", discarded_tags: [] }
};

function startMockServer(state) {
  const server = createServer(async (req, res) => {
    const url = new URL(req.url, "http://localhost");
    const send = (code, body) => { res.writeHead(code, { "Content-Type": "application/json" }); res.end(JSON.stringify(body)); };
    let raw = "";
    for await (const chunk of req) raw += chunk;
    const body = raw ? JSON.parse(raw) : {};

    if (url.pathname === "/" || url.pathname === "/bookmarks/12") return serveFile(res, path.join(webDir, "index.html"));
    const asset = url.pathname.match(/^\/assets\/(.+)$/);
    if (asset) return serveFile(res, path.join(webDir, path.normalize(asset[1])));
    const cache_identity = { schema_version: 1, content_revision: 1,
      body_revision: state.identityRevision, personal_revision: state.revision,
      latest_decision_id: 0, latest_entity_revision: state.revision };
    const curated = { ...BOOKMARK, why: state.curation.why, curation_status: state.curation.status,
      classification_reviewed: state.curation.reviewed,
      ...(state.combinedReading ? {
        classification: { ...BOOKMARK.classification, topics: state.selection.topics,
          resource_kinds: state.selection.resource_kinds, content_functions: state.selection.content_functions },
        custom_tags: [{ id: "personal", tag_ref: "custom/default/personal", label: "我的项目", revision: 2, status: "active" }]
      } : {}) };
    if (url.pathname === "/api/bookmarks/12/reading" && state.combinedReading) {
      state.readingReads++;
      if (state.holdReading) {
        state.holdReading = false;
        await new Promise((resolve) => { state.releaseReading = resolve; });
      }
      const bodyUnchanged = url.searchParams.get("body_revision") === String(state.identityRevision);
      if (bodyUnchanged) state.readingBodyOmissions++;
      return send(200, { version: 1, body_unchanged: bodyUnchanged,
        detail: { ...curated, original_text: bodyUnchanged ? null : BOOKMARK.original_text,
          translated_text: bodyUnchanged ? null : BOOKMARK.translated_text,
          ai_title: state.remoteTitle || BOOKMARK.ai_title,
          cache_identity, updated_at: "2026-09-20T00:00:00Z" },
        selection: { available: true, id: 12, revision: state.revision, selection: state.selection,
          v1_projection: { topics: state.selection.topics.slice(0, 3), form: state.selection.form, use: state.selection.use } },
        entities: { id: 12, available: true, state: state.entityState, stale: false,
          entities: state.entities, human: state.entities, revision: state.revision } });
    }
    if (url.pathname === "/api/bookmarks/12") {
      state.detailReads++;
      return send(200, { ...curated, ai_title: state.remoteTitle || BOOKMARK.ai_title,
        cache_identity, updated_at: "2026-09-20T00:00:00Z" });
    }
    if (url.pathname === "/api/bookmarks/12/identity") {
      state.identityReads++;
      return send(200, { id: 12, status: BOOKMARK.status, updated_at: "2026-09-20T00:00:00Z",
        paid_call_unresolved: false, cache_identity });
    }
    if (url.pathname === "/api/taxonomy") {
      return send(200, { version: "2026-09-20.1", topics: taxonomyV2().topics, forms: taxonomyV2().forms, uses: taxonomyV2().uses });
    }
    if (url.pathname === "/api/v2-taxonomy") return send(200, { ...taxonomyV2(),
      ...(state.combinedReading ? { resource_kinds: [{ id: "software", label: "软件与服务", active: true, aliases: [], description: "d" }] } : {}) });
    if (url.pathname === "/api/overview") return send(200, { views: { all: 1, inbox: 1, kept: 0, compiled: 0, drop: 0, uncertain: 0 }, counts: { total: 1 }, attention: 0, queued: 0 });
    if (url.pathname === "/status") return send(200, { ready: true, build: {} });
    if (url.pathname === "/api/bookmarks/12/v2-selection") {
      if (req.method === "GET") {
        state.selectionReads++;
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
      // Mirror the real Worker's validation: only field actions, never a
      // why/status accept event.
      if (body.field === "why" || body.field === "status") return send(400, { error: "invalid_override" });
      const singleValued = ["carriers", "form", "use"].includes(body.field);
      if (body.action === "accept") {
        if (singleValued) state.selection[body.field] = [body.term];
        else if (!state.selection[body.field].includes(body.term)) state.selection[body.field].push(body.term);
      }
      if (body.action === "reject") state.selection[body.field] = state.selection[body.field].filter((id) => id !== body.term);
      if (body.action === "set_empty") state.selection[body.field] = [];
      // The Worker projects a human override to links.curation. A successful
      // edit makes the bookmark reviewed, so the redundant confirm action
      // disappears when the detail refresh completes.
      state.curation.reviewed = true;
      state.revision += 1;
      const response = { id: 12, field: body.field, term: body.term, action: body.action, revision: state.revision, replayed: false };
      state.operations.set(key, response);
      return send(200, response);
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
    if (url.pathname === "/api/bookmarks/12/classification-status") return send(200, { status: "completed", attempts: 1, error: null });
    if (url.pathname === "/api/bookmarks/12/entities") {
      if (req.method === "GET") {
        state.entityReads++;
        return send(200, { available: true, state: state.entityState, stale: false, entities: state.entities, human: state.entities, revision: state.revision });
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
      if (state.failNextCuration) {
        state.failNextCuration = false;
        return send(503, { error: "backend_error" });
      }
      if (body.operation_key && state.curationOperations.has(body.operation_key)) {
        return send(200, state.curationOperations.get(body.operation_key));
      }
      if (body.expected_revision !== undefined && body.expected_revision !== state.revision) {
        return send(409, { error: "revision_conflict", revision: state.revision });
      }
      if (state.delayNextCurationMs > 0) {
        const delay = state.delayNextCurationMs;
        state.delayNextCurationMs = 0;
        await new Promise((resolve) => setTimeout(resolve, delay));
      }
      if (state.holdWhy && "why" in body) {
        await new Promise((resolve) => { state.releaseWhy = resolve; });
      }
      if ("why" in body) state.curation.why = body.why;
      if ("curation_status" in body) state.curation.status = body.curation_status;
      if ("classification" in body) {
        state.curation.reviewed = body.classification !== null;
        state.revision += 1;
      }
      const updated = { ...BOOKMARK, why: state.curation.why, curation_status: state.curation.status,
        classification_reviewed: state.curation.reviewed,
        cache_identity: { schema_version: 1, content_revision: 1, body_revision: state.identityRevision,
          personal_revision: state.revision, latest_decision_id: 0, latest_entity_revision: state.revision } };
      if (body.operation_key) state.curationOperations.set(body.operation_key, updated);
      if (state.loseNextCurationResponse) {
        state.loseNextCurationResponse = false;
        return send(503, { error: "response_lost" });
      }
      return send(200, updated);
    }
    if (url.pathname === "/api/bookmarks") {
      state.listQueries.push(Object.fromEntries(url.searchParams));
      const filtered = url.searchParams.has("topics") || url.searchParams.has("content_functions");
      return send(200, {
        items: filtered ? [] : [{ ...curated, content_loaded: false, original_text: undefined, translated_text: undefined }],
        counts: { total: filtered ? 0 : 1 }, next_before_id: null,
        ...(state.oldFilterBackend ? {} : { filter_contract_version: 1 })
      });
    }
    return send(404, { error: "not_found" });
  });
  return new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve(server)));
}

const overrides = (state) => state.requests.filter((entry) => entry.path.endsWith("/v2-override"));
const chipTerms = (page, selector) => page.$$eval(selector, (nodes) => nodes.map((node) => node.dataset.term));

async function partA(browser) {
  const state = createMock();
  const server = await startMockServer(state);
  const base = `http://127.0.0.1:${server.address().port}`;
  const page = await browser.newPage({ viewport: { width: 1280, height: 860 } });
  const pageErrors = [];
  page.on("pageerror", (error) => pageErrors.push(String(error)));
  await page.goto(`${base}/bookmarks/12`, { waitUntil: "networkidle" });
  await openCuration(page);

  // 1. The multidimensional editor renders every effective topic; the fourth
  // is folded for list cards, never deleted.
  await page.waitForSelector("#v2-topics .chip.on");
  equal("all four effective topics are rendered as selected", await chipTerms(page, "#v2-topics .chip.on"), ["llm", "eng", "eval", "design"]);
  check("additional topics are folded, not deleted", /其余标签仍然保留/.test(await page.textContent("#v2-folded-note") || ""));

  // 2. content_functions multi-select and the carrier are independent.
  equal("content functions are independently selected", (await chipTerms(page, "#v2-content_functions .chip.on")).sort(), ["data", "method", "tool"]);
  equal("carrier is independent", await chipTerms(page, "#v2-carriers .chip.on"), ["author_continuation"]);

  // 3. Rejecting a candidate sends a reject override and does not touch the body.
  await page.click("#v2-topics .chip.on[data-term='eng']");
  await waitFor(() => state.requests.some((entry) => entry.body.action === "reject"));
  const rejectRequest = state.requests.find((entry) => entry.body.action === "reject");
  check("reject sends a reject override", rejectRequest?.body.term === "eng", JSON.stringify(rejectRequest));
  check("reject override carries an operation key", typeof rejectRequest?.body.operation_key === "string");
  check("reject override carries expected_revision", rejectRequest?.body.expected_revision === 3);

  // 4. set_empty and reset are different explicit actions.
  await page.click("#v2-affordances [data-edit='affordances']");
  await page.click("#v2-affordances button[data-action='set_empty']");
  await waitFor(() => state.requests.some((entry) => entry.body.action === "set_empty"));
  const emptyRequest = state.requests.find((entry) => entry.body.action === "set_empty");
  check("set_empty sends its own action", emptyRequest?.body.field === "affordances", JSON.stringify(emptyRequest));
  await page.click("#v2-topics [data-edit='topics']");
  await page.click("#v2-topics button[data-action='reset']");
  await waitFor(() => state.requests.some((entry) => entry.body.action === "reset"));
  const resetRequest = state.requests.find((entry) => entry.body.action === "reset");
  check("reset sends a distinct action", resetRequest !== undefined && resetRequest.body.action !== "set_empty", JSON.stringify(resetRequest));
  check("set_empty and reset payloads differ", JSON.stringify(emptyRequest.body) !== JSON.stringify(resetRequest.body));
  await waitFor(() => page.evaluate(() => document.querySelector("#curate")?.getAttribute("aria-busy") !== "true"));

  // 5. Replaying the same operation key returns the stored result regardless
  // of the revision having advanced, because the operation already happened.
  const storedKey = [...state.operations.keys()][0];
  const replay = await page.evaluate(async (payload) => {
    const response = await fetch("/api/bookmarks/12/v2-override", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
    return response.status;
  }, { field: "topics", term: "eng", action: "accept", operation_key: storedKey });
  check("replaying the same operation key is accepted", replay === 200);

  // 6. Malicious original text is rendered as text, never as markup.
  await page.click("#original-toggle");
  const original = await page.$eval("#original", (node) => ({ html: node.innerHTML.toLowerCase(), text: node.textContent, elements: node.querySelectorAll("script, img").length }));
  check("malicious original is escaped, not executed", original.elements === 0 && original.text.includes("<script>"), original.html.slice(0, 120));
  check("no page error from malicious content", pageErrors.length === 0, pageErrors.join("; "));

  // 7. A reason-only save sends neither a classification nor an override.
  const requestsBeforeWhy = state.requests.length;
  await page.fill("#curation-why", "只是有趣");
  await page.press("#curation-why", "Enter");
  await waitFor(() => state.requests.slice(requestsBeforeWhy).some((entry) => entry.path.endsWith("/curation")));
  const whyRequest = state.requests.slice(requestsBeforeWhy).find((entry) => entry.path.endsWith("/curation"));
  check("why-only save reaches the curation endpoint", whyRequest?.body.why === "只是有趣", JSON.stringify(whyRequest));
  check("why-only save omits classification and status", whyRequest && !("classification" in whyRequest.body) && !("curation_status" in whyRequest.body), JSON.stringify(whyRequest));
  check("why-only save produces no override event", !state.requests.slice(requestsBeforeWhy).some((entry) => entry.path.endsWith("/v2-override")));

  // 8. Rapid actions are queued, each with its own operation id, and none is
  // dropped while the previous request is still in flight.
  const beforeRapid = overrides(state).length;
  check("the draft reflects the server state before the rapid actions", !(await chipTerms(page, "#v2-topics .chip.on")).includes("eng"));
  state.delayNextMs = 400;
  await page.click("#v2-topics .chip.on[data-term='llm']");
  await page.click("#v2-topics .chip.option[data-term='eng']");
  await waitFor(() => overrides(state).length >= beforeRapid + 2, 8000);
  const rapid = overrides(state).slice(beforeRapid);
  check("both rapid actions reached the server", rapid.length === 2, JSON.stringify(rapid.map((entry) => entry.body)));
  const rapidKeys = new Set(rapid.map((entry) => entry.body.operation_key));
  check("each new logical action has a distinct operation id", rapidKeys.size === rapid.length);
  equal("rapid actions preserve both intents", rapid.map((entry) => `${entry.body.action}:${entry.body.term}`).sort(), ["accept:eng", "reject:llm"]);

  // 9. A reload starts a new action identity.
  await page.reload({ waitUntil: "networkidle" });
  await openCuration(page);
  await page.waitForSelector("#v2-topics .chip.on[data-term='eval']");
  const beforeReload = overrides(state).length;
  await page.click("#v2-topics .chip.on[data-term='eval']");
  await waitFor(() => overrides(state).length > beforeReload);
  check("a post-reload action uses a fresh operation id", !rapidKeys.has(overrides(state)[beforeReload]?.body.operation_key));

  // 10. A CAS conflict keeps the draft and requires an explicit re-apply.
  await waitFor(() => page.evaluate(() => document.querySelector("#curate")?.getAttribute("aria-busy") !== "true"));
  await page.waitForSelector("#v2-topics .chip.on[data-term='design']");
  state.forceConflict = true;
  const beforeConflict = overrides(state).length;
  await page.click("#v2-topics .chip.on[data-term='design']");
  await page.waitForSelector("#v2-conflict:not([hidden])");
  check("conflict is explained to the user", /其他客户端/.test(await page.textContent("#v2-conflict") || ""));
  check("conflict does not discard the unsaved draft", !(await chipTerms(page, "#v2-topics .chip.on")).includes("design"));
  await page.click("#v2-topics .chip.on[data-term='eng']");
  await page.waitForTimeout(150);
  check("editing is paused until the conflict is resolved", overrides(state).length === beforeConflict + 1);
  await page.click("#v2-conflict [data-conflict='retry']");
  await waitFor(() => overrides(state).length > beforeConflict + 1);
  const reapplied = overrides(state)[beforeConflict + 1];
  check("re-apply submits the preserved action", reapplied?.body.action === "reject" && reapplied.body.term === "design", JSON.stringify(reapplied?.body));
  check("re-apply reuses the preserved operation key", reapplied?.body.operation_key === overrides(state)[beforeConflict].body.operation_key);
  await page.waitForSelector("#v2-conflict", { state: "hidden" });

  // 11. Evidence, classification status, entities and the three explicit redo
  // actions, each with its own stated cost.
  await page.click("#diagnostics > summary");
  await page.waitForSelector("#v2-evidence-blocks .v2-evidence-role");
  equal("evidence blocks render their real roles", await page.$$eval("#v2-evidence-blocks .v2-evidence-role", (nodes) => nodes.map((node) => node.textContent)), ["原帖", "引用"]);
  await waitFor(async () => /分类完成/.test(await page.textContent("#v2-classification-status") || ""));
  check("classification status is shown independently", /分类完成/.test(await page.textContent("#v2-classification-status") || ""));
  check("the entity row renders the stored entities", /acme/.test(await page.textContent("#v2-entity-list") || ""));
  await page.fill("#v2-entity-input", "widget");
  await page.click("#v2-entity-add");
  await waitFor(() => state.actions.some((entry) => entry.action === "entity" && entry.body.term === "widget"));
  await waitFor(async () => /widget/.test(await page.textContent("#v2-entity-list") || ""));
  check("a human entity correction is submitted and shown", /widget/.test(await page.textContent("#v2-entity-list") || ""));
  const entityRequest = state.actions.find((entry) => entry.action === "entity");
  check("entity corrections carry an operation key and revision", typeof entityRequest?.body.operation_key === "string" && Number.isInteger(entityRequest.body.expected_revision));

  await page.click("#v2-retry-classification");
  await waitFor(() => state.actions.some((entry) => entry.action === "retry_classification"));
  check("classification retry is a separate action", state.actions.some((entry) => entry.action === "retry_classification"));
  await page.click("#v2-refresh-source");
  await page.waitForSelector("dialog[open] [data-action='confirm']");
  check("refresh-source asks before re-reading the source", !state.actions.some((entry) => entry.action === "refresh_source"));
  await page.click("dialog[open] [data-action='confirm']");
  await waitFor(() => state.actions.some((entry) => entry.action === "refresh_source"));
  check("refresh-source is a separate, confirmed action", state.actions.some((entry) => entry.action === "refresh_source"));
  await page.click("#v2-replay-policy");
  await page.waitForSelector("#v2-replay-result button");
  check("policy replay reports zero model calls", /模型调用 0 次/.test(await page.textContent("#v2-replay-result") || ""));
  await page.click("#v2-replay-result button");
  await waitFor(async () => /未授权/.test(await page.textContent("#v2-replay-result") || ""));
  check("an unauthorized write-back explains itself instead of silently failing", /未授权/.test(await page.textContent("#v2-replay-result") || ""));
  check("no model or X Search call happened", state.modelCalls === 0 && state.xSearchCalls === 0);

  // 12. An explicit override is already a human review, so confirming the old
  // AI selection after it would undo the edit.
  check("an explicit override hides redundant AI confirmation", await waitFor(() => page.isHidden("#confirm-classification")));

  // 13. Keyboard reachability and narrow viewports.
  check("tag chips are keyboard focusable", await page.evaluate(() => {
    const chip = document.querySelector("#v2-topics .chip");
    chip.focus();
    return document.activeElement === chip;
  }));
  for (const width of [375, 320]) {
    await page.setViewportSize({ width, height: 720 });
    await page.waitForTimeout(350);
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    check(`no horizontal overflow at ${width}px`, overflow <= 1, `overflow=${overflow}px`);
  }
  await page.setViewportSize({ width: 1280, height: 860 });

  // 14. The library carries every dimension through requests and URLs.
  const latestQuery = () => state.listQueries.at(-1);
  await page.goto(`${base}/?topic=design&content_functions=method,data&carriers=single&affordances=practice&entity_state=failed`, { waitUntil: "networkidle" });
  await page.waitForSelector("[data-facet='topics'][data-value='llm']", { state: "attached" });
  equal("legacy topic URL becomes the full topic filter", latestQuery().topics, "design");
  equal("library filter negotiation is explicit", latestQuery().filter_contract_version, "1");
  equal("URL restores selected function values", await page.$$eval("[data-facet='content_functions'][aria-pressed='true']", (nodes) => nodes.map((node) => node.dataset.value)), ["method", "data"]);
  const toggleFacet = async (key, value) => {
    const group = page.locator(`details[data-group='${key}']`);
    await group.evaluate((node) => {
      for (let parent = node.parentElement; parent; parent = parent.parentElement) if (parent instanceof HTMLDetailsElement) parent.open = true;
    });
    if (!await group.evaluate((node) => node.open)) await group.locator("summary").click();
    const before = state.listQueries.length;
    await page.click(`[data-facet='${key}'][data-value='${value}']`);
    await waitFor(() => state.listQueries.length > before);
  };
  await toggleFacet("topics", "llm");
  await toggleFacet("content_functions", "tool");
  await toggleFacet("content_functions", "method");
  await toggleFacet("carriers", "author_continuation");
  await toggleFacet("affordances", "background");
  await toggleFacet("entity_state", "completed_empty");
  await toggleFacet("entity_state", "stale");
  await toggleFacet("entity_state", "failed");
  const sorted = (value) => String(value || "").split(",").filter(Boolean).sort().join(",");
  for (const [key, value] of Object.entries({ topics: "design,llm", content_functions: "data,tool", carriers: "author_continuation,single", affordances: "background,practice", entity_state: "completed_empty,stale" })) {
    equal(`library forwards ${key}`, sorted(latestQuery()[key]), value);
    equal(`library URL preserves ${key}`, sorted(new URL(page.url()).searchParams.get(key)), value);
  }
  check("active filters are listed above the results", (await page.$$eval("#active-filters .filter-chip", (nodes) => nodes.length)) >= 10);
  state.oldFilterBackend = true;
  await toggleFacet("topics", "llm");
  await page.waitForSelector("#list-notice:not([hidden])");
  check("an old backend cannot silently accept ignored filters", /不支持完整筛选/.test(await page.textContent("#list-notice") || ""));
  const beforeClear = state.listQueries.length;
  await page.click("#clear-filters");
  await waitFor(() => state.listQueries.length > beforeClear);
  check("clearing filters stops negotiating the contract", !latestQuery().filter_contract_version && !latestQuery().topics);
  equal("clearing filters clears every facet", await page.$$eval("[data-facet][aria-pressed='true']", (nodes) => nodes.length), 0);
  await waitFor(() => page.isHidden("#list-notice"));
  check("ordinary browsing still works with an old backend", await page.isHidden("#list-notice"));
  check("no JavaScript exceptions in part A", pageErrors.length === 0, pageErrors.join("; "));
  await page.close();
  server.close();
}

// --- Part B: triage flows on realistic data ------------------------------------------

async function partB(browser) {
  const state = createFixtureState();
  const { server, url: base } = await startFixtureServer({ state });
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, acceptDownloads: true });
  const page = await context.newPage();
  const pageErrors = [];
  page.on("pageerror", (error) => pageErrors.push(String(error)));
  const curationRequests = () => state.requests.filter((entry) => entry.path.endsWith("/curation"));
  const selectedId = () => page.$eval("#rows li.row.selected", (node) => Number(node.dataset.id)).catch(() => 0);
  const rowIds = () => page.$$eval("#rows li.row", (nodes) => nodes.map((node) => Number(node.dataset.id)));
  const idle = () => page.waitForFunction(() => document.querySelector("#list-pane")?.dataset.loading === "false");

  await page.goto(base, { waitUntil: "networkidle" });
  await idle();
  await page.waitForSelector("#rows li.row.selected");
  const inbox = state.items.filter((item) => item.curation_status === "inbox").map((item) => item.id);
  equal("the landing page is the inbox, newest first", (await rowIds()).slice(0, 5), inbox.slice(0, 5));
  equal("the first bookmark opens without a click", await selectedId(), inbox[0]);
  check("the reading pane shows the selected bookmark", (await page.textContent("#detail-title")) === (state.items.find((item) => item.id === inbox[0]).ai_title || await page.textContent("#detail-title")));
  check("the navigation shows live counts", /\d+/.test(await page.textContent("[data-view='inbox'] .nav-count") || ""));

  // Enter on a list control must keep the button's native click behaviour.
  await page.locator("#list-menu").focus();
  await page.keyboard.press("Enter");
  await page.waitForSelector("[role='menu']");
  check("Enter opens the focused list menu", await page.locator("[role='menu']").isVisible());
  await page.keyboard.press("Escape");
  await page.locator("#list-menu").evaluate((node) => node.blur());

  // J/K move through the list and keep the URL shareable.
  const beforeJRequests = state.requests.length;
  await page.keyboard.press("j");
  await waitFor(async () => (await selectedId()) === inbox[1]);
  await page.waitForTimeout(350); // let the adjacent prefetch scheduled by this step run
  const jRequests = state.requests.slice(beforeJRequests);
  // This fixture exercises the old detail, selection and entities routes.
  // The combined reading route has its own one-request check below.
  check("one J step stays within three legacy API requests", jRequests.length <= 3 &&
    jRequests.every((entry) => entry.path.startsWith("/api/bookmarks/")), JSON.stringify(jRequests));
  equal("J selects the next bookmark", await selectedId(), inbox[1]);
  check("the URL follows the selection", new URL(page.url()).pathname === `/bookmarks/${inbox[1]}`);
  await page.keyboard.press("k");
  await page.keyboard.press("j");
  await waitFor(async () => (await selectedId()) === inbox[1]);

  // A number key files the bookmark, drops it from the inbox and advances.
  const beforeKeep = curationRequests().length;
  const overviewBeforeKeep = state.requests.filter((entry) => entry.path === "/api/overview").length;
  const statusBeforeKeep = state.requests.filter((entry) => entry.path === "/status").length;
  const inboxCountBefore = Number(await page.textContent("[data-view='inbox'] .nav-count"));
  const uncertainCountBefore = Number(await page.textContent("[data-view='uncertain'] .nav-count"));
  const filed = state.items.find((item) => item.id === inbox[1]);
  const wasUncertain = !filed.classification_reviewed && (!filed.classification || filed.classification.uncertainty);
  await page.keyboard.press("2");
  await waitFor(() => curationRequests().length > beforeKeep);
  const keep = curationRequests().at(-1);
  equal("status keys send only the status", keep.body, { curation_status: "kept" });
  await waitFor(async () => !(await rowIds()).includes(inbox[1]));
  check("a filed bookmark leaves the inbox view", !(await rowIds()).includes(inbox[1]));
  await waitFor(async () => Number(await page.textContent("[data-view='inbox'] .nav-count")) === inboxCountBefore - 1);
  await waitFor(async () => Number(await page.textContent("[data-view='uncertain'] .nav-count")) === uncertainCountBefore - Number(wasUncertain));
  await page.waitForTimeout(900);
  equal("filing updates navigation before an overview request", state.requests.filter((entry) => entry.path === "/api/overview").length, overviewBeforeKeep);
  equal("filing does not request service status", state.requests.filter((entry) => entry.path === "/status").length, statusBeforeKeep);
  check("filing makes one trailing overview request", await waitFor(() =>
    state.requests.filter((entry) => entry.path === "/api/overview").length === overviewBeforeKeep + 1, 5500));
  equal("trailing overview does not request service status", state.requests.filter((entry) => entry.path === "/status").length, statusBeforeKeep);
  equal("the next bookmark opens automatically", await selectedId(), inbox[2]);
  await page.waitForSelector(".toast .toast-action");
  check("the change can be undone from the toast", /撤销/.test(await page.textContent(".toast .toast-action") || ""));

  // Z undoes it and brings the bookmark back where it was.
  const beforeUndo = curationRequests().length;
  await page.keyboard.press("z");
  await waitFor(() => curationRequests().length > beforeUndo);
  equal("undo restores the previous status", curationRequests().at(-1).body, { curation_status: "inbox" });
  await waitFor(async () => (await rowIds()).includes(inbox[1]));
  await waitFor(async () => Number(await page.textContent("[data-view='inbox'] .nav-count")) === inboxCountBefore);
  equal("undo keeps an explicitly curated inbox item out of uncertain", Number(await page.textContent("[data-view='uncertain'] .nav-count")), uncertainCountBefore - Number(wasUncertain));
  equal("undo puts the bookmark back in order", (await rowIds()).slice(0, 3), inbox.slice(0, 3));
  equal("undo reopens the bookmark", await selectedId(), inbox[1]);

  // Batch: X marks rows, one click files them all.
  await page.keyboard.press("x");
  await page.keyboard.press("j");
  await page.keyboard.press("x");
  await page.waitForSelector("#batch-bar:not([hidden])");
  check("the batch bar counts the selection", /已选 2 条/.test(await page.textContent("#batch-bar") || ""));
  const beforeBatch = curationRequests().length;
  await page.click("#batch-bar [data-batch-status='drop']");
  await waitFor(() => curationRequests().length >= beforeBatch + 2);
  const batch = curationRequests().slice(beforeBatch);
  equal("batch filing sends one status-only request per bookmark", batch.map((entry) => entry.body), [{ curation_status: "drop" }, { curation_status: "drop" }]);
  await waitFor(async () => !(await rowIds()).includes(inbox[1]) && !(await rowIds()).includes(inbox[2]));
  check("batch-filed bookmarks leave the inbox", !(await rowIds()).some((id) => id === inbox[1] || id === inbox[2]));
  check("the batch bar closes after the action", await page.isHidden("#batch-bar"));

  // The reason field saves on its own and never touches tags.
  await page.keyboard.press("r");
  check("R focuses the reason field", await page.evaluate(() => document.activeElement?.id === "curation-why"));
  const beforeWhy = curationRequests().length;
  await page.keyboard.type("写进周报");
  await page.keyboard.press("Enter");
  await waitFor(() => curationRequests().length > beforeWhy);
  equal("the reason saves by itself", curationRequests().at(-1).body, { why: "写进周报" });
  await waitFor(async () => /写进周报/.test(await page.textContent("#rows li.row.selected") || ""));
  check("the saved reason shows in the list row", /写进周报/.test(await page.textContent("#rows li.row.selected") || ""));

  // A reason draft stays with its bookmark even when the user moves away and
  // back while the save is still in flight.
  const draftOwner = await selectedId();
  state.delayNextCurationMs = 900;
  await page.keyboard.press("r");
  await page.keyboard.press("End");
  await page.keyboard.type("，下周复盘");
  await page.keyboard.press("Escape");
  await page.keyboard.press("j");
  await waitFor(async () => (await selectedId()) !== draftOwner);
  check("the next bookmark does not inherit the draft", !(await page.inputValue("#curation-why")).includes("下周复盘"));
  await page.keyboard.press("k");
  await waitFor(async () => (await selectedId()) === draftOwner);
  check("returning mid-save shows the bookmark's own draft", (await page.inputValue("#curation-why")) === "写进周报，下周复盘", await page.inputValue("#curation-why"));
  await waitFor(() => curationRequests().at(-1)?.body.why === "写进周报，下周复盘");
  await page.waitForTimeout(1000);
  check("the draft survives the save completing", (await page.inputValue("#curation-why")) === "写进周报，下周复盘");

  // Search: "/" focuses, typing filters with highlighted matches, Esc clears.
  await page.keyboard.press("/");
  check("/ focuses search", await page.evaluate(() => document.activeElement?.id === "search"));
  await page.keyboard.type("评估");
  await waitFor(() => state.requests.some((entry) => entry.path === "/api/bookmarks" && entry.query.q === "评估"));
  await idle();
  await page.waitForSelector("#rows li.row mark");
  check("search results highlight the match", (await page.$$eval("#rows mark", (nodes) => nodes.map((node) => node.textContent))).every((text) => text === "评估"));
  check("search keeps the view scope", state.requests.filter((entry) => entry.path === "/api/bookmarks").at(-1).query.curation_status === "inbox");
  await page.press("#search", "Escape");
  await waitFor(() => new URL(page.url()).searchParams.get("q") === null);
  check("Esc clears the search", (await page.inputValue("#search")) === "");

  // G then A jumps to every bookmark; the view is part of the URL.
  await page.locator("#detail-scroll").focus();
  await page.keyboard.press("g");
  await page.keyboard.press("a");
  await waitFor(() => new URL(page.url()).searchParams.get("curation_status") === "all");
  await idle();
  check("G A opens every bookmark", /全部收藏/.test(await page.textContent("#list-title") || ""));

  // In the uncertain view, confirming tags files the bookmark out of the view.
  await page.keyboard.press("g");
  await page.keyboard.press("u");
  await waitFor(() => new URL(page.url()).searchParams.get("uncertain") === "true");
  await idle();
  await page.waitForSelector("#rows li.row.selected");
  // Unprocessed bookmarks are also "uncertain" (nothing to confirm yet), so
  // pick the first row that actually carries AI tags.
  const uncertainFirst = (await rowIds()).find((id) => state.items.find((item) => item.id === id)?.classification);
  await page.click(`#rows li.row[data-id='${uncertainFirst}'] a.row-main`);
  await page.locator("#detail-scroll").focus();
  const target = state.items.find((item) => item.id === uncertainFirst);
  const beforeConfirm = curationRequests().length;
  if (target?.classification) {
    await page.keyboard.press("a");
    await waitFor(() => curationRequests().length > beforeConfirm);
    const confirm = curationRequests().at(-1);
    check("A confirms the AI tags explicitly", Array.isArray(confirm.body.classification?.topics) && !("curation_status" in confirm.body));
    await waitFor(async () => !(await rowIds()).includes(uncertainFirst));
    check("a confirmed bookmark leaves the uncertain view", !(await rowIds()).includes(uncertainFirst));
  } else {
    check("the uncertain view starts with a classified bookmark", false, JSON.stringify(target));
  }

  // Export of the current bookmark downloads Markdown with its full text.
  const download = page.waitForEvent("download");
  await page.locator("#detail-scroll").focus();
  await page.keyboard.press("e");
  let exported = "";
  for await (const chunk of await (await download).createReadStream()) exported += chunk;
  check("E exports the open bookmark as Markdown", exported.startsWith("# Cairn 收藏摘录") && exported.includes("收藏 ID："));

  // Backstage recovery submits exactly once and then shows the in-progress state.
  state.recovery = { state: "waiting", can_recover: true, reason: "timeout", next_check_at: Date.now()+300000 };
  // Backstage is a view in the same shell.
  await page.click("#service-link");
  await page.waitForSelector("#backstage-view:not([hidden]) .attention-item");
  check("the service view lists bookmarks that need a retry", (await page.$$eval(".attention-item", (nodes) => nodes.length)) >= 1);
  check("the service view lives at /backstage", new URL(page.url()).pathname === "/backstage");
  await page.locator("#service-recover").click();
  await page.waitForFunction(() => document.querySelector("#service-recover")?.textContent.includes("正在检查"));
  check("manual recovery disables repeat requests while checking", await page.locator("#service-recover").isDisabled());
  check("manual recovery is one JSON mutation", state.requests.filter(r => r.path === "/api/service/recover" && r.method === "POST").length === 1);
  delete state.recovery;
  await page.goBack();
  await page.waitForSelector("#backstage-view", { state: "hidden" });
  check("back returns to the library", !new URL(page.url()).pathname.startsWith("/backstage"));

  // Phone layout: the list opens a full-screen reader with a status bar.
  const phone = await browser.newPage({ viewport: { width: 375, height: 812 }, hasTouch: true });
  phone.on("pageerror", (error) => pageErrors.push(String(error)));
  await phone.goto(`${base}/?curation_status=kept`, { waitUntil: "networkidle" });
  await phone.waitForSelector("#rows li.row");
  check("the phone list does not auto-open a bookmark", !(await phone.evaluate(() => document.getElementById("app").classList.contains("detail-open"))));
  const opener = phone.locator("#list-pane [data-open-sidebar]");
  await opener.click();
  check("phone navigation has modal semantics", await phone.locator("#sidebar").getAttribute("aria-modal") === "true");
  check("phone navigation makes the background inert", await phone.locator("#list-pane").evaluate((node) => node.inert));
  await phone.locator("#sidebar .brand").focus();
  await phone.keyboard.press("Shift+Tab");
  check("sidebar reverse tab wraps to its last control", await phone.evaluate(() => document.activeElement?.id === "service-link"));
  await phone.keyboard.press("Tab");
  check("sidebar tab wraps to its first control", await phone.evaluate(() => document.activeElement?.classList.contains("brand")));
  await phone.keyboard.press("Escape");
  check("closing sidebar restores the opening button", await opener.evaluate((node) => document.activeElement === node));
  check("closed phone navigation leaves the background usable", await phone.locator("#list-pane").evaluate((node) => !node.inert));
  check("closed phone navigation leaves the tab order", await phone.locator("#sidebar").evaluate((node) => node.inert));
  const phoneFirst = await phone.$eval("#rows li.row", (node) => Number(node.dataset.id));
  await phone.click("#rows li.row a.row-main");
  await phone.waitForFunction(() => document.getElementById("app").classList.contains("detail-open"));
  check("tapping a row opens the reader", new URL(phone.url()).pathname === `/bookmarks/${phoneFirst}`);
  const beforePhone = curationRequests().length;
  await phone.click("#mobile-actions [data-status='compiled']");
  await waitFor(() => curationRequests().length > beforePhone);
  equal("the phone status bar files the bookmark", curationRequests().at(-1).body, { curation_status: "compiled" });
  await phone.click("#detail-back");
  await phone.waitForFunction(() => !document.getElementById("app").classList.contains("detail-open"));
  check("back returns to the phone list", !new URL(phone.url()).pathname.startsWith("/bookmarks/"));
  for (const width of [375, 320]) {
    await phone.setViewportSize({ width, height: 700 });
    await phone.waitForTimeout(300);
    const overflow = await phone.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    check(`no horizontal overflow on the phone list at ${width}px`, overflow <= 1, `overflow=${overflow}px`);
  }
  await phone.close();

  check("no model call was made by any triage action", state.modelCalls === 0);
  check("no JavaScript exceptions in part B", pageErrors.length === 0, pageErrors.join("; "));
  await context.close();
  server.close();
}

// --- Part C: a Worker without the multidimensional API -------------------------------

async function partC(browser) {
  const state = createFixtureState({ v2: false });
  const { server, url: base } = await startFixtureServer({ state });
  const page = await browser.newPage({ viewport: { width: 1280, height: 860 } });
  const pageErrors = [];
  page.on("pageerror", (error) => pageErrors.push(String(error)));
  const curationRequests = () => state.requests.filter((entry) => entry.path.endsWith("/curation"));
  const item = state.items.find((entry) => entry.classification && !entry.classification_reviewed && entry.curation_status === "inbox");
  await page.goto(`${base}/bookmarks/${item.id}`, { waitUntil: "networkidle" });
  await openCuration(page);
  await page.waitForSelector("#v1-topics .chip.on");
  check("the v1 editor replaces the multidimensional one", await page.isVisible("#v1-topics") && !(await page.$("#v2-topics")));
  check("the capability gap is explained, not hidden", /多维词表暂不可用/.test(await page.textContent("#facets") || ""));
  check("filters the backend cannot evaluate are not offered", !(await page.$("[data-facet='topics']")) && Boolean(await page.$("[data-facet='source']")));

  const beforeWhy = curationRequests().length;
  await page.fill("#curation-why", "旧后端也能写原因");
  await page.press("#curation-why", "Enter");
  await waitFor(() => curationRequests().length > beforeWhy);
  equal("a v1 reason save sends only the reason", curationRequests().at(-1).body, { why: "旧后端也能写原因" });

  const beforeTag = curationRequests().length;
  const topics = await page.$$eval("#v1-topics .chip.on", (nodes) => nodes.map((node) => node.dataset.term));
  await page.click(`#v1-topics .chip.on[data-term='${topics[0]}']`);
  await waitFor(() => curationRequests().length > beforeTag, 4000);
  const tagSave = curationRequests().at(-1);
  equal("an explicit v1 tag edit sends the whole v1 selection", Object.keys(tagSave.body), ["classification"]);
  equal("the removed topic is gone from the saved selection", tagSave.body.classification.topics, topics.slice(1));
  check("no override endpoint is called on a v1 backend", !state.requests.some((entry) => entry.path.endsWith("/v2-override")));
  check("no JavaScript exceptions in part C", pageErrors.length === 0, pageErrors.join("; "));
  await page.close();
  server.close();
}

// A detail left open on an idle, non-first-page-like route must discover a
// remote revision without losing a locally saving reason draft.
async function partD(browser) {
  const state = createMock();
  const server = await startMockServer(state);
  const base = `http://127.0.0.1:${server.address().port}`;
  const page = await browser.newPage({ viewport: { width: 1280, height: 860 } });
  const pageErrors = [];
  page.on("pageerror", (error) => pageErrors.push(String(error)));
  try {
    await page.clock.install();
    await page.goto(`${base}/bookmarks/12`, { waitUntil: "networkidle" });
    await openCuration(page);
    check("visible detail initially loads its body", await waitFor(() => state.detailReads > 0));
    const initialReads = state.detailReads;
    state.identityRevision++;
    state.remoteTitle = "另一客户端的新标题";
    await page.clock.fastForward(16_000);
    check("idle visible detail discovers a remote version", await waitFor(async () =>
      (await page.textContent("#detail-title")) === state.remoteTitle));
    check("version change fetches one new detail", state.identityReads >= 1 && state.detailReads === initialReads + 1);

    const unchangedReads = state.detailReads;
    await page.clock.fastForward(16_000);
    const secondIdentity = await waitFor(() => state.identityReads >= 2);
    await new Promise((resolve) => setTimeout(resolve, 100));
    check("unchanged identity avoids a full detail request", secondIdentity && state.detailReads === unchangedReads,
      JSON.stringify({ identityReads: state.identityReads, detailReads: state.detailReads, unchangedReads }));

    state.holdWhy = true;
    await page.fill("#curation-why", "本地尚未确认的原因");
    await page.press("#curation-why", "Enter");
    check("local reason save is in flight", await waitFor(() => typeof state.releaseWhy === "function"));
    state.identityRevision++;
    state.remoteTitle = "第三次远端更新";
    await page.clock.fastForward(31_000);
    check("remote update still refreshes a visible detail", await waitFor(async () =>
      (await page.textContent("#detail-title")) === state.remoteTitle));
    equal("remote refresh keeps the local reason draft", await page.inputValue("#curation-why"), "本地尚未确认的原因");
    state.releaseWhy?.();
    check("visible identity check has no page error", pageErrors.length === 0, pageErrors.join("; "));
  } finally {
    state.releaseWhy?.();
    await page.close();
    server.close();
  }
}

async function partE(browser) {
  const state = createMock();
  state.combinedReading = true;
  state.holdReading = true;
  state.selection = { ...state.selection, topics: ["llm"], resource_kinds: ["software"], content_functions: ["tool"] };
  const server = await startMockServer(state);
  const base = `http://127.0.0.1:${server.address().port}`;
  const page = await browser.newPage({ viewport: { width: 1280, height: 860 } });
  const pageErrors = [];
  page.on("pageerror", (error) => pageErrors.push(String(error)));
  try {
    await page.clock.install();
    await page.goto(`${base}/bookmarks/12`, { waitUntil: "domcontentloaded" });
    await page.waitForFunction(() => document.querySelector('.row[data-id="12"] .row-meta .tag-resource')?.textContent === "软件与服务");
    equal("list previews topic resource function and custom tags before delayed reading", await page.locator('.row[data-id="12"] .row-meta .tag').allTextContents(), ["LLM", "软件与服务", "工具", "我的项目"]);
    check("four visible tags need no overflow before reading", await page.locator('.row[data-id="12"] .tag-more').count() === 0);
    check("reading waits until the complete list has rendered", await waitFor(() => Boolean(state.releaseReading)));
    state.releaseReading?.();
    await page.waitForFunction(() => document.querySelector("#detail-title")?.textContent === "测试标题");
    await openCuration(page);
    await page.waitForSelector("#v2-topics .chip.on");
    await page.locator("#curate > summary .curate-summary-label").click();
    equal("late combined reading and panel toggle retain list tags", await page.locator('.row[data-id="12"] .row-meta .tag').allTextContents(), ["LLM", "软件与服务", "工具", "我的项目"]);
    check("late combined reading keeps all four tags visible", await page.locator('.row[data-id="12"] .tag-more').count() === 0);
    equal("combined snapshot keeps all three dimensions and personal tag", await page.evaluate(async () => {
      const { getItem } = await import("/assets/js/store.js");
      const item = getItem(12);
      return { topics: item.classification.topics, resources: item.classification.resource_kinds,
        functions: item.classification.content_functions, custom: item.custom_tags.map((tag) => tag.label) };
    }), { topics: ["llm"], resources: ["software"], functions: ["tool"], custom: ["我的项目"] });
    await openCuration(page);
    check("combined reading displays the article", (await page.textContent("#detail-title")) === BOOKMARK.ai_title);
    check("combined reading displays effective tags", (await chipTerms(page, "#v2-topics .chip.on")).includes("llm"));
    check("one request supplies detail, selection and entities",
      state.readingReads === 1 && state.detailReads === 0 && state.selectionReads === 0 && state.entityReads === 0,
      JSON.stringify({ reading: state.readingReads, detail: state.detailReads,
        selection: state.selectionReads, entities: state.entityReads }));
    state.revision++;
    await page.clock.fastForward(16_000);
    check("a personal revision refresh uses a body-free snapshot", await waitFor(() => state.readingReads >= 2) &&
      state.readingBodyOmissions === 1 && state.detailReads === 0 && state.selectionReads === 0 && state.entityReads === 0,
      JSON.stringify({ reading: state.readingReads, omitted: state.readingBodyOmissions,
        detail: state.detailReads, selection: state.selectionReads, entities: state.entityReads }));
    await page.click("#original-toggle");
    check("a body-free refresh keeps the cached original", (await page.textContent("#original")).includes("<script>"));
    check("combined reading causes no page error", pageErrors.length === 0, pageErrors.join("; "));
  } finally {
    state.releaseReading?.();
    await page.close();
    server.close();
  }
}

// The first save of an unreviewed bookmark, a failed save and its retry must
// keep reason/status independent from the explicit tag confirmation action.
async function partF(browser) {
  const state = createMock();
  const server = await startMockServer(state);
  const base = `http://127.0.0.1:${server.address().port}`;
  const page = await browser.newPage({ viewport: { width: 1280, height: 860 } });
  const pageErrors = [];
  page.on("pageerror", (error) => pageErrors.push(String(error)));
  const curationRequests = () => state.requests.filter((entry) => entry.path.endsWith("/curation"));
  try {
    await page.goto(`${base}/bookmarks/12?curation_status=all`, { waitUntil: "networkidle" });
    await openCuration(page);
    await page.waitForSelector("#confirm-classification:visible");
    equal("unreviewed bookmark starts without a curation write", curationRequests().length, 0);
    await page.fill("#curation-why", "首次只保存原因");
    await page.press("#curation-why", "Enter");
    check("first reason save reaches Worker", await waitFor(() => curationRequests().length === 1));
    equal("first reason save sends only why", curationRequests()[0]?.body, { why: "首次只保存原因" });
    check("first reason save leaves AI tags unconfirmed", await page.isVisible("#confirm-classification"));

    state.failNextCuration = true;
    await page.fill("#curation-why", "网络失败后保留的原因");
    await page.press("#curation-why", "Enter");
    check("failed reason save is reported", await waitFor(async () =>
      (await page.textContent("#save-state") || "").includes("保存失败")));
    equal("failed reason save keeps the draft", await page.inputValue("#curation-why"), "网络失败后保留的原因");
    equal("failed reason save sends only why", curationRequests()[1]?.body, { why: "网络失败后保留的原因" });
    await page.press("#curation-why", "Enter");
    check("retry sends the saved draft again", await waitFor(() => curationRequests().length === 3));
    equal("reason retry still sends only why", curationRequests()[2]?.body, { why: "网络失败后保留的原因" });
    check("reason retry succeeds without confirming tags", await waitFor(async () =>
      (await page.textContent("#save-state") || "").includes("已保存")) && await page.isVisible("#confirm-classification"));

    await page.click("#status-seg button[data-status='kept']");
    check("status-only save reaches Worker", await waitFor(() => curationRequests().length === 4));
    equal("status-only save omits reason and tags", curationRequests()[3]?.body, { curation_status: "kept" });
    await page.click("#confirm-classification");
    check("explicit confirm sends a separate curation request", await waitFor(() => curationRequests().length === 5));
    equal("explicit confirm sends guarded classification", Object.keys(curationRequests()[4]?.body || {}).sort(),
      ["classification", "expected_revision", "operation_key"]);
    check("explicit confirm uses the displayed revision", curationRequests()[4]?.body.expected_revision === 3);
    check("reviewed bookmark hides the confirm action", await waitFor(() => page.isHidden("#confirm-classification")));
    await page.fill("#curation-why", "确认后只改原因");
    await page.press("#curation-why", "Enter");
    check("reviewed bookmark saves reason separately", await waitFor(() => curationRequests().length === 6));
    equal("reviewed reason save still omits tags", curationRequests()[5]?.body, { why: "确认后只改原因" });
    await page.click("#status-seg button[data-status='compiled']");
    check("reviewed bookmark saves status separately", await waitFor(() => curationRequests().length === 7));
    equal("reviewed status save still omits tags", curationRequests()[6]?.body, { curation_status: "compiled" });
    check("ordinary saves retain reviewed state", state.curation.reviewed && await page.isHidden("#confirm-classification"));
    const beforeEdit = overrides(state).length;
    await page.click("#v2-topics .chip.on[data-term='llm']");
    check("explicit tag edit uses its own override", await waitFor(() => overrides(state).length === beforeEdit + 1));
    check("tag edit never resends the old confirmed selection", curationRequests().length === 7);
    check("first save, failure and confirmation cause no model call", state.modelCalls === 0 && state.xSearchCalls === 0);
    check("first save and retry cause no page error", pageErrors.length === 0, pageErrors.join("; "));
  } finally {
    await page.close();
    server.close();
  }
}

// A v2 edit can commit before the detail read updates the v1 projection. In
// that window the old AI confirmation must stay unavailable, including through
// the keyboard shortcut and a failed override that is later discarded.
async function partG(browser) {
  const state = createMock();
  const server = await startMockServer(state);
  const base = `http://127.0.0.1:${server.address().port}`;
  const page = await browser.newPage({ viewport: { width: 1280, height: 860 } });
  const curationRequests = () => state.requests.filter((entry) => entry.path.endsWith("/curation"));
  try {
    await page.goto(`${base}/bookmarks/12?curation_status=all`, { waitUntil: "networkidle" });
    await openCuration(page);
    await page.waitForSelector("#confirm-classification:visible");
    state.delayNextMs = 400;
    await page.click("#v2-topics .chip.on[data-term='eng']");
    check("pending override immediately hides stale AI confirmation", await page.isHidden("#confirm-classification"));
    await page.keyboard.press("a");
    equal("confirmation shortcut cannot race a pending override", curationRequests().length, 0);
    check("override eventually commits", await waitFor(() => state.curation.reviewed));
    check("committed override leaves confirmation hidden", await waitFor(() => page.isHidden("#confirm-classification")));
    equal("committed override does not send legacy classification", curationRequests().length, 0);

    // A rejected edit remains blocked until the user explicitly discards it.
    // Once fresh detail arrives, the original unreviewed confirmation returns.
    state.curation.reviewed = false;
    state.forceConflict = true;
    await page.click("#v2-topics .chip.on[data-term='llm']");
    check("conflicted edit exposes discard", await waitFor(() => page.isVisible("#v2-conflict [data-conflict='discard']")));
    check("blocked edit keeps confirmation hidden", await page.isHidden("#confirm-classification"));
    await page.click("#v2-conflict [data-conflict='discard']");
    check("discard restores confirmation after fresh detail", await waitFor(() => page.isVisible("#confirm-classification")));
    equal("discard never confirms tags", curationRequests().length, 0);
  } finally {
    await page.close();
    server.close();
  }
}

async function partH(browser) {
  const state = createMock();
  const server = await startMockServer(state);
  const base = `http://127.0.0.1:${server.address().port}`;
  const page = await browser.newPage({ viewport: { width: 1280, height: 860 } });
  const confirms = () => state.requests.filter((entry) => entry.path.endsWith("/curation") && "classification" in entry.body);
  try {
    await page.goto(`${base}/bookmarks/12?curation_status=all`, { waitUntil: "networkidle" });
    await openCuration(page);
    await page.waitForSelector("#confirm-classification:visible");
    state.loseNextCurationResponse = true;
    await page.click("#confirm-classification");
    check("lost confirm response is shown as a failure", await waitFor(async () =>
      (await page.textContent("#save-state") || "").includes("确认失败")));
    check("failed confirmation keeps its action available", await page.isVisible("#confirm-classification"));
    await page.click("#confirm-classification");
    check("confirmation retry reaches Worker", await waitFor(() => confirms().length === 2));
    equal("confirmation retry reuses the operation key", confirms()[1]?.body.operation_key, confirms()[0]?.body.operation_key);
    equal("one logical confirmation is stored", state.curationOperations.size, 1);
    check("replayed confirmation is shown as reviewed", await waitFor(() => page.isHidden("#confirm-classification")));
  } finally {
    await page.close();
    server.close();
  }
}

async function partI(browser) {
  const state = createMock();
  const server = await startMockServer(state);
  const base = `http://127.0.0.1:${server.address().port}`;
  const page = await browser.newPage({ viewport: { width: 1280, height: 860 } });
  const confirms = () => state.requests.filter((entry) => entry.path.endsWith("/curation") && "classification" in entry.body);
  try {
    await page.goto(`${base}/bookmarks/12?curation_status=all`, { waitUntil: "networkidle" });
    await openCuration(page);
    await page.waitForSelector("#confirm-classification:visible");
    // Another client committed before this tab could confirm its old view.
    state.revision += 1;
    state.curation.reviewed = true;
    await page.click("#confirm-classification");
    check("stale confirmation reaches the version guard", await waitFor(() => confirms().length === 1));
    check("conflict refreshes the reviewed state", await waitFor(() => page.isHidden("#confirm-classification")));
    equal("stale confirmation did not append another write", state.curationOperations.size, 0);
  } finally {
    await page.close();
    server.close();
  }
}

async function partJ(browser) {
  const state = createMock();
  const server = await startMockServer(state);
  const base = `http://127.0.0.1:${server.address().port}`;
  const page = await browser.newPage({ viewport: { width: 1280, height: 860 } });
  try {
    await page.goto(`${base}/bookmarks/12?curation_status=all`, { waitUntil: "networkidle" });
    await openCuration(page);
    await page.waitForSelector("#confirm-classification:visible");
    state.delayNextCurationMs = 2000;
    await page.click("#confirm-classification");
    check("confirmation is in flight", await waitFor(() => state.requests.some((entry) =>
      entry.path.endsWith("/curation") && "classification" in entry.body)));
    await page.click("#v2-topics .chip.on[data-term='eng']");
    equal("tag edit waits for the pending confirmation", overrides(state).length, 0);
    check("confirmation eventually completes", await waitFor(() => page.isHidden("#confirm-classification")));
  } finally {
    await page.close();
    server.close();
  }
}

async function main() {
  const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || undefined, args: ["--no-sandbox"] });
  try {
    if (process.env.CAIRN_BROWSER_CASE === "reading") {
      await partE(browser);
    } else {
    await partA(browser);
    await partB(browser);
    await partC(browser);
    await partD(browser);
    await partE(browser);
    await partF(browser);
    await partG(browser);
    await partH(browser);
    await partI(browser);
    await partJ(browser);
    }
  } finally {
    await browser.close();
  }
  process.stdout.write(`\n${checks - failures}/${checks} browser checks passed\n`);
  process.exit(failures === 0 ? 0 : 1);
}

main().catch((error) => {
  process.stderr.write(`browser harness failed: ${error?.stack || error}\n`);
  process.exit(1);
});
