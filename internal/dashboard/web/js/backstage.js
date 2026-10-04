// Service status and the bookmarks that need a manual retry. It is a view in
// the same shell, reached from the status line at the bottom of the sidebar.
import { api, errorLabel, prepareSourceSubmission } from "./api.js";
import { byId, clear, h } from "./dom.js";
import { displayTitle, shortURL } from "./format.js";
import { icon } from "./icons.js";
import { emit, mergeItem, state } from "./store.js";
import { toast } from "./ui.js";
import { failureReason } from "./process-status.js";

const REFRESH_INTERVAL = 15000;
const els = {};
let hooks = {};
let timer = 0;
let refreshFlight = null, refreshDue = 0, refreshDelay = REFRESH_INTERVAL, refreshSignature = "";
let qualityLoaded = false;
let qualityBusy = false;
let recoveryBusy = false;

async function loadQuality() {
  if (qualityBusy) return;
  qualityBusy = true;
  const target = byId("tag-quality-report");
  const button = byId("tag-quality-refresh");
  button.disabled = true;
  try {
    const report = await api.tagQuality();
    if (report.available === false || report.version !== 1) { clear(target, h("p.muted", "后端暂不支持标签统计。")); return; }
    qualityLoaded = true;
    const coverage = report.coverage || {};
    clear(target, h("p.muted", `${coverage.human_operations || 0} 次纠正操作 · ${coverage.human_facts || 0} 条明确操作事实 · ${coverage.unknown_facts || 0} 条来源不明确的事实`));
    const terms = [...(report.terms || [])].sort((a, b) =>
      (b.rejections + b.additions) - (a.rejections + a.additions) || b.current_count - a.current_count).slice(0, 20);
    const table = h("table.quality-table", h("thead", h("tr", ...["标签", "当前收藏", "补加", "移除", "确认"].map((name) => h("th", name)))));
    table.append(h("tbody", terms.map((term) => h("tr", h("td", term.label || term.term_id),
      ...[term.current_count, term.additions, term.rejections, term.confirmations].map((value) => h("td", String(value ?? 0)))))));
    target.append(h("div.quality-table-scroll", table));
    const dimensions = { topics: "主题", resource_kinds: "资源类型", content_functions: "内容特征" };
    for (const dimension of report.retrieval?.dimensions || []) {
      target.append(h("p.muted", `${dimensions[dimension.dimension] || dimension.dimension}：${dimension.tagged_links} 条有标签，${dimension.distinct_terms} 种标签；最常用标签覆盖 ${dimension.largest_term_count} / ${report.retrieval.total_links} 条收藏。`));
    }
    const labels = new Map((report.terms || []).map((term) => [term.tag_ref, term.label]));
    for (const pair of (report.confusion_pairs || []).slice(0, 8)) {
      target.append(h("p.muted", `同次操作的移除 → 补加：${labels.get(pair.from_tag_ref) || pair.from_tag_ref} → ${labels.get(pair.to_tag_ref) || pair.to_tag_ref}（${pair.count} 次）`));
    }
    target.append(h("p.muted", "这些统计用于发现候选问题，不自动调整阈值或修改收藏。"));
  } catch { clear(target, h("p.error", "标签统计暂时不可用，请稍后刷新。")); }
  finally { qualityBusy = false; button.disabled = false; }
}

function reasonFor(item) {
  return failureReason(item);
}

function stat(label, value, tone = "") {
  return h("div.stat", { class: tone ? `stat-${tone}` : "" }, h("span.stat-value", String(value ?? 0)), h("span.stat-label", label));
}

