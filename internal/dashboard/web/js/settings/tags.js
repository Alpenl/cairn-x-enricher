// 设置 · 标签库: master list (grouped by type, topics as a two-level tree) and
// a detail editor. Fields save on Enter / blur; every write is one catalog
// operation with a durable receipt (see core.send).
import { h, clear } from "../dom.js";
import { icon } from "../icons.js";
import { toast } from "../ui.js";
import { fetchJSON } from "../api.js";
import { apiParams, emptyFilters } from "../query.js";
import { displayTitle } from "../format.js";
import { DIMENSIONS, MORE_DIMENSIONS, dimLabel, dotClass, fold, marked, ddButton, openMenu, openPick, closePick, renderPick, pickSearchShown, segmented, switchButton, failure, closePopovers } from "./core.js";
import { db, loadTags, loadCollectionsData, tagByRef, topicById, childrenOf, rulesUsing, editTag, setArchived, deleteCustom, createTag, mergeTag, undoMerge } from "./data.js";
import { registerSection, syncUrl, openSettings, setActions, highlight } from "./page.js";

const SEGS = [["all", "全部"], ["topics", "主题"], ["resource_kinds", "资源类型"], ["content_functions", "内容特征"], ["custom", "个人"], ["archived", "已归档"]];
const ui = { seg: "all", q: "", sel: null, creating: null, nameErr: "", aliasErr: "", confirm: null, loaded: false, error: null, ctx: null, hooks: {} };
let body = null;
const subOf = (t) => (t ? `${t.d}:${t.id}` : "");
function resolve(sub) {
  if (!sub) return null;
  const [d, ...rest] = sub.split(":");
  const id = rest.join(":");
  const byKey = db.tags.find((t) => t.d === d && t.id === id);
  if (byKey) return byKey;
  const low = fold(sub);
  const t = db.tags.find((x) => fold(x.label) === low) || db.tags.find((x) => x.aliases.some((a) => fold(a) === low));
  if (t) return t;
  const merged = db.tags.find((x) => x.mergedInto && fold(x.label) === low);
  return merged ? topicById(merged.mergedInto) : null;
}
const sorter = (a, b) => Number(a.archived) - Number(b.archived) || b.count - a.count || a.label.localeCompare(b.label, "zh");
const matches = (t, ql) => !ql || fold(t.label).includes(ql) || t.aliases.some((a) => fold(a).includes(ql));
const displayName = (t) => t.label;

function rows() {
  const ql = fold(ui.q.trim()), seg = ui.seg;
  const inSeg = (t) => (seg === "all" ? !t.archived : seg === "archived" ? t.archived : t.d === seg && !t.archived);
  const pool = db.tags.filter((t) => !t.mergedInto && inSeg(t) && matches(t, ql));
  const out = [];
  const types = seg === "all" ? [...DIMENSIONS.map((d) => d.key), "more"] : seg === "archived" ? [null] : [seg];
  for (const type of types) {
    const list = pool.filter((t) => type == null || (type === "more" ? t.more : t.d === type));
    if (!list.length) continue;
    if (seg === "all") out.push({ head: type, n: list.length });
    if (type === "topics" && !ql) {
      const ids = new Set(list.map((t) => t.id));
      for (const top of list.filter((t) => !t.parent || !ids.has(t.parent)).sort(sorter)) {
        out.push({ t: top, depth: 0 });
        const kids = list.filter((k) => k.parent === top.id).sort(sorter);
        kids.forEach((k, i) => out.push({ t: k, depth: 1, last: i === kids.length - 1 }));
      }
    } else list.sort(sorter).forEach((t) => out.push({ t, depth: 0, flat: true }));
  }
  return out;
}

