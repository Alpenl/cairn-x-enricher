// Service status and the bookmarks that need a manual retry. It is a view in
// the same shell, reached from the status line at the bottom of the sidebar.
import { api, errorLabel, prepareSourceSubmission } from "./api.js";
import { byId, clear, h } from "./dom.js";
import { displayTitle, formatRelative, shortURL } from "./format.js";
import { icon } from "./icons.js";
import { emit, mergeItem, state } from "./store.js";
import { toast } from "./ui.js";

const REFRESH_INTERVAL = 15000;
const els = {};
let hooks = {};
let timer = 0;

function reasonFor(item) {
  if (item.status === "exhausted") return `已经试过 ${item.attempts} 次仍然失败${item.error ? `：${item.error}` : ""}`;
  if (item.next_retry_at) return `上次读取失败，${formatRelative(item.next_retry_at)}会自动重试${item.error ? `（${item.error}）` : ""}`;
  return item.error || "上次读取失败，稍后会自动重试";
}

function stat(label, value, tone = "") {
  return h("div.stat", { class: tone ? `stat-${tone}` : "" }, h("span.stat-value", String(value ?? 0)), h("span.stat-label", label));
}

function attentionRow(item) {
  const title = displayTitle(item);
  const retry = h("button.btn.btn-sm", { type: "button" }, icon("refresh", 14), "再试一次");
  const paste = h("button.btn.btn-sm", { type: "button" }, icon("clipboard", 14), "粘贴原文");
  const form = h("form.source-form", { hidden: true });
  const textarea = h("textarea.source-input", { name: "original_text", maxLength: 100000, rows: 7, placeholder: "粘贴原帖正文，会直接根据这段文字生成标题、译文和摘要" });
  const submit = h("button.btn.btn-sm.btn-primary", { type: "submit" }, "提交生成");
  const cancel = h("button.btn.btn-sm", { type: "button" }, "取消");
  let submission = null;
  form.append(textarea, h("div.source-actions", cancel, submit));

  retry.addEventListener("click", async () => {
    retry.disabled = true;
    try {
      const result = await api.process([item.id]);
      if (result.accepted.length) {
        clear(retry, icon("check", 14), "已提交");
        toast("已提交处理请求", { tone: "ok" });
        setTimeout(refresh, 900);
      } else {
        toast(errorLabel(result.rejected[0]?.error), { tone: "error" });
        retry.disabled = false;
      }
    } catch (error) {
      toast(`提交处理请求失败：${errorLabel(error?.message)}`, { tone: "error" });
      retry.disabled = false;
    }
  });
  paste.addEventListener("click", () => {
    form.hidden = !form.hidden;
    if (!form.hidden) textarea.focus();
  });
  cancel.addEventListener("click", () => { form.hidden = true; });
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const text = textarea.value;
    if (!text.trim()) {
      toast("原文不能为空", { tone: "error" });
      return;
    }
    submit.disabled = true;
    try {
      submission = await prepareSourceSubmission(item.id, text, submission);
      const result = await api.submitSource(item.id, submission);
      if (result.accepted.length) {
        toast("原文已保存，阅读内容已排队", { tone: "ok" });
        form.hidden = true;
        setTimeout(refresh, 900);
      } else {
        if (["input_changed", "operation_conflict"].includes(result.rejected[0]?.error)) submission = null;
        toast(errorLabel(result.rejected[0]?.error), { tone: "error" });
      }
    } catch (error) {
      toast(`提交原文失败：${errorLabel(error?.message)}`, { tone: "error" });
    } finally {
      submit.disabled = false;
    }
  });

  const open = h("a.attention-title", { href: hooks.href(item.id) }, title.raw ? shortURL(item.url) : title.text);
  open.addEventListener("click", (event) => {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) return;
    event.preventDefault();
    mergeItem(item);
    hooks.openItem(item.id);
  });
  return h("li.attention-item", { dataset: { id: String(item.id) } },
    h("div.attention-row",
      h("span.attention-icon", { class: item.status === "exhausted" ? "danger" : "warn" }, icon("alert", 16)),
      h("div.attention-body", open, h("p.attention-reason", reasonFor(item))),
      h("div.attention-actions", retry, paste)),
    form);
}

export async function refresh() {
  let summary;
  try {
    summary = await api.backstage();
  } catch (error) {
    clear(els.status, h("h2.status-title", "读取失败"),
      h("p.status-text", `无法读取 Cloudflare 后端（${errorLabel(error?.message)}），请检查网络和 CAIRN_ENRICHER_TOKEN。`));
    els.status.dataset.tone = "danger";
    return;
  }
  const counts = summary.counts || {};
  const attention = Array.isArray(summary.attention) ? summary.attention : [];
  const total = Number.isFinite(summary.attention_total) ? summary.attention_total : attention.length;
  const tone = /未就绪|错误/.test(summary.title || "") ? "danger" : total > 0 ? "warn" : "ok";
  els.status.dataset.tone = tone;
  clear(els.status,
    h("div.status-head", h("span.status-dot", { dataset: { tone } }), h("h2.status-title#back-title", summary.title || "一切正常")),
    h("p.status-text#back-state", summary.state || ""),
    summary.last_error ? h("p.status-error", icon("alert", 14), summary.last_error) : null);
  clear(els.stats,
    stat("全部收藏", counts.total),
    stat("已完成", counts.completed, "ok"),
    stat("排队中", counts.pending, counts.pending ? "work" : ""),
    stat("处理中", counts.processing, counts.processing ? "work" : ""),
    stat("失败待重试", counts.failed, counts.failed ? "warn" : ""),
    stat("需人工处理", counts.exhausted, counts.exhausted ? "danger" : ""),
    stat("非 X 链接", counts.unsupported));
  els.attentionHead.hidden = attention.length === 0;
  els.attentionHead.textContent = total > attention.length
    ? `需要你看一眼的 ${total} 条（显示最近 ${attention.length} 条）` : `需要你看一眼的 ${attention.length} 条`;
  els.list.replaceChildren(...attention.map(attentionRow));
  els.allGood.hidden = attention.length > 0;
  const build = summary.build || {};
  const commit = build.commit && build.commit !== "none" ? build.commit.slice(0, 8) : "local";
  els.foot.textContent = `cairn-x-enricher ${build.version || "dev"} · 提交 ${commit} · 共 ${counts.total ?? 0} 条收藏`;
  emit("backstage", summary);
}

export function showBackstage(visible) {
  clearInterval(timer);
  if (!visible) return;
  refresh();
  timer = setInterval(() => {
    if (!document.hidden && state.route.name === "backstage") refresh();
  }, REFRESH_INTERVAL);
}

export function initBackstage(options) {
  hooks = options;
  Object.assign(els, {
    root: byId("backstage-view"), status: byId("backstage-status"), stats: byId("backstage-stats"),
    attentionHead: byId("attention-head"), list: byId("attention-list"), allGood: byId("attention-empty"),
    foot: byId("backstage-foot")
  });
  byId("backstage-refresh").addEventListener("click", refresh);
}
