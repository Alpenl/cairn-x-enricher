// 设置 · AI 打标 / 阅读偏好 / 关于: single-column sections of grouped cards.
import { h, clear } from "../dom.js";
import { icon } from "../icons.js";
import { toast } from "../ui.js";
import { request } from "./core.js";
import { groupHead, segmented, switchButton, dimLabel, dotClass } from "./core.js";
import { db, loadTags, loadCollectionsData, loadPrefs, savePrefs, PREF_DEFAULTS } from "./data.js";
import { registerSection, setActions, openSettings } from "./page.js";
import { FONT_SIZES, FONT_SIZE_LABELS, DEFAULT_SIZE, DEFAULT_VERSION, defaultVersion, setDefaultVersion, fontSize, setFontSize } from "../reading-prefs.js";
import { SHORTCUTS } from "../shortcuts.js";

let hooks = {};
const keepFocus = (body, fn) => {
  const fk = body.contains(document.activeElement) ? document.activeElement.dataset?.fk : null;
  const top = body.scrollTop;
  fn();
  body.scrollTop = top;
  if (fk) body.querySelector(`[data-fk="${CSS.escape(fk)}"]`)?.focus({ preventScroll: true });
};
const withFk = (node, prefix) => {
  for (const b of node.querySelectorAll("button")) b.dataset.fk = `${prefix}-${b.dataset.v ?? ""}`;
  return node;
};

