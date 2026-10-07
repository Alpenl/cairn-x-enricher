// "分类依据与诊断": stored evidence, independent classification status, entity
// provenance and the three explicit redo actions. Everything here reads stored
// data; each action states its cost before it runs. The section loads lazily,
// only when opened, so ordinary reading costs no extra requests.
import { api, errorLabel } from "./api.js";
import { byId, clear, h } from "./dom.js";
import { formatDateTime } from "./format.js";
import { entityPayload } from "./curation.js";
import { emit, on, state } from "./store.js";
import { confirmAction, toast } from "./ui.js";

const ROLE_LABELS = Object.freeze({
  primary: "原帖", author_continuation: "作者续帖", quoted: "引用", external_article: "外链文章",
  third_party: "第三方", legacy_unknown: "来源未知"
});
const STATUS_LABELS = Object.freeze({
  pending: "等待分类", processing: "分类中", failed: "分类失败（可重试）", exhausted: "分类已耗尽重试",
  completed: "分类完成", waiting_source: "等待来源"
});
const DECISIONS = Object.freeze({ relevant: "实质讨论", incidental: "偶然提及", none: "非实体", unknown: "无法确认" });
const KINDS = Object.freeze({ person: "人物", organization: "组织", product: "产品", project: "项目", place: "地点" });

const els = {};
let currentId = 0;
let loadedId = 0;
let statusTimer = 0;
let statusPolls = 0;
let historyId = 0;
let historyCursor = null;
let historyBusy = false;
const statusFlights = new Map();
function visible() { return !document.hidden && els.root?.open && state.route.name !== "backstage" && (state.layout !== "narrow" || state.route.name === "bookmark"); }
export function syncVisibility() {
  if (!visible()) { clearTimeout(statusTimer); statusTimer = 0; }
  else if (["pending", "processing"].includes(els.status?.dataset.status)) scheduleStatusPoll(currentId);
}

async function loadRunHistory(id, append = false) {
  if (historyBusy) return;
  historyBusy = true;
  els.historyMore.disabled = true;
  try {
    const page = await api.runHistory(id, append ? historyCursor : null);
    if (id !== currentId) return;
    if (!append) els.historyItems.replaceChildren();
    historyId = id;
    historyCursor = page.next_after_id;
    els.historyMore.hidden = !historyCursor;
    if (page.available === false) { note(els.historyStatus, "后端暂不支持分页历史。"); return; }
    note(els.historyStatus, page.runs?.length ? "按时间倒序；展开一条记录读取完整判断。" : "还没有分类运行记录。");
    for (const run of page.runs || []) {
      const row = h("details.run-record", h("summary", `${formatDateTime(run.created_at)} · ${run.status} · ${run.resolved_model || run.requested_model}${run.archived ? " · 已归档" : ""}`));
      let loaded = false;
      row.addEventListener("toggle", async () => {
        if (!row.open || loaded) return;
        loaded = true;
        row.querySelector("pre")?.remove();
        const body = h("pre.run-record-body", "正在读取…");
        row.append(body);
        try { body.textContent = JSON.stringify(await api.runDetail(id, run.id), null, 2); }
        catch { body.textContent = "读取失败，收起后重试。"; loaded = false; }
      });
      els.historyItems.append(row);
    }
  } catch {
    if (id === currentId) note(els.historyStatus, "读取运行历史失败，重新展开可重试。", true);
  } finally {
    historyBusy = false;
    els.historyMore.disabled = false;
    if (id !== currentId && currentId && els.history.open) loadRunHistory(currentId);
  }
}

function note(node, message, isError = false) {
  node.textContent = message || "";
  node.hidden = !message;
  node.classList.toggle("error", Boolean(isError));
}

