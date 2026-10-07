import { openWorkspace, openEditor, restoreWorkspace } from "./workspace.js";
import { control as h } from "./controls.js";
import { managedTagChoices, tagPicker } from "./tag-manager.js";
import { fetchJSON, errorLabel, invalidateQueryReads } from "./api.js";
import { byId, clear, h as el } from "./dom.js";
import { state, on, emit } from "./store.js";
import { openDialog, confirmAction, toast, openMenu } from "./ui.js";
import { openOrganizing } from "./collection-organizing.js";
import { emptyFilters } from "./query.js";
import { icon } from "./icons.js";

let catalog = [],
  hooks = {},
  loading = null,
  pinnedSignature = "",
  loadEpoch = 0;
// randomUUID is unavailable on plain HTTP NAS addresses; getRandomValues works there.
const uuid = () => {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 15) | 64;
  bytes[8] = (bytes[8] & 63) | 128;
  const hex = [...bytes].map((b) => b.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
};
const labels = {
  collections_unsupported: "服务暂不支持合集，请先更新后端",
  invalid_name: "名称不能为空，最多 80 个字符",
  invalid_note: "备注最多 2000 个字符",
  collection_deleted: "这个合集已被删除，请到已删除中恢复",
  member_not_found: "这条收藏已移出合集，请刷新后再操作",
  collection_limit: "合集或成员数量已达上限",
  collection_write_failed: "保存未完成，请刷新合集后重试",
};
const failure = (e) =>
  toast(labels[e.message] || errorLabel(e.message), { tone: "error" });
const button = (text, run, cls = "btn") =>
  h(
    `button.${cls}`,
    {
      type: "button",
      onclick: async (event) => {
        const node = event.currentTarget;
        node.disabled = true;
        try {
          await run();
        } catch (e) {
          failure(e);
        } finally {
          node.disabled = false;
        }
      },
    },
    text,
  );
const live = () => catalog.filter((c) => !c.deleted);
export const collectionName = (id) =>
  catalog.find((c) => c.id === id)?.name || "合集";
