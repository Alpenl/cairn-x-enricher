import assert from "node:assert/strict";
import { topicSections, primaryTags, matchesTopic } from "./web/js/topic-presentation.js";
import { apiParams, parseQuery, buildQuery, toggleValue, clearFacets } from "./web/js/query.js";

const catalog = [
  { id: "image_creation", label: "图像生成", granularity: "broad", navigation: true },
  { id: "portrait", label: "写真", granularity: "specific", navigation: false, aliases: ["个人写真"], recall_terms: ["AI写真"] },
  { id: "avatar", label: "头像", granularity: "specific", navigation: false },
  { id: "old", label: "旧主题", active: false, granularity: "specific", navigation: false }
];
const selection = { topics: ["image_creation", "portrait", "avatar", "manual_four", "manual_five", "manual_six"], resource_kinds: ["skill"] };
const tags = primaryTags(selection, [{ id: "personal", label: "自定义" }], catalog);
assert.deepEqual(tags.slice(0, 3).map((tag) => tag.id), ["portrait", "avatar", "image_creation"]);
assert.equal(tags.length, 8);
assert.equal(selection.topics[0], "image_creation");
assert.deepEqual(topicSections(catalog, new Set(), new Set(), new Map()).flatMap((s) => s.terms.map((t) => t.id)), ["image_creation"]);
assert.deepEqual(topicSections(catalog, new Set(["image_creation"]), new Set(), new Map([["portrait", 2], ["avatar", 0]]))
  .find((s) => s.id === "specific").terms.map((t) => t.id), ["portrait"]);
assert.equal(matchesTopic(catalog[1], "个人写真"), true);
assert.equal(matchesTopic(catalog[1], "AI写真"), false);
assert.equal(topicSections(catalog, new Set(), new Set(["portrait"]), new Map())[0].id, "pinned");
assert.equal(topicSections(catalog, new Set(["old"]), new Set(), new Map()).flatMap((s) => s.terms).some((t) => t.id === "old"), true);
const original = parseQuery("?topics=image_creation,design&curation_status=inbox").filters;
const refined = toggleValue(original, "topic_refinements", "portrait");
assert.equal(refined.topics, original.topics);
assert.equal(refined.topics_mode, "");
assert.equal(parseQuery(buildQuery(refined, "照片")).filters.topic_refinements, "portrait");
assert.equal(apiParams(refined, "照片").get("topic_refinements"), "portrait");
assert.equal(toggleValue(refined, "topic_refinements", "portrait").topics, original.topics);
assert.equal(clearFacets(refined).topic_refinements, "");
console.log("Topic presentation, semantic aliases, and independent refinement query checks passed");
