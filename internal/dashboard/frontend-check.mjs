// Frontend checks for the embedded dashboard assets.
//
// Uses only Node's standard library so CI needs no package installation. It
// runs each page script against a minimal DOM shim and asserts the behaviour
// the performance work depends on: cached formatters, memoised date and URL
// helpers, safe highlight alignment, and async export chunking.
//
// Run with: node frontend-check.mjs [asset-directory]

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const assetDir = process.argv[2] ? process.argv[2] : join(here, "..");
const read = (name) => readFileSync(join(assetDir, name), "utf8");

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

// --- Minimal DOM shim -------------------------------------------------------
// Enough of the DOM for the asset scripts to load and for the helper functions
// under test to run. Deliberately tiny and dependency-free.

class ClassList {
  constructor() { this.set = new Set(); }
  add(...names) { for (const name of names) this.set.add(name); }
  remove(...names) { for (const name of names) this.set.delete(name); }
  toggle(name, force) { (force ?? !this.set.has(name)) ? this.set.add(name) : this.set.delete(name); }
  contains(name) { return this.set.has(name); }
}

class Node {
  constructor(tag) {
    this.tagName = String(tag || "").toUpperCase();
    this.children = [];
    this.parentNode = null;
    this.attributes = {};
    this.dataset = {};
    this.style = {};
    this.classList = new ClassList();
    this.listeners = {};
    this._text = "";
    this.hidden = false;
    this.disabled = false;
    this.value = "";
    this.checked = false;
    this.type = "";
    this.href = "";
    this.src = "";
    this.alt = "";
    this.title = "";
    this.id = "";
    this._className = "";
    this.loading = "";
    this.decoding = "";
    this.timer = 0;
  }
  // element() and the render path assign className directly, so keep classList
  // in sync or selector matching silently finds nothing.
  get className() { return this._className; }
  set className(value) {
    this._className = String(value);
    this.classList = new ClassList();
    for (const name of this._className.split(/\s+/)) if (name) this.classList.add(name);
  }
  get textContent() {
    if (this.children.length === 0) return this._text;
    return this.children.map((child) => child.textContent).join("");
  }
  set textContent(value) { this._text = String(value); this.children = []; }
  get childElementCount() { return this.children.length; }
  get firstChild() { return this.children[0] || null; }
  append(...nodes) {
    for (const node of nodes) {
      // Appending a DocumentFragment moves its children, leaving the fragment
      // empty. The render path relies on this to batch insertions, so the shim
      // must model it rather than keeping the fragment as a child.
      if (node instanceof DocumentFragment) {
        const moved = node.children;
        node.children = [];
        for (const child of moved) {
          child.parentNode = this;
          this.children.push(child);
        }
        continue;
      }
      const child = typeof node === "string" ? new Text(node) : node;
      child.parentNode = this;
      this.children.push(child);
    }
  }
  appendChild(node) { this.append(node); return node; }
  replaceChildren(...nodes) { this.children = []; this.append(...nodes); }
  replaceWith(node) {
    if (!this.parentNode) return;
    const index = this.parentNode.children.indexOf(this);
    if (index >= 0) { node.parentNode = this.parentNode; this.parentNode.children[index] = node; }
  }
  remove() { this.replaceWith(new Node("#removed")); }
  setAttribute(name, value) { this.attributes[name] = String(value); }
  getAttribute(name) { return this.attributes[name] ?? null; }
  addEventListener(type, handler) { (this.listeners[type] ||= []).push(handler); }
  removeEventListener() {}
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
  querySelectorAll(selector) {
    const out = [];
    const matches = (node, sel) => {
      if (sel.startsWith(".")) return node.classList.contains(sel.slice(1));
      if (sel.endsWith(":checked")) return node.checked;
      return node.tagName === sel.toUpperCase();
    };
    const walk = (node) => {
      for (const child of node.children) {
        if (matches(child, selector)) out.push(child);
        walk(child);
      }
    };
    walk(this);
    return out;
  }
  closest() { return null; }
  click() { for (const handler of this.listeners.click || []) handler({ preventDefault() {}, currentTarget: this }); }
  focus() {}
  getBoundingClientRect() { return { top: 0, left: 0, width: 0, height: 0 }; }
}

