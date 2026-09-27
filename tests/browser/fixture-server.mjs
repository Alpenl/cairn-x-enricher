// A dependency-free emulation of the dashboard HTTP API over synthetic data.
//
// It serves the real embedded assets from internal/dashboard and answers the
// same /api routes the Go service exposes, so the UI can be previewed,
// screenshot-tested and exercised by Playwright with zero Worker, D1 or paid
// model calls. Run standalone for a local preview:
//
//   node tests/browser/fixture-server.mjs --port 8099
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { createBookmarks, fixtureImage, taxonomyV1, taxonomyV2 } from "./fixture-data.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const dashboardDir = path.resolve(here, "../../internal/dashboard");
const webDir = path.join(dashboardDir, "web");

const TYPES = {
  ".html": "text/html; charset=utf-8", ".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8", ".svg": "image/svg+xml", ".json": "application/json", ".png": "image/png",
  ".ico": "image/x-icon", ".webmanifest": "application/manifest+json"
};

const CURATION = new Set(["inbox", "kept", "compiled", "drop"]);
const SINGLE = new Set(["carriers", "form", "use"]);

function summaryOf(item) {
  const { v2, entities, classificationJob, original_text, translated_text, related_links, images, ...rest } = item;
  return { ...rest, related_links: [], images, content_loaded: false };
}

function publicDetail(item) {
  const { v2, entities, classificationJob, ...rest } = item;
  return rest;
}

function matchesSearch(item, terms) {
  const haystack = [item.url, item.note, item.ai_title, item.summary, item.translated_text, item.original_text, item.why,
    item.classification?.why_suggestion, ...(item.entities?.entities || [])].join("\n").toLowerCase();
  return terms.every((termValue) => haystack.includes(termValue.toLowerCase()));
}

function effective(item) {
  if (item.v2) return item.v2.selection;
  return { topics: item.classification?.topics || [], content_functions: [], carriers: [], affordances: [], form: item.classification?.form || "", use: item.classification?.use || "" };
}

function entityState(item) {
  return item.entities?.stale ? "stale" : item.entities?.state || "not_run";
}

export function createFixtureState(options = {}) {
  return {
    items: createBookmarks(options),
    requests: [],
    modelCalls: 0,
    v2: options.v2 !== false,
    operations: new Map(),
    latencyMs: options.latencyMs ?? 0
  };
}

function filterItems(state, params) {
  const terms = (params.get("q") || "").trim().split(/\s+/).filter(Boolean);
  let items = state.items.slice();
  const curation = params.get("curation_status");
  if (curation && curation !== "all") items = items.filter((item) => item.curation_status === curation);
  const source = params.get("source");
  if (source) items = items.filter((item) => item.source === source);
  if (params.get("uncertain") === "true") {
    items = items.filter((item) => !item.classification_reviewed && (!item.classification || item.classification.uncertainty));
  }
  const since = params.get("since");
  if (since) items = items.filter((item) => item.created_at >= new Date(since).toISOString());
  const legacyTopic = params.get("topic");
  if (legacyTopic) items = items.filter((item) => effective(item).topics.includes(legacyTopic));
  for (const key of ["topics", "content_functions", "carriers", "affordances"]) {
    const wanted = (params.get(key) || "").split(",").filter(Boolean);
    if (wanted.length) items = items.filter((item) => wanted.some((value) => effective(item)[key].includes(value)));
  }
  for (const key of ["form", "use"]) {
    const wanted = params.get(key);
    if (wanted) items = items.filter((item) => effective(item)[key] === wanted);
  }
  const entityStates = (params.get("entity_state") || "").split(",").filter(Boolean);
  if (entityStates.length) items = items.filter((item) => entityStates.includes(entityState(item)));
  if (terms.length) items = items.filter((item) => matchesSearch(item, terms));
  return items;
}

function countsOf(items) {
  const counts = { total: items.length, pending: 0, processing: 0, completed: 0, failed: 0, exhausted: 0, unsupported: 0 };
  for (const item of items) counts[item.status] = (counts[item.status] || 0) + 1;
  return counts;
}

async function readBody(request) {
  let raw = "";
  for await (const chunk of request) raw += chunk;
  if (!raw) return {};
  try { return JSON.parse(raw); } catch { return null; }
}

