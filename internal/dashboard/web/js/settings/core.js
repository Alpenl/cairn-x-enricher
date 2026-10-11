// Settings page shared pieces: section table, search index, data loading,
// receipt-backed writes, and the two popovers (menu, picker) the sections use.
import { h, clear } from "../dom.js";
import { icon } from "../icons.js";
import { toast } from "../ui.js";

export const TABS = [
  { id: "tags", group: "整理", label: "标签库", icon: "tag", title: "标签库", desc: "整理每个标签的名称、含义与层级；重复或同义的标签可以合并。", md: true },
  { id: "ai", group: "整理", label: "AI 打标", icon: "sparkles", title: "AI 打标", desc: "决定 AI 为新收藏建议多少标签，以及哪些建议需要你确认。" },
  { id: "rules", group: "整理", label: "合集规则", icon: "sliders", title: "合集规则", desc: "按最终标签把收藏放进合集；一个合集可以挂多个标签。", md: true },
  { id: "reading", group: "阅读", label: "阅读偏好", icon: "book", title: "阅读偏好", desc: "外观、默认阅读版本与正文字号，只保存在这台设备上。" },
  { id: "about", group: "系统", label: "关于", icon: "help", title: "关于", desc: "版本、服务、离线与导出，以及快捷键。" },
];
export const GROUPS = ["整理", "阅读", "系统"];
export const tabById = (id) => TABS.find((t) => t.id === id) || TABS[0];
/** Individual settings for 搜索设置 and ⌘K. key = the [data-setting] anchor in the section. */
export const SETTING_INDEX = [
  { key: "lib-search", section: "tags", label: "搜索标签", kw: "查找 别名 同义词" },
  { key: "tag-new", section: "tags", label: "新建标签", kw: "添加 创建 主题 个人标签" },
  { key: "tag-name", section: "tags", label: "标签名称", kw: "重命名 改名" },
  { key: "tag-type", section: "tags", label: "标签类型", kw: "主题 资源类型 内容特征 个人标签" },
  { key: "tag-parent", section: "tags", label: "上级主题", kw: "层级 细分主题 父级" },
  { key: "tag-desc", section: "tags", label: "含义说明", kw: "定义 描述 AI 判断" },
  { key: "tag-examples", section: "tags", label: "会打的例子 / 不打的情况", kw: "正例 反例 例外 includes excludes" },
  { key: "tag-alias", section: "tags", label: "同义词 / 别名", kw: "别名 同义 alias" },
  { key: "tag-ai", section: "tags", label: "参与 AI 自动打标", kw: "AI 开关 打标 只手动" },
  { key: "tag-usage", section: "tags", label: "使用情况", kw: "收藏 用到 数量" },
  { key: "tag-rules", section: "tags", label: "关联合集规则", kw: "规则 合集" },
  { key: "tag-merge", section: "tags", label: "合并标签", kw: "合并到 重复 同义 撤销" },
  { key: "tag-archive", section: "tags", label: "归档 / 恢复标签", kw: "归档 恢复 隐藏 停用" },
  { key: "tag-delete", section: "tags", label: "删除个人标签", kw: "删除 移除" },
  { key: "ai-count", section: "ai", label: "每条建议数量", kw: "建议 数量 最多 个数" },
  { key: "ai-confirm", section: "ai", label: "建议需人工确认后生效", kw: "确认 审核 直接写入 自动" },
  { key: "ai-low", section: "ai", label: "低置信度仍需确认", kw: "置信度 不确定" },
  { key: "ai-preview", section: "ai", label: "AI 打标预览", kw: "预览 示例 效果" },
  { key: "ai-scope", section: "ai", label: "参与 AI 的标签范围", kw: "参与范围 不参与 AI 关" },
  { key: "ai-reset", section: "ai", label: "恢复 AI 打标默认设置", kw: "重置 默认" },
  { key: "rule-on", section: "rules", label: "启用 / 关闭合集规则", kw: "开关 启用 关闭 规则 暂停" },
  { key: "rule-desc", section: "rules", label: "合集说明", kw: "描述 说明" },
  { key: "rule-tags", section: "rules", label: "命中标签", kw: "规则 标签 多个标签 添加标签" },
  { key: "rule-match", section: "rules", label: "匹配方式（任一 / 全部标签）", kw: "任一 全部 同时 或 且" },
  { key: "rule-exclude", section: "rules", label: "排除标签", kw: "排除 除外 不加入" },
  { key: "rule-mode", section: "rules", label: "收录方式（审核 / 直接加入）", kw: "审核 直接加入 自动加入" },
  { key: "rule-conf", section: "rules", label: "只看已确认标签", kw: "确认 AI 建议 待确认" },
  { key: "rule-preview", section: "rules", label: "规则匹配预览与待审核", kw: "预览 待审核 忽略 加入 补收" },
  { key: "rule-new", section: "rules", label: "新建合集", kw: "创建 合集" },
  { key: "read-theme", section: "reading", label: "外观（浅色 / 深色）", kw: "主题 深色 夜间 暗色 跟随系统" },
  { key: "read-version", section: "reading", label: "默认阅读版本", kw: "原文 整理版 默认" },
  { key: "read-size", section: "reading", label: "正文字号", kw: "字号 字体 大小" },
  { key: "read-preview", section: "reading", label: "阅读效果预览", kw: "预览" },
  { key: "read-reset", section: "reading", label: "恢复阅读默认设置", kw: "重置 默认" },
  { key: "about-version", section: "about", label: "版本与服务", kw: "版本 更新 服务状态" },
  { key: "about-offline", section: "about", label: "离线阅读与导出", kw: "离线 下载 导出 Markdown 清除" },
  { key: "about-legacy", section: "about", label: "旧版搁置收藏", kw: "整理状态 搁置 不要" },
  { key: "about-shortcuts", section: "about", label: "快捷键", kw: "键盘 快捷键 shortcut" },
];

