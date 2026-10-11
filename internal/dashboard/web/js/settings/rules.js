// 设置 · 合集规则: one rule per collection. Several hit tags (任一 / 全部),
// exclude tags, review-or-direct admission, confirmed-only, a live preview and
// the review queue (加入 / 忽略). Every control writes immediately.
import { h, clear } from "../dom.js";
import { icon } from "../icons.js";
import { toast } from "../ui.js";
import { DIMENSIONS, MORE_DIMENSIONS, dotClass, fold, marked, ddButton, openMenu, openPick, closePick, renderPick, segmented, switchButton, closePopovers, uuid, send } from "./core.js";
import { db, loadTags, loadCollectionsData, collById, ruleOf, saveRule, saveCollectionMeta, collectionOp, rulePreview, ruleCandidates, tagByRef, refLabel } from "./data.js";
import { registerSection, syncUrl, setActions, refreshBadge, openSettings } from "./page.js";
import { invalidateQueryReads } from "../api.js";
import { emit } from "../store.js";

const ui = { sel: null, showIgnored: false, loaded: false, ctx: null, hooks: {}, preview: null, queue: null, token: 0, busy: false };
let body = null;
const live = () => db.collections.filter((c) => !c.deleted).sort((a, b) => Number(Boolean(a.archived)) - Number(Boolean(b.archived)) || Number(Boolean(b.pinned)) - Number(Boolean(a.pinned)) || a.name.localeCompare(b.name, "zh"));
const v2 = (c) => c && "rule_admission" in c;
const tagUsable = (ref) => { const t = tagByRef(ref); return t && !t.archived && !t.mergedInto; };

function render(host, ctx) {
  body = host;
  ui.ctx = ctx;
  clear(body, h("div.md.md-rules",
    h("aside.md-list", { "aria-label": "合集" },
      h("div.md-list-head", h("div.sx-label", "合集", h("span.aux#rl-count"))),
      h("div.md-list-body.md-scroll#rule-list", { role: "listbox", "aria-label": "合集", onkeydown: listKeys }),
      h("div.md-list-foot", h("span", "规则只会添加，不会移出；你随时可以手动调整。"))),
    h("section.md-detail", { "aria-label": "合集规则详情" },
      h("div.md-narrow#rule-narrow"),
      h("div.md-scroll.md-detail-body#rule-detail"),
      h("div.md-foot#rule-foot"))));
  setActions([h("button.btn.btn-outline", { type: "button", dataset: { setting: "rule-new" }, onclick: newCollection }, icon("plus", 15), "新建合集")]);
  if (!ui.loaded) {
    clear(body.querySelector("#rule-list"), h("div.sp-loading", "正在读取合集…"));
    load(ctx.sub);
    return;
  }
  if (ctx.sub) pickSub(ctx.sub);
  paint();
}
async function load(sub) {
  try {
    await Promise.all([loadCollectionsData(), db.catalog ? Promise.resolve() : loadTags()]);
    ui.loaded = true;
  } catch (e) {
    if (body?.isConnected) clear(body, h("div.sp-error", h("p", "无法读取合集。"), h("button.btn.btn-outline", { type: "button", onclick: () => load(sub) }, "重试")));
    return;
  }
  if (!body?.isConnected) return;
  if (sub) pickSub(sub);
  paint();
}
function pickSub(sub) {
  if (collById(sub) && !collById(sub).deleted) { if (ui.sel !== sub) { ui.sel = sub; ui.showIgnored = false; ui.preview = ui.queue = null; } }
  else toast(`没有找到合集「${sub}」`);
}
function paint() {
  if (!collById(ui.sel) || collById(ui.sel).deleted) ui.sel = live()[0]?.id ?? null;
  renderList();
  renderDetail();
}