async function loadEvidence(id) {
  let payload;
  try {
    payload = await api.evidence(id);
  } catch {
    if (id === currentId) note(els.evidenceStatus, "依据不可用：读取来源快照失败。", true);
    return;
  }
  if (id !== currentId) return;
  els.evidenceBlocks.replaceChildren();
  if (!payload || payload.available === false || !Array.isArray(payload.snapshot?.blocks)) {
    note(els.evidenceStatus, "依据不可用：这条收藏还没有保存的来源快照。");
    return;
  }
  const facts = [payload.current === false ? "这是较早的来源版本。" : "",
    payload.snapshot.fetched_at ? `抓取于 ${formatDateTime(payload.snapshot.fetched_at)}` : "",
    payload.truncated ? "来源在保存时被截断，覆盖范围有限。" : ""].filter(Boolean).join(" ");
  note(els.evidenceStatus, facts, Boolean(payload.truncated));
  payload.snapshot.blocks.forEach((block, index) => {
    const head = h("div.v2-evidence-head", h("span.v2-evidence-role", ROLE_LABELS[block.role] || "未知角色"));
    if (block.url) {
      head.append(h("a", { href: block.url, target: "_blank", rel: "noopener noreferrer" }, block.url));
    }
    if (block.relation) head.append(h("span.muted", block.relation));
    els.evidenceBlocks.append(h("div.v2-evidence-block", { dataset: { index: String(index) } },
      head, h("p.v2-evidence-text", String(block.text || "").slice(0, 600))));
  });
}

function renderStatus(id, payload) {
  const status = payload?.status || "unknown";
  const label = STATUS_LABELS[status] || status;
  const attempts = Number.isSafeInteger(payload?.attempts) ? `，已尝试 ${payload.attempts} 次` : "";
  const error = payload?.error ? `：${payload.error}` : "";
  note(els.status, `${label}${attempts}${error}`, status === "failed" || status === "exhausted");
  els.status.dataset.status = status;
  // Keep polling while the queue is actually working, with a bounded count.
  if ((status === "pending" || status === "processing") && id === currentId) scheduleStatusPoll(id);
}

function loadStatus(id) {
  if (statusFlights.has(id)) return statusFlights.get(id);
  const flight = fetchStatus(id).finally(() => statusFlights.delete(id));
  statusFlights.set(id, flight); return flight;
}
async function fetchStatus(id) {
  try {
    const payload = await api.classificationStatus(id);
    if (id !== currentId) return;
    if (payload?.available === false) {
      note(els.status, "后端不支持分类状态查询。");
      return;
    }
    const previous = els.status.dataset.status;
    renderStatus(id, payload);
    if (["pending", "processing"].includes(previous) &&
        !["pending", "processing"].includes(payload?.status)) emit("classification:changed", id);
  } catch {
    if (id === currentId) note(els.status, "分类状态暂不可用。");
  }
}

function scheduleStatusPoll(id) {
  if (!visible() || statusTimer || statusPolls++ >= 20) return;
  statusTimer = setTimeout(() => {
    statusTimer = 0;
    if (id === currentId && visible()) loadStatus(id);
  }, 4000);
}

function renderObservations(id) {
  const payload = entityPayload(id);
  const observations = Array.isArray(payload?.observations) ? payload.observations : [];
  els.provenance.hidden = observations.length === 0;
  els.observations.replaceChildren();
  const entities = Array.isArray(payload?.entities) ? payload.entities : [];
  for (const observation of observations) {
    const candidate = observation.candidate || {};
    const current = !payload.stale && entities.includes(candidate.surface) && observation.decision === "relevant";
    const identity = observation.canonical_state === "matched"
      ? `归一化：${observation.canonical_label}（${KINDS[observation.canonical_kind] || observation.canonical_kind}）`
      : observation.canonical_state === "none" ? "无匹配身份" : "身份未确认";
    const item = h("li", `原文「${candidate.surface || ""}」 · ${DECISIONS[observation.decision] || "未知"} · ${identity}${current ? "" : " · 非当前有效结果"}`);
    if (candidate.block_id) item.append(` · 来源片段 ${candidate.block_id}，字符 ${candidate.start + 1}–${candidate.end}`);
    for (const evidence of Array.isArray(observation.canonical_evidence) ? observation.canonical_evidence : []) {
      try {
        const url = new URL(evidence.identifier);
        if (!["http:", "https:"].includes(url.protocol) || url.username || url.password) continue;
        item.append(" · ", h("a", { href: url.href, target: "_blank", rel: "noopener noreferrer" }, "身份依据"));
      } catch {
        // Malformed legacy evidence is shown as text only, never made a link.
      }
    }
    els.observations.append(item);
  }
}

function load(id) {
  emit("detail:aux-needed", id);
  loadedId = id;
  statusPolls = 0;
  clearTimeout(statusTimer);
  statusTimer = 0;
  els.replay.hidden = true;
  els.replay.replaceChildren();
  els.status.dataset.status = "";
  note(els.status, "正在读取分类状态…");
  note(els.evidenceStatus, "正在读取来源快照…");
  els.evidenceBlocks.replaceChildren();
  loadStatus(id);
  loadEvidence(id);
  renderObservations(id);
}