export const DIMENSIONS = [
  { key: "topics", label: "主题", seg: "topics" },
  { key: "resource_kinds", label: "资源类型", seg: "resource_kinds" },
  { key: "content_functions", label: "内容特征", seg: "content_functions" },
  { key: "custom", label: "个人标签", seg: "custom" },
];
// Kept read-only under 更多类型 (decision 3).
export const MORE_DIMENSIONS = [
  { key: "carriers", label: "载体" },
  { key: "affordances", label: "潜在用途" },
  { key: "forms", label: "旧版形态" },
  { key: "uses", label: "旧版用途" },
];
export const dimLabel = (d) => [...DIMENSIONS, ...MORE_DIMENSIONS].find((x) => x.key === d)?.label || d;
export const dotClass = (d) => (["topics", "resource_kinds", "content_functions", "custom"].includes(d) ? `type-${d}` : "type-other");

const ERRORS = {
  tag_collision: "名称或别名已被其他标签使用",
  system_tag_exists: "已有同名的系统标签",
  custom_tag_exists: "已有同名的个人标签",
  invalid_tag_definition: "名称和说明都不能为空，检查字段长度",
  invalid_label: "名称不能为空，最多 80 个字符",
  last_ai_tag: "这一类至少要保留一个参与 AI 的标签",
  revision_conflict: "已在别处修改，已载入最新内容，请再试一次",
  operation_conflict: "这次修改与上一次未确认的操作冲突，已刷新",
  tag_limit: "这一类的标签数量已达上限",
  personal_use_human_only: "「反对」只能由你手动添加",
  tag_in_use: "这个标签还在使用中",
  tag_not_found: "这个标签已不存在，已载入最新标签库",
  invalid_tag_operation: "标签操作无效，请载入最新标签库后重试",
  invalid_dimension: "这一类暂不支持新建标签",
  tag_deprecated: "已归档的标签不能合并，请先恢复",
  invalid_merge: "不能这样合并：只能合并同一类的主题或个人标签",
  invalid_rule: "规则无效：同一个标签不能既命中又排除，最多 20 个标签",
  invalid_preferences: "设置值无效",
  capability_mismatch: "服务尚未支持这项设置，升级 Worker 后可用",
  candidate_not_found: "这些收藏已被处理过，已刷新",
  collection_limit: "合集最多 1000 条收藏",
  backend_error: "保存结果暂未确认，请重试原操作",
  network_error: "暂时无法连接服务",
};
export const errorText = (e) => ERRORS[e?.code] || ERRORS[e?.message] || e?.message || "保存失败";
export const failure = (e) => toast(errorText(e), { tone: "error" });

export const uuid = () => {
  const b = crypto.getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 15) | 64;
  b[8] = (b[8] & 63) | 128;
  const x = [...b].map((v) => v.toString(16).padStart(2, "0")).join("");
  return `${x.slice(0, 8)}-${x.slice(8, 12)}-${x.slice(12, 16)}-${x.slice(16, 20)}-${x.slice(20)}`;
};