/* ---------- AI 打标 ---------- */
function renderAi(body) {
  clear(body, h("div.sp-col", h("div.sp-loading", "正在读取 AI 打标设置…")));
  setActions([]);
  Promise.all([loadPrefs(), db.catalog ? null : loadTags()]).then(() => body.isConnected && paintAi(body)).catch((e) => {
    if (!body.isConnected) return;
    clear(body, h("div.sp-col", h("div.sp-error", h("p", db.prefsSupported ? "无法读取 AI 打标设置。" : "服务尚未支持跨设备的 AI 打标设置，升级 Worker 后可用。"),
      db.prefsSupported ? h("button.btn.btn-outline", { type: "button", onclick: () => renderAi(body) }, "重试") : null)));
  });
}
function paintAi(body) {
  const p = { ...PREF_DEFAULTS, ...(db.prefs?.value || {}) };
  const isDefault = Object.entries(PREF_DEFAULTS).every(([k, v]) => p[k] === v);
  setActions([h("button.btn.btn-ghost", { type: "button", dataset: { setting: "ai-reset" }, disabled: isDefault, title: isDefault ? "已是默认设置" : null,
    onclick: () => update(body, { ...PREF_DEFAULTS }, "已恢复 AI 打标默认设置") }, icon("undo", 15), "恢复默认")]);
  const confirmAll = p.ai_confirm_before_apply;
  const aiTags = db.tags.filter((t) => t.kind === "system" && !t.more && !t.archived && !t.mergedInto && t.ai);
  const off = db.tags.filter((t) => t.kind === "system" && !t.more && !t.archived && !t.mergedInto && !t.ai && !t.humanOnly);
  // A sample built from the library's own most-used AI tags, so the preview reads like the real thing.
  const cands = aiTags.slice().sort((a, b) => b.count - a.count).slice(0, 6);
  const low = new Set(cands.slice(-2).map((t) => t.ref));
  const shown = cands.slice(0, p.ai_suggestion_limit), over = cands.slice(p.ai_suggestion_limit);
  const pending = (t) => confirmAll || (p.ai_low_confidence_review && low.has(t.ref));
  const pendN = shown.filter(pending).length, autoN = shown.length - pendN;
  clear(body, h("div.sp-col",
    h("section.sp-group", { "aria-labelledby": "g-ai-1" },
      groupHead("g-ai-1", "建议与确认", "新收藏保存后，AI 会依据每个标签的含义说明给出建议。这些设置在所有设备上同步。"),
      h("div.set-card",
        h("div.set-row", { dataset: { setting: "ai-count" } },
          h("div.txt", h("div.ttl#lbl-sc", "每条收藏最多建议 ", h("b", String(p.ai_suggestion_limit)), " 个标签"), h("div.dsc", "数量越少，建议越集中在最有把握的标签上。")),
          withFk(segmented("每条建议数量", [...new Set([2, 3, 4, 5, p.ai_suggestion_limit])].sort((x, y) => x - y).map((n) => [n, String(n)]), p.ai_suggestion_limit, (n) => update(body, { ai_suggestion_limit: n }, `每条收藏最多建议 ${n} 个标签`), ".sm"), "sc")),
        h("div.set-row", { dataset: { setting: "ai-confirm" } },
          h("div.txt", h("div.ttl#lbl-rc", "建议需人工确认后生效"), h("div.dsc", "开启后，AI 建议先以虚线标签显示，你确认后才算数，合集规则和筛选也只看确认过的；关闭时直接写入（现在的做法）。")),
          switchButton(confirmAll, { "aria-labelledby": "lbl-rc", dataset: { fk: "rc" }, onclick: () => update(body, { ai_confirm_before_apply: !confirmAll }, !confirmAll ? "AI 建议改为确认后生效" : "AI 建议改为直接写入") })),
        h("div.set-row", { class: confirmAll ? "disabled" : "", dataset: { setting: "ai-low" } },
          h("div.txt", h("div.ttl#lbl-lc", "低置信度仍需确认"), h("div.dsc", confirmAll ? "已开启人工确认，所有建议都会先进入待确认。" : "直接写入时，把模型不太确定的建议留给你确认。")),
          switchButton(p.ai_low_confidence_review, { "aria-labelledby": "lbl-lc", dataset: { fk: "lc" }, disabled: confirmAll, onclick: () => update(body, { ai_low_confidence_review: !p.ai_low_confidence_review }, "已保存") })))),
    h("section.sp-group", { "aria-labelledby": "g-ai-2", dataset: { setting: "ai-preview" } },
      groupHead("g-ai-2", "预览", "用你最常用的几个标签演示：按当前设置，一条新收藏的建议会怎样出现。"),
      h("div.pv-card",
        h("div.pv-src", "示例 · 不会写入任何收藏"),
        h("div.pv-title", "一条新收藏"),
        h("div.pv-tags", shown.length ? shown.map((t) => pending(t)
          ? h("span.tag.tag-ai", t.label, low.has(t.ref) ? h("span.lowc", { title: "低置信度" }, "?") : null)
          : h("span.tag.tag-auto", icon("sparkles", 10), t.label)) : h("span.tag.tag-empty", "没有建议")),
        h("div.pv-legend", pendN ? h("span", h("i.lg.lg-ai"), `${pendN} 个待你确认`) : null, autoN ? h("span", h("i.lg.lg-auto"), `${autoN} 个直接写入`) : null),
        over.length ? h("div.pv-skip", h("div", h("span.k", "超出数量"), over.map((t) => t.label).join("、"))) : null)),
    h("section.sp-group", { "aria-labelledby": "g-ai-3", dataset: { setting: "ai-scope" } },
      groupHead("g-ai-3", "参与范围", "", `${aiTags.length} 个标签参与`),
      h("div.set-card",
        h("div.set-row", h("div.txt", h("div.ttl", "不参与 AI 的标签"), h("div.dsc", "在标签库中逐个开关；已归档的标签始终不参与。点击标签可直接打开它。"))),
        h("div.ai-scope", off.length
          ? off.map((t) => h("button.tag.tag-off", { type: "button", title: `在标签库中查看 · ${dimLabel(t.d)}`, onclick: () => openSettings("tags", `${t.d}:${t.id}`) }, h("span.type-dot", { class: dotClass(t.d) }), t.label))
          : h("span.sx-quiet", "所有未归档的标签都参与 AI 打标。"))))));
}
async function update(body, patch, message) {
  try {
    await savePrefs(patch);
    toast(message, { tone: "ok" });
  } catch (e) {
    if (e.code === "revision_conflict") toast("设置已在其他设备修改，已载入最新值");
  }
  if (body.isConnected) keepFocus(body, () => paintAi(body));
}

