// API cache safety checks with controlled responses and clock, no live backend.
import assert from "node:assert/strict";
import { api, invalidateQueryReads } from "./web/js/api.js";
import { emit, mergeItem, state } from "./web/js/store.js";

const realFetch = globalThis.fetch;
const realNow = Date.now;
let now = realNow();
let holdWrite = null;
let holdRead = null;
let oversized = false;
const calls = [];
Date.now = () => now;
globalThis.fetch = async (path, options = {}) => {
  calls.push({ path, method: options.method || "GET", priority: options.priority });
  if (options.method && holdWrite) await holdWrite.promise;
  if (!options.method && holdRead) await holdRead.promise;
  return new Response(JSON.stringify(options.method ? { saved: true } : {
    items: [{ id: 501, classification: { topics: ["llm"] } }], counts: { total: 1 }, filter_contract_version: 1,
    ...(oversized ? { body: "x".repeat(2_100_000) } : {})
  }), { status: 200, headers: { "Content-Type": "application/json" } });
};
const params = new URLSearchParams({ topics: "llm", topics_mode: "any", view: "summary", limit: "40", filter_contract_version: "1" });
const read = (query = params) => api.list(query, undefined, { reuse: true });
const checked = (label) => process.stdout.write(`ok   ${label}\n`);
const gate = () => {
  let release;
  const promise = new Promise((resolve) => { release = resolve; });
  return { promise, release };
};
try {
  const first = await read();
  first.items[0].classification.topics.push("changed-locally");
  assert.deepEqual((await read()).items[0].classification.topics, ["llm"]);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].priority, "high");
  checked("exact queries reuse isolated response copies and list reads receive high priority");

  const reordered = new URLSearchParams([...params.entries()].reverse());
  await read(reordered);
  assert.equal(calls.length, 1);
  for (const [key, value] of [["topics_mode", "all"], ["q", "different"], ["source", "wechat"], ["before_id", "400"], ["limit", "20"], ["custom_tags", "stable-id"]]) {
    const distinct = new URLSearchParams(params); distinct.set(key, value);
    const before = calls.length; await read(distinct); assert.equal(calls.length, before + 1);
  }
  checked("cache keys include every mode, search, source, page size, cursor and custom filter");

  const beforeExpiry = calls.length;
  now += 10_001;
  await read();
  assert.equal(calls.length, beforeExpiry + 1);
  await api.list(params);
  assert.equal(calls.length, beforeExpiry + 2);
  checked("ten-second expiry and explicit fresh reads bypass reusable snapshots");

  const controller = new AbortController(); controller.abort();
  await assert.rejects(api.list(params, controller.signal, { reuse: true }), { name: "AbortError" });
  const beforeFailure = calls.length;
  globalThis.fetch = async () => { throw new Error("disconnected"); };
  const missed = new URLSearchParams({ q: "failed" });
  await assert.rejects(read(missed), { message: "network_error" });
  globalThis.fetch = realFetch;
  // Restore the controlled responder after asserting failures are not stored.
  globalThis.fetch = async (path, options = {}) => {
    calls.push({ path, method: options.method || "GET" });
    if (options.method && holdWrite) await holdWrite.promise;
    if (!options.method && holdRead) await holdRead.promise;
    return new Response(JSON.stringify(options.method ? {} : { items: [], ...(oversized ? { body: "x".repeat(2_100_000) } : {}) }));
  };
  await read(missed);
  assert.equal(calls.length, beforeFailure + 1);
  checked("aborted callers and failed reads never receive or populate successful snapshots");

  invalidateQueryReads();
  for (let index = 0; index < 12; index++) await read(new URLSearchParams({ q: `entry-${index}` }));
  assert.ok(api.cacheStats().query_items <= 10);
  assert.ok(api.cacheStats().query_bytes <= 4 * 1024 * 1024);
  oversized = true;
  const large = new URLSearchParams({ q: "large" });
  const beforeLarge = calls.length; await read(large); await read(large);
  assert.equal(calls.length, beforeLarge + 2);
  oversized = false;
  checked("query snapshots stay within ten entries and four MiB; large search results are not retained");

  state.items.clear();
  const item = { id: 501, status: "completed", enriched_at: "2026-09-30T00:00:00Z", classification: { topics: ["llm"] },
    cache_identity: { content_revision: 1, personal_revision: 0, latest_decision_id: 1, latest_entity_revision: 0 } };
  mergeItem(item); await read();
  const beforeBody = calls.length;
  mergeItem({ ...item, original_text: "full article", content_loaded: true });
  await read(); assert.equal(calls.length, beforeBody);
  mergeItem({ ...item, classification: { topics: ["design"] }, cache_identity: { ...item.cache_identity, latest_decision_id: 2 } });
  await read(); assert.equal(calls.length, beforeBody + 1);
  checked("full-text hydration preserves cache; classification and identity changes discovered by detail invalidate it");

  for (const action of [() => api.createCustomTag({ label: "Project" }), () => api.renameCustomTag("custom-id", { label: "New" }), () => api.archiveCustomTag("custom-id", {})]) {
    await read(); holdWrite = gate();
    const mutation = action();
    assert.equal(api.cacheStats().query_items, 0);
    await read(); assert.ok(api.cacheStats().query_items > 0);
    holdWrite.release(); await mutation; holdWrite = null;
    assert.equal(api.cacheStats().query_items, 0);
  }
  checked("create, rename and archive invalidate globally both before and after each custom-tag write");

  invalidateQueryReads(); holdRead = gate();
  const oldRead = read();
  await api.curation(501, { curation_status: "kept" });
  holdRead.release(); await oldRead; holdRead = null;
  assert.equal(api.cacheStats().query_items, 0);
  await read(); emit("tags:changed", 501);
  assert.equal(api.cacheStats().query_items, 0);
  checked("reads started before a write cannot refill cache; tag events invalidate snapshots too");
  process.stdout.write("8 query-cache safety checks passed\n");
} finally {
  globalThis.fetch = realFetch; Date.now = realNow; invalidateQueryReads(); state.items.clear();
}
