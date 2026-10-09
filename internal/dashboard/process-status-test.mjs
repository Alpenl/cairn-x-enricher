import assert from "node:assert/strict";
import { failureReason, processingPaused } from "./web/js/process-status.js";
assert.equal(processingPaused({ ready: false, degraded_components: { source: "paused", reading: "paused" } }), true);
for (const health of [{ ready: true, degraded_components: { source: "paused" } }, { ready: false }, { ready: false, degraded_components: { classification: "failed", source: "paused" } }]) assert.equal(processingPaused(health), false);
const past = { status: "failed", next_retry_at: "2026-01-01T00:00:00Z" };
assert.match(failureReason(past), /已到重试时间/);
assert.doesNotMatch(failureReason(past), /前会|会自动重试/);
assert.match(failureReason({ ...past, original_text: "正文" }), /正文已保存.*阅读增强未完成/);
assert.match(failureReason({ ...past, error: "[recovered_source] HTTP 502" }), /正文已保存/);
assert.match(failureReason({ ...past, paid_call_unresolved: true, paid_stage: "reading" }), /正文已保存.*自动重试已暂停/);
assert.doesNotMatch(failureReason({ ...past, paid_call_unresolved: true }), /正文已保存|已到重试时间/);
console.log("Processing status regressions passed");

assert.match(failureReason({status:"pending", error:"capture_required", paid_call_unresolved:true}), /等待浏览器插件采集原文/);
