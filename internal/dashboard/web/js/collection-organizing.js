import { openWorkspace } from "./workspace.js";
import { control as h } from "./controls.js";
import { fetchJSON, invalidateQueryReads, errorLabel } from "./api.js";
import { clear } from "./dom.js";
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
const button = (label, run) =>
  h(
    "button.btn",
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
  const mode = h(
    "select.collection-input",
    { "aria-label": "自动整理模式" },
    h("option", { value: "review" }, "生成预选，审核后加入（默认）"),
    h("option", { value: "apply" }, "直接应用预选，无需审核"),
  );
  const targets = h(
    "div.collection-tools",
    active.map((c) => {
      const box = h("input", { type: "checkbox", checked: chosen.has(c.id) });
      box.onchange = () => {
        if (box.checked && chosen.size >= 32) {
          box.checked = false;
          toast("一次最多判断 32 个合集");
          return;
        }
        box.checked ? chosen.add(c.id) : chosen.delete(c.id);
      };
      return h("label.collection-pick", box, h("span", c.name));
    }),
  );
  const history = h("div.collection-list");
  const form = h(
    "div",
    h(
      "p.collection-hint",
      "只判断你选中的已有合集。现有归属保留，没有明确匹配时不会强行加入。",
    ),
    targets,
    mode,
  );
  let creatingKey = null;
  let creatingSignature = "";
  let dialog;
  const refresh = async () => {
    const data = await fetchJSON(root);
    clear(
      history,
      data.items.length
        ? data.items.map((r) =>
            h(
              "div.collection-row",
              button(
                `${new Date(r.created_at).toLocaleString()} · ${r.pending ? `处理中 ${r.total - r.pending}/${r.total}` : r.review_count ? `${r.review_count} 条待审核` : "已处理"}${r.next_attempt_at ? " · 等待次日预算" : ""}`,
                () => {
                  dialog.close();
                  return reviewOrganizing(r.id, uuid, changed);
                },
              ),
              h("small", r.mode === "apply" ? "直接应用" : "人工审核"),
            ),
          )
        : h("p.collection-hint", "还没有自动整理记录。"),
    );
  };
  dialog = openWorkspace({
    key: "organize",
    title: "AI 整理",
    body: [
      form,
      h(
        "div.collection-tools",
        button("开始整理现有收藏", async () => {
          if (!chosen.size) {
            toast("请至少选择一个合集");
            return;
          }
          const signature = JSON.stringify([[...chosen].sort(), mode.value]);
          if (signature !== creatingSignature) {
            creatingKey = uuid();
            creatingSignature = signature;
          }
          const result = await post("", {
            operation_key: creatingKey,
            collection_ids: [...chosen],
            mode: mode.value,
          });
          dialog.close();
          await reviewOrganizing(result.run.id, uuid, changed);
        }),
        button("刷新记录", refresh),
      ),
      history,
    ],
  });
  await refresh().catch(failure);
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
          const input = h("input", {
            type: "checkbox",
            checked: selected.has(c.id),
          });
          input.onchange = () => {
            input.checked ? selected.add(c.id) : selected.delete(c.id);
            intents.delete(item.link_id);
          };
          return h(
            "label.collection-pick",
            input,
            h(
              "span",
              `${c.name} · ${Math.round((item.probabilities[c.id] ?? 0) * 100)}%`,
            ),
          );
        });
        return h(
          "div.collection-review-item",
          h("p", item.title),
          item.error ? h("small", item.error) : null,
          h("div.collection-tools", choices),
          h(
            "div.collection-tools",
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