export function createFixtureHandler(state, { legacyPages = !existsSync(path.join(webDir, "index.html")) } = {}) {
  const assetRoot = legacyPages ? dashboardDir : webDir;

  async function serveFile(response, file) {
    try {
      const body = await readFile(file);
      response.writeHead(200, {
        "Content-Type": TYPES[path.extname(file)] || "application/octet-stream",
        "Cache-Control": "no-cache",
        "Content-Security-Policy": "default-src 'none'; connect-src 'self'; img-src 'self'; style-src 'self'; script-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
      });
      response.end(body);
    } catch {
      response.writeHead(404, { "Content-Type": "text/plain" });
      response.end("not found");
    }
  }

  return async function handle(request, response) {
    const url = new URL(request.url, "http://fixture.local");
    const route = url.pathname;
    const send = (code, body) => {
      response.writeHead(code, { "Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store" });
      response.end(JSON.stringify(body));
    };
    const body = ["POST", "PATCH", "PUT"].includes(request.method) ? await readBody(request) : {};
    if (body === null) return send(400, { error: "invalid_json" });
    if (route.startsWith("/api/")) {
      state.requests.push({ method: request.method, path: route, query: Object.fromEntries(url.searchParams), body });
      if (state.latencyMs) await new Promise((resolve) => setTimeout(resolve, state.latencyMs));
    }

    // Pages.
    if (request.method === "GET" && (route === "/" || /^\/bookmarks\/[1-9][0-9]*$/.test(route) || route === "/backstage")) {
      if (!legacyPages) return serveFile(response, path.join(webDir, "index.html"));
      const page = route === "/" ? "index.html" : route === "/backstage" ? "backstage.html" : "reader.html";
      return serveFile(response, path.join(dashboardDir, page));
    }
    const asset = route.match(/^\/assets\/(.+)$/);
    if (asset && request.method === "GET") {
      const file = path.normalize(path.join(assetRoot, asset[1]));
      if (!file.startsWith(assetRoot)) return send(404, { error: "not_found" });
      return serveFile(response, file);
    }

    // Images.
    const image = route.match(/^\/api\/images\/(.+)$/);
    if (image) {
      response.writeHead(200, { "Content-Type": "image/svg+xml", "Cache-Control": "private, no-store" });
      response.end(fixtureImage(decodeURIComponent(image[1])));
      return;
    }

    if (route === "/status") return send(200, { ready: true, state: "ok", build: { version: "fixture", commit: "0000000" } });
    if (route === "/api/taxonomy") return send(200, taxonomyV1());
    if (route === "/api/v2-taxonomy") return state.v2 ? send(200, taxonomyV2()) : send(200, { available: false, reason: "v2_unsupported" });
    if (route === "/api/extensions") return send(200, { entities: true, evidence: false, rerank: false, proposal: false, quality_verified: false });

    if (route === "/api/overview") {
      const all = state.items;
      const counts = countsOf(all);
      const views = { all: all.length };
      for (const name of CURATION) views[name] = all.filter((item) => item.curation_status === name).length;
      views.uncertain = all.filter((item) => !item.classification_reviewed && (!item.classification || item.classification.uncertainty)).length;
      return send(200, { views, counts, attention: counts.failed + counts.exhausted, queued: counts.pending + counts.processing });
    }

    if (route === "/api/bookmarks" && request.method === "GET") {
      const limit = Math.min(60, Math.max(1, Number(url.searchParams.get("limit") || 20)));
      const filtered = filterItems(state, url.searchParams);
      const counts = countsOf(filtered);
      let page = filtered;
      const before = Number(url.searchParams.get("before_id") || 0);
      if (before) page = page.filter((item) => item.id < before);
      const status = url.searchParams.get("status");
      if (status && status !== "all") page = page.filter((item) => item.status === status);
      const items = page.slice(0, limit);
      const summary = url.searchParams.get("view") === "summary";
      return send(200, {
        items: items.map((item) => (summary ? summaryOf(item) : publicDetail(item))),
        next_before_id: page.length > limit ? items.at(-1).id : null,
        counts,
        ...(url.searchParams.get("filter_contract_version") === "1" ? { filter_contract_version: 1 } : {})
      });
    }

    if (route === "/api/export") {
      const items = filterItems(state, url.searchParams).slice(0, Number(url.searchParams.get("limit") || 200));
      const lines = ["# Cairn 收藏导出", "", `共 ${items.length} 条；导出包含多维有效结果与人工来源，不调用模型。`, ""];
      for (const item of items) lines.push(`## ${item.url}`, "", `- 收藏 ID：${item.id}`, `- 整理状态：${item.curation_status}`, "");
      response.writeHead(200, { "Content-Type": "text/markdown; charset=utf-8", "Content-Disposition": "attachment; filename=\"cairn-export.md\"" });
      response.end(lines.join("\n"));
      return;
    }

    if (route === "/api/backstage") {
      const counts = countsOf(state.items);
      const attention = state.items.filter((item) => item.status === "failed" || item.status === "exhausted").map(summaryOf);
      const queued = counts.pending + counts.processing;
      return send(200, {
        title: attention.length ? `需要处理 ${attention.length} 条` : "一切正常",
        state: `最近一次实际处理领取 3 条，完成 2 条，失败 1 条。${queued ? `队列里还有 ${queued} 条在等待处理。` : ""}${attention.length ? `还有 ${attention.length} 条需要人工处理。` : ""}新收藏一般在几分钟内出现在列表里，平时不需要打开这一页。`,
        last_error: "", attention, attention_total: attention.length, counts, build: { version: "fixture", commit: "0000000" }
      });
    }

    if (route === "/api/bookmarks/process" && request.method === "POST") {
      const accepted = [];
      const rejected = [];
      for (const id of body.ids || []) {
        const item = state.items.find((entry) => entry.id === id);
        if (!item) rejected.push({ id, error: "not_found" });
        else if (item.status === "processing") rejected.push({ id, error: "job_busy" });
        else if (!item.processable) rejected.push({ id, error: "not_found" });
        else { item.status = "processing"; accepted.push(id); scheduleCompletion(state, item); }
      }
      return send(accepted.length ? 202 : 409, { accepted, rejected });
    }

    const match = route.match(/^\/api\/bookmarks\/([1-9][0-9]*)(?:\/([a-z0-9-]+))?$/);
    if (!match) return send(404, { error: "not_found" });
    const item = state.items.find((entry) => entry.id === Number(match[1]));
    if (!item) return send(404, { error: "not_found" });
    const action = match[2] || "";

    if (!action && request.method === "GET") return send(200, publicDetail(item));

    if (action === "curation" && request.method === "PATCH") {
      // Test hook: hold the next curation response so a UI race can be staged.
      if (state.delayNextCurationMs) {
        const delay = state.delayNextCurationMs;
        state.delayNextCurationMs = 0;
        await new Promise((resolve) => setTimeout(resolve, delay));
      }
      if ("why" in body) {
        if (typeof body.why !== "string" || [...body.why].length > 200) return send(400, { error: "invalid_curation" });
        item.why = body.why.trim();
      }
      if ("curation_status" in body) {
        if (!CURATION.has(body.curation_status)) return send(400, { error: "invalid_curation" });
        item.curation_status = body.curation_status;
      }
      if ("classification" in body) {
        if (body.classification === null) {
          item.classification_reviewed = false;
          if (item.v2) for (const key of ["topics", "form", "use"]) item.v2.selection[key] = structuredClone(item.v2.automatic[key]);
        } else {
          item.classification_reviewed = true;
          item.classification = { ...(item.classification || {}), ...body.classification };
          if (item.v2) {
            item.v2.selection.topics = [...body.classification.topics.slice(0, 3), ...item.v2.selection.topics.slice(3)];
            item.v2.selection.form = body.classification.form;
            item.v2.selection.use = body.classification.use;
            item.v2.revision++;
          }
        }
      }
      return send(200, publicDetail(item));
    }

    if (action === "v2-selection" && request.method === "GET") {
      if (!state.v2 || !item.v2) return send(200, { available: false, reason: "v2_unsupported" });
      return send(200, {
        available: true, selection: item.v2.selection, automatic: item.v2.automatic, revision: item.v2.revision,
        v1_projection: { topics: item.v2.selection.topics.slice(0, 3), form: item.v2.selection.form, use: item.v2.selection.use }
      });
    }

    if (action === "v2-override" && request.method === "POST") {
      if (!state.v2 || !item.v2) return send(409, { error: "v2_unsupported" });
      const key = body.operation_key;
      if (!key) return send(400, { error: "invalid_override" });
      if (state.operations.has(key)) return send(200, state.operations.get(key));
      if (body.expected_revision !== undefined && body.expected_revision !== item.v2.revision) {
        return send(409, { error: "revision_conflict", revision: item.v2.revision });
      }
      const selection = item.v2.selection;
      const field = body.field;
      if (!(field in selection)) return send(400, { error: "invalid_override" });
      const list = () => (Array.isArray(selection[field]) ? selection[field] : [selection[field]].filter(Boolean));
      const assign = (values) => { selection[field] = Array.isArray(selection[field]) ? values : values[0] || ""; };
      if (body.action === "accept") assign(SINGLE.has(field) ? [body.term] : [...new Set([...list(), body.term])]);
      else if (body.action === "reject") assign(list().filter((value) => value !== body.term));
      else if (body.action === "set_empty") assign([]);
      else if (body.action === "reset") selection[field] = structuredClone(item.v2.automatic[field]);
      else return send(400, { error: "invalid_override" });
      item.v2.revision++;
      item.classification_reviewed = true;
      const result = { id: item.id, field, term: body.term, action: body.action, operation_key: key, revision: item.v2.revision, replayed: false };
      state.operations.set(key, result);
      return send(200, result);
    }

    if (action === "v2-effective") {
      if (!item.v2) return send(200, { available: false });
      return send(200, { id: item.id, effective: { ...item.v2.selection, reviewed: item.classification_reviewed, entities: item.entities.entities }, projected: true, stale: false });
    }

    if (action === "evidence") {
      if (!item.original_text) return send(200, { available: false, reason: "no_snapshot" });
      return send(200, {
        available: true, current: true, truncated: false,
        snapshot: {
          blocks: [
            { id: "b1", role: "primary", url: item.url, text: item.original_text },
            ...(item.v2?.selection.carriers[0] === "author_continuation" ? [{ id: "b2", role: "author_continuation", text: "(2/2) The rest of the thread expands on the same point with concrete numbers." }] : []),
            ...(item.related_links.length ? [{ id: "b3", role: "external_article", url: item.related_links[0], text: "Linked article body excerpt used as supporting evidence." }] : [])
          ],
          fetched_at: item.created_at, retrieval: "x_search"
        }
      });
    }

    if (action === "classification-status") return send(200, item.classificationJob);

    if (action === "entities") {
      if (request.method === "GET") return send(200, { available: true, ...item.entities });
      if (body.expected_revision !== undefined && body.expected_revision !== item.entities.revision) {
        return send(409, { error: "revision_conflict", revision: item.entities.revision });
      }
      const entities = item.entities;
      if (body.action === "accept" && !entities.entities.includes(body.term)) { entities.entities.push(body.term); entities.human.push(body.term); }
      if (body.action === "reject") entities.entities = entities.entities.filter((value) => value !== body.term);
      entities.revision++;
      if (entities.entities.length) entities.state = "completed_nonempty";
      return send(200, { available: true, ...entities });
    }

    if (action === "retry-classification" && request.method === "POST") {
      item.classificationJob = { status: "pending", attempts: item.classificationJob.attempts, error: null };
      setTimeout(() => { item.classificationJob = { status: "completed", attempts: item.classificationJob.attempts + 1, error: null }; }, 3000);
      return send(200, { id: item.id, action: "retry_classification", model_calls: 0, detail: "只重新入分类队列；不抓取来源，不立即调用模型。" });
    }
    if (action === "refresh-source" && request.method === "POST") {
      return send(200, { id: item.id, action: "refresh_source", fetch: true, detail: "重新抓取原文；旧内容与人工整理在新内容到达前保持不变。" });
    }
    if (action === "replay-policy" && request.method === "POST") {
      if (!item.v2) return send(409, { error: "no_replayable_run" });
      if (body.commit) return send(200, { id: item.id, model_calls: 0, changed: [], committed: false, committed_reason: "写回未授权：需要服务端显式设置 CAIRN_ALLOW_DECISION_WRITE=1" });
      return send(200, { id: item.id, run_id: 1, model_calls: 0, changed: [], before: {}, after: {}, committed: false });
    }
    if (action === "source" && request.method === "POST") {
      if (!String(body.original_text || "").trim()) return send(409, { accepted: [], rejected: [{ id: item.id, error: "invalid_source" }] });
      item.status = "processing";
      scheduleCompletion(state, item, body.original_text);
      return send(202, { accepted: [item.id], rejected: [] });
    }
    return send(404, { error: "not_found" });
  };
}