/* ---------- 阅读偏好 ---------- */
function themeChoice() {
  try { return localStorage.getItem("cairn.theme") || "system"; } catch { return "system"; }
}
function renderReading(body) {
  const version = defaultVersion(), size = fontSize(), theme = themeChoice();
  const isDefault = version === DEFAULT_VERSION && size === DEFAULT_SIZE && theme === "system";
  setActions([h("button.btn.btn-ghost", { type: "button", dataset: { setting: "read-reset" }, disabled: isDefault, title: isDefault ? "已是默认设置" : null,
    onclick: () => { setDefaultVersion(DEFAULT_VERSION); setFontSize(DEFAULT_SIZE); hooks.theme?.("system"); toast("已恢复阅读默认设置", { tone: "ok" }); keepFocus(body, () => renderReading(body)); } }, icon("undo", 15), "恢复默认")]);
  const rerender = () => keepFocus(body, () => renderReading(body));
  clear(body, h("div.sp-col",
    h("section.sp-group", { "aria-labelledby": "g-read-1" },
      groupHead("g-read-1", "打开收藏时", "这些偏好只保存在这台设备上，不会改写收藏内容。"),
      h("div.set-card",
        h("div.set-row", { dataset: { setting: "read-theme" } },
          h("div.txt", h("div.ttl", "外观"), h("div.dsc", "浅色、深色，或跟随设备设置。")),
          withFk(segmented("外观", [["system", "跟随系统"], ["light", "浅色"], ["dark", "深色"]], theme, (v) => { hooks.theme?.(v); rerender(); }, ".sm"), "th")),
        h("div.set-row", { dataset: { setting: "read-version" } },
          h("div.txt", h("div.ttl", "默认阅读版本"), h("div.dsc", "打开一条收藏时先显示的正文版本；没有整理版时显示原文，阅读时仍可随时切换。")),
          withFk(segmented("默认阅读版本", [["original", "原文"], ["organized", "整理版"]], version, (v) => { setDefaultVersion(v); toast(`打开收藏时先显示${v === "original" ? "原文" : "整理版"}`, { tone: "ok" }); rerender(); }, ".sm"), "rv")),
        h("div.set-row", { dataset: { setting: "read-size" } },
          h("div.txt", h("div.ttl", "正文字号"), h("div.dsc", "只影响阅读区正文，界面文字保持不变。")),
          withFk(segmented("正文字号", FONT_SIZES.map((n) => [n, FONT_SIZE_LABELS[n]]), size, (n) => { setFontSize(n); rerender(); }, ".sm"), "fs")))),
    h("section.sp-group", { "aria-labelledby": "g-read-2", dataset: { setting: "read-preview" } },
      groupHead("g-read-2", "预览", "", `${size} px · 默认${version === "original" ? "原文" : "整理版"}`),
      h("div.read-preview", h("p", "把最终有效的描述沉淀成可复用的提示片段：结构词、材质词、禁止项。下次练习从这里起步，而不是从空白对话框起步。")))));
}