class Text extends Node {
  constructor(value) { super("#text"); this._text = String(value); }
}

class DocumentFragment extends Node {
  constructor() { super("#fragment"); }
}

function buildWindow() {
  const document = new Node("#document");
  // byId is a Map for internal wiring; getById is the lookup used by callers.
  const byId = new Map();
  const register = (id, node) => { node.id = id; byId.set(id, node); return node; };

  // Elements the asset scripts look up on load.
  for (const id of [
    "stream", "feature", "feature-band", "feature-band-count", "result-count", "empty", "loading", "tail",
    "sentinel", "find", "find-form", "clear-filters", "export-markdown", "backstage-link", "load-error",
    "retry-load", "toast", "filter-curation_status", "filter-topic", "filter-form", "filter-use",
    "filter-source", "filter-since", "filter-uncertain", "read-back", "read-export", "read-process",
    "read-source", "read-figures", "read-why", "read-entities", "read-meta", "read-title", "read-body",
    "curation-fields", "curation-why", "curation-status", "curation-topics", "curation-form-value",
    "curation-use", "curation-dirty", "topic-count", "why-suggestion", "why-suggestion-block",
    "reset-classification", "confirm-classification", "curation-error", "back-title", "back-state", "back-attention", "back-counts",
    "back-error", "back-refresh", "back-source-dialog", "back-source-form", "back-source-text",
    "back-source-title", "back-source-id", "back-source-error", "back-build",
  ]) register(id, new Node("div"));

  const form = register("find-form", new Node("form"));
  form.addEventListener = Node.prototype.addEventListener.bind(form);
  byId.get("feature-band").append(new Node("span"), new Node("i"), byId.get("feature-band-count"));
  byId.get("find-form").append(byId.get("find"));
  byId.get("filter-uncertain").checked = false;

  // Auto-create unknown IDs so the shim keeps working as the pages evolve,
  // while still returning the same node for repeated lookups.
  const autoById = (id) => {
    if (!byId.has(id)) byId.set(id, register(id, new Node("div")));
    return byId.get(id);
  };

  const window = {
    location: { pathname: "/", search: "", href: "https://nas.local/" },
    document: {
      ...document,
      createElement: (tag) => new Node(tag),
      createTextNode: (value) => new Text(value),
      createDocumentFragment: () => new DocumentFragment(),
      getElementById: autoById,
      querySelector: () => null,
      querySelectorAll: () => [],
      body: new Node("body"),
      hidden: false,
      addEventListener() {},
    },
    innerWidth: 1280,
    scrollY: 0,
    history: { replaceState() {}, pushState() {} },
    setTimeout: (fn, ms) => setTimeout(fn, ms),
    clearTimeout: (id) => clearTimeout(id),
    setInterval: () => 0,
    clearInterval() {},
    requestAnimationFrame: (fn) => setTimeout(fn, 0),
    Intl,
    URL,
    Blob: class { constructor(parts) { this.parts = parts; } },
    URLSearchParams,
    Option: class extends Node {
      constructor(text, value) { super("option"); this.textContent = text; this.value = value; }
    },
    AbortController,
    IntersectionObserver: class { observe() {} disconnect() {} },
    fetch: async () => ({ ok: true, status: 200, json: async () => ({}) }),
    addEventListener() {},
  };
  document.getElementById = autoById;
  window.window = window;
  window.globalThis = window;
  window.URL.createObjectURL = () => "blob:noop";
  window.URL.revokeObjectURL = () => {};
  return { window, elements: byId, byId: autoById };
}

function loadScript(window, source) {
  const names = Object.keys(window);
  const values = names.map((name) => window[name]);
  // eslint-disable-next-line no-new-func
  const fn = new Function(...names, `${source}\n//# sourceURL=asset.js`);
  fn(...values);
}

// reader.js parses the bookmark ID out of the path and dereferences the
// elements it finds, so it needs a bookmark URL and its own window.
function buildReaderWindow() {
  const built = buildWindow();
  built.window.location.pathname = "/bookmarks/12";
  return built.window;
}

// --- Checks -----------------------------------------------------------------

const { window } = buildWindow();