function attentionRow(item) {
  const title = displayTitle(item);
  const retry = h("button.btn.btn-sm", { type: "button" }, icon("refresh", 14), "再试一次");
  retry.disabled = Boolean(item.paid_call_unresolved);
  if (retry.disabled) retry.title = "先核对上次模型调用，避免重复计费";
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

function recoveryPanel(summary) {
  const check = summary.recovery;
  if (!check || (!summary.processing_paused && check.state === "healthy")) return null;
  const labels = { unavailable: "模型接口暂时无法连接", timeout: "模型检查超时", unauthorized: "模型接口认证失败", rate_limited: "模型接口暂时限流", budget_exhausted: "当天模型调用保护额度已用完", invalid_response: "模型返回内容未通过检查" };
  const date = (ms) => new Date(ms).toLocaleString("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" });
  const description = check.state === "checking" ? "正在检查模型接口，通过后继续处理排队任务。" : check.state === "healthy" ? "自检已通过，正在恢复自动处理。" : `${labels[check.reason] || "等待检查模型接口"}。${check.next_check_at > Date.now() ? `下次自动检查：${date(check.next_check_at)}。` : "即将自动检查。"}`;
  const button = h("button.btn.btn-sm#service-recover", { type: "button" }, icon("refresh", 14), check.state === "checking" ? "正在检查…" : "手动恢复");
  button.disabled = recoveryBusy || !check.can_recover;
  const hint = h("p.muted", check.manual_after > Date.now() && check.state !== "checking" ? `可再次操作时间：${date(check.manual_after)}` : "会提前检查一次模型接口；不会重跑已完成收藏或清空调用记录。");
  button.addEventListener("click", async () => {
    if (recoveryBusy) return;
    recoveryBusy = true; button.disabled = true;
    try {
      const result = await api.recoverService();
      toast((result.accepted || result.state === "pending") ? "已提交恢复检查，通过后自动继续处理" : result.state === "checking" ? "检查正在进行，无需重复提交" : "请稍候再试", { tone: result.accepted ? "ok" : "info" });
      refreshSignature = ""; refreshDelay = REFRESH_INTERVAL;
    } catch (error) { toast(`恢复请求失败：${errorLabel(error?.message)}`, { tone: "error" }); }
    finally { recoveryBusy = false; await refresh(); }
  });
  return h("div.service-recovery", h("p.status-text", description), button, hint);
}

export function refresh() {
  if (refreshFlight) return refreshFlight;
  refreshFlight = loadSummary().finally(() => { refreshFlight = null; refreshDue = Date.now() + refreshDelay; });
  return refreshFlight;
}
async function loadSummary() {
  emit("backstage:loading");
  let summary;
  try {
    summary = await api.backstage();
    const signature = JSON.stringify(summary);
    refreshDelay = signature === refreshSignature ? Math.min(60_000, refreshDelay * 2) : REFRESH_INTERVAL;
    refreshSignature = signature;
  } catch (error) {
    refreshDelay = Math.min(60_000, refreshDelay * 2);
    clear(els.status, h("h2.status-title", "读取失败"),
      h("p.status-text", `无法读取 Cloudflare 后端（${errorLabel(error?.message)}），请检查网络和 CAIRN_ENRICHER_TOKEN。`));
    els.status.dataset.tone = "danger";
    return;
  }
  const counts = summary.counts || {};
  const attention = Array.isArray(summary.attention) ? summary.attention : [];
  const total = Number.isFinite(summary.attention_total) ? summary.attention_total : attention.length;
  const tone = /未就绪|错误/.test(summary.title || "") ? "danger" : /暂停/.test(summary.title || "") || total > 0 ? "warn" : "ok";
  els.status.dataset.tone = tone;
  clear(els.status,
    h("div.status-head", h("span.status-dot", { dataset: { tone } }), h("h2.status-title#back-title", summary.title || "一切正常")),
    h("p.status-text#back-state", summary.state || ""),
    recoveryPanel(summary),
    summary.last_error ? h("details.status-error", h("summary", "处理状态详情"), h("p", summary.last_error)) : null);
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
  refreshDelay = REFRESH_INTERVAL; refreshDue = 0;
  refresh();
  timer = setInterval(() => {
    if (!document.hidden && state.route.name === "backstage" && Date.now() >= refreshDue) refresh();
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
  document.addEventListener("visibilitychange", () => { if (!document.hidden && state.route.name === "backstage") refresh(); });
  byId("tag-quality").addEventListener("toggle", (event) => { if (event.target.open && !qualityLoaded) loadQuality(); });
  byId("tag-quality-refresh").addEventListener("click", loadQuality);
}
