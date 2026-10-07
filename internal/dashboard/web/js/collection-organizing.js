import { openWorkspace } from "./workspace.js";
import { control as h } from "./controls.js";
import { fetchJSON, invalidateQueryReads, errorLabel } from "./api.js";
import { clear, h as el } from "./dom.js";
import { icon } from "./icons.js";
import { openDialog, toast } from "./ui.js";
const root = "/api/collections/organizing";
const post = (path, body) =>
  fetchJSON(root + path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
const labels = {
  organizing_active: "已有整理任务进行中，请查看进度",
  preselection_stale: "正文或合集定义已变化，请重新生成预选",
  revision_conflict: "合集已在其他设备修改。刷新核对后再应用",
  invalid_scope: "一次最多整理 1000 条收藏",
  forbidden: "只能查看或审核整理结果",
};
const isRunning = (data) =>
  data.items.some((i) => ["queued", "processing"].includes(i.status)) ||
  (data.run.mode === "apply" && !data.run.auto_finished);
const failure = (e) =>
  toast(labels[e.message] || errorLabel(e.message), { tone: "error" });
const button = (label, run, cls = "button.btn.btn-sm") =>
  el(
    cls,
    {
      type: "button",
      onclick: async (e) => {
        const n = e.currentTarget;
        n.disabled = true;
        try {
          await run();
        } catch (error) {
          failure(error);
        } finally {
          n.disabled = false;
        }
      },
    },
    label,
  );
export async function openOrganizing(definitions, uuid, changed) {
  const active = definitions.filter((c) => !c.deleted && !c.archived);
  const chosen = new Set(active.slice(0, 32).map((c) => c.id));
  let mode = "review";
  const count = el("span.num");
  const syncCount = () => (count.textContent = `已选 ${chosen.size} / ${active.length}`);
  const targets = el("div.og-chips", active.map((c) => el("button.tag-pick", {
    type: "button", "aria-pressed": String(chosen.has(c.id)),
    onclick: (e) => {
      if (!chosen.has(c.id) && chosen.size >= 32) return toast("一次最多判断 32 个合集");
      chosen.has(c.id) ? chosen.delete(c.id) : chosen.add(c.id);
      e.currentTarget.setAttribute("aria-pressed", String(chosen.has(c.id)));
      syncCount();
    },
  }, c.name)));
  syncCount();
  const modes = el("div.og-modes", { role: "radiogroup", "aria-label": "自动整理模式" }, [
    ["review", "生成预选，由我审核后加入", "推荐。匹配度 80% 以上的会默认勾选"],
    ["apply", "直接加入预选结果", "不需要审核，之后可以整次撤销"],
  ].map(([value, title, hint]) => el("label.og-mode",
    el("input", { type: "radio", name: "og-mode", value, checked: value === mode, onchange: () => (mode = value) }),
    el("span", el("b", title), el("small", hint)))));
  const history = el("div.og-history");
  let creatingKey = null, creatingSignature = "", dialog;
  const status = (r) => r.pending ? ["处理中", `${r.total - r.pending} / ${r.total}`, "accent"] : r.review_count ? ["待审核", `${r.review_count} 条`, "ai"] : ["已处理", `${r.total} 条`, ""];
  const refresh = async () => {
    const data = await fetchJSON(root);
    clear(history, data.items.length
      ? data.items.map((r) => {
          const [label, detail, tone] = status(r);
          return el("button.og-run", { type: "button", onclick: () => { dialog.close(); return reviewOrganizing(r.id, uuid, changed).catch(failure); } },
            el("span.og-run-main", el("b", new Date(r.created_at).toLocaleString("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" })), el("small", r.mode === "apply" ? "直接加入" : "人工审核", r.next_attempt_at ? " · 今日额度已用完，次日继续" : "")),
            el("span.og-badge", { dataset: { tone } }, label, " ", detail), icon("chevronRight", 16));
        })
      : el("p.tl-empty", "还没有整理记录。"));
  };
  const start = el("button.btn.btn-primary", { type: "button", onclick: async (e) => {
    const node = e.currentTarget;
    if (!chosen.size) return toast("请至少选择一个合集");
    node.disabled = true;
    try {
      const signature = JSON.stringify([[...chosen].sort(), mode]);
      if (signature !== creatingSignature) { creatingKey = uuid(); creatingSignature = signature; }
      const result = await post("", { operation_key: creatingKey, collection_ids: [...chosen], mode });
      dialog.close();
      await reviewOrganizing(result.run.id, uuid, changed);
    } catch (error) {
      failure(error);
    } finally {
      node.disabled = false;
    }
  } }, icon("sparkles", 14), "开始整理现有收藏");
  dialog = openWorkspace({
    key: "organize",
    title: "AI 整理",
    body: active.length
      ? el("div.og",
          el("section.og-card",
            el("h2", "开始一次整理"),
            el("div.og-field", el("div.og-label", el("b", "判断哪些合集"), count), targets),
            el("div.og-field", el("div.og-label", el("b", "结果怎么处理")), modes),
            el("p.tl-note", "AI 只会把收藏加入你选中的已有合集，不会移出、新建或修改合集。每次加入都可以撤销。"),
            el("div.og-actions", start)),
          el("section.og-card", el("header.og-card-head", el("h2", "整理记录"), el("button.btn.btn-sm.btn-ghost", { type: "button", onclick: () => refresh().catch(failure) }, icon("refresh", 14), "刷新")), history))
      : el("div.og-card", el("h2", "还没有可用的合集"), el("p.tl-note", "AI 整理会把收藏加入已有合集。先到合集页新建一个。")),
  });
  if (active.length) await refresh().catch(failure);
}
export async function reviewOrganizing(id, uuid, changed) {
  const summary = h("p.collection-hint"),
    rows = h("div.collection-list"),
    actions = h("div.collection-list");
  const drafts = new Map(),
    intents = new Map();
  let data,
    closed = false,
    flight = false,
    timer;
  const notify = () => {
    invalidateQueryReads();
    changed?.();
  };
  const applyItem = async (item, selection) => {
    const signature = JSON.stringify([...selection].sort());
    let intent = intents.get(item.link_id);
    if (!intent || intent.signature !== signature) {
      const current = await fetchJSON("/api/collections");
      const expected = {};
      for (const cid of selection) {
        const c = current.items.find((c) => c.id === cid);
        if (!c || c.deleted || c.archived) throw Error("preselection_stale");
        expected[cid] = c.revision;
      }
      intent = {
        signature,
        body: {
          operation_key: uuid(),
          link_ids: [item.link_id],
          collection_ids: [...selection],
          expected_revisions: expected,
        },
      };
      intents.set(item.link_id, intent);
    }
    try {
      await post("/" + id + "/apply", intent.body);
      drafts.delete(item.link_id);
      intents.delete(item.link_id);
      notify();
    } catch (e) {
      if (e.message === "revision_conflict") intents.delete(item.link_id);
      throw e;
    }
  };
  const render = () => {
    const pending = data.items.filter((i) =>
      ["queued", "processing"].includes(i.status),
    ).length;
    const ready = data.items.filter((i) => i.status === "ready"),
      failed = data.items.filter((i) => i.status === "failed");
    summary.textContent =
      `${data.run.mode === "apply" ? "直接应用预选" : "人工审核"} · 共 ${data.items.length} 条，${pending} 条处理中，${ready.length} 条待审核，${failed.length} 条无法判断。` +
      (data.run.next_attempt_at ? "当日预算已用完，次日继续。" : "") +
      " 概率只表示匹配判断，默认勾选明确匹配项。";
    clear(
      rows,
      ready.slice(0, 30).map((item) => {
        let selected = drafts.get(item.link_id);
        if (!selected) {
          selected = new Set(
            data.run.definitions
              .filter((c) => item.probabilities[c.id] >= 0.8)
              .map((c) => c.id),
          );
          drafts.set(item.link_id, selected);
        }
        const choices = data.run.definitions.map((c) => {
          const input = el("input", {
            type: "checkbox",
            checked: selected.has(c.id),
          });
          input.onchange = () => {
            input.checked ? selected.add(c.id) : selected.delete(c.id);
            intents.delete(item.link_id);
          };
          const p = Math.round((item.probabilities[c.id] ?? 0) * 100);
          return el("label.og-cand", { class: p >= 80 ? "is-high" : "" }, input, el("span", c.name), el("span.og-bar", el("i", { style: `width:${p}%` })), el("span.og-p.num", `${p}%`));
        });
        return el(
          "div.collection-review-item.og-review",
          el("b.og-review-title", item.title),
          item.error ? el("small", item.error) : null,
          el("div.og-cands", choices),
          el(
            "div.og-actions",
            button("确认加入", async () => {
              if (!selected.size) {
                toast("请选择合集或跳过");
                return;
              }
              await applyItem(item, selected);
              await refresh();
            }),
            button("跳过", async () => {
              await post("/" + id + "/dismiss", { link_ids: [item.link_id] });
              drafts.delete(item.link_id);
              await refresh();
            }),
          ),
        );
      }),
      ready.length > 30
        ? h("p.collection-hint", "先显示 30 条，处理后继续显示下一批。")
        : null,
      failed
        .slice(0, 10)
        .map((i) => h("p.collection-hint", `${i.title} · ${i.error}`)),
    );
    clear(
      actions,
      data.actions
        .filter((a) => a.status !== "undone")
        .map((a) =>
          h(
            "div.collection-row",
            h(
              "span",
              `${a.actor === "direct" ? "直接应用" : "审核应用"} · ${a.payload.link_ids.length} 条${a.status === "pending" ? " · 部分完成" : ""}`,
            ),
            button("撤销这次加入", async () => {
              await post("/" + id + "/undo", { action_id: a.id });
              notify();
              await refresh();
            }),
          ),
        ),
    );
    if (!isRunning(data) && timer) {
      clearInterval(timer);
      timer = null;
    }
  };
  const refresh = async () => {
    if (flight || closed) return;
    flight = true;
    try {
      data = await fetchJSON(root + "/" + id);
      if (!closed) render();
    } finally {
      flight = false;
    }
  };
  const bulk = async () => {
    for (const item of data.items.filter((i) => i.status === "ready")) {
      const selected =
        drafts.get(item.link_id) ||
        new Set(
          data.run.definitions
            .filter((c) => item.probabilities[c.id] >= 0.8)
            .map((c) => c.id),
        );
      if (selected.size) await applyItem(item, selected);
      else await post("/" + id + "/dismiss", { link_ids: [item.link_id] });
    }
    await refresh();
  };
  openWorkspace({
    key: "organize",
    title: "核对整理结果",
    body: [
      summary,
      h(
        "div.collection-tools",
        button("刷新进度", refresh),
        button("确认全部预选", bulk),
      ),
      rows,
      h(
        "p.collection-hint",
        "撤销只移出这次新加入的收藏。后续人工修改过合集时会要求重新核对。",
      ),
      actions,
    ],
    onClose: () => {
      closed = true;
      clearInterval(timer);
    },
  });
  await refresh().catch(failure);
  if (!closed && data && isRunning(data))
    timer = setInterval(() => refresh().catch(() => {}), 5000);
}