function scheduleCompletion(state, item, source) {
  setTimeout(() => {
    item.status = "completed";
    item.attempts += 1;
    item.error = "";
    if (!item.ai_title) {
      item.ai_title = "重新处理后生成的中文标题";
      item.summary = "这条收藏已经重新读取，并生成了新的中文摘要。";
      item.translated_text = "重新生成的中文译文第一段。\n\n第二段。";
      item.original_text = source || "Re-fetched original text.";
    }
  }, 2500);
}

export function startFixtureServer({ port = 0, host = "127.0.0.1", state = createFixtureState(), legacyPages } = {}) {
  const handler = createFixtureHandler(state, legacyPages === undefined ? {} : { legacyPages });
  const server = createServer((request, response) => {
    handler(request, response).catch((error) => {
      response.writeHead(500, { "Content-Type": "application/json" });
      response.end(JSON.stringify({ error: String(error?.message || error) }));
    });
  });
  return new Promise((resolve) => server.listen(port, host, () => resolve({ server, state, url: `http://${host}:${server.address().port}` })));
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2);
  const flag = (name) => { const index = args.indexOf(name); return index >= 0 ? args[index + 1] : undefined; };
  const legacy = args.includes("--legacy") ? true : undefined;
  const { url } = await startFixtureServer({
    port: Number(flag("--port") || 8099),
    state: createFixtureState({ v2: !args.includes("--v1"), latencyMs: Number(flag("--latency") || 0) }),
    legacyPages: legacy
  });
  process.stdout.write(`fixture dashboard at ${url}\n`);
}