async function load() {
  if (loading) return loading;
  const epoch = ++loadEpoch;
  const flight = fetchJSON("/api/collections")
    .then((data) => {
      if (epoch === loadEpoch) {
        catalog = data.items;
        renderNav();
        emit("collections:loaded");
      }
      return catalog;
    })
    .finally(() => {
      if (loading === flight) loading = null;
    });
  loading = flight;
  return flight;
}
function select(id) {
  hooks.select({
    ...emptyFilters(),
    curation_status: "all",
    collection_id: id,
  });
}
function renderNav() {
  const nav = byId("pinned-collections");
  if (!nav) return;
  const pinned = live()
    .filter((c) => c.pinned && !c.archived)
    .slice(0, 6);
  const signature = JSON.stringify([
    state.filters.collection_id,
    pinned.map((c) => [c.id, c.name, c.item_count]),
  ]);
  nav.hidden = !pinned.length;
  const heading = byId("pinned-heading");
  if (heading) heading.hidden = !pinned.length;
  if (signature !== pinnedSignature) {
    pinnedSignature = signature;
    clear(
      nav,
      pinned.map((c) =>
        h(
          "button.nav-item.collection-shortcut",
          {
            type: "button",
            title: c.name,
            "aria-current":
              state.filters.collection_id === c.id ? "page" : null,
            onclick: () => select(c.id),
          },
          h("span", { slot: "start" }, icon("folder", 16)),
          h("span.nav-label", c.name),
          h("span.nav-count", { slot: "end" }, String(c.item_count)),
        ),
      ),
    );
  }
  renderContext();
}
function renderContext() {
  const id = state.filters.collection_id,
    c = catalog.find((x) => x.id === id),
    box = byId("collection-context");
  if (!box) return;
  box.hidden = !id;
  clear(
    box,
    id
      ? [
          h(
            "span",
            c?.description ||
              (c?.archived ? "已归档合集" : "按你安排的顺序阅读"),
          ),
          button("管理", () => { view.selected = id; view.scope = c?.deleted ? "deleted" : c?.archived ? "archived" : "active"; return browse(); }, "link-btn"),
        ]
      : [],
  );
}
// One closure owns one operation key. A lost response retries the same receipt.
function mutation(id, revision, type, initial) {
  let body = null,
    last = "",
    expected = revision;
  return async (values) => {
    const fields = values ?? initial,
      signature = JSON.stringify(fields);
    if (!body || signature !== last) {
      last = signature;
      body = {
        operation_key: uuid(),
        expected_revision: expected,
        type,
        ...fields,
      };
    }
    try {
      const result = await fetchJSON(`/api/collections/${id}/operations`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      expected = result.collection.revision;
      body = null;
      invalidateQueryReads();
      loadEpoch++;
      loading = null;
      catalog = [...catalog.filter((c) => c.id !== id), result.collection];
      renderNav();
      emit("collections:loaded");
      emit("collections:changed", id);
      // A failed catalog refresh must not turn a committed write into a new operation.
      load().catch(() => {});
      return result.collection;
    } catch (e) {
      if (e.message === "revision_conflict") {
        const current = await fetchJSON(`/api/collections/${id}`);
        expected = current.collection.revision;
        if (
          await confirmAction({
            title: "合集已在其他设备更新",
            message: `最新名称：${current.collection.name}。${type === "note" ? "最新备注：" + (current.items.find((item) => item.link_id === fields.link_id)?.note || "无") : "最新说明：" + (current.collection.description || "无")}。你的编辑仍保留，按最新版本重新提交这次操作？`,
            confirmLabel: "重新提交",
          })
        ) {
          body = null;
          return mutation(id, expected, type, fields)();
        }
      }
      throw e;
    }
  };
}
function create(onCreated) {
  const name = h("input.collection-input", {
    placeholder: "例如：个人网站改版",
    maxLength: 80,
    "aria-label": "合集名称",
  });
  const description = h("textarea.collection-input", {
    placeholder: "这个合集用来做什么？（可选）",
    maxLength: 2000,
    rows: 3,
    "aria-label": "合集说明",
  });
  const id = uuid(),
    save = mutation(id, 0, "create");
  openDialog({
    title: "新建合集",
    body: [
      h("label.collection-field", "名称", name),
      h("label.collection-field", "说明", description),
    ],
    initialFocus: () => name,
    actions: [
      {
        label: "创建",
        primary: true,
        run: async () => {
          if (!name.value.trim()) {
            name.focus();
            return false;
          }
          try {
            await save({
              name: name.value.trim(),
              description: description.value,
            });
            await onCreated?.(id);
          } catch (e) {
            failure(e);
            return false;
          }
        },
      },
    ],
  });
}
// --- Collections page: list on the left, the selected collection on the right.
export async function loadCollections(force = false) {
  if (force) {
    loadEpoch++;
    loading = null;
  }
  return load();
}
export async function setRuleTags(c, refs, { enabled = true, mode } = {}) {
  return mutation(c.id, c.revision, "rule", {
    enabled: enabled && refs.length > 0,
    mode: mode || c.rule_mode || "any",
    tag_refs: refs,
  })();
}
const refsOf = (c) => {
  try {
    return JSON.parse(c.rule_tags || "[]");
  } catch {
    return [];
  }
};
let tagLabels = new Map();
async function loadTagLabels() {
  try {
    const choices = await managedTagChoices();
    tagLabels = new Map(choices.map((t) => [t.ref, t]));
  } catch {}
  return tagLabels;
}
const tagText = (ref) => {
  const t = tagLabels.get(ref);
  return t ? (t.group === "custom" ? `#${t.label}` : t.label) : "已停用的标签";
};
const view = { scope: "active", q: "", selected: null, reorder: false };
export async function browse() {
  if (restoreWorkspace("collections")) return;
  const actions = el("div.page-head-actions-inner");
  const page = openWorkspace({ key: "collections", title: "合集", body: el("p.tl-empty", "正在读取合集…"), actions });
  try {
    await Promise.all([load(), loadTagLabels()]);
  } catch (e) {
    page.discard();
    page.setBody(el("div.tl-error", el("p", "无法读取合集。"), el("button.btn", { type: "button", onclick: () => location.reload() }, "重新载入")));
    failure(e);
    return;
  }
  if (!page.isCurrent()) return page.discard();
  const list = el("div.cl-list"), detail = el("section.cl-detail", { "aria-label": "合集详情" });
  const search = el("input.tl-input", { type: "search", placeholder: "查找合集", "aria-label": "查找合集" });
  search.addEventListener("input", () => {
    view.q = search.value;
    renderList();
  });
  const scopes = [
    ["active", "使用中", (c) => !c.deleted && !c.archived],
    ["archived", "已归档", (c) => !c.deleted && c.archived],
    ["deleted", "已删除", (c) => c.deleted],
  ];
  const tabs = el("div.tl-filters", { role: "tablist", "aria-label": "合集范围" });
  const head = el("div.cl-head", el("div.tl-searchbox", icon("search", 16), search), tabs);
  let detailData = null, detailToken = 0, pendingPaint = false;
  const menuClosed = () => { if (pendingPaint) { pendingPaint = false; paintDetail(); } };
  const visible = () => {
    const fn = scopes.find((s) => s[0] === view.scope)[2];
    const q = view.q.trim().toLowerCase();
    return catalog
      .filter(fn)
      .filter((c) => !q || (c.name + " " + (c.description || "")).toLowerCase().includes(q))
      .sort((a, b) => Number(b.pinned) - Number(a.pinned) || a.name.localeCompare(b.name, "zh"));
  };
  function renderList() {
    clear(tabs, scopes.map(([k, l, fn]) => el("button.tl-filter", { type: "button", role: "tab", "aria-selected": String(view.scope === k), onclick: () => { view.scope = k; view.selected = null; renderAll(); } }, l, el("span.num", String(catalog.filter(fn).length)))));
    const rows = visible();
    if (!rows.some((c) => c.id === view.selected)) view.selected = rows[0]?.id ?? null;
    clear(list, rows.length
      ? rows.map((c) => el("button.collection-row.cl-row", { type: "button", "aria-current": String(c.id === view.selected), onclick: () => { view.selected = c.id; view.reorder = false; renderAll(); } },
          el("span.cl-row-top", icon("folder", 15), el("b", c.name), c.pinned ? el("span.cl-pin", { title: "已固定到侧栏" }, "置顶") : null, el("span.num", `${c.item_count} 条`)),
          refsOf(c).length ? el("span.cl-row-tags", refsOf(c).map((r) => el("span.cl-hook", { class: c.rule_enabled ? "" : "is-paused" }, tagText(r)))) : null))
      : el("div.tl-empty", view.q ? `没有找到「${view.q}」` : view.scope === "active" ? "还没有合集。" : `没有${scopes.find((s) => s[0] === view.scope)[1]}的合集。`,
          view.scope === "active" && !view.q ? el("button.btn.btn-sm.btn-primary", { type: "button", onclick: () => create(async (id) => { view.selected = id; await load(); renderAll(); }) }, icon("plus", 14), "新建合集") : null));
  }
  async function renderDetail() {
    const id = view.selected, token = ++detailToken;
    if (!id) return clear(detail, el("p.tl-empty", "选择左侧的一个合集。"));
    if (!detailData || detailData.collection.id !== id) clear(detail, el("p.tl-empty", "正在读取…"));
    let next;
    try {
      next = await fetchJSON(`/api/collections/${id}`);
    } catch (e) {
      if (token === detailToken) clear(detail, el("div.tl-error", el("p", "无法读取这个合集。"), el("button.btn.btn-sm", { type: "button", onclick: renderDetail }, "重试")));
      return;
    }
    if (token !== detailToken) return;
    // Background refreshes keep open menus and half-typed fields intact.
    const same = detailData && JSON.stringify(next) === JSON.stringify(detailData) && detail.querySelector(".cl-card");
    detailData = next;
    if (same) return;
    // Never pull a menu out from under the pointer; repaint once it closes.
    if (detail.querySelector("wa-dropdown")) pendingPaint = true;
    else paintDetail();
  }
  const after = async () => {
    await load();
    renderList();
    await renderDetail();
  };
  function paintDetail() {
    const c = detailData.collection, items = detailData.items;
    if (c.deleted)
      return clear(detail, el("div.cl-card.cl-deleted", el("h2", c.name), el("p", "合集已删除，里面的收藏都还在。恢复后合集、顺序和备注都会回来。"),
        el("button.btn.btn-primary", { type: "button", onclick: () => run(async () => { await mutation(c.id, c.revision, "restore", {})(); view.scope = "active"; await after(); toast("合集已恢复"); }) }, "恢复合集")));
    const name = el("input.cl-title", { value: c.name, maxLength: 80, "aria-label": "合集名称" });
    const desc = el("input.cl-desc", { value: c.description || "", maxLength: 2000, placeholder: "这个合集用来做什么？（可选）", "aria-label": "合集说明" });
    // One mutation per open detail: a retry after a lost response reuses its operation key.
    const draft = { name: c.name, description: c.description || "", pinned: !!c.pinned, archived: !!c.archived };
    const saveMeta = (patch) => run(async () => {
      Object.assign(draft, patch);
      await editFor(c.id)({ ...draft });
      editFailed = false;
      await after();
    }).finally(() => { if (editFailed === null) editFailed = true; });
    for (const input of [name, desc]) {
      const field = input === name ? "name" : "description";
      const commit = () => {
        const value = input.value.trim();
        if (field === "name" && !value) return (name.value = c.name);
        if (value === (field === "name" ? c.name : c.description || "") && value === draft[field] && !input.dataset.dirty) return;
        if (input.dataset.saving) return;
        input.dataset.dirty = "1";
        input.dataset.saving = "1";
        return saveMeta({ [field]: value }).then(() => {
          if (detailData?.collection?.[field] === value) { delete input.dataset.dirty; toast("已保存"); }
        }).finally(() => delete input.dataset.saving);
      };
      input.addEventListener("input", () => (input.dataset.dirty = "1"));
      input.addEventListener("keydown", (e) => {
        if (e.isComposing) return;
        if (e.key === "Enter") { e.preventDefault(); commit(); }
        if (e.key === "Escape") { input.value = field === "name" ? c.name : c.description || ""; delete input.dataset.dirty; input.blur(); }
      });
      input.addEventListener("blur", () => { if (input.dataset.dirty) commit(); });
    }
    const pin = el("label.tl-inline-check", el("input", { type: "checkbox", checked: !!c.pinned, onchange: (e) => saveMeta({ pinned: e.target.checked }).then(() => toast(e.target.checked ? "已固定到侧栏" : "已从侧栏移除")) }), "置顶");
    const refs = refsOf(c);
    const menu = el("button.icon-btn", { type: "button", "aria-label": "合集操作", title: "更多", onclick: (e) => openMenu(e.currentTarget, [
      { label: c.archived ? "取消归档" : "归档", hint: "归档后不出现在「加入合集」里，自动归入也会暂停", run: () => saveMeta({ archived: !c.archived }).then(() => { view.scope = c.archived ? "active" : "archived"; toast(c.archived ? "已取消归档" : "已归档"); }) },
      "separator",
      { label: "删除合集", hint: "收藏都会保留，可以在「已删除」里恢复", danger: true, run: () => run(async () => {
        if (!(await confirmAction({ title: "删除这个合集？", message: `「${c.name}」里的收藏和备注都会保留，可以在「已删除」中恢复。`, confirmLabel: "删除", danger: true }))) return;
        const deleted = await mutation(c.id, latest().revision, "delete", {})();
        await after();
        toast("合集已删除，收藏仍保留", { action: { label: "撤销", run: () => run(async () => { await mutation(c.id, deleted.revision, "restore", {})(); await after(); }) } });
      }) },
    ], { onClose: menuClosed }) }, icon("more", 18));
    const hookBox = el("section.cl-hooks",
      el("header", icon("tag", 15), el("b", "挂着的标签"),
        refs.length ? el("label.tl-inline-check.cl-switch", el("input", { type: "checkbox", checked: !!c.rule_enabled, onchange: (e) => run(async () => { await setRuleTags(latest(), refs, { enabled: e.target.checked }); await after(); toast(e.target.checked ? "已恢复自动归入" : "已暂停自动归入，已有收藏保留"); }) }), c.rule_enabled ? "自动归入中" : "已暂停") : null),
      el("div.tl-chips", refs.map((r) => el("span.tl-chip.is-hook", tagText(r), el("button", { type: "button", "aria-label": `取下「${tagText(r)}」`, onclick: () => run(async () => {
        const next = refs.filter((x) => x !== r);
        await setRuleTags(latest(), next, { enabled: c.rule_enabled && next.length > 0 });
        await after();
        toast(`已取下「${tagText(r)}」，已归入的收藏保留`);
      }) }, icon("x", 11)))), el("button.tl-chip-add", { type: "button", onclick: () => hookTags(c) }, icon("plus", 12), refs.length ? "挂标签" : "挂上标签")),
      el("p.cl-sentence", refs.length
        ? [`新收藏带有${refs.length > 1 ? "" : "这个标签"}`, refs.length > 1 ? el("span.cl-mode", ["any", "all"].map((m) => el("button", { type: "button", "aria-pressed": String((c.rule_mode || "any") === m), onclick: () => run(async () => { await setRuleTags(latest(), refs, { enabled: c.rule_enabled, mode: m }); await after(); }) }, m === "any" ? "任一" : "全部"))) : null, refs.length > 1 ? "标签时" : "时", `，自动归入「${c.name}」`, c.rule_enabled ? "" : "（已暂停）", "。"]
        : "挂上标签后，带这些标签的新收藏会自动归入这个合集，不用再手动添加。"),
      el("div.cl-offer-host"),
      el("details.tl-more.cl-how", el("summary", icon("chevronRight", 13), "自动归入怎么运作"), el("p.tl-note", "只看挂上标签之后的新收藏，按最终标签精确匹配，不调用模型。你手动移出的收藏不会再被自动加入。取下标签或暂停后，已经归入的收藏留在合集里。已搁置的收藏不会被归入。")));
    if (refs.length && c.rule_enabled) offer(c, hookBox.querySelector(".cl-offer-host"));
    const members = items.length
      ? el("ol.cl-members", items.map((item, index) => member(c, items, item, index)))
      : el("div.tl-empty", "还没有内容。", refs.length ? "带挂着标签的新收藏会自动出现在这里，" : "", "也可以在阅读页或列表多选后加入。");
    clear(detail, el("div.cl-card",
      el("header.cl-detail-head", el("div.cl-detail-copy", name, desc), el("div.cl-detail-tools",
        el("button.btn.btn-sm", { type: "button", onclick: () => { page.close(); select(c.id); } }, icon("book", 14), "阅读"), menu)),
      el("div.cl-detail-meta", pin, c.archived ? el("span.tl-tag", "已归档") : null),
      hookBox,
      el("section.cl-items",
        el("header", el("b", `收藏 · ${items.length} 条`), items.length > 1 ? el("button.btn.btn-sm.btn-ghost", { type: "button", "aria-pressed": String(view.reorder), onclick: () => { view.reorder = !view.reorder; paintDetail(); } }, view.reorder ? "完成" : "调整顺序") : null,
          el("button.btn.btn-sm.btn-ghost", { type: "button", onclick: () => toast("在列表中勾选收藏，或在阅读页点「加入合集」") }, icon("plus", 14), "添加收藏")),
        members)));
  }
  function member(c, items, item, index) {
    const origin = item.origin === "rule" ? ["按标签归入", "accent"] : item.origin === "organize" ? ["AI 整理加入", "ai"] : ["手动加入", ""];
    const move = (before) => run(async () => { await mutation(c.id, latest().revision, "move", { link_id: item.link_id, before_id: before })(); await renderDetail(); });
    const node = el("li.cl-member", { draggable: view.reorder },
      view.reorder ? el("span.cl-grip", { "aria-hidden": "true" }, "⋮⋮") : null,
      el("button.cl-member-main", { type: "button", disabled: view.reorder, onclick: () => { page.close(); hooks.openItem?.(item.link_id, c.id) ?? select(c.id); } },
        el("b", item.title || "未命名收藏"),
        el("span.cl-member-meta", el("span.cl-origin", { dataset: { tone: origin[1] } }, origin[0]), item.note ? el("span", icon("pencil", 11), item.note) : null)),
      view.reorder
        ? el("span.cl-member-tools",
            el("button.icon-btn", { type: "button", disabled: !index, "aria-label": "上移", onclick: () => move(items[index - 1].link_id) }, icon("chevronUp", 16)),
            el("button.icon-btn", { type: "button", disabled: index === items.length - 1, "aria-label": "下移", onclick: () => move(items[index + 2]?.link_id ?? null) }, icon("chevronDown", 16)))
        : el("button.icon-btn", { type: "button", "aria-label": "操作", onclick: (e) => openMenu(e.currentTarget, [
            { label: item.note ? "修改合集内备注" : "写合集内备注", run: () => note(latest(), item, renderDetail) },
            { label: "移出合集", hint: item.origin === "rule" ? "之后不会再自动归入这条" : "收藏本身不受影响", danger: true, run: () => run(async () => {
              const removed = await mutation(c.id, latest().revision, "remove", { link_ids: [item.link_id] })();
              await after();
              toast("已移出合集", { action: { label: "撤销", run: () => run(async () => { await mutation(c.id, removed.revision, "add", { link_ids: [item.link_id] })(); await after(); }) } });
            }) },
          ], { onClose: menuClosed }) }, icon("more", 16)));
    if (view.reorder) {
      node.addEventListener("dragstart", (e) => { e.dataTransfer.setData("text/plain", String(item.link_id)); node.classList.add("is-dragging"); });
      node.addEventListener("dragend", () => node.classList.remove("is-dragging"));
      node.addEventListener("dragover", (e) => { e.preventDefault(); node.classList.add("is-over"); });
      node.addEventListener("dragleave", () => node.classList.remove("is-over"));
      node.addEventListener("drop", (e) => {
        e.preventDefault();
        node.classList.remove("is-over");
        const dragged = Number(e.dataTransfer.getData("text/plain"));
        if (dragged && dragged !== item.link_id) run(async () => { await mutation(c.id, latest().revision, "move", { link_id: dragged, before_id: item.link_id })(); await renderDetail(); });
      });
    }
    return node;
  }
  async function offer(c, host) {
    let preview;
    try {
      preview = await fetchJSON(`/api/collections/${c.id}/rules/preview`);
    } catch {
      return;
    }
    if (!preview.rule_valid || !preview.items.length || !host.isConnected) return;
    clear(host, el("div.cl-offer", icon("layers", 15), el("span", `已有 ${preview.items.length}${preview.has_more ? "+" : ""} 条收藏也符合，还没在合集里。`),
      el("button.btn.btn-sm", { type: "button", onclick: () => openDialog({
        title: `把已有收藏归入「${c.name}」`,
        body: [el("ul.cl-preview", preview.items.map((i) => el("li", i.title))), el("p.tl-note", "已搁置和你手动移出过的收藏不会被归入。")],
        actions: [{ label: `归入 ${preview.items.length} 条`, primary: true, run: async () => { await mutation(c.id, preview.revision, "backfill")(); await after(); toast(`已归入 ${preview.items.length} 条`); } }],
      }) }, "查看并归入")));
  }
  async function hookTags(c) {
    const choices = await managedTagChoices().catch((e) => (failure(e), null));
    if (!choices) return;
    const selected = new Set(refsOf(c));
    openDialog({
      title: `给「${c.name}」挂标签`,
      body: [tagPicker(choices, selected), el("p.tl-note", "带上任意一个（或全部，可在合集页切换）所选标签的新收藏会自动归入。")],
      actions: [{ label: "保存", primary: true, run: async () => {
        try {
          await setRuleTags(latest(), [...selected], { enabled: selected.size > 0 });
          await after();
          toast(selected.size ? "已保存，之后的新收藏会自动归入" : "已取下全部标签");
        } catch (e) {
          failure(e);
          return false;
        }
      } }],
    });
  }
  async function run(task) {
    try {
      await task();
    } catch (e) {
      failure(e);
    }
  }
  // Edits reuse one operation key until it succeeds, so a lost response is
  // retried idempotently; otherwise they always target the latest revision.
  let editState = null, editFailed = false;
  function editFor(id) {
    const rev = detailData.collection.revision;
    if (!editState || editState.id !== id || (!editFailed && editState.rev !== rev)) editState = { id, rev, call: mutation(id, rev, "edit") };
    editFailed = null;
    return editState.call;
  }
  const latest = () => detailData.collection;
  function renderAll() {
    renderList();
    renderDetail();
  }
  const newButton = el("button.btn.btn-primary.btn-sm", { type: "button", onclick: () => create(async (id) => { view.scope = "active"; view.selected = id; await load(); renderAll(); }) }, icon("plus", 14), "新建合集");
  const aiButton = el("button.btn.btn-sm", { type: "button", onclick: () => { page.close(); organize(); } }, icon("sparkles", 14), "AI 整理");
  clear(actions, aiButton, newButton);
  page.setBody(el("div.cl", el("div.cl-split", el("div.cl-side", head, list), detail)));
  renderAll();
  page.onActivate(async () => { await Promise.all([load(), loadTagLabels()]); renderAll(); });
}

export async function organize() {
  await load();
  return openOrganizing(catalog, uuid, () => {
    load().catch(() => {});
    hooks.reload();
  });
}
export async function pick(ids) {
  ids = [...new Set(ids.filter(Boolean))];
  if (!ids.length) return;
  if (ids.length > 100) {
    toast("一次最多选择 100 条收藏");
    return;
  }
  let definitions;
  try {
    definitions = (
      await fetchJSON(`/api/collections?link_ids=${ids.join(",")}`)
    ).items.filter((c) => !c.deleted && !c.archived);
  } catch (e) {
    failure(e);
    return;
  }
  const touched = new Map(),
    requests = new Map();
  const query = h("input.collection-input", {
      type: "search",
      placeholder: "查找合集",
      "aria-label": "查找合集",
    }),
    rows = h("div.collection-list");
  const render = () =>
    clear(
      rows,
      definitions
        .filter((c) => c.name.toLowerCase().includes(query.value.toLowerCase()))
        .map((c) => {
          const input = h("input", {
            type: "checkbox",
            checked: touched.has(c.id)
              ? touched.get(c.id)
              : c.selected_count === ids.length,
            "aria-label": c.name,
          });
          input.indeterminate =
            !touched.has(c.id) &&
            c.selected_count > 0 &&
            c.selected_count < ids.length;
          input.addEventListener("change", () => {
            touched.set(c.id, input.checked);
            requests.delete(c.id);
          });
          return h(
            "label.collection-pick",
            input,
            h("span", c.name),
            h("small", `${c.item_count} 条`),
          );
        }),
    );
  query.addEventListener("input", render);
  openDialog({
    title: ids.length === 1 ? "加入合集" : `将 ${ids.length} 条收藏加入合集`,
    body: [
      h(
        "div.collection-tools",
        query,
        button("新建", () =>
          create(async (id) => {
            definitions = (
              await fetchJSON(`/api/collections?link_ids=${ids.join(",")}`)
            ).items.filter((c) => !c.deleted && !c.archived);
            touched.set(id, true);
            render();
          }),
        ),
      ),
      rows,
      h("p.collection-hint", "可加入多个合集，移出不会删除收藏。"),
    ],
    actions: [
      {
        label: "保存",
        primary: true,
        run: async () => {
          for (const [id, selected] of touched) {
            const c = definitions.find((c) => c.id === id);
            if (!requests.has(id))
              requests.set(
                id,
                mutation(id, c.revision, selected ? "add" : "remove", {
                  link_ids: ids,
                }),
              );
            try {
              await requests.get(id)();
              touched.delete(id);
              c.selected_count = selected ? ids.length : 0;
            } catch (e) {
              failure(e);
              render();
              return false;
            }
          }
          toast("合集已更新", { tone: "ok" });
        },
      },
    ],
  });
  render();
}
function note(c, item, after) {
  const input = h("textarea.collection-input", {
    value: item.note,
    rows: 4,
    maxLength: 2000,
    "aria-label": "合集内备注",
  });
  const save = mutation(c.id, c.revision, "note", { link_id: item.link_id });
  openDialog({
    title: "合集内备注",
    body: input,
    initialFocus: () => input,
    actions: [
      {
        label: "保存",
        primary: true,
        run: async () => {
          try {
            await save({ link_id: item.link_id, note: input.value });
            await after();
          } catch (e) {
            failure(e);
            return false;
          }
        },
      },
    ],
  });
}
export function initCollections(options) {
  hooks = options;
  byId("browse-collections")?.addEventListener("click", () => browse());
  byId("add-to-collection")?.addEventListener("click", () =>
    pick([state.selectedId]),
  );
  on("collections:changed", (id) => {
    if (state.filters.collection_id === id) hooks.reload();
    renderContext();
  });
  on("list:loaded", renderNav);
  load().catch(() => {});
  window.addEventListener("focus", () => load().catch(() => {}));
}
