// Markdown export. The server export carries every effective v2 dimension and
// the human origin for the current filters; the local builder exports exactly
// the chosen bookmarks with their full text. Neither calls a model.
import { api } from "./api.js";
import { curationLabels, displayTitle } from "./format.js";
import { termLabel } from "./taxonomy.js";

// Yields to the event loop so a large export does not freeze the page: a
// full-text export can involve megabytes of string work on the UI thread.
function yieldToBrowser() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

const EXPORT_CHUNK_SIZE = 40;

function download(blob, name) {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = name;
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function today() {
  return new Date().toISOString().slice(0, 10);
}

export async function buildMarkdown(items, { hasMore = false, scope = "所选收藏", fetchDetail = (id) => api.detail(id), fetchEntities = (id) => api.entities(id) } = {}) {
  // Capture the list so a change of selection during a long export cannot mix
  // two different sets of bookmarks.
  items = items.slice();
  const escape = (value) => String(value || "").replace(/[\\`*_{}[\]()<>#!|]/g, "\\$&");
  const line = (value) => escape(value).replace(/[\r\n]+/g, " ");
  const link = (value) => {
    try {
      const url = new URL(value);
      return /^(https?:)$/.test(url.protocol) ? `<${url.href.replace(/[<>]/g, encodeURIComponent)}>` : "";
    } catch {
      return "";
    }
  };
  const lines = ["# Cairn 收藏摘录", "", `导出时间：${new Date().toISOString()}`, `条目数量：${items.length}`,
    `范围：${scope}${hasMore ? "（还有未加载的结果）" : ""}`, ""];
  for (let index = 0; index < items.length; index++) {
    const summary = items[index];
    // Summary rows hydrate only when exported. A failed read aborts the whole
    // export instead of silently producing a partial file.
    const item = summary.content_loaded === false || summary.original_text === undefined
      ? await fetchDetail(summary.id)
      : summary;
    const classification = item.classification || {};
    lines.push(`## ${line(displayTitle(item).text)}`, "", `收藏 ID：${item.id}`, `来源：${link(item.url)}`,
      `收藏时间：${line(item.created_at)}`, `整理状态：${curationLabels[item.curation_status || "inbox"]}`,
      `主题：${(classification.topics || []).map((id) => line(termLabel("topics", id))).join(" / ")}`,
      `形态：${line(termLabel("forms", classification.form || ""))}`, `用途：${line(termLabel("uses", classification.use || ""))}`,
      `分类确认：${item.classification_reviewed ? "已确认" : "未确认"}`, "");
    for (const [label, value] of [["收藏原因", item.why], ["收藏备注", item.note], ["用途建议（AI）", classification.why_suggestion],
      ["摘要", item.summary], ["中文全文", item.translated_text], ["原文", item.original_text]]) {
      if (value) lines.push(`### ${label}`, "", escape(value), "");
    }
    let entityView = null;
    try {
      entityView = await fetchEntities(item.id);
    } catch {
      entityView = null;
    }
    const entities = !entityView || entityView.available === false ? classification.entities || [] : entityView.entities || [];
    if (entities.length) lines.push(`实体：${entities.map(line).join(" / ")}`, "");
    if (item.related_links?.length) lines.push("### 相关链接", "", ...item.related_links.map((value) => `- ${link(value)}`), "");
    if ((index + 1) % EXPORT_CHUNK_SIZE === 0) await yieldToBrowser();
  }
  return lines.join("\n");
}

export async function exportItems(items, options = {}) {
  const markdown = await buildMarkdown(items, options);
  download(new Blob([markdown], { type: "text/markdown;charset=utf-8" }), `cairn-${today()}.md`);
}

// exportServer downloads the server-rendered export for the current filters.
// A backend without the endpoint reports export_unsupported so the caller can
// fall back to the local builder.
export async function exportServer(params) {
  const response = await fetch(`/api/export?${params}`, { cache: "no-store" });
  if (response.status === 404 || response.status === 405) throw new Error("export_unsupported");
  if (!response.ok) throw new Error("export_failed");
  download(await response.blob(), `cairn-${today()}.md`);
}
