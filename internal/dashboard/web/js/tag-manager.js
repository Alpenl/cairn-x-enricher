// Tag library: one scrolling list grouped by dimension. Rows expand in place
// for editing; every field saves on blur. Catalog writes keep a durable receipt
// so a lost response retries the same operation instead of creating twice.
import { openWorkspace, restoreWorkspace } from "./workspace.js";
import { h, clear, byId } from "./dom.js";
import { icon } from "./icons.js";
import { openDialog, confirmAction, toast } from "./ui.js";
import { loadV1, loadV2, loadCustomTags } from "./taxonomy.js";
import { invalidateTaxonomyReads } from "./api.js";
import { emit } from "./store.js";
import { emptyFilters } from "./query.js";

const GROUPS = [
  { key: "topics", label: "主题", hint: "讲的是什么", create: true },
  { key: "resource_kinds", label: "资源类型", hint: "有什么能直接拿来用" },
  { key: "content_functions", label: "内容特征", hint: "内容是怎么写的" },
  { key: "custom", label: "自定义标记", hint: "只由你添加，AI 不会使用", create: true },
];
const MORE = [
  { key: "carriers", label: "载体", hint: "单帖、续帖、长文或视频" },
  { key: "affordances", label: "潜在用途", hint: "可以拿来做什么" },
  { key: "forms", label: "旧版形态", hint: "只为兼容旧标签保留" },
  { key: "uses", label: "旧版用途", hint: "只为兼容旧标签保留" },
];
const dimensions = Object.fromEntries([...GROUPS, ...MORE].filter((g) => g.key !== "custom").map((g) => [g.key, g.label]));
const FILTERS = [
  ["all", "全部"],
  ["ai", "AI 自动打"],
  ["manual", "只手动"],
  ["attention", "需要留意"],
];
const labels = {
  tag_collision: "名称或别名已被其他标签使用",
  system_tag_exists: "已有同名的系统标签",
  custom_tag_exists: "已有同名的自定义标记",
  invalid_tag_definition: "名称和说明都不能为空，检查字段长度",
  invalid_label: "名称不能为空，最多 80 个字符",
  last_ai_tag: "这一组至少要保留一个由 AI 自动打的标签",
  revision_conflict: "标签已在别处修改，已载入最新内容，请再试一次",
  tag_limit: "这一组的标签数量已达上限",
  personal_use_human_only: "「反对」只能由你手动添加",
  tag_in_use: "这个标记还在使用中",
  tag_not_found: "这个标签已不存在，已载入最新标签库",
  invalid_tag_operation: "标签操作无效，请载入最新标签库后重试",
  invalid_dimension: "这一组暂不支持新建标签",
  backend_error: "保存结果暂未确认，请重试原操作",
};
const uuid = () => {
  const b = crypto.getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 15) | 64;
  b[8] = (b[8] & 63) | 128;
  const x = [...b].map((v) => v.toString(16).padStart(2, "0")).join("");
  return `${x.slice(0, 8)}-${x.slice(8, 12)}-${x.slice(12, 16)}-${x.slice(16, 20)}-${x.slice(20)}`;
};
const RECEIPT = "cairn.tag-manager.pending.v1";
const COLLAPSED = "cairn.tag-manager.collapsed.v1";
async function request(path, body, method) {
  const r = await fetch(path, {
    method: method || (body ? "POST" : "GET"),
    headers: { "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) {
    const e = new Error(labels[data.error] || data.error || "保存失败");
    e.status = r.status;
    e.code = data.error;
    throw e;
  }
  return data;
}
const failure = (e) => toast(e.message || "暂时无法连接服务", { tone: "error" });
const read = (key, fallback) => {
  try {
    return JSON.parse(localStorage.getItem(key) || "null") ?? fallback;
  } catch {
    return fallback;
  }
};
const write = (key, value) => {
  try {
    if (value === null) localStorage.removeItem(key);
    else localStorage.setItem(key, JSON.stringify(value));
    return true;
  } catch {
    return false;
  }
};
const fold = (s) => String(s || "").toLowerCase();
const countField = (d) => (d === "forms" ? "form" : d === "uses" ? "use" : d);
const humanOnly = (d, t) => d === "uses" && t.id === "contra";

// Choices for the tag picker used by collections.
export async function managedTagChoices() {
  const [data, custom] = await Promise.all([request("/api/tag-catalog"), request("/api/custom-tags")]);
  return [
    ...Object.keys(dimensions).flatMap((d) =>
      (data.catalog[d] || [])
        .filter((t) => t.active && !t.deprecated)
        .map((t) => ({ ref: `system/${d}/${t.id}`, label: t.label, aliases: t.aliases || [], dimension: dimensions[d], group: d })),
    ),
    ...(custom.items || custom.tags || [])
      .filter((t) => t.status === "active")
      .map((t) => ({ ref: t.tag_ref, label: t.label, aliases: [], dimension: "自定义标记", group: "custom" })),
  ];
}

// tagPicker renders searchable chips grouped by dimension. `selected` is mutated.
export function tagPicker(choices, selected = new Set(), { onChange } = {}) {
  const query = h("input.tl-input", { type: "search", placeholder: "查找标签或别名", "aria-label": "查找标签" });
  const host = h("div.tag-pick-groups");
  const render = () => {
    const q = fold(query.value.trim());
    const groups = [...GROUPS, ...MORE]
      .map((g) => [g, choices.filter((c) => (c.group || "custom") === g.key && (!q || fold([c.label, ...(c.aliases || [])].join(" ")).includes(q)))])
      .filter(([, list]) => list.length);
    const missing = [...selected].filter((ref) => !choices.some((c) => c.ref === ref));
    clear(
      host,
      missing.length
        ? h("div.tag-pick-group", h("h4", "已停用或已删除，建议移除"), h("div.tag-pick-chips", missing.map((ref) => chip({ ref, label: ref.split("/").pop() }))))
        : null,
      groups.map(([g, list]) => h("div.tag-pick-group", h("h4", g.label), h("div.tag-pick-chips", list.map(chip)))),
      groups.length || missing.length ? null : h("p.tl-empty", "没有找到匹配的标签"),
    );
  };
  function chip(c) {
    return h(
      "button.tag-pick",
      {
        type: "button",
        "aria-pressed": String(selected.has(c.ref)),
        onclick: (e) => {
          selected.has(c.ref) ? selected.delete(c.ref) : selected.add(c.ref);
          e.currentTarget.setAttribute("aria-pressed", String(selected.has(c.ref)));
          onChange?.(selected);
        },
      },
      c.group === "custom" ? `#${c.label}` : c.label,
    );
  }
  query.addEventListener("input", render);
  render();
  return h("div.tag-picker", query, host);
}

let hooks = {};
export async function browseTags() {
  if (restoreWorkspace("tags")) return;
  const actions = h("div.page-head-actions-inner");
  const page = openWorkspace({ key: "tags", title: "标签库", body: h("p.tl-empty", "正在读取标签…"), actions });
  let data, quality = new Map(), cols = [];
  const ui = { q: "", filter: "all", open: null, adding: null, addName: "", collapsed: new Set(read(COLLAPSED, [])) };

  const loadAll = async () => {
    const [catalog, q, c] = await Promise.all([
      request("/api/tag-catalog"),
      request("/api/tag-quality").catch(() => null),
      import("./collections.js").then((m) => m.loadCollections()).catch(() => []),
    ]);
    data = catalog;
    quality = new Map((q?.terms || []).map((t) => [t.tag_ref, t]));
    cols = c;
  };
  try {
    await loadAll();
  } catch (e) {
    page.discard();
    page.setBody(h("div.tl-error", h("p", "无法读取标签库。"), h("button.btn", { type: "button", onclick: () => location.reload() }, "重新载入")));
    failure(e);
    return;
  }
  if (!page.isCurrent()) return page.discard();
  if (!data.catalog) {
    page.setBody(h("p.tl-empty", "当前服务还未升级标签管理。"));
    return;
  }

  const counts = () => new Map((data.counts || []).map((c) => [`${c.field}:${c.term}`, c.n]));
  const terms = () => {
    const n = counts();
    const out = [];
    for (const [d] of Object.entries(dimensions))
      for (const t of data.catalog[d] || [])
        out.push({
          kind: "system",
          d,
          t,
          ref: `system/${d}/${t.id}`,
          label: t.label,
          active: t.active && !t.deprecated,
          ai: t.ai_enabled !== false && !humanOnly(d, t),
          count: n.get(`${countField(d)}:${t.id}`) || 0,
          search: [t.label, ...(t.aliases || []), t.description].join(" "),
        });
    for (const t of data.custom_tags || [])
      out.push({ kind: "custom", d: "custom", t, ref: t.tag_ref, label: t.label, active: t.status === "active", ai: false, count: t.link_count || 0, search: t.label });
    const collator = new Intl.Collator("zh-Hans-CN", { numeric: true });
    return out.sort((a, b) => collator.compare(a.label, b.label));
  };
  const flag = (x) => {
    if (!x.active) return null;
    const qv = quality.get(x.ref);
    if (x.ai && qv && qv.rejections >= 3 && (qv.rejection_rate ?? 0) >= 0.3) return "常被移除";
    return null;
  };
  const hooked = (ref) => cols.filter((c) => !c.deleted && ruleRefs(c).includes(ref));

  // --- writes -------------------------------------------------------------
  const refresh = async () => {
    await loadAll();
    invalidateTaxonomyReads();
    emit("custom-tags:changed");
    await Promise.all([loadV1(), loadV2(), loadCustomTags()]).catch(() => {});
    emit("taxonomy");
    emit("tags:catalog-changed");
  };
  const send = async (body, path = "/api/tag-catalog/operations", method = "POST") => {
    const old = read(RECEIPT, null);
    if (old && JSON.stringify(old.body) !== JSON.stringify(body)) {
      toast("还有一项修改没有确认，请先处理页面顶部的提示");
      render();
      return null;
    }
    if (!write(RECEIPT, { body, path, method })) {
      toast("浏览器不允许本地存储，无法安全保存修改", { tone: "error" });
      return null;
    }
    try {
      const result = await request(path, body, method);
      write(RECEIPT, null);
      await refresh();
      render();
      return result;
    } catch (e) {
      if (e.status && e.status < 500) {
        write(RECEIPT, null);
        await refresh().catch(() => {});
      }
      failure(e);
      render();
      return null;
    }
  };
  const editSystem = (x, definition, { quiet } = {}) =>
    send({ operation_key: uuid(), expected_revision: data.revision, dimension: x.d, id: x.t.id, type: "edit", definition }).then((ok) => {
      if (ok && !quiet) toast("已保存");
      return ok;
    });
  const editCustom = (x, patch) =>
    send({ operation_key: uuid(), expected_revision: x.t.revision, label: x.t.label, ...patch }, `/api/custom-tags/${x.t.id}`, "PATCH");
  const lastAI = (x) => x.kind === "system" && x.ai && !terms().some((o) => o !== x && o.d === x.d && o.active && o.ai && o.ref !== x.ref);

  async function toggleAI(x) {
    if (x.kind !== "system" || humanOnly(x.d, x.t)) return;
    if (x.ai && lastAI(x)) return toast(labels.last_ai_tag);
    const next = !x.ai;
    const ok = await editSystem(x, { ai_enabled: next }, { quiet: true });
    if (!ok) return;
    toast(next ? `AI 会给新收藏打「${x.label}」` : `「${x.label}」改为只手动，已有收藏上的标签保留`);
    if (next && !x.t.description) ui.open = x.ref;
  }
  async function setActive(x, active) {
    if (!active && x.ai && lastAI(x)) return toast(labels.last_ai_tag);
    if (!active && hooked(x.ref).length && !(await confirmAction({
      title: `停用「${x.label}」？`,
      message: `它挂在 ${hooked(x.ref).map((c) => `「${c.name}」`).join("")} 上，停用后不会再自动归入新收藏。已有收藏上的标签保留，可随时恢复。`,
      confirmLabel: "停用",
    }))) return;
    const ok = x.kind === "system"
      ? await send({ operation_key: uuid(), expected_revision: data.revision, dimension: x.d, id: x.t.id, type: active ? "restore" : "archive" })
      : await editCustom(x, { archived: !active });
    if (!ok) return;
    if (!active && ui.open === x.ref) ui.open = null;
    render();
    toast(active ? `已恢复「${x.label}」` : `已停用「${x.label}」，已有收藏上的标签保留`, active ? {} : {
      action: { label: "撤销", run: () => { const now = terms().find((o) => o.ref === x.ref); if (now) setActive(now, true); } },
    });
  }
  async function create(dim, name, description) {
    name = name.trim();
    if (!name) return false;
    if (terms().some((o) => fold(o.label) === fold(name) || (o.t.aliases || []).some((a) => fold(a) === fold(name)))) {
      toast("这个名字已被其他标签使用");
      return false;
    }
    let ok;
    if (dim === "custom") ok = await send({ operation_key: uuid(), label: name }, "/api/custom-tags", "POST");
    else {
      if (!description.trim()) {
        toast("写一句说明，AI 才知道什么时候打这个标签");
        return false;
      }
      ok = await send({ operation_key: uuid(), expected_revision: data.revision, dimension: "topics", type: "create", definition: { label: name, description: description.trim(), aliases: [], includes: [], excludes: [], ai_enabled: true, granularity: "specific" } });
    }
    if (!ok) return false;
    const made = terms().find((o) => o.d === dim && o.label === name);
    ui.adding = null;
    ui.addName = "";
    ui.q = "";
    ui.open = made?.ref ?? null;
    render();
    toast(`已创建「${name}」`);
    requestAnimationFrame(() => byId("management-page").querySelector(".tl-row[aria-expanded='true']")?.scrollIntoView({ block: "nearest" }));
    return true;
  }
  async function setHooks(x, ids) {
    const { setRuleTags } = await import("./collections.js");
    for (const c of cols.filter((c) => !c.deleted)) {
      const has = ruleRefs(c).includes(x.ref), want = ids.includes(c.id);
      if (has === want) continue;
      const refs = want ? [...ruleRefs(c), x.ref] : ruleRefs(c).filter((r) => r !== x.ref);
      try {
        await setRuleTags(c, refs, { enabled: want ? true : c.rule_enabled && refs.length > 0 });
      } catch (e) {
        failure(e);
        break;
      }
    }
    cols = await import("./collections.js").then((m) => m.loadCollections(true));
    render();
  }
  function pickCollections(x) {
    const chosen = new Set(hooked(x.ref).map((c) => c.id));
    const live = cols.filter((c) => !c.deleted && !c.archived);
    openDialog({
      title: `新收藏带「${x.label}」时归入`,
      body: live.length
        ? [
            h("div.tl-checks", live.map((c) => h("label.tl-check",
              h("input", { type: "checkbox", checked: chosen.has(c.id), onchange: (e) => (e.target.checked ? chosen.add(c.id) : chosen.delete(c.id)) }),
              h("span", c.name),
              ruleRefs(c).filter((r) => r !== x.ref).length ? h("small", `已挂 ${ruleRefs(c).filter((r) => r !== x.ref).length} 个标签`) : null))),
            h("p.tl-note", "只影响之后的新收藏。已有收藏可以在合集页「一并归入」。"),
          ]
        : h("p.tl-note", "还没有合集。先到合集页新建一个。"),
      actions: live.length ? [{ label: "保存", primary: true, run: () => setHooks(x, [...chosen]) }] : [],
    });
  }
  async function history(x) {
    if (x.kind !== "system") return;
    let items = [];
    try {
      items = (await request(`/api/tag-catalog/history?dimension=${x.d}&id=${x.t.id}`)).items || [];
    } catch (e) {
      return failure(e);
    }
    const verb = { create: "创建", edit: "修改", archive: "停用", restore: "恢复" };
    const when = (s) => new Date(s).toLocaleString("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" });
    const change = (it) => {
      if (it.action !== "edit" || !it.before_value || !it.after_value) return "";
      try {
        const a = JSON.parse(it.before_value), b = JSON.parse(it.after_value);
        const names = { label: "名称", description: "说明", aliases: "别名", includes: "会打的例子", excludes: "不打的情况", ai_enabled: "AI 开关", granularity: "粒度" };
        return Object.entries(names).filter(([k]) => JSON.stringify(a[k]) !== JSON.stringify(b[k])).map(([, v]) => v).join("、");
      } catch {
        return "";
      }
    };
    openDialog({
      title: `「${x.label}」变更记录`,
      body: items.length
        ? h("ol.tl-history", items.map((it) => h("li", h("time", when(it.created_at)), h("span", verb[it.action] || it.action, change(it) ? `：${change(it)}` : ""), h("small", `版本 ${it.revision}`))))
        : h("p.tl-note", "这个标签还没有被修改过。"),
    });
  }
  function openLibrary(x) {
    const filters = { ...emptyFilters(), curation_status: "all" };
    if (x.kind === "custom") filters.custom_tags = x.t.id;
    else if (x.d === "forms") filters.form = x.t.id;
    else if (x.d === "uses") filters.use = x.t.id;
    else filters[x.d] = x.t.id;
    hooks.select?.(filters);
  }

  // --- rendering ------------------------------------------------------------
  const root = h("div.tl");
  const search = h("input.tl-search", { type: "search", placeholder: "搜索标签，或输入新名称创建", "aria-label": "搜索或新建标签", autocomplete: "off", spellcheck: false });
  const headDyn = h("div.tl-top-dyn");
  const head = h("div.tl-top", h("div.tl-searchbox", icon("search", 16), search, h("kbd", "/")), headDyn);
  const body = h("div.tl-body");
  root.append(head, body);
  search.addEventListener("input", () => {
    ui.q = search.value;
    render();
  });
  search.addEventListener("keydown", (e) => {
    if (e.isComposing) return;
    if (e.key === "Escape" && search.value) {
      e.preventDefault();
      search.value = ui.q = "";
      render();
    }
    if (e.key === "Enter") {
      e.preventDefault();
      const q = search.value.trim();
      if (!q) return;
      const hit = terms().find((o) => fold(o.label) === fold(q)) || terms().find((o) => o.active && fold(o.search).includes(fold(q)));
      if (hit) {
        ui.open = hit.ref;
        ui.q = search.value = "";
        render();
        body.querySelector(".tl-row[aria-expanded='true']")?.scrollIntoView({ block: "center" });
      } else startAdd("topics", q);
    }
  });
  function startAdd(dim, name = "") {
    ui.adding = dim;
    ui.addName = name;
    ui.q = search.value = "";
    ui.open = null;
    ui.collapsed.delete(dim);
    render();
    const target = body.querySelector(`.tl-add[data-dim="${dim}"] ${name ? "[name=desc]" : "[name=label]"}`);
    target?.focus();
    target?.scrollIntoView({ block: "center" });
  }

  function render() {
    const all = terms(), q = fold(ui.q.trim());
    const match = (x) => (!q || fold(x.search).includes(q)) && (ui.filter === "all" || (ui.filter === "ai" ? x.ai : ui.filter === "manual" ? !x.ai : Boolean(flag(x))));
    const exact = q && all.some((x) => fold(x.label) === q);
    const receipt = read(RECEIPT, null);
    clear(
      headDyn,
      h("div.tl-filters", { role: "tablist", "aria-label": "按方式筛选" }, FILTERS.map(([k, l]) => h("button.tl-filter", {
        type: "button", role: "tab", "aria-selected": String(ui.filter === k), onclick: () => { ui.filter = k; render(); },
      }, l, k === "attention" ? h("span.num", String(all.filter(flag).length)) : null))),
      q && !exact
        ? h("div.tl-create", icon("plus", 15), h("span", "新建「", h("b", ui.q.trim()), "」作为"),
            h("button.btn.btn-sm.btn-primary", { type: "button", onclick: () => startAdd("topics", ui.q.trim()) }, icon("sparkles", 13), "主题 · AI 自动打", h("kbd", "↵")),
            h("button.btn.btn-sm", { type: "button", onclick: () => create("custom", ui.q.trim()) }, icon("pencil", 13), "自定义标记 · 只手动"))
        : null,
      receipt
        ? h("div.tl-receipt", icon("alert", 15), h("span", "有一项修改还没有收到服务确认。重试会沿用原来的操作，不会重复创建。"),
            h("button.btn.btn-sm", { type: "button", onclick: () => send(receipt.body, receipt.path, receipt.method) }, "重试"),
            h("button.btn.btn-sm.btn-ghost", { type: "button", onclick: async () => { write(RECEIPT, null); await refresh(); render(); } }, "放弃并刷新"))
        : null,
    );
    const group = (g) => {
      const list = all.filter((x) => x.d === g.key && x.active && match(x));
      const adding = ui.adding === g.key;
      if (!list.length && !adding && (q || ui.filter !== "all" || !g.create)) return null;
      const collapsed = ui.collapsed.has(g.key) && !q && !adding;
      return h("section.tl-group", { dataset: { dim: g.key } },
        h("header.tl-group-head",
          h("button.tl-group-toggle", { type: "button", "aria-expanded": String(!collapsed), onclick: () => {
            collapsed ? ui.collapsed.delete(g.key) : ui.collapsed.add(g.key);
            write(COLLAPSED, [...ui.collapsed]);
            render();
          } }, icon("chevronDown", 14), h("b", g.label), h("span", g.hint), h("span.num", String(list.length))),
          g.create && !q ? h("button.btn.btn-sm.btn-ghost", { type: "button", onclick: () => startAdd(g.key) }, icon("plus", 14), "添加") : null),
        collapsed ? null : [
          adding ? addRow(g.key) : null,
          list.map(row),
          !list.length && !adding ? h("p.tl-empty", g.key === "custom" ? "还没有自定义标记。项目名、个人记号都可以建成自定义标记。" : "这一组还没有标签。") : null,
        ]);
    };
    const more = MORE.map(group).filter(Boolean);
    const off = all.filter((x) => !x.active && (!q || fold(x.search).includes(q)));
    clear(
      body,
      GROUPS.map(group),
      more.length ? (q || ui.filter !== "all" ? more : h("details.tl-more", h("summary", icon("chevronRight", 14), "更多分类", h("span", "载体、潜在用途、旧版分类")), more)) : null,
      off.length && ui.filter === "all"
        ? h("details.tl-more", { open: Boolean(q) }, h("summary", icon("chevronRight", 14), "已停用", h("span.num", String(off.length))),
            off.map((x) => h("div.tl-row.is-off", h("span.tl-name", x.kind === "custom" ? `#${x.label}` : x.label), h("span.tl-meta", dimensions[x.d] || "自定义标记"), h("span.tl-count.num", `${x.count} 条`),
              h("button.btn.btn-sm.btn-ghost", { type: "button", onclick: () => setActive(x, true) }, "恢复"))))
        : null,
      q && !all.some(match) && !off.length ? h("p.tl-empty", `没有找到「${ui.q.trim()}」。可以用上方按钮直接新建。`) : null,
      h("p.tl-foot", "修改说明和 AI 开关只影响之后的新收藏，不会重算已有收藏。停用后已有收藏上的标签和合集里的引用都保留。"),
    );
  }
  function addRow(dim) {
    const name = h("input.tl-input", { name: "label", maxLength: 80, value: ui.addName, placeholder: dim === "custom" ? "标记名称，例如：读完要写笔记" : "主题名称，例如：浏览器插件", "aria-label": "新标签名称" });
    const desc = dim === "custom" ? null : h("input.tl-input", { name: "desc", maxLength: 1000, placeholder: "AI 什么时候打它？一句话，例如：介绍或提供浏览器扩展的收藏", "aria-label": "新标签说明" });
    const submit = () => create(dim, name.value, desc?.value || "");
    const cancel = () => { ui.adding = null; ui.addName = ""; render(); };
    for (const input of [name, desc].filter(Boolean))
      input.addEventListener("keydown", (e) => {
        if (e.isComposing) return;
        if (e.key === "Escape") { e.preventDefault(); cancel(); }
        if (e.key === "Enter") { e.preventDefault(); if (input === name && desc && !desc.value.trim()) desc.focus(); else submit(); }
      });
    name.addEventListener("input", () => (ui.addName = name.value));
    return h("div.tl-add", { dataset: { dim } }, name, desc,
      h("div.tl-add-actions", h("button.btn.btn-sm.btn-primary", { type: "button", onclick: submit }, "创建"), h("button.btn.btn-sm.btn-ghost", { type: "button", onclick: cancel }, "取消")),
      h("p.tl-note", dim === "custom" ? "自定义标记只由你添加，AI 不会打。" : "新主题会由 AI 自动打到之后的新收藏上；你随时可以改成只手动。"));
  }
  function row(x) {
    const open = ui.open === x.ref, f = flag(x), hk = hooked(x.ref);
    const aiButton = x.kind === "custom"
      ? h("span.tl-ai.is-static", { title: "自定义标记只由你添加" }, icon("pencil", 12), "手动")
      : h("button.tl-ai", {
          type: "button", "aria-pressed": String(x.ai), disabled: humanOnly(x.d, x.t),
          title: humanOnly(x.d, x.t) ? "只能由你手动添加" : x.ai ? "AI 自动打 · 点击改为只手动" : "只手动 · 点击让 AI 自动打",
          "aria-label": `${x.label}：${x.ai ? "AI 自动打" : "只手动"}`, onclick: () => toggleAI(x),
        }, icon(x.ai ? "sparkles" : "pencil", 12), x.ai ? "AI" : "手动");
    return [
      h("div.tl-row", { dataset: { ref: x.ref }, "aria-expanded": String(open) },
        h("button.tl-main", { type: "button", "aria-expanded": String(open), onclick: () => { ui.open = open ? null : x.ref; ui.adding = null; render(); } },
          h("span.tl-name", x.kind === "custom" ? `#${x.label}` : x.label),
          f ? h("span.tl-tag.is-warn", f) : null,
          hk.map((c) => h("span.tl-hook", { title: `带上它的新收藏会归入「${c.name}」` }, icon("folder", 11), c.name)),
          h("span.tl-count.num", String(x.count))),
        aiButton,
        h("button.tl-off", { type: "button", title: "停用", "aria-label": `停用「${x.label}」`, onclick: () => setActive(x, false) }, icon("x", 14))),
      open ? panel(x) : null,
    ];
  }
  function panel(x) {
    const custom = x.kind === "custom", t = x.t;
    const field = (labelText, control, hint) => h("div.tl-field", h("span.tl-label", labelText), h("div.tl-control", control, hint ? h("small", hint) : null));
    const name = h("input.tl-input", { value: x.label, maxLength: 80, "aria-label": "名称" });
    name.addEventListener("change", async () => {
      const v = name.value.trim();
      if (!v || v === x.label) return (name.value = x.label);
      if (terms().some((o) => o !== x && fold(o.label) === fold(v))) {
        toast("这个名字已被其他标签使用");
        return (name.value = x.label);
      }
      const ok = custom ? await editCustom(x, { label: v }) : await editSystem(x, { label: v }, { quiet: true });
      if (ok) {
        ui.open = custom ? x.ref : x.ref;
        render();
        toast("已改名，使用它的收藏会同步显示新名字");
      } else name.value = x.label;
    });
    const enterBlur = (el) => el.addEventListener("keydown", (e) => { if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); el.blur(); } });
    enterBlur(name);
    const list = (key, placeholder) => {
      const values = [...(t[key] || [])];
      const input = h("input.tl-chip-input", { placeholder, "aria-label": placeholder, dataset: { list: key } });
      const save = (next) => editSystem(x, { [key]: next }).then(() => requestAnimationFrame(() => body.querySelector(`[data-ref="${CSS.escape(x.ref)}"] + .tl-panel [data-list="${key}"]`)?.focus()));
      input.addEventListener("keydown", (e) => {
        if (e.isComposing) return;
        if (e.key === "Enter" && input.value.trim()) {
          e.preventDefault();
          const v = input.value.trim();
          if (!values.includes(v)) save([...values, v]);
        }
        if (e.key === "Backspace" && !input.value && values.length) save(values.slice(0, -1));
      });
      return h("div.tl-chips", values.map((v, i) => h("span.tl-chip", v, h("button", { type: "button", "aria-label": `删除「${v}」`, onclick: () => save(values.filter((_, j) => j !== i)) }, icon("x", 11)))), input);
    };
    let desc = null;
    if (!custom && x.ai) {
      desc = h("textarea.tl-input.tl-desc", { rows: 1, maxLength: 1000, value: t.description || "", placeholder: "一句话说明，例如：AI 辅助写代码、审查和调试", "aria-label": "AI 什么时候打" });
      const fit = () => { desc.style.height = "auto"; desc.style.height = `${desc.scrollHeight + 2}px`; };
      desc.addEventListener("input", fit);
      requestAnimationFrame(fit);
      enterBlur(desc);
      desc.addEventListener("change", async () => {
        const v = desc.value.trim();
        if (!v) {
          toast("说明不能为空，AI 要靠它判断");
          desc.value = t.description || "";
          return;
        }
        if (v !== t.description) await editSystem(x, { description: v });
      });
    }
    const qv = quality.get(x.ref);
    const hk = hooked(x.ref);
    return h("div.tl-panel",
      field("名称", name),
      custom ? h("p.tl-note", "自定义标记只由你添加，AI 不会打，也不会参考它。改名会同步到所有使用它的收藏。") : null,
      desc ? field([icon("sparkles", 12), "AI 什么时候打"], desc, "只影响之后的新收藏") : null,
      !custom && x.ai ? field("会打，比如", list("includes", "加一个例子，回车")) : null,
      !custom && x.ai ? field("这些情况不打", list("excludes", "加一个例外，回车")) : null,
      !custom && !x.ai ? h("p.tl-note", humanOnly(x.d, t) ? "这是个人判断，只能由你手动添加。" : "AI 不会再把它打到新收藏上；已有收藏上的标签保留，你仍可手动添加。") : null,
      !custom ? field("别名", list("aliases", "搜索时等同于这个名字，回车添加")) : null,
      field("新收藏自动归入",
        h("div.tl-chips", hk.map((c) => h("span.tl-chip.is-hook", icon("folder", 12), c.name, h("button", { type: "button", "aria-label": `不再归入「${c.name}」`, onclick: () => setHooks(x, hk.filter((o) => o !== c).map((o) => o.id)) }, icon("x", 11)))),
          h("button.tl-chip-add", { type: "button", onclick: () => pickCollections(x) }, icon("plus", 12), hk.length ? "更改" : "选择合集"))),
      qv && (qv.confirmations || qv.additions || qv.rejections)
        ? h("p.tl-stats", `你的纠正：确认 ${qv.confirmations} 次 · 手动补上 ${qv.additions} 次 · 移除 ${qv.rejections} 次`)
        : null,
      h("div.tl-panel-foot",
        h("button.btn.btn-sm", { type: "button", disabled: !x.count, onclick: () => openLibrary(x) }, icon("layers", 14), `查看 ${x.count} 条收藏`),
        x.kind === "system" && x.d === "topics"
          ? h("label.tl-inline-check", { title: "宽主题是大方向，筛选时先出现；细分主题用来缩小范围" },
              h("input", { type: "checkbox", checked: t.granularity === "broad", onchange: (e) => editSystem(x, { granularity: e.target.checked ? "broad" : "specific" }) }), "宽主题")
          : null,
        h("span.tl-spacer"),
        x.kind === "system" ? h("button.btn.btn-sm.btn-ghost", { type: "button", onclick: () => history(x) }, "变更记录") : null,
        h("button.btn.btn-sm.btn-ghost.btn-danger", { type: "button", onclick: () => setActive(x, false) }, "停用")));
  }

  clear(actions, h("button.btn.btn-sm.btn-primary", { type: "button", onclick: () => startAdd("topics") }, icon("plus", 14), "新建标签"));
  page.setBody(root);
  render();
  page.onActivate(async () => {
    await refresh();
    render();
  });
  root.addEventListener("keydown", (e) => {
    if (e.key === "/" && !e.target.closest("input,textarea")) {
      e.preventDefault();
      search.focus();
    }
  });
}
export function ruleRefs(c) {
  try {
    return Array.isArray(c.rule_tags) ? c.rule_tags : JSON.parse(c.rule_tags || "[]");
  } catch {
    return [];
  }
}
export function initTagManager(options = {}) {
  hooks = options;
  byId("browse-tags")?.addEventListener("click", () => browseTags());
}