function brief(r) {
  const n = r.tags.length;
  if (!n) return "还没有命中标签";
  return `${n} 个标签 · ${n > 1 ? `${r.mode === "all" ? "全部" : "任一"} · ` : ""}${r.admission === "review" ? "审核后加入" : "直接加入"}`;
}
function renderList() {
  const box = body?.querySelector("#rule-list");
  if (!box) return;
  const list = live();
  body.querySelector("#rl-count").textContent = String(list.length);
  clear(box, list.length
    ? list.map((c) => {
        const r = ruleOf(c), sel = c.id === ui.sel, pend = Number(c.pending_review_count) || 0;
        const warn = [...r.tags, ...r.exclude].some((ref) => !tagUsable(ref));
        return h("div.rl-row", { class: [sel ? "selected" : "", r.enabled ? "" : "off"].filter(Boolean).join(" "), role: "option", "aria-selected": String(sel), tabindex: sel ? "0" : "-1", dataset: { coll: c.id }, onclick: () => select(c.id, { focus: true }) },
          h("span.rl-dot", { class: r.enabled ? "on" : "", title: r.enabled ? "规则已启用" : "规则已关闭" }),
          h("span.rl-txt", h("span.nm", c.name, c.archived ? h("span.muted", " · 已归档") : null), h("span.sub", warn ? h("span.w", icon("alert", 11)) : null, r.enabled ? brief(r) : r.tags.length ? "规则已关闭" : "还没有规则")),
          pend ? h("span.pend", { title: `${pend} 条待审核` }, String(pend)) : null,
          h("span.n", String(c.item_count ?? 0)));
      })
    : h("div.lib-empty", "还没有合集", h("button.btn.btn-outline.btn-sm", { type: "button", onclick: newCollection }, icon("plus", 14), "新建合集")));
  const c = collById(ui.sel);
  clear(body.querySelector("#rule-narrow"), c ? ddButton([icon("folder", 15), c.name, h("span.muted", ruleOf(c).enabled ? "· 启用" : "· 关闭")], {
    "aria-label": "选择合集", onclick: (e) => openMenu(e.currentTarget, live().map((x) => ({ value: x.id, label: x.name, icon: "folder", meta: x.pending_review_count ? `${x.pending_review_count} 待审核` : "" })), { value: ui.sel, matchWidth: true, onSelect: (id) => select(id) }) }) : null);
}
function listKeys(e) {
  const items = [...body.querySelectorAll("#rule-list .rl-row")];
  const i = items.indexOf(document.activeElement);
  if (i < 0) return;
  const step = { ArrowDown: 1, ArrowUp: -1 }[e.key];
  if (!step) return;
  e.preventDefault();
  select(items[Math.max(0, Math.min(items.length - 1, i + step))].dataset.coll, { focus: true });
}
function select(id, { focus = false } = {}) {
  if (id !== ui.sel) {
    commit();
    closePick(false);
    ui.sel = id;
    ui.showIgnored = false;
    ui.preview = ui.queue = null;
    renderDetail();
    body.querySelector("#rule-detail").scrollTop = 0;
  }
  renderList();
  if (focus) { const row = body.querySelector(`#rule-list .rl-row[data-coll="${CSS.escape(id)}"]`); row?.focus({ preventScroll: true }); row?.scrollIntoView({ block: "nearest" }); }
}