// Every asset must parse. A syntax error would otherwise only surface in the
// browser after deploy.
for (const asset of ["common.js", "home.js", "backstage.js"]) {
  try {
    loadScript(window, read(asset));
    check(`${asset} parses`, true);
  } catch (error) {
    check(`${asset} parses`, false, error.message);
  }
}
{
  const readerWindow = buildReaderWindow();
  try {
    loadScript(readerWindow, read("common.js"));
    loadScript(readerWindow, read("reader.js"));
    check("reader.js parses", true);
  } catch (error) {
    check("reader.js parses", false, error.message);
  }
  // Polling must be bounded, so a stuck job cannot poll the LAN server forever
  // for as long as the tab stays open.
  check("reader bounds its polling attempts", /MAX_POLLS/.test(read("reader.js")));
}

const ui = window.CairnUI;
if (!ui) {
  process.stdout.write("FAIL common.js did not expose CairnUI\n");
  process.exit(1);
}

// Date helpers: cached formatters must still honour formatting rules.
equal("formatDate empty", ui.formatDate(""), "-");
equal("formatDate invalid", ui.formatDate("nope"), "-");
check("formatDate formats", ui.formatDate("2026-09-11T00:00:00Z").includes("2026"), ui.formatDate("2026-09-11T00:00:00Z"));
{
  // Two instants on the same local day must render identically. Local time is
  // used deliberately: the formatter renders in the viewer's timezone.
  const noon = new Date(2026, 8, 11, 12, 0, 0);
  const evening = new Date(2026, 8, 11, 20, 0, 0);
  check("formatDate same local day is stable", ui.formatDate(noon.toISOString()) === ui.formatDate(evening.toISOString()));
}
check(
  "formatDate different days differ",
  ui.formatDate("2026-01-01T00:00:00Z") !== ui.formatDate("2026-06-01T00:00:00Z"),
);
check("formatDateTime formats", ui.formatDateTime("2026-09-11T12:30:00Z").length > 0);
{
  // Distinct instants must not collide in the memo cache, and identical ones
  // must reuse the cached string.
  const a = new Date(2026, 8, 11, 9, 0, 0).toISOString();
  const b = new Date(2026, 8, 11, 21, 45, 0).toISOString();
  check("formatDateTime distinguishes instants", ui.formatDateTime(a) !== ui.formatDateTime(b),
    `${ui.formatDateTime(a)} vs ${ui.formatDateTime(b)}`);
  check("formatDateTime is stable for one instant", ui.formatDateTime(a) === ui.formatDateTime(a));
  check("formatDate still collapses one local day", ui.formatDate(a) === ui.formatDate(b));
}

// Buckets: the memo key is the calendar day, so boundaries must still hold.
equal("bucket invalid", ui.bucketLabel(""), "更早");
equal("bucket today", ui.bucketLabel(new Date().toISOString()), "今天");
equal("bucket 3 days", ui.bucketLabel(new Date(Date.now() - 3 * 864e5).toISOString()), "近七天");
equal("bucket 10 days", ui.bucketLabel(new Date(Date.now() - 10 * 864e5).toISOString()), "近三十天");
check("bucket 60 days", ui.bucketLabel(new Date(Date.now() - 60 * 864e5).toISOString()).length > 0);

// shortURL: cached parsing must not change results.
equal("shortURL strips www", ui.shortURL("https://www.example.com/a/b?q=1"), "example.com/a/b");
equal("shortURL repeated", ui.shortURL("https://www.example.com/a/b?q=1"), "example.com/a/b");
equal("shortURL invalid", ui.shortURL("not a url"), "not a url");

// termLabel must degrade to the raw id before the vocabulary loads.
equal("termLabel unknown", ui.termLabel("topics", "missing"), "missing");

// metadata must tolerate sparse records, which the list renders while loading.
check("metadata sparse", ui.metadata({ id: 1, url: "https://x.com/a/status/1" }).childElementCount > 0);
check("metadata empty", ui.metadata({}).childElementCount > 0);

// Export must be awaitable so callers can keep the UI responsive.
const exported = ui.exportMarkdown([{ id: 1, url: "https://x.com/a/status/1", ai_title: "标题", summary: "摘要" }]);
check("exportMarkdown returns a promise", exported && typeof exported.then === "function");
await exported;
check("exportMarkdown resolves", true);