function render(host, ctx) {
  body = host;
  ui.ctx = ctx;
  clear(body, h("div.md",
    h("aside.md-list", { "aria-label": "标签列表" },
      h("div.md-list-head",
        h("div.lib-tools", { dataset: { setting: "lib-search" } },
          h("label.field", icon("search", 15), h("input#lib-search", { type: "search", placeholder: "搜索标签或别名", autocomplete: "off", value: ui.q, "aria-label": "搜索标签", dataset: { fk: "lib-search" },
            oninput: (e) => { ui.q = e.target.value; renderList(); },
            onkeydown: (e) => { if (e.key === "ArrowDown") { e.preventDefault(); focusRow(ui.sel, true); } else if (e.key === "Escape" && e.target.value) { e.preventDefault(); e.stopPropagation(); e.target.value = ui.q = ""; renderList(); } } }))),
        h("div#lib-seg-host")),
      h("div.md-list-body.md-scroll#lib-list", { role: "listbox", "aria-label": "标签", onkeydown: listKeys }),
      h("div.md-list-foot#lib-foot")),
    h("section.md-detail", { "aria-label": "标签详情" },
      h("div.md-narrow#lib-narrow"),
      h("div.md-scroll.md-detail-body#lib-detail"),
      h("div.md-foot#lib-detail-foot"))));
  setActions([h("button.btn.btn-outline", { type: "button", dataset: { setting: "tag-new" }, onclick: () => startCreate() }, icon("plus", 15), "新建标签")]);
  if (!ui.loaded) {
    clear(body.querySelector("#lib-list"), h("div.sp-loading", "正在读取标签库…"));
    load(ctx.sub);
    return;
  }
  if (ctx.sub) { const t = resolve(ctx.sub); if (t) select(t, { quiet: true }); else toast(`没有找到标签「${ctx.sub}」`); }
  paint();
}
async function load(sub) {
  try {
    await Promise.all([loadTags(), loadCollectionsData().catch(() => [])]);
    ui.loaded = true;
    ui.error = null;
  } catch (e) {
    ui.error = e;
  }
  if (!body?.isConnected) return;
  if (ui.error) {
    clear(body, h("div.sp-error", h("p", "无法读取标签库。"), h("button.btn.btn-outline", { type: "button", onclick: () => load(sub) }, "重试")));
    return;
  }
  if (!db.catalog?.catalog) {
    clear(body, h("div.sp-error", h("p", "当前服务还未升级标签管理。")));
    return;
  }
  const t = sub ? resolve(sub) : null;
  if (sub && !t) toast(`没有找到标签「${sub}」`);
  if (t) select(t, { quiet: true });
  paint();
  focusRow(ui.sel, false);
}
function paint() {
  renderSeg();
  renderList();
  renderDetail();
}
function renderSeg() {
  const host = body.querySelector("#lib-seg-host");
  if (!host) return;
  clear(host, segmented("标签范围", SEGS.map(([v, l]) => [v, l]), ui.seg, (v) => { ui.seg = v; renderSeg(); renderList(); }, ".sm.full"));
}
function renderList() {
  const box = body?.querySelector("#lib-list");
  if (!box || !ui.loaded) return;
  const list = rows(), q = ui.q.trim();
  const visible = list.filter((r) => r.t);
  if (!ui.creating && !tagByRef(ui.sel) && visible.length) ui.sel = visible[0].t.ref;
  const typeLabel = (k) => (k === "more" ? "更多类型" : dimLabel(k));
  clear(box, visible.length
    ? list.map((r) => {
        if (r.head) return h("div.lib-head", { role: "presentation" }, h("span.type-dot", { class: r.head === "more" ? "type-other" : dotClass(r.head) }), typeLabel(r.head), h("span.n", String(r.n)));
        const t = r.t, sel = t.ref === ui.sel && !ui.creating;
        const par = r.flat && t.parent ? topicById(t.parent) : null;
        const name = marked(displayName(t), q);
        if (par) name.prepend(h("span.par", `${par.label} /`));
        return h("div.lib-row", { class: [sel ? "selected" : "", t.archived ? "archived" : "", r.depth ? "child" : "", r.last ? "last" : ""].filter(Boolean).join(" "),
          role: "option", "aria-selected": String(sel), tabindex: sel ? "0" : "-1", dataset: { ref: t.ref, fk: `row-${t.ref}` }, onclick: () => select(t, { focus: true }) },
          h("span.type-dot", { class: dotClass(t.d) }), name,
          t.archived ? h("span.mk", "已归档") : t.kind === "system" && !t.ai ? h("span.mk", { title: "不参与 AI 自动打标" }, "AI 关") : null,
          h("span.n", String(t.count)));
      })
    : h("div.lib-empty", q ? `没有匹配「${q}」的标签` : "这里还没有标签",
        q ? h("button.btn.btn-outline.btn-sm", { type: "button", onclick: () => startCreate(q) }, icon("plus", 14), `新建「${q}」`) : null));
  if (!box.querySelector('.lib-row[tabindex="0"]')) box.querySelector(".lib-row")?.setAttribute("tabindex", "0");
  const active = db.tags.filter((t) => !t.archived && !t.mergedInto);
  clear(body.querySelector("#lib-foot"), h("span", `${active.length} 个标签`), h("span.dot", "·"), h("span", `${active.filter((t) => t.kind === "system" && !t.ai).length} 个不参与 AI`),
    db.tags.length - active.length ? [h("span.dot", "·"), h("span", `${db.tags.filter((t) => t.archived && !t.mergedInto).length} 个已归档`)] : null);
  const sel = tagByRef(ui.sel);
  clear(body.querySelector("#lib-narrow"), ddButton(sel ? [h("span.type-dot", { class: dotClass(sel.d) }), sel.label, h("span.muted", `· ${sel.count} 条`)] : "选择标签",
    { "aria-label": "选择标签", dataset: { fk: "lib-narrow" }, onclick: (e) => narrowMenu(e.currentTarget) }));
}
function listKeys(e) {
  const items = [...body.querySelectorAll("#lib-list .lib-row")];
  const i = items.indexOf(document.activeElement);
  if (i < 0) return;
  const step = { ArrowDown: 1, ArrowUp: -1 }[e.key];
  if (step) {
    e.preventDefault();
    if (step < 0 && i === 0) return body.querySelector("#lib-search")?.focus();
    const n = items[Math.max(0, Math.min(items.length - 1, i + step))];
    const t = tagByRef(n.dataset.ref);
    if (t) select(t, { focus: true });
  } else if (e.key === "Enter") {
    e.preventDefault();
    body.querySelector("#td-name")?.focus();
  } else if (e.key === "Home" || e.key === "End") {
    e.preventDefault();
    const t = tagByRef(items[e.key === "Home" ? 0 : items.length - 1].dataset.ref);
    if (t) select(t, { focus: true });
  }
}
function focusRow(ref, focus) {
  const row = body?.querySelector(`#lib-list .lib-row[data-ref="${CSS.escape(ref || "")}"]`);
  if (!row) return;
  row.scrollIntoView({ block: "nearest" });
  if (focus) row.focus({ preventScroll: true });
}
function select(t, { focus = false, quiet = false } = {}) {
  if (!t) return;
  if (t.ref === ui.sel && !ui.creating) { if (focus) focusRow(t.ref, true); return; }
  if (!quiet) commit();
  ui.sel = t.ref;
  ui.creating = null;
  ui.confirm = null;
  ui.nameErr = ui.aliasErr = "";
  // Make sure the row is visible under the current segment/search.
  if (!rows().some((r) => r.t?.ref === t.ref)) { ui.seg = t.archived ? "archived" : "all"; ui.q = ""; const s = body?.querySelector("#lib-search"); if (s) s.value = ""; renderSeg(); }
  if (!body?.isConnected) return;
  renderList();
  renderDetail();
  body.querySelector("#lib-detail").scrollTop = 0;
  if (focus) focusRow(t.ref, true);
}
function narrowMenu(anchor) {
  const items = [];
  for (const d of [...DIMENSIONS, ...MORE_DIMENSIONS]) {
    const list = db.tags.filter((t) => t.d === d.key && !t.mergedInto).sort(sorter);
    if (!list.length) continue;
    if (items.length) items.push({ sep: true });
    items.push({ head: d.label });
    list.forEach((t) => items.push({ value: t.ref, label: `${t.parent ? "　" : ""}${t.label}${t.archived ? "（已归档）" : t.kind === "system" && !t.ai ? " · AI 关" : ""}`, dot: dotClass(t.d) }));
  }
  openMenu(anchor, items, { value: ui.sel, matchWidth: true, onSelect: (ref) => select(tagByRef(ref)) });
}