/* ---------- 关于 ---------- */
function renderAbout(body) {
  setActions([]);
  const status = h("span#sp-about-service", document.querySelector("#sp-service-text")?.textContent?.trim() || "服务状态");
  const version = document.querySelector('meta[name="cairn-version"]')?.content || "";
  const legacy = h("span.val", "…");
  const counts = h("span.val", "…");
  const paintCounts = () => { counts.textContent = `${db.tags.filter((t) => !t.archived && !t.mergedInto).length} 个标签 · ${db.collections.filter((c) => !c.deleted).length} 个合集`; };
  if (db.tags.length) paintCounts();
  else Promise.all([loadTags(), loadCollectionsData(false)]).then(paintCounts, () => { counts.textContent = "—"; });
  const keys = SHORTCUTS.flatMap((g) => g.items.map(([k, label]) => [k.filter((x) => !["/", "+", "然后"].includes(x)), label]));
  const row = ([k, l]) => h("div.sc-row", h("span", l), h("span.keys", k.map((x) => h("span.kbd", x))));
  clear(body, h("div.sp-col",
    h("section.sp-group", { "aria-labelledby": "g-ab-1", dataset: { setting: "about-version" } },
      groupHead("g-ab-1", "版本与服务"),
      h("div.set-card",
        h("div.set-row.kv", h("div.txt", h("div.ttl", "应用")), h("div.val", version ? `Cairn ${version}` : "Cairn")),
        h("div.set-row.kv", h("div.txt", h("div.ttl", "服务")), h("div.val", h("a.btn.btn-ghost.btn-sm", { href: "/backstage", onclick: (e) => { e.preventDefault(); hooks.service?.(); } }, status, icon("chevronRight", 14)))),
        h("div.set-row.kv", h("div.txt", h("div.ttl", "数据")), counts))),
    h("section.sp-group", { "aria-labelledby": "g-ab-2", dataset: { setting: "about-offline" } },
      groupHead("g-ab-2", "离线与导出", "读过的文章会在这台设备上保存文字副本，断网时也能打开。"),
      h("div.set-card",
        h("div.set-row", h("div.txt", h("div.ttl", "下载离线阅读文件"), h("div.dsc", "把当前打开的收藏保存为单个离线文件。")), h("button.btn.btn-outline.btn-sm", { type: "button", onclick: () => hooks.offline?.() }, icon("download", 14), "下载")),
        h("div.set-row", h("div.txt", h("div.ttl", "导出当前结果"), h("div.dsc", "按收藏库当前的筛选导出为 Markdown。")), h("button.btn.btn-outline.btn-sm", { type: "button", onclick: () => hooks.export?.() }, icon("fileText", 14), "导出")),
        h("div.set-row", h("div.txt", h("div.ttl", "清除本机离线副本"), h("div.dsc", "只删除这台设备上的副本，收藏本身不受影响。")), h("button.btn.btn-ghost.btn-sm.danger", { type: "button", onclick: () => hooks.forget?.() }, "清除")))),
    h("section.sp-group", { "aria-labelledby": "g-ab-3", dataset: { setting: "about-legacy" } },
      groupHead("g-ab-3", "旧版整理状态"),
      h("div.set-card", h("div.set-row.kv", h("div.txt", h("div.ttl", "旧版标为「不要」的收藏"), h("div.dsc", "新界面不再提供整理状态；这些收藏仍不进待读、不参与 AI 打标和合集规则。")), legacy))),
    h("section.sp-group", { "aria-labelledby": "g-ab-4", dataset: { setting: "about-shortcuts" } },
      groupHead("g-ab-4", "快捷键", "在输入框之外按下即可。"),
      h("div.set-card.sc-card",
        h("div.sc-sub", "收藏库"), h("div.sc-list", keys.map(row)),
        h("div.sc-sub", "设置页"), h("div.sc-list", [[["/"], "搜索设置"], [["↑", "↓"], "在标签 / 合集列表中移动"], [["Esc"], "返回收藏库 / 关闭弹层"], [[","], "从收藏库打开设置"]].map(row))))));
  request("/api/links/legacy-dropped").then((r) => { legacy.textContent = `${r.count} 条`; }).catch(() => { legacy.textContent = "—"; });
}

export function initSimpleSections(options = {}) {
  hooks = options;
  registerSection("ai", { render: (body) => renderAi(body) });
  registerSection("reading", { render: (body) => renderReading(body) });
  registerSection("about", { render: (body) => renderAbout(body) });
}