// --- Rendered output --------------------------------------------------------
// The optimisations live in the render path, so assert on the DOM the pages
// actually build rather than only on exported helpers.

function collectText(node, out = []) {
  // Elements created with textContent keep their text on the node itself
  // rather than in a child text node, so collect both.
  if (node.tagName === "#text") { out.push(node.textContent); return out; }
  const own = node.children.length === 0 ? node.textContent : "";
  if (own) out.push(own);
  for (const child of node.children) collectText(child, out);
  return out;
}

function collectTags(node, out = []) {
  if (node.tagName !== "#text") out.push(node.tagName);
  for (const child of node.children) collectTags(child, out);
  return out;
}

async function renderHome(items, query = "") {
  const { window: w, byId: lookup } = buildWindow();
  w.location.search = query;
  w.fetch = async (url) => {
    const value = String(url);
    if (value.startsWith("/api/taxonomy")) {
      return { ok: true, status: 200, json: async () => ({
        version: "v1",
        topics: [{ id: "llm", label: "LLM", aliases: [], active: true }],
        forms: [{ id: "tool", label: "工具", aliases: [], active: true }],
        uses: [{ id: "try", label: "待试", aliases: [], active: true }],
      }) };
    }
    if (value.startsWith("/api/bookmarks")) {
      return { ok: true, status: 200, json: async () => ({ items, next_before_id: null, counts: { total: items.length, completed: items.length } }) };
    }
    return { ok: true, status: 200, json: async () => ({}) };
  };
  loadScript(w, read("common.js"));
  loadScript(w, read("home.js"));
  // Allow the initial fetch and taxonomy load to settle.
  for (let i = 0; i < 12; i++) await new Promise((resolve) => setTimeout(resolve, 0));
  return { window: w, byId: lookup };
}

const renderItem = (id) => ({
  id,
  url: `https://x.com/user/status/${id}`,
  note: "收藏备注",
  created_at: new Date().toISOString(),
  status: "completed",
  curation_status: "kept",
  classification_reviewed: true,
  classification: { topics: ["llm"], form: "tool", use: "try", entities: ["项目甲"], uncertainty: false, why_suggestion: "建议" },
  ai_title: "人工智能生成的测试中文标题",
  summary: "这是一段测试摘要内容。",
  translated_text: "完整中文译文。",
  original_text: "source text",
  related_links: [],
  images: [],
});

{
  // The first four items become the highlighted feature strip, so use more than
  // that to exercise the compact stream path too.
  const items = Array.from({ length: 7 }, (_, i) => renderItem(i + 1));
  const { byId } = await renderHome(items);
  const stream = byId("stream");
  const cards = stream.querySelectorAll(".item");
  check("home renders cards into the stream", cards.length > 0, `${cards.length} cards`);
  check("home renders the bucket band", stream.querySelectorAll(".band").length > 0);
  check("home renders titles", collectText(stream).some((text) => text.includes("人工智能生成的测试中文标题")));
  check("home keeps features out of the stream", stream.querySelectorAll(".fcard").length === 0);
}

{
  // The search path builds highlighted results, which is where the UTF-16
  // alignment guard matters.
  const { byId } = await renderHome(
    [{ ...renderItem(3), ai_title: "测试搜索命中标题", summary: "包含关键词的摘要" }],
    "?q=%E5%85%B3%E9%94%AE%E8%AF%8D",
  );
  const stream = byId("stream");
  const marks = stream.querySelectorAll("mark");
  check("search highlights matches", marks.length > 0, `${marks.length} marks`);
  check("highlight marks carry the matched text", marks.every((mark) => mark.textContent.includes("关键词")));
}

{
  // Offscreen work: a page with no results must not render placeholder cards.
  const { byId } = await renderHome([]);
  check("empty result renders no cards", byId("stream").querySelectorAll(".item").length === 0);
  check("empty state is revealed", byId("empty").hidden === false);
}