/* ---------- detail ---------- */
function keep(fn) {
  const a = document.activeElement;
  const fk = a && body?.contains(a) ? a.dataset?.fk : null;
  const scroll = body?.querySelector("#rule-detail")?.scrollTop ?? 0;
  fn();
  if (!body?.isConnected) return;
  body.querySelector("#rule-detail").scrollTop = scroll;
  if (fk) body.querySelector(`[data-fk="${CSS.escape(fk)}"]`)?.focus({ preventScroll: true });
}
const names = (refs, word) => {
  const n = refs.map((ref) => h("b", refLabel(ref)));
  return n.length <= 1 ? n : [n.slice(0, -1).flatMap((x, i) => (i ? ["、", x] : [x])), ` ${word} `, n.at(-1)];
};
function sentence(c, r) {
  const inc = r.tags.filter(tagUsable), exc = r.exclude.filter(tagUsable);
  if (!inc.length) return "还没有可用的命中标签；添加至少一个标签后，规则才会收录。";
  return [r.mode === "all" && inc.length > 1 ? ["同时带有 ", names(inc, "和")] : ["带有 ", names(inc, "或")], ` 的${v2(c) ? "" : "新"}收藏${r.admission === "review" ? "经你审核后加入" : "会直接加入"}「${c.name}」`, exc.length ? ["，带有 ", names(exc, "或"), " 的除外"] : null, "。"];
}
function token(c, ref, list) {
  const t = tagByRef(ref);
  const merged = t?.mergedInto ? tagByRef(`system/topics/${t.mergedInto}`) : null;
  const warn = !t ? "这个标签已不存在，请移除" : merged ? `已合并到「${merged.label}」，请改用新标签` : t.archived ? "已归档 · 不参与匹配" : "";
  return h("span.rtok", { class: warn ? "warn" : "", title: warn || null, dataset: { id: ref } },
    warn ? icon("alert", 12) : h("span.type-dot", { class: dotClass(t.d) }),
    h("span.nm", refLabel(ref)), warn ? h("span.why", !t ? "已删除" : merged ? "已合并" : "已归档") : null,
    h("button.rtok-x", { type: "button", "aria-label": `移除 ${refLabel(ref)}`, dataset: { fk: `tokx-${list}-${ref}` }, onclick: () => change(c, { [list]: ruleOf(c)[list].filter((x) => x !== ref) }, `已移除${list === "tags" ? "命中" : "排除"}标签「${refLabel(ref)}」`) }, icon("x", 10)));
}
function renderDetail() {
  const box = body?.querySelector("#rule-detail"), foot = body?.querySelector("#rule-foot");
  if (!box) return;
  const c = collById(ui.sel);
  if (!c) {
    clear(box, h("div.md-empty", icon("folder", 28), h("div.t", "还没有合集"), h("div.s", "新建一个合集，再给它挂上标签，带这些标签的收藏就会自动归入。")));
    foot.hidden = true;
    syncUrl();
    return;
  }
  foot.hidden = false;
  const r = ruleOf(c), supported = v2(c);
  const desc = h("input.rd-desc#rd-desc", { value: c.description || "", placeholder: "添加一句说明，写清这个合集收什么…", maxLength: 2000, autocomplete: "off", "aria-label": "合集说明", dataset: { setting: "rule-desc", fk: "rd-desc" },
    onkeydown: (e) => { if (e.isComposing) return; if (e.key === "Enter") { e.preventDefault(); desc.blur(); } else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); desc.value = c.description || ""; desc.blur(); } },
    onblur: () => saveDesc(c, desc.value) });
  clear(box, h("div.rd",
    h("div.rd-head",
      h("div.rd-title-row",
        h("span.rd-ic", icon("folder", 18)),
        h("div.rd-titles", h("h4", c.name), h("span.n", `${c.item_count ?? 0} 条收藏`)),
        h("button.btn.btn-ghost.btn-sm", { type: "button", title: "在收藏库中打开这个合集，管理顺序与备注", onclick: () => ui.hooks.openCollection?.(c.id) }, icon("book", 14), "打开合集"),
        h("label.rd-switch", { dataset: { setting: "rule-on" } }, h("span#lbl-rule-on", r.enabled ? "规则已启用" : "规则已关闭"),
          switchButton(r.enabled, { "aria-labelledby": "lbl-rule-on", dataset: { fk: "rule-on" }, disabled: !r.tags.length && !r.enabled, title: !r.tags.length ? "先添加命中标签" : null,
            onclick: () => change(c, { enabled: !r.enabled }, r.enabled ? "已关闭规则，已归入的收藏保留" : "已启用规则") }))),
      desc,
      r.enabled ? null : h("div.td-banner", icon("help", 15), h("span", r.tags.length ? "规则已关闭：不会自动收录或提示待审核，下面的设置仍可编辑。" : "添加命中标签后即可启用规则。")),
      supported ? null : h("div.sp-note-warn", icon("alert", 14), h("span", "服务尚未支持排除标签、审核后加入和只看已确认；升级 Worker 后可用。"))),
    h("div.rd-grid",
      h("div.rd-form",
        h("div.sx", { dataset: { setting: "rule-tags" } },
          h("div.sx-label", "命中标签", h("span.cnt", String(r.tags.length)),
            h("span.sx-right", { dataset: { setting: "rule-match" } }, h("span.k#lbl-match", "匹配方式"),
              segmented("匹配方式", [["any", "任一标签"], ["all", "全部标签"]], r.mode, (m) => change(c, { mode: m }, m === "all" ? "改为同时带有全部标签才加入" : "改为带有任一标签即加入"), ".sm"))),
          h("div.tok-field", { dataset: { list: "tags" } }, r.tags.map((ref) => token(c, ref, "tags")),
            h("button.tok-add", { type: "button", "aria-haspopup": "dialog", "aria-expanded": "false", dataset: { fk: "pick-tags" }, onclick: (e) => picker(e.currentTarget, "tags") }, icon("plus", 13), "添加标签")),
          h("p.rd-sentence#rule-sentence", sentence(c, r))),
        h("div.sx.sx-ex", { class: r.exclude.length ? "" : "empty", dataset: { setting: "rule-exclude" } },
          h("div.sx-label", "排除标签", h("span.aux", "可选 · 带有任一即不加入")),
          h("div.tok-field", { dataset: { list: "exclude" } }, r.exclude.map((ref) => token(c, ref, "exclude")),
            h("button.tok-add", { type: "button", "aria-haspopup": "dialog", "aria-expanded": "false", dataset: { fk: "pick-exclude" }, disabled: !supported, onclick: (e) => picker(e.currentTarget, "exclude") }, icon("plus", 13), r.exclude.length ? "添加" : "添加排除标签"))),
        h("div.sx", h("div.rd-opts",
          h("div.rd-opt", { dataset: { setting: "rule-mode" } },
            h("div.sx-label#lbl-mode", "收录方式"),
            segmented("收录方式", [["review", "审核后再加入"], ["direct", "直接加入"]], r.admission, (v) => change(c, { admission: v }, v === "review" ? "之后匹配的收藏先进入待审核" : "之后匹配的收藏直接加入"), ".sm"),
            h("div.sx-help", r.admission === "review" ? "先进入待审核，侧栏会提示数量" : "标签确定后立即加入")),
          h("div.rd-opt", { dataset: { setting: "rule-conf" } },
            h("div.sx-label#lbl-conf", "只看已确认标签"),
            switchButton(r.confirmedOnly, { "aria-labelledby": "lbl-conf", dataset: { fk: "rule-conf" }, disabled: !supported, onclick: () => change(c, { confirmedOnly: !r.confirmedOnly }, !r.confirmedOnly ? "只按你确认过的标签匹配" : "AI 自动打的标签也算命中") }),
            h("div.sx-help", r.confirmedOnly ? "只按你确认过的标签（个人标签总算确认）" : "AI 自动打的标签也算命中"))))),
      h("aside.sx.rd-pv", { dataset: { setting: "rule-preview" }, "aria-label": "匹配预览" },
        h("div.sx-label", r.admission === "review" ? "待审核" : "匹配预览", h("span.aux", r.admission === "review" ? "审核后加入" : "直接加入")),
        h("div.pv-box", h("div.pv-head#rule-pv-head", "正在计算…"), h("div.pv-list#rule-pv-list"))))));
  renderFoot(c);
  syncUrl();
  loadPreview(c);
}
async function loadPreview(c) {
  const t = ++ui.token;
  const r = ruleOf(c);
  try {
    const [preview, queue] = await Promise.all([
      r.tags.length ? rulePreview(c) : Promise.resolve(null),
      v2(c) && r.admission === "review" ? ruleCandidates(c, ui.showIgnored ? "ignored" : "pending") : Promise.resolve(null),
    ]);
    if (t !== ui.token) return;
    ui.preview = preview;
    ui.queue = queue;
  } catch (e) {
    if (t !== ui.token) return;
    ui.preview = ui.queue = null;
    const head = body?.querySelector("#rule-pv-head");
    if (head) clear(head, "预览暂时无法读取 ", h("button.link", { type: "button", onclick: () => loadPreview(collById(ui.sel)) }, "重试"));
    return;
  }
  paintPreview(c);
}
function paintPreview(c) {
  const head = body?.querySelector("#rule-pv-head"), list = body?.querySelector("#rule-pv-list");
  if (!head || c.id !== ui.sel) return;
  const r = ruleOf(c), p = ui.preview, q = ui.queue;
  const counts = p?.counts || { would_add: p?.items?.length || 0, pending: Number(c.pending_review_count) || 0, ignored: 0, excluded_by_tag: 0 };
  if (!r.tags.length) {
    clear(head, "添加命中标签后，这里会列出匹配的收藏");
    clear(list);
    renderFoot(c);
    return;
  }
  const notQueued = Math.max(0, counts.would_add - counts.pending);
  if (r.admission === "review" && q) {
    clear(head,
      ui.showIgnored ? ["已忽略 ", h("b", String(counts.ignored)), " 条"] : ["待审核 ", h("b", String(counts.pending)), " 条", notQueued ? ["，另有 ", h("b", String(notQueued)), " 条现有收藏也匹配"] : null],
      counts.excluded_by_tag ? h("span.muted", ` · ${counts.excluded_by_tag} 条被排除标签挡住`) : null,
      counts.ignored || ui.showIgnored ? h("button.link", { type: "button", dataset: { fk: "show-ign" }, onclick: () => { ui.showIgnored = !ui.showIgnored; loadPreview(c); } }, ui.showIgnored ? "返回待审核" : `已忽略 ${counts.ignored} 条`) : null);
    const items = q.items || [];
    clear(list, items.length
      ? items.map((it) => h("div.pv-row", { dataset: { id: String(it.link_id) } },
          h("div.pv-main", h("button.tt.link-title", { type: "button", onclick: () => ui.hooks.openItem?.(it.link_id) }, it.title || it.url || "未命名收藏"),
            h("div.hits", (it.matched_tags || []).map((ref) => h("span.tag.hit", refLabel(ref))))),
          h("div.pv-acts",
            ui.showIgnored ? null : h("button.btn.btn-ghost.btn-sm", { type: "button", dataset: { fk: `ri-${it.link_id}` }, disabled: ui.busy, onclick: () => review(c, "review_ignore", [it.link_id]) }, "忽略"),
            h("button.btn.btn-outline.btn-sm", { type: "button", dataset: { fk: `ra-${it.link_id}` }, disabled: ui.busy, onclick: () => review(c, "review_accept", [it.link_id]) }, "加入"))))
      : h("div.pv-empty", ui.showIgnored ? "没有被忽略的收藏" : notQueued ? "待审核为空；可以把现有匹配的收藏放进待审核" : "没有待审核的收藏"));
  } else {
    const items = (p?.items || []).filter((x) => x.candidate_status !== "ignored");
    clear(head, "当前有 ", h("b", String(counts.would_add)), " 条现有收藏匹配、尚未在合集中",
      counts.excluded_by_tag ? h("span.muted", ` · ${counts.excluded_by_tag} 条被排除标签挡住`) : null);
    clear(list, items.length
      ? items.slice(0, 50).map((it) => h("div.pv-row", h("div.pv-main", h("button.tt.link-title", { type: "button", onclick: () => ui.hooks.openItem?.(it.id) }, it.title || "未命名收藏"),
          h("div.hits", (it.matched_tags || []).map((ref) => h("span.tag.hit", refLabel(ref)))))))
      : h("div.pv-empty", "匹配的收藏都已在合集中"));
  }
  renderFoot(c);
}
function renderFoot(c) {
  const foot = body?.querySelector("#rule-foot");
  if (!foot) return;
  const r = ruleOf(c), p = ui.preview, q = ui.queue;
  const counts = p?.counts || { would_add: p?.items?.length || 0, pending: Number(c.pending_review_count) || 0 };
  const pending = q?.items?.length ?? counts.pending;
  const notQueued = Math.max(0, counts.would_add - counts.pending);
  let note, primary = null, secondary = null;
  if (!r.enabled) note = "规则已关闭";
  else if (r.admission === "review") {
    note = pending ? `${counts.pending} 条等待审核` : "没有等待审核的收藏";
    if (notQueued && !ui.showIgnored) secondary = h("button.btn.btn-ghost.btn-sm", { type: "button", disabled: ui.busy, onclick: () => backfill(c, notQueued) }, `把现有 ${notQueued} 条放进待审核`);
    primary = h("button.btn.btn-primary.btn-sm", { type: "button", dataset: { fk: "rule-add-all" }, disabled: ui.busy || !pending || ui.showIgnored,
      onclick: () => review(c, "review_accept", (q?.items || []).map((x) => x.link_id).slice(0, 100)) }, `全部加入${pending ? `（${Math.min(pending, 100)}）` : ""}`);
  } else {
    note = counts.would_add ? `${counts.would_add} 条可以立即加入` : "没有等待处理的收藏";
    primary = h("button.btn.btn-primary.btn-sm", { type: "button", dataset: { fk: "rule-apply" }, disabled: ui.busy || !counts.would_add,
      onclick: () => backfill(c, counts.would_add) }, `立即应用到现有收藏${counts.would_add ? `（${counts.would_add}${p?.has_more ? "+" : ""}）` : ""}`);
  }
  clear(foot, h("span.foot-note", { class: r.enabled ? "" : "off" }, h("span.pip", { class: r.enabled ? "on" : "" }), note), secondary, primary);
}

