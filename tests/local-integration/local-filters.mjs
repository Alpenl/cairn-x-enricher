import assert from "node:assert/strict";
import { localFilterResult } from "../../internal/dashboard/web/js/local-filters.js";

// GET-only: callable against the disposable real stack and the deployed NAS.
export async function verifyLocalFilters(baseURL) {
  const read = async path => {
    const response = await fetch(`${baseURL}${path}`, { headers: { "X-Cairn-Tag-System": "1", "X-Cairn-Topic-Granularity": "1" } });
    assert.equal(response.status, 200, path);
    return response.json();
  };
  const origin = new URLSearchParams({ view: "summary", limit: "60", include_cache_identity: "1", local_filter: "1" });
  const snapshot = await read(`/api/bookmarks?${origin}`);
  assert.equal(snapshot.local_filter_version, 1);
  assert.equal(snapshot.next_before_id, null, "this small-fixture comparison requires a complete page");
  const catalog = await read("/api/v2-taxonomy");
  const queries = [{}, { curation_status: "inbox" }, { curation_status: "kept" }];
  const modes = { topics: "topics_mode", resource_kinds: "resource_mode", content_functions: "functions_mode" };
  for (const [field, modeKey] of Object.entries(modes)) {
    const present = [...new Set(snapshot.items.flatMap(item => item.classification?.[field] || []))];
    const terms = [...new Set([...present, ...catalog[field].map(term => term.id)])].slice(0, 3);
    for (const term of terms) queries.push({ [field]: term });
    if (terms.length > 1) for (const mode of ["any", "all"]) queries.push({ [field]: terms.slice(0,2).join(","), [modeKey]: mode });
  }
  const specific = catalog.topics.find(term => term.granularity === "specific");
  if (specific) queries.push({ topic_refinements: specific.id }, { topics: catalog.topics.slice(0,2).map(term => term.id).join(","), topic_refinements: specific.id });
  const custom = snapshot.items.flatMap(item => item.custom_tags || []);
  for (const mode of ["any", "all"]) queries.push({ custom_tags: custom.length ? [...new Set(custom.map(tag => tag.id))].slice(0,2).join(",") : "00000000-0000-0000-0000-000000000000", custom_mode: mode });
  queries.push({ topics: catalog.topics[0].id, resource_kinds: catalog.resource_kinds[0].id, content_functions: catalog.content_functions[0].id });
  const normalizeCounts = value => Object.fromEntries(Object.entries(value).map(([key, entries]) => [key, Array.isArray(entries) ? entries.slice().sort((a,b) => a.id.localeCompare(b.id)) : entries]));
  for (const extra of queries) {
    const params = new URLSearchParams({ ...Object.fromEntries(origin), filter_contract_version: "1", ...extra });
    const actual = await read(`/api/bookmarks?${params}`);
    const local = localFilterResult("/api/bookmarks", params, origin, snapshot, catalog);
    assert.ok(local, JSON.stringify(extra));
    assert.deepEqual(local.items.map(item => item.id), actual.items.map(item => item.id), JSON.stringify(extra));
    assert.deepEqual(local.counts, actual.counts, JSON.stringify(extra));
    const counts = await read(`/api/tag-counts?${params}`);
    assert.deepEqual(normalizeCounts(localFilterResult("/api/tag-counts",params,origin,snapshot,catalog)), normalizeCounts(counts), JSON.stringify(extra));
  }
  console.log(`${queries.length} real Worker list/count comparisons passed (GET only)`);
  return queries.length;
}