// The error map must cover every code the backend can return.
{
  const { window: w, byId } = buildWindow();
  w.location.pathname = "/bookmarks/12";
  byId("read-original").hidden = true;
  let item = { ...renderItem(12), status: "processing", images: [{ key: "image.png" }] };
  let poll;
  w.setInterval = (callback) => { poll = callback; };
  w.fetch = async (url) => ({ ok: true, json: async () => String(url) === "/api/bookmarks/12" ? { ...item }
    : String(url) === "/api/taxonomy" ? { topics: [], forms: [], uses: [] }
      : { items: [] } });
  loadScript(w, read("common.js"));
  loadScript(w, read("reader.js"));
  const settle = async () => { for (let i = 0; i < 10; i++) await Promise.resolve(); };
  await settle();
  equal("reader defers collapsed original paragraphs", byId("read-original").children.length, 0);
  const body = byId("read-body").firstChild;
  const figure = byId("read-figures").firstChild;
  poll();
  await settle();
  check("reader preserves unchanged body nodes during polling", byId("read-body").firstChild === body);
  check("reader preserves unchanged images during polling", byId("read-figures").firstChild === figure);
  byId("read-toggle").click();
  equal("reader expands original text on demand", byId("read-original").textContent, item.original_text);
  item = { ...item, original_text: "new original", translated_text: "新译文" };
  poll();
  await settle();
  equal("reader refreshes changed visible translation", byId("read-body").textContent, "新译文");
  equal("reader refreshes changed expanded original", byId("read-original").textContent, "new original");
}

{
  const { window: w } = buildWindow();
  const requests = [];
  let downloaded = "";
  let fail = false;
  w.Blob = class { constructor(parts) { downloaded = parts.join(""); } };
  w.fetch = async (url) => {
    requests.push(url);
    if (fail) return { ok: false, status: 503, json: async () => ({ error: "backend_error" }) };
    return { ok: true, json: async () => ({ ...renderItem(9), original_text: "exported full source", translated_text: "导出的完整译文" }) };
  };
  loadScript(w, read("common.js"));
  await w.CairnUI.exportMarkdown([{ id: 9, content_loaded: false }]);
  equal("summary export hydrates the selected detail", requests[0], "/api/bookmarks/9");
  check("summary export includes both full languages", downloaded.includes("exported full source") && downloaded.includes("导出的完整译文"));
  fail = true;
  downloaded = "";
  let rejected = false;
  try { await w.CairnUI.exportMarkdown([{ id: 9, content_loaded: false }]); } catch { rejected = true; }
  check("failed hydration cannot produce a partial export", rejected && downloaded === "");
}

// Saving a reason or a curation status is not a review of the AI tags. Only an
// explicit tag edit or an explicit confirmation may send `classification`.
for (const [label, act] of [
  ["why", (byId) => { byId("curation-why").value = "只是因为有趣"; byId("curation-form").listeners.submit[0]({ preventDefault() {} }); }],
  ["status", (byId) => { byId("curation-status").value = "kept"; byId("curation-status").listeners.change[0]({}); byId("curation-form").listeners.submit[0]({ preventDefault() {} }); }],
  ["why+status", (byId) => { byId("curation-why").value = "x"; byId("curation-status").value = "drop"; byId("curation-form").listeners.submit[0]({ preventDefault() {} }); }],
]) {
  const { window: w, byId } = buildWindow();
  w.location.pathname = "/bookmarks/12";
  byId("read-original").hidden = true;
  const item = { ...renderItem(12), status: "completed", classification_reviewed: false };
  const patches = [];
  w.fetch = async (url, options) => {
    if (String(url) === "/api/bookmarks/12") return { ok: true, json: async () => ({ ...item }) };
    if (String(url) === "/api/taxonomy") {
      return { ok: true, json: async () => ({ topics: [{ id: "llm", label: "LLM", active: true }], forms: [{ id: "tool", label: "工具", active: true }], uses: [{ id: "try", label: "待试", active: true }] }) };
    }
    if (String(url).includes("/curation")) {
      patches.push(JSON.parse(options.body));
      return { ok: true, json: async () => ({ ...item, curation_status: JSON.parse(options.body).curation_status || item.curation_status }) };
    }
    return { ok: true, json: async () => ({ items: [] }) };
  };
  loadScript(w, read("common.js"));
  loadScript(w, read("reader.js"));
  const settle = async () => { for (let i = 0; i < 20; i++) await Promise.resolve(); };
  await settle();
  act(byId);
  await settle();
  equal(`reader sends one patch for a ${label}-only save`, patches.length, 1);
  check(`${label}-only save omits classification`, !("classification" in patches[0]), JSON.stringify(patches[0]));
}

