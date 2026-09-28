// Frontend checks for the embedded dashboard application.
//
// Uses only Node's standard library so CI needs no package installation. The
// pure modules (formatting, URL state, error labels, Markdown export) are
// imported and exercised directly; every module is syntax-checked; and static
// guards catch a broken deploy (missing asset, unresolved import) or an unsafe
// pattern (HTML injection, inline script/style the CSP would block).
// Behaviour that needs a real DOM is covered by tests/browser/run.mjs.
//
// Run with: node frontend-check.mjs [dashboard-directory]
import { readdirSync, readFileSync, existsSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(process.argv[2] || here);
const webDir = existsSync(join(root, "web")) ? join(root, "web") : root;
const jsDir = join(webDir, "js");
const read = (name) => readFileSync(join(webDir, name), "utf8");
const load = (name) => import(pathToFileURL(join(jsDir, name)).href);

let failures = 0;
let checks = 0;

function check(label, condition, detail = "") {
  checks++;
  if (condition) {
    process.stdout.write(`ok   ${label}\n`);
    return;
  }
  failures++;
  process.stdout.write(`FAIL ${label}${detail ? `: ${detail}` : ""}\n`);
}

function equal(label, got, want) {
  check(label, JSON.stringify(got) === JSON.stringify(want), `got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
}

// --- Every module parses ------------------------------------------------------

const modules = readdirSync(jsDir).filter((name) => name.endsWith(".js")).sort();
for (const name of modules) {
  // theme-boot.js is a classic script; everything else is an ES module.
  const result = spawnSync(process.execPath, ["--check", join(jsDir, name)], { encoding: "utf8" });
  check(`${name} parses`, result.status === 0, result.stderr.trim().split("\n")[0]);
}

// --- Static guards ----------------------------------------------------------------

const html = read("index.html");
for (const [, asset] of html.matchAll(/(?:src|href)="\/assets\/([^"]+)"/g)) {
  check(`index.html asset /assets/${asset} exists`, existsSync(join(webDir, asset)));
}
check("index.html loads the application module", /<script type="module" src="\/assets\/js\/main\.js"><\/script>/.test(html));
check("index.html has no inline script the CSP would block", !/<script(?![^>]*\bsrc=)[^>]*>/.test(html));
check("index.html has no inline style attribute the CSP would block", !/\sstyle="/.test(html));

const exportsOf = new Map();
const sources = new Map();
for (const name of modules) {
  const source = readFileSync(join(jsDir, name), "utf8");
  sources.set(name, source);
  exportsOf.set(name, new Set([...source.matchAll(/export (?:async )?(?:function|const|let|class) (\w+)/g)].map((match) => match[1])));
}
for (const [name, source] of sources) {
  for (const [, list, target] of source.matchAll(/import \{([^}]+)\} from "\.\/([\w-]+\.js)"/g)) {
    check(`${name} imports an existing ${target}`, exportsOf.has(target));
    for (const imported of list.split(",").map((value) => value.trim()).filter(Boolean)) {
      check(`${name} imports ${imported} exported by ${target}`, exportsOf.get(target)?.has(imported));
    }
  }
  for (const [, alias, target] of source.matchAll(/import \* as (\w+) from "\.\/([\w-]+\.js)"/g)) {
    for (const [, member] of source.matchAll(new RegExp(`\\b${alias}\\.(\\w+)\\(`, "g"))) {
      check(`${name} calls ${alias}.${member} exported by ${target}`, exportsOf.get(target)?.has(member));
    }
  }
  // User content must only reach the page as text.
  check(`${name} never injects HTML`, !/\.(innerHTML|outerHTML)\s*=|insertAdjacentHTML|document\.write/.test(source));
  check(`${name} never sets an inline style attribute`, !/setAttribute\(\s*["']style["']/.test(source));
}
check("reader polling is bounded", /MAX_POLLS/.test(sources.get("detail.js") || ""));
check("classification status polling is bounded", /statusPolls\+\+ >= \d+/.test(sources.get("diagnostics.js") || ""));

// --- Formatting -------------------------------------------------------------------

const format = await load("format.js");
equal("formatDate empty", format.formatDate(""), "-");
equal("formatDate invalid", format.formatDate("nope"), "-");
check("formatDate formats", format.formatDate("2026-09-11T00:00:00Z").includes("2026"));
{
  // Local time is used deliberately: the formatter renders in the viewer's zone.
  const noon = new Date(2026, 8, 11, 12, 0, 0);
  const evening = new Date(2026, 8, 11, 20, 0, 0);
  check("formatDate same local day is stable", format.formatDate(noon.toISOString()) === format.formatDate(evening.toISOString()));
  const a = new Date(2026, 8, 11, 9, 0, 0).toISOString();
  const b = new Date(2026, 8, 11, 21, 45, 0).toISOString();
  check("formatDateTime distinguishes instants", format.formatDateTime(a) !== format.formatDateTime(b));
  check("formatDateTime is stable for one instant", format.formatDateTime(a) === format.formatDateTime(a));
}
check("formatDate different days differ", format.formatDate("2026-01-01T00:00:00Z") !== format.formatDate("2026-06-01T00:00:00Z"));
{
  const now = new Date(2026, 8, 27, 15, 0, 0);
  const at = (days, hour = 10) => new Date(2026, 8, 27 - days, hour, 0, 0).toISOString();
  equal("bucket invalid", format.bucketLabel("", now), "更早");
  equal("bucket today", format.bucketLabel(at(0), now), "今天");
  equal("bucket yesterday", format.bucketLabel(at(1), now), "昨天");
  equal("bucket 3 days", format.bucketLabel(at(3), now), "近七天");
  equal("bucket 10 days", format.bucketLabel(at(10), now), "近三十天");
  check("bucket 60 days names the month", /2026/.test(format.bucketLabel(at(60), now)));
  check("listTime today is a clock time", /\d{2}:\d{2}/.test(format.listTime(at(0, 9), now)));
  equal("listTime yesterday", format.listTime(at(1), now), "昨天");
  equal("listTime this week", format.listTime(at(4), now), "4 天前");
  equal("listTime this year", format.listTime(new Date(2026, 1, 3).toISOString(), now), "2月3日");
  equal("listTime earlier year", format.listTime(new Date(2024, 1, 3).toISOString(), now), "2024/2/3");
}
equal("shortURL strips www", format.shortURL("https://www.example.com/a/b?q=1"), "example.com/a/b");
equal("shortURL repeated", format.shortURL("https://www.example.com/a/b?q=1"), "example.com/a/b");
equal("shortURL invalid", format.shortURL("not a url"), "not a url");
equal("sourceLine names an X handle", format.sourceLine("https://x.com/karpathy/status/1"), "@karpathy");
equal("sourceLine names WeChat", format.sourceLine("https://mp.weixin.qq.com/s/abc"), "微信公众号");
equal("displayTitle falls back to the URL", format.displayTitle({ url: "https://x.com/a/status/1" }), { text: "x.com/a/status/1", raw: true });
equal("displaySummary explains a pending item", format.displaySummary({ status: "pending" }).wait, true);
check("needsReview for an uncertain suggestion", format.needsReview({ classification: { uncertainty: true } }));
check("needsReview is false once reviewed", !format.needsReview({ classification_reviewed: true, classification: { uncertainty: true } }));
equal("highlight finds every term", format.highlightRanges("Prompt 评估集 and 评估", ["评估", "prompt"]), [[0, 6], [7, 9], [15, 17]]);
equal("highlight prefers the longer term at one position", format.highlightRanges("评估集", ["评估", "评估集"]), [[0, 3]]);
// Lowercasing "İ" changes the string length, which would shift every span.
equal("highlight refuses misaligned text", format.highlightRanges("İstanbul 评估", ["评估"]), []);
equal("highlight without terms", format.highlightRanges("text", []), []);
{
  const excerpt = format.searchExcerpt({ summary: "无关", original_text: `${"x".repeat(80)} needle here` }, ["needle"]);
  check("searchExcerpt reaches into the original text", excerpt.text.includes("needle") && excerpt.text.startsWith("…"), excerpt.text);
}
equal("paragraphs drop blank lines", format.paragraphsOf("a\n\n b \n"), ["a", "b"]);

// --- URL state ---------------------------------------------------------------------

const query = await load("query.js");
{
  const bare = query.parseQuery("");
  equal("bare landing opens the inbox", bare.filters.curation_status, "inbox");
  equal("a link without status means every status", query.parseQuery("?topics=llm").filters.curation_status, "all");
  equal("legacy topic URL becomes the full topic filter", query.parseQuery("?topic=design&topics=llm").filters.topics, "llm,design");
  equal("bare inbox builds the bare URL", query.buildQuery(bare.filters, ""), "");
  const round = query.parseQuery(query.buildQuery({ ...bare.filters, curation_status: "kept", topics: "llm,eval" }, "评估"));
  equal("filters survive a URL round trip", [round.filters.curation_status, round.filters.topics, round.search], ["kept", "llm,eval", "评估"]);
  equal("all view is explicit in the URL", query.buildQuery({ ...query.emptyFilters(), curation_status: "all" }, ""), "?curation_status=all");
}
{
  const filters = { ...query.emptyFilters(), curation_status: "all", topics: "llm", source: "x" };
  const params = query.apiParams(filters, "", { limit: 40, beforeId: 99 });
  equal("browsing uses the summary representation", params.get("view"), "summary");
  check("the all view sends no status filter", !params.has("curation_status"));
  equal("multidimensional filters negotiate the contract", params.get("filter_contract_version"), "1");
  equal("paging carries the cursor", params.get("before_id"), "99");
  const search = query.apiParams({ ...query.emptyFilters(), curation_status: "inbox" }, "关键词");
  check("search reads full rows for excerpts", !search.has("view") && search.get("q") === "关键词" && search.get("curation_status") === "inbox");
  check("form and use also require the contract", query.needsFilterContract({ ...query.emptyFilters(), form: "method" }));
  check("status and source alone do not require the contract", !query.needsFilterContract({ ...query.emptyFilters(), curation_status: "kept", source: "x" }));
}
{
  const inbox = { ...query.emptyFilters(), curation_status: "inbox" };
  equal("activeView inbox", query.activeView(inbox), "inbox");
  const uncertain = query.withView(inbox, "uncertain");
  equal("uncertain view spans every status", [uncertain.curation_status, uncertain.uncertain], ["all", "true"]);
  equal("activeView uncertain", query.activeView(uncertain), "uncertain");
  equal("leaving the uncertain view drops its filter", query.withView(uncertain, "kept").uncertain, "");
  const toggled = query.toggleValue(query.toggleValue(inbox, "topics", "llm"), "topics", "eval");
  equal("same-dimension values accumulate", toggled.topics, "llm,eval");
  equal("toggling again removes a value", query.toggleValue(toggled, "topics", "llm").topics, "eval");
  equal("single-valued filters replace", query.toggleValue({ ...inbox, source: "x" }, "source", "wechat").source, "wechat");
  equal("facet count ignores the view", query.facetFilterCount({ ...toggled, source: "x" }), 3);
  equal("clearing facets keeps the view", query.clearFacets({ ...toggled, source: "x" }), { ...query.emptyFilters(), curation_status: "inbox" });
  check("a kept item leaves the inbox view", !query.matchesStatusView({ curation_status: "kept" }, inbox));
  check("a kept item stays in the all view", query.matchesStatusView({ curation_status: "kept" }, { ...inbox, curation_status: "all" }));
  check("a reviewed item leaves the uncertain view", !query.matchesStatusView({ curation_status: "inbox", classification_reviewed: true }, uncertain));
}
{
  const now = new Date(2026, 8, 27, 15, 0);
  equal("since presets are local midnights", query.sinceLabel(query.sinceDaysAgo(7, now), now), "近 7 天");
  equal("since today", query.sinceLabel(query.sinceDaysAgo(0, now), now), "今天");
  equal("date input round trip", query.dateInputFromSince(query.sinceFromDateInput("2026-09-01")), "2026-09-01");
  equal("invalid date input clears the filter", query.sinceFromDateInput("2026-13-99x"), "");
}

// --- Error labels -------------------------------------------------------------------

const { api, errorLabel } = await load("api.js");
for (const code of ["job_busy", "not_found", "backend_error", "queue_full", "invalid_ids", "invalid_source", "invalid_curation",
  "invalid_id", "invalid_query", "invalid_json", "invalid_content_type", "revision_conflict", "unsupported_filter_contract", "lease_conflict",
  "manual_queue_full", "invalid_operation_key"]) {
  check(`errorLabel(${code})`, errorLabel(code) !== code, errorLabel(code));
}
equal("errorLabel unknown is shown verbatim", errorLabel("brand_new_code"), "brand_new_code");
{
  const originalFetch = globalThis.fetch;
  const keys = [];
  globalThis.fetch = async (_path, options) => {
    keys.push(JSON.parse(options.body).operation_keys["7"]);
    if (keys.length === 1) throw new Error("lost response");
    return { ok: true, json: async () => ({ accepted: [7], rejected: [] }) };
  };
  try {
    await api.process([7]).catch(() => {});
    await api.process([7]);
    check("manual process reuses its operation key after a lost response", keys.length === 2 && keys[0] === keys[1]);
  } finally { globalThis.fetch = originalFetch; }
}
{
  const originalFetch = globalThis.fetch;
  const keys = [];
  globalThis.fetch = async (_path, options) => {
    keys.push(JSON.parse(options.body).operation_key);
    if (keys.length === 1) throw new Error("lost response");
    return { ok: true, json: async () => ({ id: 8, action: "refresh_source" }) };
  };
  try {
    await api.refreshSource(8).catch(() => {});
    await api.refreshSource(8);
    check("source refresh reuses its operation key after a lost response", keys.length === 2 && keys[0] === keys[1]);
  } finally { globalThis.fetch = originalFetch; }
}

// --- Markdown export ---------------------------------------------------------------------

const { buildMarkdown } = await load("export.js");
{
  const requests = [];
  const markdown = await buildMarkdown([{ id: 9, content_loaded: false }], {
    fetchDetail: async (id) => {
      requests.push(id);
      return { id, url: "https://x.com/a/status/9", ai_title: "标题", original_text: "exported full source", translated_text: "导出的完整译文", classification: { topics: ["llm"] } };
    },
    fetchEntities: async () => ({ available: true, entities: ["Acme"] })
  });
  equal("summary export hydrates the selected detail", requests, [9]);
  check("export includes both full languages", markdown.includes("exported full source") && markdown.includes("导出的完整译文"));
  check("export lists current entities", markdown.includes("实体：Acme"));
  check("export escapes Markdown in titles", (await buildMarkdown([{ id: 1, url: "https://x.com/1", ai_title: "a *b* [c](d)", original_text: "" }], {
    fetchEntities: async () => ({ available: false })
  })).includes("a \\*b\\* \\[c\\]\\(d\\)"));
  let rejected = false;
  try {
    await buildMarkdown([{ id: 9, content_loaded: false }], { fetchDetail: async () => { throw new Error("backend_error"); } });
  } catch {
    rejected = true;
  }
  check("failed hydration cannot produce a partial export", rejected);
  const unsafe = await buildMarkdown([{ id: 2, url: "javascript:alert(1)", original_text: "", related_links: ["javascript:alert(1)", "https://ok.example/x"] }], {
    fetchEntities: async () => ({ available: false })
  });
  check("export never emits a non-http link", !unsafe.includes("javascript:") && unsafe.includes("<https://ok.example/x>"));
}

process.stdout.write(`\n${checks - failures}/${checks} checks passed\n`);
process.exit(failures === 0 ? 0 : 1);