/* ---------- detail ---------- */
/** Re-render while keeping focus (by data-fk), caret and scroll. */
function keep(fn) {
  const a = document.activeElement;
  const fk = a && body?.contains(a) ? a.dataset?.fk : null;
  const caret = fk && typeof a.selectionStart === "number" ? [a.selectionStart, a.selectionEnd] : null;
  const scroll = body?.querySelector("#lib-detail")?.scrollTop ?? 0;
  const listScroll = body?.querySelector("#lib-list")?.scrollTop ?? 0;
  fn();
  if (!body?.isConnected) return;
  body.querySelector("#lib-detail").scrollTop = scroll;
  body.querySelector("#lib-list").scrollTop = listScroll;
  if (fk) {
    const el = body.querySelector(`[data-fk="${CSS.escape(fk)}"]`);
    if (el && !el.disabled) { el.focus({ preventScroll: true }); if (caret) try { el.setSelectionRange(...caret); } catch {} }
  }
}
const refresh = () => keep(() => { renderList(); renderDetail(); });

function renderDetail() {
  const box = body?.querySelector("#lib-detail"), foot = body?.querySelector("#lib-detail-foot");
  if (!box) return;
  if (ui.creating) return renderCreate(box, foot);
  const t = tagByRef(ui.sel);
  if (!t) {
    clear(box, h("div.md-empty", icon("tag", 28), h("div.t", "选择一个标签"), h("div.s", "在左侧选择标签，查看并编辑它的含义、别名与层级。")));
    foot.hidden = true;
    syncUrl();
    return;
  }
  foot.hidden = false;
  const system = t.kind === "system";
  const kids = childrenOf(t), par = t.parent ? topicById(t.parent) : null;
  const kicker = t.d === "topics" ? (par ? `细分主题 · 属于「${par.label}」` : kids.length ? `一级主题 · ${kids.length} 个细分主题` : "一级主题") : dimLabel(t.d);
  const name = h("input.td-name#td-name", { class: ui.nameErr ? "invalid" : "", value: t.label, maxLength: 80, autocomplete: "off", spellcheck: false, "aria-label": "标签名称", "aria-describedby": "td-name-msg", dataset: { fk: "td-name" },
    onkeydown: (e) => { if (e.isComposing) return; if (e.key === "Enter") { e.preventDefault(); commitName(t, name.value); } else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); name.value = t.label; ui.nameErr = ""; name.blur(); } },
    onblur: () => commitName(t, name.value, { silent: true }) });
  clear(box, h("div.td",
    h("div.td-head", { dataset: { setting: "tag-name" } },
      h("div.td-kicker", h("span.type-dot", { class: dotClass(t.d) }), kicker, t.archived ? h("span.mk", "已归档") : null),
      name,
      h("div.td-msg#td-name-msg", { class: ui.nameErr ? "err" : "" }, ui.nameErr ? [icon("alert", 12), ui.nameErr] : "点击名称即可修改，按 Enter 保存"),
      t.archived ? h("div.td-banner", icon("archive", 15), h("span", "已归档：不会再出现在 AI 建议和标签筛选中，已有收藏上的标签保留。")) : null),
    h("div.sx", { dataset: { setting: "tag-type" } }, h("div.sx-label", "类型"),
      segmented("标签类型", (t.more ? [[t.d, dimLabel(t.d)]] : DIMENSIONS.map((d) => [d.key, d.label, h("span.type-dot", { class: dotClass(d.key) })])), t.d, () => {}),
      h("div.sx-help", t.more ? "这一类只为兼容保留，不在标签筛选和新建中出现。" : "类型在新建时确定，之后不能更改；需要换类型时，新建一个标签再把这个合并过去。")),
    t.d === "topics" ? parentBlock(t, kids, par) : null,
    system ? descBlock(t) : h("div.sx", h("div.sx-help", icon("pencil", 12), "个人标签只由你添加，AI 不会打，也不会参考它。改名会同步到所有使用它的收藏。")),
    system && t.ai ? examplesBlock(t) : null,
    system ? aliasBlock(t) : null,
    system ? h("div.sx.sx-switch", { dataset: { setting: "tag-ai" } },
      h("div.txt", h("div.ttl#td-ai-lbl", "参与 AI 自动打标"), h("div.sx-help", t.humanOnly ? "这是个人判断，只能由你手动添加。" : t.ai ? "AI 会参考含义说明，为新收藏建议这个标签。" : "AI 不会再建议这个标签，只能由你手动添加；已有收藏上的标签保留。")),
      switchButton(t.ai, { "aria-labelledby": "td-ai-lbl", dataset: { fk: "td-ai" }, disabled: t.archived || t.humanOnly, title: t.archived ? "已归档的标签不参与打标" : null, onclick: () => toggleAI(t) })) : null,
    t.d === "topics" && !t.archived ? h("div.sx.sx-switch", h("div.txt", h("div.ttl#td-broad-lbl", "宽主题"), h("div.sx-help", "宽主题是大方向，在标签筛选里排在前面；细分主题用来缩小范围。")),
      switchButton(t.granularity === "broad", { "aria-labelledby": "td-broad-lbl", dataset: { fk: "td-broad" }, onclick: () => save(t, { granularity: t.granularity === "broad" ? "specific" : "broad" }, t.granularity === "broad" ? `「${t.label}」改为细分主题` : `「${t.label}」设为宽主题`) })) : null,
    usageBlock(t),
    rulesBlock(t)));
  renderFoot(t);
  syncUrl();
}
function parentBlock(t, kids, par) {
  const choices = db.tags.filter((x) => x.d === "topics" && !x.parent && !x.archived && !x.mergedInto && x.id !== t.id);
  return h("div.sx", { dataset: { setting: "tag-parent" } },
    h("div.sx-label", "上级主题"),
    h("div.sx-inline", ddButton(par ? [h("span.type-dot.type-topics"), par.label] : h("span.muted", "无 · 作为一级主题"), {
      id: "td-parent", "aria-label": "上级主题", dataset: { fk: "td-parent" }, disabled: kids.length > 0 || t.archived,
      onclick: (e) => openMenu(e.currentTarget, [{ value: "", label: "无 · 作为一级主题" }, choices.length ? { sep: true } : null, ...choices.sort(sorter).map((x) => ({ value: x.id, label: x.label, dot: "type-topics" }))].filter(Boolean),
        { value: t.parent || "", minWidth: 240, onSelect: (v) => setParent(t, v || null) }) })),
    h("div.sx-help", kids.length ? `它下面已有细分主题（${kids.map((k) => k.label).join("、")}），不能再挂到其他主题下。` : "选一个一级主题，让它成为其下的细分主题；层级只用于展示和筛选分组，不会自动继承。"));
}
function descBlock(t) {
  const area = h("textarea.td-desc#td-desc", { rows: 3, maxLength: 1000, value: t.description, placeholder: "用一两句话写清什么样的收藏该打上这个标签…", dataset: { fk: "td-desc" },
    onblur: () => saveDesc(t, area.value), onkeydown: (e) => { if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); area.value = t.description; area.blur(); } } });
  return h("div.sx", { dataset: { setting: "tag-desc" } },
    h("div.sx-label", h("label", { for: "td-desc" }, "含义说明")),
    area,
    h("div.sx-help", icon("sparkles", 12), t.ai ? `AI 会依据这段定义判断一条收藏是否该打上「${t.label}」。只影响之后的新收藏。` : "AI 关闭时这段说明只作备忘。"));
}
/** Chip input used for aliases and the include/exclude examples. */
function chipBox(t, key, { placeholder, max = 30, fk, describedby, invalid }) {
  const values = [...(t.raw?.[key] || [])];
  const input = h("input", { placeholder: values.length ? "添加…" : placeholder, maxLength: 80, autocomplete: "off", "aria-label": placeholder, "aria-describedby": describedby, dataset: { fk } });
  const commitValue = () => {
    const v = input.value.trim();
    if (!v) return;
    if (key === "aliases") {
      const err = aliasConflict(t, v);
      if (err) { ui.aliasErr = err; keep(renderDetail); return; }
    } else if (values.some((x) => fold(x) === fold(v))) { input.value = ""; return; }
    if (values.length >= max) return toast(`最多 ${max} 条`);
    ui.aliasErr = "";
    save(t, { [key]: [...values, v] }, key === "aliases" ? `已为「${t.label}」添加别名「${v}」` : "已保存").then(() => { const n = body?.querySelector(`[data-fk="${fk}"]`); if (n) { n.value = ""; n.focus(); } });
  };
  input.addEventListener("keydown", (e) => {
    if (e.isComposing) return;
    if (e.key === "Enter") { e.preventDefault(); commitValue(); }
    else if (e.key === "Backspace" && !input.value && values.length) save(t, { [key]: values.slice(0, -1) }, `已移除「${values.at(-1)}」`);
    else if (e.key === "Escape" && input.value) { e.preventDefault(); e.stopPropagation(); input.value = ""; ui.aliasErr = ""; keep(renderDetail); }
  });
  input.addEventListener("blur", () => { if (input.value.trim() && key !== "aliases") commitValue(); });
  return h("div.alias-box", { class: invalid ? "invalid" : "", onclick: (e) => { if (e.target === e.currentTarget) input.focus(); } },
    values.map((v, i) => h("span.alias", h("span", v), h("button.alias-x", { type: "button", "aria-label": `移除「${v}」`, dataset: { fk: `${fk}-x-${i}` }, onclick: () => save(t, { [key]: values.filter((_, j) => j !== i) }, `已移除「${v}」`) }, icon("x", 10)))),
    input);
}
function aliasBlock(t) {
  return h("div.sx", { dataset: { setting: "tag-alias" } },
    h("div.sx-label", "同义词 / 别名"),
    chipBox(t, "aliases", { placeholder: "添加别名，按 Enter", fk: "alias-input", describedby: "alias-msg", invalid: Boolean(ui.aliasErr), max: 20 }),
    h("div.sx-help#alias-msg", { class: ui.aliasErr ? "err" : "" }, ui.aliasErr ? [icon("alert", 12), ui.aliasErr] : "搜索与筛选时，别名会被当作同一个标签。"));
}
function examplesBlock(t) {
  return h("div.sx", { dataset: { setting: "tag-examples" } },
    h("div.sx-label", "会打的例子"),
    chipBox(t, "includes", { placeholder: "加一个例子，按 Enter", fk: "inc-input" }),
    h("div.sx-label", { style: "margin-top:14px" }, "这些情况不打"),
    chipBox(t, "excludes", { placeholder: "加一个例外，按 Enter", fk: "exc-input" }),
    h("div.sx-help", "具体的正反例能让 AI 少打错；只影响之后的新收藏。"));
}
let usageToken = 0;
function usageBlock(t) {
  const host = h("div.use-list-host");
  const sx = h("div.sx", { dataset: { setting: "tag-usage" } },
    h("div.sx-label", "使用情况", h("span.aux", `${t.count} 条收藏`)),
    host);
  if (!t.count) clear(host, h("div.sx-quiet", "还没有收藏使用这个标签。"));
  else {
    const token = ++usageToken;
    clear(host, h("div.sx-quiet", "正在读取…"));
    const params = apiParams({ ...emptyFilters(), curation_status: "all", tags: t.ref, tags_mode: "any" }, "", { limit: 4 });
    fetchJSON(`/api/bookmarks?${params}`).then((page) => {
      if (token !== usageToken || !host.isConnected) return;
      const items = page.items || [];
      clear(host, items.length ? h("div.use-list", items.map((it) => h("button.use-row", { type: "button", onclick: () => openItem(it.id) },
        icon("fileText", 15), h("span.tt", displayTitle(it).text), icon("chevronRight", 14)))) : null,
      h("button.btn.btn-ghost.btn-sm", { type: "button", style: "margin-top:6px", onclick: () => showInLibrary(t) }, icon("layers", 14), `在收藏库中查看全部 ${t.count} 条`));
    }).catch(() => {
      if (token === usageToken && host.isConnected) clear(host, h("button.btn.btn-ghost.btn-sm", { type: "button", onclick: () => showInLibrary(t) }, icon("layers", 14), `在收藏库中查看 ${t.count} 条`));
    });
  }
  return sx;
}
function rulesBlock(t) {
  const users = rulesUsing(t.ref);
  return h("div.sx", { dataset: { setting: "tag-rules" } },
    h("div.sx-label", "关联合集规则"),
    users.length
      ? h("div.rule-chips", users.map((r) => h("button.rule-chip", { type: "button", onclick: () => openSettings("rules", r.collection_id) },
          icon("folder", 13), h("span", r.name), h("span.role", { class: r.role === "exclude" ? "ex" : "" }, r.role === "exclude" ? "排除" : "命中"), r.enabled ? null : h("span.role", "已关闭"))))
      : h("div.sx-quiet", "没有合集规则使用这个标签。"));
}
function renderFoot(t) {
  const foot = body.querySelector("#lib-detail-foot");
  if (ui.confirm?.kind === "delete" && ui.confirm.ref === t.ref) {
    clear(foot, h("div.foot-confirm", { role: "alertdialog", "aria-label": "确认删除" }, icon("alert", 15),
      h("span", t.count ? `删除「${t.label}」？它会从 ${t.count} 条收藏上移除，不能撤销。` : `删除「${t.label}」？`),
      h("button.btn.btn-ghost.btn-sm", { type: "button", dataset: { fk: "cf-cancel" }, onclick: () => { ui.confirm = null; keep(() => renderFoot(t)); body.querySelector('[data-fk="tag-delete"]')?.focus(); } }, "取消"),
      h("button.btn.btn-danger.btn-sm", { type: "button", dataset: { fk: "cf-delete" }, onclick: () => doDelete(t) }, "删除")));
    foot.querySelector('[data-fk="cf-delete"]').focus();
    return;
  }
  const rules = rulesUsing(t.ref);
  const canMerge = (t.d === "topics" || t.d === "custom") && !t.archived;
  clear(foot,
    h("span.foot-note", icon("check", 13), "修改即时保存"),
    t.kind === "custom"
      ? h("button.btn.btn-ghost.btn-sm.danger", { type: "button", dataset: { fk: "tag-delete", setting: "tag-delete" }, disabled: rules.length > 0,
          title: rules.length ? `被合集规则使用：${rules.map((r) => r.name).join("、")}` : "删除这个个人标签",
          onclick: () => { ui.confirm = { kind: "delete", ref: t.ref }; renderFoot(t); } }, "删除")
      : null,
    h("button.btn.btn-outline.btn-sm", { type: "button", dataset: { fk: "tag-arch", setting: "tag-archive" }, onclick: () => archive(t, !t.archived) }, icon(t.archived ? "undo" : "archive", 14), t.archived ? "恢复" : "归档"),
    h("button.btn.btn-outline.btn-sm", { type: "button", dataset: { fk: "tag-merge", setting: "tag-merge" }, "aria-haspopup": "dialog", "aria-expanded": "false", disabled: !canMerge,
      title: t.archived ? "请先恢复再合并" : canMerge ? null : "只有主题和个人标签可以合并", onclick: (e) => mergePicker(e.currentTarget, t) }, icon("layers", 14), "合并到…"));
}