// Explicitly touching a tag is a human choice and must be submitted.
{
  const { window: w, byId } = buildWindow();
  w.location.pathname = "/bookmarks/12";
  byId("read-original").hidden = true;
  const item = { ...renderItem(12), status: "completed", classification_reviewed: false };
  const patches = [];
  w.fetch = async (url, options) => {
    if (String(url) === "/api/bookmarks/12") return { ok: true, json: async () => ({ ...item }) };
    if (String(url) === "/api/taxonomy") {
      return { ok: true, json: async () => ({ topics: [{ id: "llm", label: "LLM", active: true }], forms: [{ id: "tool", label: "工具", active: true }], uses: [{ id: "try", label: "待试", active: true }] }) };
    }
    if (String(url).includes("/curation")) {
      patches.push(JSON.parse(options.body));
      return { ok: true, json: async () => ({ ...item, classification_reviewed: true }) };
    }
    return { ok: true, json: async () => ({ items: [] }) };
  };
  loadScript(w, read("common.js"));
  loadScript(w, read("reader.js"));
  const settle = async () => { for (let i = 0; i < 20; i++) await Promise.resolve(); };
  await settle();
  // The rendered topic checkbox for llm is checked because the AI suggested it;
  // unchecking it is an explicit rejection.
  const checkbox = byId("curation-topics").children[0].children[0];
  checkbox.checked = false;
  byId("curation-topics").listeners.change[0]({});
  byId("curation-form").listeners.submit[0]({ preventDefault() {} });
  await settle();
  equal("explicit tag edit sends one patch", patches.length, 1);
  check("explicit tag edit sends classification", "classification" in patches[0], JSON.stringify(patches[0]));
  equal("explicit tag edit keeps the reason field", patches[0].why, "");
}

// An explicit "confirm these tags" click sends the labels unchanged.
{
  const { window: w, byId } = buildWindow();
  w.location.pathname = "/bookmarks/12";
  byId("read-original").hidden = true;
  const item = { ...renderItem(12), status: "completed", classification_reviewed: false };
  const patches = [];
  w.fetch = async (url, options) => {
    if (String(url) === "/api/bookmarks/12") return { ok: true, json: async () => ({ ...item }) };
    if (String(url) === "/api/taxonomy") {
      return { ok: true, json: async () => ({ topics: [{ id: "llm", label: "LLM", active: true }], forms: [{ id: "tool", label: "工具", active: true }], uses: [{ id: "try", label: "待试", active: true }] }) };
    }
    if (String(url).includes("/curation")) {
      patches.push(JSON.parse(options.body));
      return { ok: true, json: async () => ({ ...item, classification_reviewed: true }) };
    }
    return { ok: true, json: async () => ({ items: [] }) };
  };
  loadScript(w, read("common.js"));
  loadScript(w, read("reader.js"));
  const settle = async () => { for (let i = 0; i < 20; i++) await Promise.resolve(); };
  await settle();
  check("confirm button is offered before review", byId("confirm-classification").hidden === false);
  byId("confirm-classification").click();
  await settle();
  equal("explicit confirmation sends one patch", patches.length, 1);
  check("explicit confirmation sends classification", "classification" in patches[0], JSON.stringify(patches[0]));
  equal("explicit confirmation keeps the AI topics", patches[0].classification.topics, ["llm"]);
}

for (const code of ["job_busy", "not_found", "backend_error", "queue_full", "invalid_ids", "invalid_source", "invalid_curation", "invalid_id", "invalid_query", "invalid_json", "invalid_content_type"]) {
  check(`errorLabel(${code})`, ui.errorLabel(code) !== code, ui.errorLabel(code));
}
equal("errorLabel unknown is shown verbatim", ui.errorLabel("brand_new_code"), "brand_new_code");

process.stdout.write(`\n${checks - failures}/${checks} checks passed\n`);
process.exit(failures === 0 ? 0 : 1);