/** JSON request; errors carry {status, code, payload}. */
export async function request(path, body, method) {
  let r;
  try {
    r = await fetch(path, {
      method: method || (body ? "POST" : "GET"),
      headers: body ? { "Content-Type": "application/json" } : {},
      body: body ? JSON.stringify(body) : undefined,
      cache: "no-store",
    });
  } catch {
    const e = new Error("network_error");
    e.code = "network_error";
    throw e;
  }
  const data = await r.json().catch(() => ({}));
  if (!r.ok) {
    const e = new Error(data.error || `HTTP ${r.status}`);
    e.status = r.status;
    e.code = data.error;
    e.payload = data;
    throw e;
  }
  return data;
}

// A write keeps its operation key in localStorage until the service answers,
// so a lost response retries the same operation instead of applying twice.
const RECEIPT = "cairn.settings.pending.v1";
export function readReceipt() {
  try {
    return JSON.parse(localStorage.getItem(RECEIPT) || "null");
  } catch {
    return null;
  }
}
function writeReceipt(value) {
  try {
    if (value === null) localStorage.removeItem(RECEIPT);
    else localStorage.setItem(RECEIPT, JSON.stringify(value));
  } catch {
    return false;
  }
  // The page header shows 「重试 / 放弃」 while a write is unconfirmed.
  window.dispatchEvent(new Event("cairn:settings-receipt"));
  return true;
}
export const dropReceipt = () => writeReceipt(null);
/** Send a receipt-backed write. Returns the result, or null after reporting the failure. */
export async function send(path, body, method = "POST", { quiet = false } = {}) {
  const old = readReceipt();
  if (old && (old.path !== path || JSON.stringify(old.body) !== JSON.stringify(body))) {
    toast("还有一项修改没有收到服务确认，请先在页面顶部重试或放弃");
    return null;
  }
  if (!writeReceipt({ path, body, method })) {
    toast("浏览器不允许本地存储，无法安全保存修改", { tone: "error" });
    return null;
  }
  try {
    const result = await request(path, body, method);
    writeReceipt(null);
    return result;
  } catch (e) {
    // 4xx is a definite answer; 5xx/network keeps the receipt for an exact retry.
    if (e.status && e.status < 500) writeReceipt(null);
    if (!quiet) failure(e);
    throw e;
  }
}

export const fold = (s) => String(s || "").toLowerCase();
export const collator = new Intl.Collator("zh-Hans-CN", { numeric: true });

/** Name with the matching part marked. */
export function marked(text, q) {
  const node = h("span.nm");
  const i = q ? fold(text).indexOf(fold(q)) : -1;
  if (i < 0) node.textContent = text;
  else node.append(text.slice(0, i), h("mark", text.slice(i, i + q.length)), text.slice(i + q.length));
  return node;
}

export function chevron() {
  const n = icon("chevronDown", 14);
  n.classList.add("chev");
  return n;
}
export function ddButton(label, props = {}) {
  return h("button.dd", { type: "button", "aria-haspopup": "listbox", "aria-expanded": "false", ...props }, h("span.dd-label", label), chevron());
}