/* ---------- create ---------- */
function startCreate(name = "") {
  commit();
  ui.creating = { name, dim: ui.seg === "custom" ? "custom" : "topics", desc: "" };
  if (!body?.isConnected) return;
  renderList();
  renderDetail();
  body.querySelector("#new-name")?.focus();
}
function renderCreate(box, foot) {
  const c = ui.creating;
  const name = h("input.td-name#new-name", { value: c.name, maxLength: 80, placeholder: "标签名称", autocomplete: "off", "aria-label": "新标签名称", oninput: (e) => (c.name = e.target.value),
    onkeydown: (e) => { if (e.isComposing) return; if (e.key === "Enter") { e.preventDefault(); if (c.dim === "topics" && !c.desc.trim()) box.querySelector("#new-desc")?.focus(); else submitCreate(); } else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); cancelCreate(); } } });
  clear(box, h("div.td",
    h("div.td-head", h("div.td-kicker", icon("plus", 12), "新建标签"), name, h("div.td-msg", "主题由 AI 自动打；个人标签只由你手动添加。")),
    h("div.sx", h("div.sx-label", "类型"),
      segmented("新标签类型", [["topics", "主题", h("span.type-dot.type-topics")], ["custom", "个人标签", h("span.type-dot.type-custom")]], c.dim, (v) => { c.dim = v; renderDetail(); box.querySelector("#new-name")?.focus(); }),
      h("div.sx-help", "资源类型与内容特征是 AI 判定用的固定词表，不能新增。")),
    c.dim === "topics" ? h("div.sx", h("div.sx-label", h("label", { for: "new-desc" }, "含义说明")),
      h("textarea.td-desc#new-desc", { rows: 3, maxLength: 1000, value: c.desc, placeholder: "AI 什么时候打它？例如：介绍或提供浏览器扩展的收藏", oninput: (e) => (c.desc = e.target.value),
        onkeydown: (e) => { if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); submitCreate(); } else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); cancelCreate(); } } }),
      h("div.sx-help", icon("sparkles", 12), "必填：AI 靠这句话判断什么时候打这个标签。")) : null));
  foot.hidden = false;
  clear(foot, h("span.foot-note", "Enter 创建 · Esc 取消"),
    h("button.btn.btn-ghost.btn-sm", { type: "button", onclick: cancelCreate }, "取消"),
    h("button.btn.btn-primary.btn-sm#new-submit", { type: "button", onclick: submitCreate }, "创建"));
}
function cancelCreate() {
  ui.creating = null;
  renderList();
  renderDetail();
  focusRow(ui.sel, true);
}
async function submitCreate() {
  const c = ui.creating;
  if (!c) return;
  const name = c.name.trim();
  if (!name) return body.querySelector("#new-name")?.focus();
  const err = nameConflict(name, null);
  if (err) return toast(err);
  if (c.dim === "topics" && !c.desc.trim()) { toast("写一句说明，AI 才知道什么时候打这个标签"); return body.querySelector("#new-desc")?.focus(); }
  const btn = body.querySelector("#new-submit");
  if (btn) btn.disabled = true;
  try {
    const made = await createTag(c.dim, name, c.desc.trim());
    ui.creating = null;
    ui.q = "";
    ui.seg = "all";
    if (made) ui.sel = made.ref;
    paint();
    focusRow(ui.sel, false);
    body.querySelector("#td-name")?.focus();
    toast(`已新建「${name}」`, { tone: "ok" });
  } catch {
    if (btn?.isConnected) btn.disabled = false;
  }
}

