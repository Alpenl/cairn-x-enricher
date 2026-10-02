import assert from "node:assert/strict";
import { state, mergeItem, getItem, bodyCacheStats } from "./web/js/store.js";

const make = (id, size = 100_000) => ({ id, url: `https://example.com/${id}`, status: "completed", enriched_at: "2026-10-01",
  cache_identity: { schema_version: 1, content_revision: 1, body_revision: 1 }, content_loaded: true, original_text: "a".repeat(size), translated_text: "b".repeat(size),
  classification: { topics: ["ai_coding"] }, summary: "summary", why: "human reason" });
state.selectedId = 1;
mergeItem(make(1));
for (let id = 2; id <= 200; id++) mergeItem(make(id));
assert.ok(bodyCacheStats().items <= bodyCacheStats().max_items);
assert.ok(bodyCacheStats().bytes <= bodyCacheStats().max_bytes);
assert.equal(getItem(1).original_text.length, 100_000);
assert.equal(getItem(2).content_loaded, false);
assert.equal(getItem(2).summary, "summary");
assert.equal(getItem(2).why, "human reason");
assert.deepEqual(getItem(2).classification.topics, ["ai_coding"]);
console.log("ok   reading 200 articles bounds full bodies while preserving the open article and summary facts");

mergeItem({ ...make(1, 0), content_loaded: false, original_text: undefined, translated_text: undefined });
assert.equal(getItem(1).original_text.length, 100_000);
mergeItem({ ...make(1, 0), cache_identity: { schema_version: 1, content_revision: 1, body_revision: 2 }, content_loaded: false, original_text: undefined, translated_text: undefined });
assert.equal(getItem(1).content_loaded, false);
assert.equal(getItem(1).original_text, undefined);
console.log("ok   matching summary preserves a loaded body; a changed body revision invalidates it");

for (const identity of [undefined, { schema_version: 2, body_revision: 1 },
  { schema_version: 1, content_revision: 1, body_revision: -1 },
  { schema_version: 1, content_revision: 2, body_revision: 1 }]) {
  mergeItem(make(1));
  mergeItem({ ...make(1, 0), cache_identity: identity, content_loaded: false, original_text: undefined, translated_text: undefined });
  assert.equal(getItem(1).content_loaded, false);
  assert.equal(getItem(1).original_text, undefined);
}
console.log("ok   absent, invalid, unsupported and changed source versions cannot reuse an older body");

for (const content_revision of [undefined, -1, 1.5, Number.MAX_SAFE_INTEGER + 1]) {
  const invalid = { ...make(1), cache_identity: { schema_version: 1, content_revision, body_revision: 1 } };
  mergeItem(invalid);
  mergeItem({ ...invalid, content_loaded: false, original_text: undefined, translated_text: undefined });
  assert.equal(getItem(1).content_loaded, false);
  assert.equal(getItem(1).original_text, undefined);
}
console.log("ok   matching but invalid source revisions do not establish body identity");

state.selectedId = 500;
mergeItem(make(500, 3_000_000));
assert.equal(bodyCacheStats().items, 1);
assert.equal(getItem(500).original_text.length, 3_000_000);
state.selectedId = 501;
mergeItem(make(501));
assert.ok(bodyCacheStats().bytes <= bodyCacheStats().max_bytes);
assert.equal(getItem(500).content_loaded, false);
console.log("ok   an oversized open article is released on the next selection");