/* ---------- popover menu (single-choice lists, narrow selectors) ---------- */
let menuEl = null;
const menu = { anchor: null, items: [], onSelect: null };
function ensureMenu() {
  if (menuEl) return menuEl;
  menuEl = h("div.sp-menu.sp-pop", { role: "menu", tabindex: "-1", hidden: true });
  document.body.append(menuEl);
  menuEl.addEventListener("keydown", (e) => {
    const items = [...menuEl.querySelectorAll(".menu-item:not(:disabled)")];
    const i = items.indexOf(document.activeElement);
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const n = items[(i + (e.key === "ArrowDown" ? 1 : -1) + items.length) % items.length];
      n?.focus();
    } else if (e.key === "Escape" || e.key === "Tab") {
      e.preventDefault();
      e.stopPropagation();
      closeMenu(true);
    }
  });
  document.addEventListener("mousedown", (e) => {
    if (menu.anchor && !menuEl.contains(e.target) && !menu.anchor.contains(e.target)) closeMenu(false);
  }, true);
  window.addEventListener("resize", () => closeMenu(false));
  return menuEl;
}
export function openMenu(anchor, items, { value, onSelect, minWidth = 168, matchWidth = false, align } = {}) {
  ensureMenu();
  if (menu.anchor === anchor) return closeMenu(true);
  closeMenu(false);
  closePick(false);
  menu.anchor = anchor;
  menu.onSelect = onSelect;
  clear(menuEl, items.map((it) => {
    if (it.sep) return h("div.menu-sep");
    if (it.head) return h("div.menu-head", it.head);
    const on = value != null && it.value === value;
    return h("button.menu-item", {
      type: "button", role: "menuitem", tabindex: "-1", disabled: it.disabled, title: it.title,
      onclick: () => { const cb = menu.onSelect; closeMenu(true); cb?.(it.value); },
    }, it.dot ? h("span.type-dot", { class: it.dot }) : it.icon ? icon(it.icon, 15) : null, h("span", it.label), it.meta ? h("span.meta", it.meta) : null, on ? h("span.tick", icon("check", 14)) : null);
  }));
  menuEl.hidden = false;
  menuEl.style.minWidth = `${Math.max(minWidth, matchWidth ? anchor.offsetWidth : 0)}px`;
  const r = anchor.getBoundingClientRect(), mw = menuEl.offsetWidth, mh = menuEl.offsetHeight;
  let left = align === "end" ? r.right - mw : r.left;
  left = Math.max(8, Math.min(left, innerWidth - mw - 8));
  let top = r.bottom + 4;
  if (top + mh > innerHeight - 8) top = Math.max(8, r.top - mh - 4);
  menuEl.style.left = `${left}px`;
  menuEl.style.top = `${top}px`;
  anchor.setAttribute("aria-expanded", "true");
  requestAnimationFrame(() => menuEl.classList.add("show"));
  const btns = [...menuEl.querySelectorAll(".menu-item:not(:disabled)")];
  (btns.find((b) => b.querySelector(".tick")) || btns[0] || menuEl).focus({ preventScroll: true });
}
export function closeMenu(returnFocus) {
  if (!menu.anchor) return;
  const a = menu.anchor;
  a.setAttribute("aria-expanded", "false");
  menu.anchor = null;
  menuEl.classList.remove("show");
  menuEl.hidden = true;
  menuEl.replaceChildren();
  if (returnFocus && document.contains(a)) a.focus({ preventScroll: true });
}

/* ---------- pick popover (rule tag picker, merge target) ----------
   cfg: { title, sub(), cls, width, search, placeholder, body(q) → nodes, foot() → nodes, onEscape() } */