/* ---------- writes ---------- */
function nameConflict(name, self) {
  const low = fold(name);
  const dup = db.tags.find((t) => t !== self && !t.mergedInto && fold(t.label) === low);
  if (dup) return `已有同名标签「${dup.label}」${dup.archived ? "（已归档）" : ""}`;
  const al = db.tags.find((t) => t !== self && t.aliases.some((a) => fold(a) === low));
  if (al) return `「${name}」已是「${al.label}」的别名`;
  return "";
}
function aliasConflict(t, a) {
  const low = fold(a);
  if (fold(t.label) === low) return "别名不能与标签名相同";
  if (t.aliases.some((x) => fold(x) === low)) return `「${a}」已在别名中`;
  const dup = db.tags.find((x) => x !== t && fold(x.label) === low);
  if (dup) return `已有标签叫「${dup.label}」，如是同义请用「合并到…」`;
  const al = db.tags.find((x) => x !== t && x.aliases.some((y) => fold(y) === low));
  if (al) return `「${a}」已是「${al.label}」的别名`;
  return "";
}
async function save(t, definition, message) {
  try {
    await editTag(t, definition);
    if (message) toast(message, { tone: "ok" });
    return true;
  } catch {
    return false;
  } finally {
    if (body?.isConnected) refresh();
  }
}
let naming = false;
async function commitName(t, value, { silent = false } = {}) {
  const name = (value || "").trim();
  if (naming || name === t.label) { if (ui.nameErr && name === t.label) { ui.nameErr = ""; keep(renderDetail); } return; }
  const err = !name ? "标签名称不能为空" : nameConflict(name, t);
  if (err) {
    if (silent) return;
    ui.nameErr = err;
    keep(renderDetail);
    return;
  }
  naming = true;
  ui.nameErr = "";
  const old = t.label;
  try {
    await save(t, { label: name }, `已将「${old}」重命名为「${name}」`);
  } finally {
    naming = false;
  }
}
function saveDesc(t, value) {
  const v = (value || "").trim();
  if (v === t.description) return;
  if (!v) { toast("说明不能为空，AI 要靠它判断"); keep(renderDetail); return; }
  save(t, { description: v }, `已更新「${t.label}」的含义说明`);
}
function setParent(t, pid) {
  if ((t.parent || null) === pid) return;
  const related = (t.raw.relations || []).filter((r) => r.kind === "related");
  save(t, { relations: [...related, ...(pid ? [{ id: pid, kind: "broader" }] : [])] }, pid ? `「${t.label}」现在是「${topicById(pid)?.label}」下的细分主题` : `「${t.label}」已改为一级主题`);
}
async function toggleAI(t) {
  if (t.humanOnly || t.archived) return;
  const next = !t.ai;
  if (next && !t.description) { toast("先写一句含义说明，AI 才知道什么时候打它"); body.querySelector("#td-desc")?.focus(); return; }
  await save(t, { ai_enabled: next }, next ? `AI 会给新收藏建议「${t.label}」` : `「${t.label}」改为只手动，已有收藏上的标签保留`);
}
async function archive(t, archived) {
  try {
    await setArchived(t, archived);
    const rules = rulesUsing(t.ref).length;
    ui.seg = archived ? (ui.seg === "archived" ? "archived" : ui.seg) : ui.seg;
    toast(archived ? `已归档「${t.label}」${rules ? ` · ${rules} 条合集规则将跳过它` : " · 不再出现在新建议中"}` : `已恢复「${t.label}」`, archived ? { action: { label: "撤销", run: () => { const now = tagByRef(t.ref); if (now) archive(now, false); } } } : { tone: "ok" });
  } catch {}
  if (body?.isConnected) { renderSeg(); refresh(); body.querySelector('[data-fk="tag-arch"]')?.focus(); }
}
async function doDelete(t) {
  const list = rows().filter((r) => r.t), i = list.findIndex((r) => r.t.ref === t.ref);
  try {
    await deleteCustom(t);
    ui.confirm = null;
    const next = list[i + 1] || list[i - 1];
    ui.sel = next?.t.ref ?? null;
    toast(`已删除标签「${t.label}」`, { tone: "ok" });
  } catch {
    ui.confirm = null;
  }
  if (body?.isConnected) { refresh(); focusRow(ui.sel, true); }
}
function mergePicker(anchor, src) {
  const st = { target: null, busy: false };
  const targets = (q) => db.tags.filter((t) => t.d === src.d && t.ref !== src.ref && !t.archived && !t.mergedInto && matches(t, fold(q.trim()))).sort(sorter);
  openPick(anchor, {
    title: `把「${src.label}」合并到…`, cls: "pk-merge", width: 360, placeholder: "搜索目标标签",
    body: (q) => {
      if (st.target) {
        const dst = tagByRef(st.target), rules = rulesUsing(src.ref).length;
        return h("div.mg-confirm",
          h("div.mg-pair", h("span.tag.tag-ok", src.label), icon("chevronRight", 16), h("span.tag.tag-ok", dst.label)),
          h("ul.mg-list",
            h("li", `${src.count} 条收藏上的「${src.label}」改为「${dst.label}」，重复的会去重（后台逐条处理）`),
            src.kind === "system" ? h("li", `「${src.label}」${src.aliases.length ? `的 ${src.aliases.length} 个别名并入「${dst.label}」，它本身` : ""}归档并记为「${dst.label}」的同义词`) : h("li", `「${src.label}」处理完后归档`),
            rules ? h("li", `${rules} 条合集规则改为使用「${dst.label}」`) : null,
            childrenOf(src).length ? h("li", `${childrenOf(src).length} 个细分主题改挂到「${dst.label}」下`) : null,
            h("li", "合并后可以撤销；之后被你手动改过的收藏保持你的修改")));
      }
      const list = targets(q);
      return list.length
        ? [h("div.menu-head", dimLabel(src.d)), list.map((t) => h("button.menu-item", { type: "button", dataset: { pk: "target", id: t.ref }, onclick: () => { st.target = t.ref; pickSearchShown(false); renderPick(true); pickSearchShown(false); document.querySelector(".pick-pop [data-pk='merge']")?.focus(); } },
            h("span.type-dot", { class: dotClass(t.d) }), marked(t.label, q.trim()), h("span.meta", `${t.count} 条`)))]
        : h("div.tf-none", "没有可合并的目标标签");
    },
    foot: () => st.target
      ? [h("button.btn.btn-ghost.btn-sm", { type: "button", onclick: () => { st.target = null; renderPick(true); document.querySelector(".pick-pop .pk-search input")?.focus(); } }, "返回"), h("span.grow"),
         h("button.btn.btn-primary.btn-sm", { type: "button", dataset: { pk: "merge" }, disabled: st.busy, onclick: () => doMerge(src, tagByRef(st.target), st) }, "确认合并")]
      : h("span.pk-hint", "只能合并到同一类的标签"),
    onEscape: () => {
      if (!st.target) return false;
      st.target = null;
      renderPick(true);
      document.querySelector(".pick-pop .pk-search input")?.focus();
      return true;
    },
  });
}
async function doMerge(src, dst, st) {
  if (!dst || st.busy) return;
  st.busy = true;
  renderPick();
  try {
    const merge = await mergeTag(src, dst);
    closePick(false);
    ui.sel = dst.ref;
    ui.confirm = null;
    if (body?.isConnected) { refresh(); focusRow(dst.ref, true); }
    toast(`已将「${src.label}」合并到「${dst.label}」`, { tone: "ok", duration: 9000, action: { label: "撤销", run: async () => {
      try {
        await undoMerge(merge);
        ui.sel = src.ref;
        if (body?.isConnected) { refresh(); focusRow(src.ref, true); }
        toast(`已撤销合并，「${src.label}」已恢复`);
      } catch {}
    } } });
  } catch {
    st.busy = false;
    renderPick();
  }
}

function openItem(id) {
  ui.hooks.openItem?.(id);
}
function showInLibrary(t) {
  ui.hooks.showTag?.(t.ref);
}
/** Flush the edit in progress before the section re-renders or closes. */
function commit() {
  const t = tagByRef(ui.sel);
  if (!t || !body?.isConnected) return;
  const n = body.querySelector("#td-name");
  if (n && n.value.trim() && n.value.trim() !== t.label) commitName(t, n.value, { silent: true });
  const d = body.querySelector("#td-desc");
  if (d && d.value.trim() && d.value.trim() !== t.description) saveDesc(t, d.value);
}

export function initTagsSection(hooks = {}) {
  ui.hooks = hooks;
  registerSection("tags", {
    render,
    select: (sub, opts) => { const t = resolve(sub); if (t) select(t, opts); },
    canonicalSub: () => (ui.creating ? "" : subOf(tagByRef(ui.sel))),
    commit,
    leave: () => { closePopovers(); commit(); },
    create: (name) => startCreate(name),
    focusSearch: () => body?.querySelector("#lib-search")?.focus(),
  });
}
export const highlightTagSetting = highlight;