/* ---------- writes ---------- */
// Rule edits run one after another, each against the revision the previous
// one returned, so quick clicks in the picker are queued rather than dropped.
let chain = Promise.resolve();
const serial = (fn) => (chain = chain.then(fn, fn));
function change(c, patch, message) {
  return serial(() => applyChange(collById(c.id) || c, typeof patch === "function" ? patch(collById(c.id) || c) : patch, message));
}
async function applyChange(c, patch, message) {
  if (!patch) return;
  ui.busy = true;
  try {
    await saveRule(c, patch);
    if (message) toast(message, { tone: "ok" });
  } catch {}
  ui.busy = false;
  ui.preview = ui.queue = null;
  if (body?.isConnected) keep(() => { renderList(); renderDetail(); });
  refreshBadge();
}
async function saveDesc(c, value) {
  const v = value.trim();
  if (v === (c.description || "")) return;
  try {
    await saveCollectionMeta(c, { description: v });
    toast("已保存合集说明", { tone: "ok" });
  } catch {}
  if (body?.isConnected) keep(renderList);
}
async function review(c, type, ids) {
  if (!ids.length || ui.busy) return;
  ui.busy = true;
  renderFoot(c);
  try {
    await collectionOp(c, type, { link_ids: ids });
    toast(type === "review_accept" ? `已加入「${c.name}」${ids.length > 1 ? ` ${ids.length} 条` : ""}` : "已忽略，之后不会再提议这条", { tone: "ok" });
  } catch {}
  ui.busy = false;
  if (body?.isConnected) keep(() => { renderList(); renderDetail(); });
  refreshBadge();
}
async function backfill(c, n) {
  if (ui.busy) return;
  ui.busy = true;
  renderFoot(c);
  try {
    const p = ui.preview || (await rulePreview(c));
    const latest = collById(c.id);
    await collectionOp({ ...latest, revision: p.revision ?? latest.revision }, "backfill");
    toast(ruleOf(c).admission === "review" ? `已把 ${n} 条现有收藏放进待审核` : `已归入 ${n} 条现有收藏`, { tone: "ok" });
  } catch {}
  ui.busy = false;
  if (body?.isConnected) keep(() => { renderList(); renderDetail(); });
  refreshBadge();
}
function picker(anchor, list) {
  const c = collById(ui.sel);
  if (!c) return;
  const other = list === "tags" ? "exclude" : "tags";
  const groups = [...DIMENSIONS, ...MORE_DIMENSIONS];
  openPick(anchor, {
    title: list === "tags" ? "添加命中标签" : "添加排除标签", cls: "pk-chips", width: 420,
    sub: () => `已选 ${ruleOf(collById(ui.sel))[list].length} 个`,
    body: (q) => {
      const r = ruleOf(collById(ui.sel)), ql = fold(q.trim());
      const out = [];
      for (const g of groups) {
        const tags = db.tags.filter((t) => t.d === g.key && !t.mergedInto && (!t.archived || r[list].includes(t.ref)) && (!ql || fold([t.label, ...t.aliases].join(" ")).includes(ql))).sort((a, b) => b.count - a.count);
        if (!tags.length) continue;
        out.push(h("div.tf-group", h("div.tf-group-label", h("span.type-dot", { class: dotClass(g.key) }), g.label, h("span.n", String(tags.length))),
          h("div.tf-chips", tags.map((t) => {
            const on = r[list].includes(t.ref), why = !on && r[other].includes(t.ref) ? (other === "tags" ? "已在命中标签中" : "已在排除标签中") : "";
            const alias = ql && !fold(t.label).includes(ql) ? t.aliases.find((a) => fold(a).includes(ql)) : "";
            return h("button.tf-chip", { type: "button", class: on ? "on" : "", "aria-pressed": String(on), disabled: Boolean(why), title: why || (on ? "点击移除" : null), dataset: { pk: "toggle", id: t.ref },
              onclick: () => toggle(t.ref, list) },
              on ? icon("check", 13) : null, marked(t.label, q.trim()), alias ? h("span.al", alias) : null, h("span.n", String(t.count)));
          }))));
      }
      return out.length ? out : h("div.tf-none", `没有匹配「${q.trim()}」的标签`);
    },
    foot: () => [h("span.pk-hint", list === "tags" ? "可以多选；匹配方式在上方切换" : "带有任一排除标签的收藏不会被加入"), h("button.btn.btn-primary.btn-sm", { type: "button", onclick: () => closePick(true) }, "完成")],
  });
}
async function toggle(ref, list) {
  const id = ui.sel;
  if (!collById(id)) return;
  await serial(async () => {
    const c = collById(id);
    if (!c) return;
    const r = ruleOf(c);
    const on = r[list].includes(ref);
    if (!on && r[list].length >= 20) return toast("最多 20 个标签");
    const next = on ? r[list].filter((x) => x !== ref) : [...r[list], ref];
    // The first hit tag turns a fresh rule on; removing the last turns it off.
    const patch = { [list]: next };
    if (list === "tags" && !r.tags.length && next.length && !r.enabled) patch.enabled = true;
    await applyChange(c, patch, `${on ? "已移除" : "已添加"}${list === "tags" ? "命中" : "排除"}标签「${refLabel(ref)}」`);
  });
  renderPick();
}
async function newCollection() {
  const id = uuid();
  try {
    await send(`/api/collections/${id}/operations`, { operation_key: uuid(), expected_revision: 0, type: "create", name: uniqueName("新合集"), description: "" });
    invalidateQueryReads();
    await loadCollectionsData();
    emit("collections:changed", id);
    ui.sel = id;
    ui.preview = ui.queue = null;
    if (!body?.isConnected) return openSettings("rules", id);
    paint();
    toast("已新建合集，可以在下方改名与挂标签", { tone: "ok" });
    renameInline();
  } catch {}
}
function uniqueName(base) {
  const taken = new Set(db.collections.filter((c) => !c.deleted).map((c) => c.name));
  let n = base, i = 2;
  while (taken.has(n)) n = `${base} ${i++}`;
  return n;
}
/** The title becomes an input until Enter/blur. */
function renameInline() {
  const c = collById(ui.sel), title = body?.querySelector(".rd-titles h4");
  if (!c || !title) return;
  const input = h("input.rd-name", { value: c.name, maxLength: 80, "aria-label": "合集名称" });
  title.replaceWith(input);
  input.focus();
  input.select();
  let done = false;
  const finish = async (saveIt) => {
    if (done) return;
    done = true;
    const v = input.value.trim();
    if (saveIt && v && v !== c.name) { try { await saveCollectionMeta(c, { name: v }); toast("已改名", { tone: "ok" }); } catch {} }
    if (body?.isConnected) keep(() => { renderList(); renderDetail(); });
  };
  input.addEventListener("keydown", (e) => { if (e.isComposing) return; if (e.key === "Enter") { e.preventDefault(); finish(true); } else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); finish(false); } });
  input.addEventListener("blur", () => finish(true));
}
function commit() {
  const c = collById(ui.sel), d = body?.querySelector("#rd-desc");
  if (c && d && d.value.trim() !== (c.description || "")) saveDesc(c, d.value);
}

export function initRulesSection(hooks = {}) {
  ui.hooks = hooks;
  registerSection("rules", {
    render,
    select: (sub, opts) => { if (collById(sub)) select(sub, opts); else if (ui.loaded) toast(`没有找到合集「${sub}」`); },
    canonicalSub: () => ui.sel || "",
    commit,
    leave: () => { closePopovers(); commit(); },
  });
}