let pickEl = null;
const pick = { anchor: null, cfg: null, q: "" };
function ensurePick() {
  if (pickEl) return pickEl;
  pickEl = h("div.pick-pop.sp-pop", { role: "dialog", hidden: true });
  document.body.append(pickEl);
  pickEl.addEventListener("keydown", (e) => {
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      if (pick.cfg?.onEscape?.()) return;
      const s = pickEl.querySelector(".pk-search input");
      if (e.target === s && s.value) { s.value = pick.q = ""; renderPick(); return; }
      closePick(true);
      return;
    }
    const items = [...pickEl.querySelectorAll(".pk-body [data-pk]:not(:disabled)")];
    const s = pickEl.querySelector(".pk-search input");
    if (e.target === s) {
      if (e.key === "ArrowDown" && items[0]) { e.preventDefault(); items[0].focus(); }
      else if (e.key === "Enter" && !e.isComposing && items[0]) { e.preventDefault(); items[0].click(); }
      return;
    }
    const i = items.indexOf(e.target);
    if (i < 0) return;
    const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[e.key];
    if (!step) return;
    e.preventDefault();
    if (step < 0 && i === 0 && s) return s.focus();
    const n = items[Math.max(0, Math.min(items.length - 1, i + step))];
    n.focus();
    n.scrollIntoView({ block: "nearest" });
  });
  document.addEventListener("mousedown", (e) => {
    if (pick.anchor && !pickEl.contains(e.target) && !pick.anchor.contains(e.target)) closePick(false);
  }, true);
  window.addEventListener("resize", () => pick.anchor && placePick());
  return pickEl;
}
export function openPick(anchor, cfg) {
  ensurePick();
  if (pick.anchor === anchor) return closePick(true);
  closeMenu(false);
  closePick(false);
  pick.anchor = anchor;
  pick.cfg = cfg;
  pick.q = "";
  pickEl.className = `pick-pop sp-pop ${cfg.cls || ""}`;
  pickEl.style.width = `${cfg.width || 380}px`;
  pickEl.setAttribute("aria-label", cfg.title);
  pickEl.hidden = false;
  renderPick(true);
  anchor.setAttribute("aria-expanded", "true");
  requestAnimationFrame(() => pickEl.classList.add("show"));
  const s = pickEl.querySelector(".pk-search input");
  (s || pickEl.querySelector("button:not(:disabled)"))?.focus({ preventScroll: true });
}
export function renderPick(full = false) {
  const cfg = pick.cfg;
  if (!cfg) return;
  const focusId = pickEl.contains(document.activeElement) ? document.activeElement.dataset?.id : null;
  if (full || !pickEl.querySelector(".pk-body")) {
    const input = cfg.search === false ? null : h("input", { type: "search", placeholder: cfg.placeholder || "搜索标签或别名", autocomplete: "off", "aria-label": cfg.placeholder || "搜索标签", value: pick.q });
    input?.addEventListener("input", () => { pick.q = input.value; renderPick(); });
    clear(pickEl,
      h("div.pk-head", h("div.pk-title-row", h("h4.pk-title", cfg.title), cfg.sub ? h("span.pk-sub") : null), input ? h("label.field.pk-search", icon("search", 15), input) : null),
      h("div.pk-body"),
      cfg.foot ? h("div.pk-foot") : null);
  }
  clear(pickEl.querySelector(".pk-body"), cfg.body(pick.q));
  if (cfg.foot) clear(pickEl.querySelector(".pk-foot"), cfg.foot());
  if (cfg.sub) pickEl.querySelector(".pk-sub").textContent = cfg.sub();
  if (focusId) pickEl.querySelector(`[data-id="${CSS.escape(focusId)}"]:not(:disabled)`)?.focus({ preventScroll: true });
  placePick();
}
export const pickSearchShown = (shown) => { const f = pickEl?.querySelector(".pk-search"); if (f) f.hidden = !shown; };
function placePick() {
  if (!pick.anchor || !document.contains(pick.anchor)) return;
  const field = pick.anchor.closest(".tok-field");
  const r = (field || pick.anchor).getBoundingClientRect();
  const w = pickEl.offsetWidth;
  const below = innerHeight - r.bottom - 16, above = r.top - 16;
  const up = below < 300 && above > below;
  const maxH = Math.max(220, Math.min(460, up ? above : below));
  pickEl.style.maxHeight = `${maxH}px`;
  const ph = Math.min(pickEl.scrollHeight, maxH);
  const box = (pick.anchor.closest(".md-detail, .sp-body") || document.documentElement).getBoundingClientRect();
  const maxRight = Math.min(innerWidth - 12, box.right - 12);
  let left = r.left + w > maxRight ? Math.min(r.left, (field ? r.right : pick.anchor.getBoundingClientRect().right) - w) : r.left;
  left = Math.max(12, Math.min(left, innerWidth - w - 12));
  pickEl.style.left = `${left}px`;
  pickEl.style.top = `${up ? Math.max(12, r.top - ph - 6) : r.bottom + 6}px`;
}
export function closePick(returnFocus) {
  if (!pick.anchor) return;
  const a = pick.anchor;
  a.setAttribute("aria-expanded", "false");
  pick.anchor = null;
  pick.cfg = null;
  pickEl.classList.remove("show");
  pickEl.hidden = true;
  pickEl.replaceChildren();
  if (returnFocus && document.contains(a)) a.focus({ preventScroll: true });
}
export const pickOpen = () => Boolean(pick.anchor);
export function closePopovers() {
  closeMenu(false);
  closePick(false);
}

/** Section header row inside single-column pages. */
export function groupHead(id, title, desc, aux) {
  return h("div.sp-group-head", h("div", h("h2.sp-group-title", { id }, title), desc ? h("p.sp-group-desc", desc) : null), aux ? h("span.aux", aux) : null);
}
/** A pill switch (role=switch). */
export function switchButton(on, props = {}) {
  return h("button.switch", { type: "button", role: "switch", "aria-checked": String(Boolean(on)), ...props });
}
/** A segmented radio group. options: [[value, label, extraNode?]] */
export function segmented(label, options, current, onPick, cls = "") {
  const group = h(`div.seg${cls}`, { role: "radiogroup", "aria-label": label });
  for (const [value, text, lead] of options) {
    const on = value === current;
    group.append(h("button", { type: "button", role: "radio", "aria-checked": String(on), class: on ? "active" : "", dataset: { v: String(value) }, onclick: () => { if (value !== current) onPick(value); } }, lead || null, text));
  }
  group.addEventListener("keydown", (e) => {
    const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[e.key];
    if (!step) return;
    const btns = [...group.querySelectorAll("button:not(:disabled)")];
    const i = btns.indexOf(document.activeElement);
    if (i < 0) return;
    e.preventDefault();
    btns[(i + step + btns.length) % btns.length].focus();
  });
  return group;
}
