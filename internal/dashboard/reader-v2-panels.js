// B06/B09: evidence provenance, independent classification status, entity
// lifecycle and the three explicit redo actions. Everything here reads stored
// data; the only action that costs a model call is a real reclassification or
// refresh, and each button states its cost before it runs.
(() => {
  "use strict";

  const ui = window.CairnUI;
  const match = window.location.pathname.match(/^\/bookmarks\/([1-9][0-9]*)$/);
  const bookmarkID = match ? Number(match[1]) : 0;
  if (!bookmarkID || !ui) return;

  const ROLE_LABELS = {
    primary: "原帖",
    author_continuation: "作者续帖",
    quoted: "引用",
    external_article: "外链文章",
    third_party: "第三方",
    legacy_unknown: "来源未知"
  };
  const STATUS_LABELS = {
    pending: "等待分类",
    processing: "分类中",
    failed: "分类失败（可重试）",
    exhausted: "分类已耗尽重试",
    completed: "分类完成",
    waiting_source: "等待来源"
  };
  const ENTITY_STATE_LABELS = {
    not_run: "实体未运行",
    failed: "实体失败",
    completed_empty: "实体为空（已完成）",
    completed_nonempty: "实体已完成",
    stale: "实体已过期"
  };

  let pollTimer = null;
  let entityRevision = 0;

  function setNote(id, message, isError = false) {
    const node = ui.byId(id);
    if (!node) return;
    node.textContent = message || "";
    node.hidden = !message;
    node.classList.toggle("error", Boolean(isError));
  }

  // --- Evidence provenance --------------------------------------------------

  async function loadEvidence() {
    const panel = ui.byId("v2-evidence");
    const holder = ui.byId("v2-evidence-blocks");
    if (!panel || !holder) return;
    let payload = null;
    try {
      payload = await ui.fetchJSON(`/api/bookmarks/${bookmarkID}/evidence`);
    } catch (_) {
      panel.hidden = false;
      setNote("v2-evidence-status", "依据不可用：读取来源快照失败。", true);
      return;
    }
    if (!payload || payload.available === false || !Array.isArray(payload.snapshot?.blocks)) {
      panel.hidden = false;
      setNote("v2-evidence-status", "依据不可用：这条收藏还没有保存的来源快照。");
      return;
    }
    panel.hidden = false;
    setNote("v2-evidence-status", payload.current === false ? "这是较早的来源版本。" : "");
    holder.replaceChildren();
    payload.snapshot.blocks.forEach((block, index) => {
      const row = ui.element("div", "v2-evidence-block");
      const head = ui.element("div", "v2-evidence-head");
      head.append(ui.element("span", "v2-evidence-role", ROLE_LABELS[block.role] || "未知角色"));
      if (block.url) {
        const link = ui.element("a", "", block.url);
        link.href = block.url;
        link.target = "_blank";
        link.rel = "noopener noreferrer";
        head.append(link);
      }
      if (block.relation) head.append(ui.element("span", "line-note", block.relation));
      const text = ui.element("p", "v2-evidence-text", String(block.text || "").slice(0, 600));
      row.append(head, text);
      row.dataset.index = String(index);
      holder.append(row);
    });
    if (payload.truncated) setNote("v2-evidence-status", "来源在保存时被截断，覆盖范围有限。", true);
  }

  // --- Classification status ------------------------------------------------

  function renderStatus(payload) {
    const status = payload?.status || "unknown";
    const label = STATUS_LABELS[status] || status;
    const attempts = Number.isSafeInteger(payload?.attempts) ? `，已尝试 ${payload.attempts} 次` : "";
    const error = payload?.error ? `：${payload.error}` : "";
    const isError = status === "failed" || status === "exhausted";
    setNote("v2-classification-status", `${label}${attempts}${error}`, isError);
    const actions = ui.byId("v2-actions");
    if (actions) actions.hidden = false;
    // Keep polling while the queue is actually working, with a bounded count.
    if (status === "pending" || status === "processing") scheduleStatusPoll();
  }

  async function loadStatus() {
    try {
      const payload = await ui.fetchJSON(`/api/bookmarks/${bookmarkID}/classification-status`);
      if (payload?.available === false) {
        setNote("v2-classification-status", "后端不支持分类状态查询。");
        return;
      }
      renderStatus(payload);
    } catch (_) {
      setNote("v2-classification-status", "分类状态暂不可用。");
    }
  }

  let statusPolls = 0;
  function scheduleStatusPoll() {
    if (pollTimer) return;
    if (statusPolls++ >= 20) return;
    pollTimer = setTimeout(() => {
      pollTimer = null;
      loadStatus();
    }, 4000);
  }

  // --- Entities -------------------------------------------------------------

  async function loadEntities() {
    const panel = ui.byId("v2-entities");
    const holder = ui.byId("v2-entity-list");
    if (!panel || !holder) return;
    let payload = null;
    try {
      payload = await ui.fetchJSON(`/api/bookmarks/${bookmarkID}/entities`);
    } catch (_) {
      panel.hidden = true;
      return;
    }
    if (!payload || payload.available === false) {
      panel.hidden = true;
      return;
    }
    panel.hidden = false;
    if (Number.isInteger(payload.revision)) entityRevision = payload.revision;
    const state = payload.state || "not_run";
    const stale = payload.stale ? "（内容已变化）" : "";
    ui.byId("v2-entity-state").textContent = `${ENTITY_STATE_LABELS[state] || state}${stale}`;
    holder.replaceChildren();
    const human = new Set(Array.isArray(payload.human) ? payload.human : []);
    for (const entity of Array.isArray(payload.entities) ? payload.entities : []) {
      const row = ui.element("span", "v2-entity");
      row.append(document.createTextNode(entity + (human.has(entity) ? "（人工）" : "")));
      const remove = ui.element("button", "text-btn", "移除");
      remove.type = "button";
      remove.addEventListener("click", () => submitEntity("reject", entity));
      row.append(remove);
      holder.append(row);
    }
    if (!holder.childElementCount) holder.append(ui.element("span", "line-note", "没有有效实体。"));
  }

  async function submitEntity(action, term) {
    try {
      const payload = await ui.fetchJSON(`/api/bookmarks/${bookmarkID}/entities`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          operation_key: newOperationKey("entity", action, term),
          action, term, expected_revision: currentRevision()
        })
      });
      renderEntityPayload(payload);
      ui.showToast("实体已更新");
    } catch (error) {
      if (error?.message === "revision_conflict") {
        ui.showToast("实体已被其他客户端更新，请刷新后重试", true);
        loadEntities();
        return;
      }
      ui.showToast(ui.errorLabel(error?.message || "entity_failed"), true);
    }
  }

  function renderEntityPayload(payload) {
    const holder = ui.byId("v2-entity-list");
    if (!holder || !payload) return;
    if (Number.isInteger(payload.revision)) entityRevision = payload.revision;
    const human = new Set(Array.isArray(payload.human) ? payload.human : []);
    holder.replaceChildren();
    for (const entity of Array.isArray(payload.entities) ? payload.entities : []) {
      const row = ui.element("span", "v2-entity");
      row.append(document.createTextNode(entity + (human.has(entity) ? "（人工）" : "")));
      const remove = ui.element("button", "text-btn", "移除");
      remove.type = "button";
      remove.addEventListener("click", () => submitEntity("reject", entity));
      row.append(remove);
      holder.append(row);
    }
    if (!holder.childElementCount) holder.append(ui.element("span", "line-note", "没有有效实体。"));
  }

  // A per-action identity: only a retry of the same action reuses it.
  function newOperationKey(kind, action, term) {
    const random = window.crypto?.randomUUID ? window.crypto.randomUUID() : `${Date.now()}-${Math.random().toString(16).slice(2)}`;
    return `${kind}-${bookmarkID}-${action}-${term || "none"}-${random}`;
  }

  function currentRevision() {
    return entityRevision;
  }

  // --- Three explicit redo actions ------------------------------------------

  async function retryClassification() {
    const button = ui.byId("v2-retry-classification");
    button.disabled = true;
    try {
      const payload = await ui.fetchJSON(`/api/bookmarks/${bookmarkID}/retry-classification`, { method: "POST" });
      ui.showToast(payload.detail || "已重新入分类队列");
      statusPolls = 0;
      loadStatus();
    } catch (error) {
      if (error?.message === "v2_unsupported") ui.showToast("后端不支持分类重试", true);
      else ui.showToast(ui.errorLabel(error?.message || "retry_failed"), true);
    } finally {
      button.disabled = false;
    }
  }

  async function refreshSource() {
    if (!window.confirm("重新抓取原文会再次读取来源；旧内容与人工整理在新内容到达前保持不变。确认继续？")) return;
    const button = ui.byId("v2-refresh-source");
    button.disabled = true;
    try {
      const payload = await ui.fetchJSON(`/api/bookmarks/${bookmarkID}/refresh-source`, { method: "POST" });
      ui.showToast(payload.detail || "已提交来源刷新");
    } catch (error) {
      if (error?.message === "lease_conflict") ui.showToast("已有抓取任务在进行中", true);
      else ui.showToast(ui.errorLabel(error?.message || "refresh_failed"), true);
    } finally {
      button.disabled = false;
    }
  }

  async function replayPolicy(commit) {
    const holder = ui.byId("v2-replay-result");
    holder.hidden = false;
    holder.replaceChildren(ui.element("p", "line-note", "正在按当前策略重算（0 次模型调用）…"));
    try {
      const payload = await ui.fetchJSON(`/api/bookmarks/${bookmarkID}/replay-policy`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify(commit ? { commit: true } : {})
      });
      holder.replaceChildren();
      const changed = Array.isArray(payload.changed) && payload.changed.length > 0
        ? `变化字段：${payload.changed.join("、")}` : "重算结果与当前一致";
      holder.append(ui.element("p", "", `${changed}（模型调用 ${payload.model_calls} 次）`));
      if (payload.committed === true) {
        holder.append(ui.element("p", "line-note", "已写回新的策略决定。"));
      } else if (payload.committed_reason) {
        holder.append(ui.element("p", "line-note error", payload.committed_reason));
      } else {
        const apply = ui.element("button", "text-btn", "写回这个结果");
        apply.type = "button";
        apply.addEventListener("click", () => replayPolicy(true));
        holder.append(apply);
      }
    } catch (error) {
      holder.replaceChildren(ui.element("p", "line-note error", ui.errorLabel(error?.message || "replay_failed")));
    }
  }

  // --- Wiring ---------------------------------------------------------------

  const retryButton = ui.byId("v2-retry-classification");
  const refreshButton = ui.byId("v2-refresh-source");
  const replayButton = ui.byId("v2-replay-policy");
  const entityAdd = ui.byId("v2-entity-add");
  if (retryButton) retryButton.addEventListener("click", retryClassification);
  if (refreshButton) refreshButton.addEventListener("click", refreshSource);
  if (replayButton) replayButton.addEventListener("click", () => replayPolicy(false));
  if (entityAdd) {
    entityAdd.addEventListener("click", () => {
      const input = ui.byId("v2-entity-input");
      const value = (input?.value || "").trim();
      if (!value) return;
      input.value = "";
      submitEntity("accept", value);
    });
  }

  loadEvidence();
  loadStatus();
  loadEntities();
})();