// --- Explicit redo actions --------------------------------------------------------

export async function retryClassification(id) {
  const button = els.retry;
  if (button) button.disabled = true;
  try {
    const payload = await api.retryClassification(id);
    toast(payload.detail || "已重新加入分类队列", { tone: "ok" });
    statusPolls = 0;
    if (id === currentId && els.root.open) loadStatus(id);
  } catch (error) {
    toast(error?.message === "v2_unsupported" ? "后端不支持分类重试" : errorLabel(error?.message), { tone: "error" });
  } finally {
    if (button) button.disabled = false;
  }
}

export async function refreshSource(id) {
  const confirmed = await confirmAction({
    title: "重新抓取原文？",
    message: "会再次读取来源（一次检索调用）。旧内容与人工整理在新内容到达前保持不变。",
    confirmLabel: "重新抓取"
  });
  if (!confirmed) return;
  if (els.refresh) els.refresh.disabled = true;
  try {
    const payload = await api.refreshSource(id);
    toast(payload.detail || "已提交来源刷新", { tone: "ok" });
  } catch (error) {
    toast(error?.message === "lease_conflict" ? "已有抓取任务在进行中" : errorLabel(error?.message), { tone: "error" });
  } finally {
    if (els.refresh) els.refresh.disabled = false;
  }
}

export async function replayPolicy(id, commit = false) {
  if (!els.root.open) els.root.open = true;
  const holder = els.replay;
  holder.hidden = false;
  clear(holder, h("p.muted", "正在按当前策略重算（0 次模型调用）…"));
  try {
    const payload = await api.replayPolicy(id, commit);
    if (id !== currentId) return;
    const changed = Array.isArray(payload.changed) && payload.changed.length > 0
      ? `变化字段：${payload.changed.join("、")}` : "重算结果与当前一致";
    clear(holder, h("p", `${changed}（模型调用 ${payload.model_calls} 次）`));
    if (payload.committed === true) holder.append(h("p.muted", "已写回新的策略决定。"));
    else if (payload.committed_reason) holder.append(h("p.error", payload.committed_reason));
    else holder.append(h("button.btn.btn-sm", { type: "button", onclick: () => replayPolicy(id, true) }, "写回这个结果"));
  } catch (error) {
    clear(holder, h("p.error", errorLabel(error?.message || "replay_failed")));
  }
}

export function show(id) {
  const changed = id !== currentId;
  currentId = id;
  if (changed) {
    historyId = 0;
    historyCursor = null;
    els.historyItems.replaceChildren();
    note(els.historyStatus, "");
    els.historyMore.hidden = true;
    if (id && els.history.open && !historyBusy) loadRunHistory(id);
  }
  currentId = id;
  if (els.root.open && loadedId !== id) load(id);
  else if (!els.root.open) loadedId = 0;
}

export function openDiagnostics() {
  emit("inspector:open", "processing");
  els.root.open = true;
  els.root.scrollIntoView({ block: "start", behavior: "smooth" });
}

export function initDiagnostics() {
  Object.assign(els, {
    root: byId("diagnostics"), status: byId("v2-classification-status"), retry: byId("v2-retry-classification"),
    replayButton: byId("v2-replay-policy"), refresh: byId("v2-refresh-source"), replay: byId("v2-replay-result"),
    evidenceStatus: byId("v2-evidence-status"), evidenceBlocks: byId("v2-evidence-blocks"),
    provenance: byId("v2-entity-provenance"), observations: byId("v2-entity-observations")
  });
  Object.assign(els, { history: byId("run-history"), historyStatus: byId("run-history-status"),
    historyItems: byId("run-history-items"), historyMore: byId("run-history-more") });
  els.history.addEventListener("toggle", () => { if (els.history.open && currentId && historyId !== currentId) loadRunHistory(currentId); });
  els.historyMore.addEventListener("click", () => { if (currentId && historyCursor) loadRunHistory(currentId, true); });
  els.root.addEventListener("toggle", () => {
    if (els.root.open && currentId && loadedId !== currentId) load(currentId);
    syncVisibility();
  });
  document.addEventListener("visibilitychange", syncVisibility);
  els.retry.addEventListener("click", () => retryClassification(currentId));
  els.refresh.addEventListener("click", () => refreshSource(currentId));
  els.replayButton.addEventListener("click", () => replayPolicy(currentId, false));
  on("entities", (id) => { if (id === currentId && els.root.open) renderObservations(id); });
}
