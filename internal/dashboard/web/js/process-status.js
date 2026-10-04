import { formatRelative } from "./format.js";

export function processingPaused(health) {
  const components = Object.keys(health.degraded_components || {});
  return health.ready === false && components.length > 0 && components.every(key => key === "source" || key === "reading");
}

export function failureReason(item, now = Date.now()) {
  const archived = Boolean(item.original_text || item.paid_stage === "reading" || item.error?.startsWith("[recovered_source]"));
  const prefix = archived ? "正文已保存，可直接阅读。" : "";
  if (item.paid_call_unresolved) return prefix + "上次模型调用结果尚未核对，自动重试已暂停。";
  if (item.status === "exhausted") return prefix + `后续处理已失败 ${item.attempts || 0} 次，可以重试或补充原文。`;
  const failed = archived ? "阅读增强未完成。" : "上次读取失败。";
  const retryAt = Date.parse(item.next_retry_at);
  if (Number.isFinite(retryAt)) return prefix + failed + (retryAt <= now
    ? "已到重试时间，等待后台恢复处理。" : `${formatRelative(item.next_retry_at)}可再次尝试，具体时间取决于后台状态。`);
  return prefix + failed;
}
