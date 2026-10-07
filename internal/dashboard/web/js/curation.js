// Human curation of one bookmark: the saved reason, the controlled tags and
// entities. Invariants carried over from the original reader:
//
//   - A reason or status save never sends `classification` and never produces
//     a tag override: editing one is not an implicit review of the other.
//   - Only an explicit tag edit or an explicit "确认标签" sends tags.
//   - v2 field actions (accept / reject / set_empty / reset) are distinct and
//     each new logical action carries its own operation key and the expected
//     revision. Actions are queued, never dropped; a retry reuses its key.
//   - A CAS conflict keeps the draft and the pending action until the user
//     explicitly re-applies or discards it.
//   - A refresh or poll never overwrites an unsaved draft.
import { api, errorLabel, newOperationKey } from "./api.js";
import { byId, clear, h } from "./dom.js";
import { needsReview } from "./format.js";
import { icon } from "./icons.js";
import { emit, getItem, mergeItem, on } from "./store.js";
import { terms, termActive, termLabel, V1_DIMENSIONS, V2_DIMENSIONS, visibleDimensions, vocab } from "./taxonomy.js";
import { toast } from "./ui.js";
import * as tagSystem from "./tag-system.js";
import { primaryTags } from "./topic-presentation.js";

const WHY_SAVE_DELAY = 1200;
const V1_SAVE_DELAY = 700;
const MAX_WHY = 200;

const ENTITY_STATE_LABELS = Object.freeze({
  not_run: "实体识别尚未运行",
  failed: "实体识别失败",
  completed_empty: "没有识别到实体",
  completed_nonempty: "",
  stale: "来源已变化，实体待更新"
});

const sessions = new Map();
let currentId = 0;
let remoteTimer = 0;
const els = {};

function sessionFor(id) {
  let session = sessions.get(id);
  if (!session) {
    session = {
      id,
      why: { dirty: false, timer: 0, saving: false, error: false },
      v1: { selection: null, dirty: false, timer: 0, saving: false, confirmPending: null },
      v2: { status: "idle", selection: null, automatic: null, revision: 0, queue: [], inFlight: false,
        blocked: null, awaitingDetail: false, awaitingFromRevision: 0, readEpoch: 0 },
      entities: { status: "idle", payload: null, revision: 0, readEpoch: 0 },
      editing: new Set(),
      saveState: ""
    };
    sessions.set(id, session);
  }
  return session;
}

const MAX_SESSIONS = 200;

function busy(session) {
  return session.why.dirty || session.why.saving || session.v1.dirty || session.v1.saving
    || session.v2.queue.length > 0 || session.v2.inFlight || Boolean(session.v2.blocked);
}

// pruneSessions forgets idle bookmarks once many were visited; their state is
// re-read from the server the next time they open.
function pruneSessions() {
  if (sessions.size <= MAX_SESSIONS) return;
  for (const [id, session] of sessions) {
    if (sessions.size <= MAX_SESSIONS / 2) break;
    if (id !== currentId && !busy(session)) sessions.delete(id);
  }
}

export function hasUnsavedWork() {
  if (tagSystem.hasUnsavedTagWork()) return true;
  for (const session of sessions.values()) if (busy(session)) return true;
  return false;
}

// --- Save indicator -------------------------------------------------------------

function setSaveState(session, value, { error = false } = {}) {
  session.saveState = value;
  session.saveError = error;
  if (session.id !== currentId) return;
  els.saveState.textContent = value;
  els.saveState.classList.toggle("error", error);
  els.saveState.classList.toggle("saving", value === "保存中…");
  renderSummary();
}

// The closed panel shows only saved/effective information. Suggestions and
// editor controls stay inside the native disclosure.
function renderSummary() {
  const session = sessions.get(currentId);
  if (!session || !els.summaryTags) return;
  const item = getItem(currentId);
  const modern = tagSystem.tagSystemOverview(currentId);
  const selection = modern?.selection || (session.v1.dirty ? session.v1.selection : null) || item?.effective_selection || (els.section.open && session.v2.status === "ready" ? session.v2.selection : null) || item?.classification || {};
  const tags = primaryTags(selection, modern?.custom_tags || item?.custom_tags, terms("topics"))
    .map((tag) => ({ ...tag, label: termLabel(tag.field, tag.id, tag.label) }));
  els.summaryTags.replaceChildren(...tags.slice(0, 5).map((tag) => h("button.tag.curate-summary-tag.tag-filter", {
    type: "button", title: `筛选：${tag.label || termLabel(tag.field, tag.id)}`,
    onclick: (event) => { event.preventDefault(); event.stopPropagation(); emit("tag-filter-request", { field: tag.field, term: tag.id }); }
  }, tag.label || termLabel(tag.field, tag.id))),
    ...(tags.length > 5 ? [h("span.tag.curate-summary-more", `+${tags.length - 5}`)] : []));
  els.summaryTags.hidden = !tags.length;
  const why = (session.why.dirty || session.why.saving ? session.why.draft : item?.why || "") || "";
  els.summaryWhy.textContent = why.replace(/\s+/g, " ").trim();
  els.summaryWhy.title = why;
  els.summaryWhy.hidden = !why.trim();
  const status = modern?.blocked ? "有修改待处理" : session.saveError ? "保存失败" : session.why.dirty || session.v1.dirty ? "未保存" : "";
  els.summaryState.textContent = status;
  els.summaryState.hidden = !status;
}

function openPanel() {
  if (!currentId || els.section.hidden) return false;
  emit("inspector:open","curation");
  els.section.open = true;
  autoGrow();
  return true;
}

// --- Reason (why) -----------------------------------------------------------------

function whyValue() {
  return els.why.value;
}

function scheduleWhySave(session) {
  clearTimeout(session.why.timer);
  session.why.timer = setTimeout(() => saveWhy(session), WHY_SAVE_DELAY);
}

async function saveWhy(session, { draft } = {}) {
  clearTimeout(session.why.timer);
  const value = draft ?? (session.id === currentId ? whyValue() : session.why.draft);
  if (!session.why.dirty && draft === undefined) return;
  if (session.why.saving) {
    // A save is in flight; the newer draft is saved right after it lands.
    session.why.pending = true;
    return;
  }
  session.why.saving = true;
  session.why.draft = value;
  setSaveState(session, "保存中…");
  try {
    // Only the reason travels: never classification, never an override.
    const item = await api.curation(session.id, { why: value });
    mergeItem(item);
    const latest = session.id === currentId ? whyValue() : session.why.draft;
    session.why.dirty = latest !== value;
    setSaveState(session, session.why.dirty ? "未保存" : "已保存");
    emit("item", session.id);
  } catch (error) {
    session.why.dirty = true;
    setSaveState(session, `保存失败：${errorLabel(error.message)}`, { error: true });
    toast(`收藏原因没有保存：${errorLabel(error.message)}`, { tone: "error" });
  } finally {
    session.why.saving = false;
    if (session.why.pending) {
      session.why.pending = false;
      if (session.why.dirty) saveWhy(session);
    }
    if (session.id === currentId) renderWhyCounter();
  }
}

export function flushPending(id = currentId) {
  const session = sessions.get(id);
  if (!session) return;
  if (session.why.dirty && !session.why.saving) saveWhy(session);
  if (session.v1.dirty && !session.v1.saving) saveV1(session);
}

function renderWhyCounter() {
  const length = [...whyValue()].length;
  els.whyCount.textContent = `${length}/${MAX_WHY}`;
  els.whyCount.hidden = length < MAX_WHY - 40;
}

function autoGrow() {
  els.why.style.height = "auto";
  els.why.style.height = `${Math.min(els.why.scrollHeight, 180)}px`;
}

export function focusWhy() {
  if (!openPanel()) return;
  els.why.focus();
  els.why.setSelectionRange(els.why.value.length, els.why.value.length);
}

// --- v1 tags (fallback editor) ------------------------------------------------------

function v1Selection(item) {
  const classification = item?.classification || {};
  return { topics: [...(classification.topics || [])], form: classification.form || "", use: classification.use || "" };
}

function scheduleV1Save(session) {
  clearTimeout(session.v1.timer);
  session.v1.timer = setTimeout(() => saveV1(session), V1_SAVE_DELAY);
}

async function saveV1(session) {
  clearTimeout(session.v1.timer);
  if (!session.v1.dirty || session.v1.saving) return;
  session.v1.saving = true;
  const selection = structuredClone(session.v1.selection);
  setSaveState(session, "保存中…");
  try {
    const item = await api.curation(session.id, { classification: selection });
    mergeItem(item);
    session.v1.dirty = JSON.stringify(session.v1.selection) !== JSON.stringify(selection);
    setSaveState(session, session.v1.dirty ? "未保存" : "已保存");
    emit("item", session.id);
    // Edits made while this save was in flight get their own save.
    if (session.v1.dirty) scheduleV1Save(session);
  } catch (error) {
    // The draft stays dirty; the next edit or leaving the bookmark retries.
    // Retrying on a timer would spin while the backend is down.
    setSaveState(session, `保存失败：${errorLabel(error.message)}`, { error: true });
    toast(`标签没有保存：${errorLabel(error.message)}`, { tone: "error" });
  } finally {
    session.v1.saving = false;
    if (session.id === currentId) renderTags();
  }
}

function toggleV1(session, dimension, term) {
  const item = getItem(session.id);
  if (!session.v1.selection) session.v1.selection = v1Selection(item);
  const selection = session.v1.selection;
  if (dimension.multi) {
    const values = selection[dimension.key];
    if (values.includes(term)) selection[dimension.key] = values.filter((value) => value !== term);
    else if (values.length < dimension.max) selection[dimension.key] = [...values, term];
    else {
      toast(`主题最多选 ${dimension.max} 个`, { tone: "error" });
      return;
    }
  } else {
    selection[dimension.key] = selection[dimension.key] === term ? "" : term;
  }
  session.v1.dirty = true;
  session.v1.confirmPending = null;
  setSaveState(session, "未保存");
  renderTags();
  scheduleV1Save(session);
}

// confirmTags is the explicit "these AI labels are right" action.
export async function confirmTags(id, { quiet = false } = {}) {
  if (vocab.tagSystemAvailable || tagSystem.tagSystemReady(id)) return tagSystem.confirmTagSystem(id);
  const item = getItem(id);
  if (!item || item.classification_reviewed) return false;
  const session = sessionFor(id);
  // A pending v2 edit may already have changed the effective selection. The
  // old AI projection must not be confirmed over that edit while the detail
  // read is still catching up with the committed override.
  if (session.v1.saving || session.v2.awaitingDetail || session.v2.queue.length || session.v2.inFlight || session.v2.blocked) {
    if (!quiet) toast("请等标签修改保存完成后再确认", { tone: "error" });
    return false;
  }
  const selection = v1Selection(item);
  if (!selection.topics.length && !selection.form && !selection.use) {
    if (!quiet) toast("这条还没有可确认的 AI 标签", { tone: "error" });
    return false;
  }
  const revision = item.cache_identity?.personal_revision;
  const pending = session.v1.confirmPending;
  const intent = JSON.stringify(selection);
  const request = Number.isSafeInteger(revision)
    ? pending && pending.revision === revision && pending.intent === intent ? pending
      : { revision, intent, key: newOperationKey(`confirm-tags-${id}`) }
    : null;
  session.v1.confirmPending = request;
  session.v1.saving = true;
  setSaveState(session, "保存中…");
  try {
    const updated = await api.curation(id, { classification: selection,
      ...(request ? { expected_revision: request.revision, operation_key: request.key } : {}) });
    mergeItem(updated);
    session.v1.selection = null;
    session.v1.dirty = false;
    session.v1.confirmPending = null;
    setSaveState(session, "已确认标签");
    emit("item", id);
    if (vocab.v2Available && session.v2.status === "ready") loadV2(session, { force: true });
    return true;
  } catch (error) {
    if (error?.message === "revision_conflict") {
      session.v1.confirmPending = null;
      try {
        mergeItem(await api.detail(id));
        emit("item", id);
        if (session.id === currentId) renderTags();
      } catch {
        // The next visible-detail check can refresh the stale row.
      }
    }
    setSaveState(session, `确认失败：${errorLabel(error.message)}`, { error: true });
    if (!quiet) toast(`确认标签失败：${errorLabel(error.message)}`, { tone: "error" });
    throw error;
  } finally {
    session.v1.saving = false;
  }
}

async function resetV1(session) {
  setSaveState(session, "保存中…");
  try {
    const item = await api.curation(session.id, { classification: null });
    mergeItem(item);
    session.v1.selection = null;
    session.v1.dirty = false;
    setSaveState(session, "已恢复自动分类");
    toast("已恢复自动分类");
    emit("item", session.id);
    if (session.v2.status === "ready") loadV2(session, { force: true });
  } catch (error) {
    setSaveState(session, `恢复失败：${errorLabel(error.message)}`, { error: true });
  }
}

// --- v2 tags (field-level override queue) --------------------------------------------

async function loadV2(session, { force = false } = {}) {
  // A poll or refresh must never overwrite a draft or a pending action.
  if (session.v2.queue.length || session.v2.inFlight || session.v2.blocked) return;
  if (!force && session.v2.status === "loading") return;
  const readEpoch = ++session.v2.readEpoch;
  session.v2.status = session.v2.status === "ready" ? "ready" : "loading";
  try {
    const response = await api.v2Selection(session.id, getItem(session.id)?.cache_identity, { fresh: force, signal: force ? undefined : session.remoteController?.signal });
    if (readEpoch !== session.v2.readEpoch || session.v2.queue.length || session.v2.inFlight || session.v2.blocked) return;
    if (!response) {
      session.v2.status = "idle";
      if (session.id === currentId) queueMicrotask(() => loadV2(session, { force: true }));
      return;
    }
    if (!response.available) {
      session.v2.status = "unavailable";
    } else {
      session.v2.status = "ready";
      session.v2.selection = response.selection;
      session.v2.automatic = response.automatic || null;
      // The revision authorises a CAS write; without one the client must not
      // pretend to have it.
      if (typeof response.revision === "number") session.v2.revision = response.revision;
    }
  } catch {
    if (readEpoch !== session.v2.readEpoch) return;
    session.v2.status = "unavailable";
  }
  if (session.id === currentId) renderTags();
}

function applyLocal(session, { field, term, action }) {
  const selection = session.v2.selection;
  if (!selection) return;
  const dimension = V2_DIMENSIONS.find((entry) => entry.key === field);
  const current = Array.isArray(selection[field]) ? selection[field] : [];
  switch (action) {
    case "accept":
      // A single-valued dimension replaces; a multi-valued one accumulates.
      selection[field] = dimension && !dimension.multi ? [term] : current.includes(term) ? current : [...current, term];
      break;
    case "reject":
      selection[field] = current.filter((value) => value !== term);
      break;
    case "set_empty":
      selection[field] = [];
      break;
    case "reset":
      if (session.v2.automatic && Array.isArray(session.v2.automatic[field])) selection[field] = [...session.v2.automatic[field]];
      break;
  }
}

export function enqueueOverride(id, field, term, action) {
  const session = sessionFor(id);
  if (session.v1.saving) {
    setSaveState(session, "请等标签确认完成后再修改", { error: true });
    return;
  }
  session.v1.confirmPending = null;
  if (session.v2.blocked) {
    setSaveState(session, "请先处理下面的保存问题（重试或放弃修改），再继续编辑。", { error: true });
    return;
  }
  const entry = { key: newOperationKey(`v2-${id}`), field, term, action };
  if (!session.v2.awaitingDetail) session.v2.awaitingFromRevision = session.v2.revision;
  applyLocal(session, entry);
  session.v2.queue.push(entry);
  session.v2.awaitingDetail = true;
  if (session.id === currentId) {
    renderTags();
    els.section.setAttribute("aria-busy", "true");
  }
  pump(session);
}

async function pump(session) {
  if (session.v2.inFlight || session.v2.queue.length === 0 || session.v2.blocked) return;
  const entry = session.v2.queue[0];
  session.v2.inFlight = true;
  setSaveState(session, "保存中…");
  try {
    const payload = await api.v2Override(session.id, {
      field: entry.field, term: entry.term, action: entry.action,
      operation_key: entry.key, expected_revision: session.v2.revision
    });
    session.v2.revision = payload.revision ?? session.v2.revision;
    session.v2.queue.shift();
    session.v2.inFlight = false;
    if (session.v2.queue.length) {
      pump(session);
      return;
    }
    setSaveState(session, "已保存");
    if (session.id === currentId) els.section.setAttribute("aria-busy", "false");
    await loadV2(session, { force: true });
    // The v1 projection, review state and list row follow the new decision.
    try {
      mergeItem(await api.detail(session.id));
      session.v2.awaitingDetail = false;
      emit("item", session.id);
      if (session.id === currentId) renderTags();
    } catch {
      // The tags are saved; a stale row is refreshed by the next read.
    }
  } catch (error) {
    session.v2.inFlight = false;
    const code = error?.message || "override_failed";
    if (code === "revision_conflict" || code === "snapshot_conflict") {
      // Keep the draft and the queued action, adopt the server revision and
      // require an explicit re-apply. Loading the server value here would
      // silently discard the user's unsaved intent.
      if (typeof error.revision === "number") session.v2.revision = error.revision;
      session.v2.blocked = { kind: "conflict", entry };
      setSaveState(session, "这条整理已被其他客户端更新", { error: true });
    } else {
      // A transient failure keeps the same operation key, so the retry is the
      // same logical commit rather than a new one.
      session.v2.blocked = { kind: "error", entry, code };
      setSaveState(session, `保存失败：${errorLabel(code)}`, { error: true });
    }
    if (session.id === currentId) {
      els.section.setAttribute("aria-busy", "false");
      renderConflict(session);
    } else {
      toast("有一条收藏的标签没有保存，打开它可以重试", { tone: "error", action: { label: "打开", run: () => emit("select-request", session.id) } });
    }
  }
}

function renderConflict(session) {
  const holder = els.conflict;
  const blocked = session.v2.blocked;
  holder.hidden = !blocked;
  if (!blocked) { holder.replaceChildren(); return; }
  const message = blocked.kind === "conflict"
    ? "这条整理已被其他客户端更新。你的修改还保留着，可以重新应用，或放弃并载入最新结果。"
    : `标签没有保存（${errorLabel(blocked.code)}）。你的修改还保留着，可以重试。`;
  const retry = h("button.btn.btn-sm.btn-primary", { type: "button", dataset: { conflict: "retry" } }, icon("refresh", 14), blocked.kind === "conflict" ? "重新应用我的修改" : "重试保存");
  retry.addEventListener("click", () => {
    // Clear the block before pumping, otherwise the guard would refuse to
    // resubmit the preserved action.
    session.v2.blocked = null;
    renderConflict(session);
    els.section.setAttribute("aria-busy", "true");
    pump(session);
  });
  const discard = h("button.btn.btn-sm", { type: "button", dataset: { conflict: "discard" } }, "放弃我的修改");
  discard.addEventListener("click", async () => {
    session.v2.queue = [];
    session.v2.blocked = null;
    renderConflict(session);
    setSaveState(session, "");
    await loadV2(session, { force: true });
    try {
      mergeItem(await api.detail(session.id));
      session.v2.awaitingDetail = false;
      emit("item", session.id);
      if (session.id === currentId) renderTags();
    } catch {
      // Keep confirmation disabled until a later detail read catches up.
    }
  });
  clear(holder, icon("alert", 16), h("p", message), h("div.conflict-actions", retry, discard));
}

// --- Entities ---------------------------------------------------------------------

async function loadEntities(session, { force = false } = {}) {
  const readEpoch = ++session.entities.readEpoch;
  if (session.entities.status === "idle") session.entities.status = "loading";
  try {
    const payload = await api.entities(session.id, getItem(session.id)?.cache_identity, { fresh: force, signal: force ? undefined : session.remoteController?.signal });
    if (readEpoch !== session.entities.readEpoch) return;
    if (!payload) {
      session.entities.status = "idle";
      if (session.id === currentId) queueMicrotask(() => loadEntities(session, { force: true }));
      return;
    }
    if (!payload || payload.available === false) {
      session.entities.status = "unavailable";
    } else {
      session.entities.status = "ready";
      session.entities.payload = payload;
      if (Number.isInteger(payload.revision)) session.entities.revision = payload.revision;
    }
  } catch {
    if (readEpoch !== session.entities.readEpoch) return;
    session.entities.status = "unavailable";
  }
  if (session.id === currentId) renderTags();
  emit("entities", session.id);
}

export function entityPayload(id) {
  return sessions.get(id)?.entities.payload || null;
}

async function submitEntity(session, action, term) {
  try {
    const payload = await api.correctEntity(session.id, {
      operation_key: newOperationKey(`entity-${session.id}-${action}`),
      action, term, expected_revision: session.entities.revision
    });
    session.entities.readEpoch++;
    session.entities.payload = payload;
    if (Number.isInteger(payload.revision)) session.entities.revision = payload.revision;
    toast(action === "reject" ? `已移除实体「${term}」` : `已添加实体「${term}」`, { tone: "ok" });
  } catch (error) {
    if (error?.message === "revision_conflict") {
      toast("实体已被其他客户端更新，已载入最新结果，请再操作一次", { tone: "error" });
      await loadEntities(session, { force: true });
      return;
    }
    toast(`实体没有更新：${errorLabel(error?.message)}`, { tone: "error" });
  }
  if (session.id === currentId) renderTags();
  emit("entities", session.id);
}

// --- Rendering ----------------------------------------------------------------------

function chip({ label, className, field, term, action, pressed, title, ai }) {
  const button = h(`button.chip.${className}`, {
    type: "button", dataset: { field, term, action }, "aria-pressed": pressed === undefined ? null : String(pressed), title
  });
  if (ai) button.append(icon("sparkles", 12, "chip-ai"));
  button.append(h("span", label));
  if (className === "on") button.append(icon("x", 11, "chip-x"));
  if (className === "option" || className === "rejected") button.append(icon("plus", 11, "chip-x"));
  return button;
}

function editToggle(key, editing, label) {
  return h("button.chip.chip-edit", {
    type: "button", dataset: { edit: key }, "aria-expanded": String(editing), title: editing ? "收起选项" : `编辑${label}`
  }, icon(editing ? "check" : "plus", 12), h("span", editing ? "完成" : "编辑"));
}

function row(key, label, field, { id } = {}) {
  return h("div.curate-row.tag-row", { id: id || null, dataset: { dimension: key }, role: "group", "aria-label": label },
    h("span.curate-label", label), field);
}

function renderV2Rows(session, item) {
  const selection = session.v2.selection || {};
  const automatic = session.v2.automatic;
  const unreviewed = !item?.classification_reviewed;
  const rows = [];
  for (const dimension of visibleDimensions()) {
    const selected = Array.isArray(selection[dimension.key]) ? selection[dimension.key] : [];
    const suggested = Array.isArray(automatic?.[dimension.key]) ? automatic[dimension.key] : [];
    const editing = session.editing.has(dimension.key);
    const field = h("div.curate-field.chips");
    for (const term of selected) {
      const fromAI = suggested.includes(term);
      field.append(chip({
        label: termLabel(dimension.key, term) + (termActive(dimension.key, term) || !vocab.v2 ? "" : "（停用）"),
        className: "on", field: dimension.key, term, action: "reject", pressed: true,
        title: fromAI && unreviewed ? "AI 建议，点击移除" : "点击移除", ai: fromAI && unreviewed
      }));
    }
    // A rejected AI suggestion stays visible so the decision is reversible.
    for (const term of suggested.filter((value) => !selected.includes(value))) {
      field.append(chip({ label: termLabel(dimension.key, term), className: "rejected", field: dimension.key, term, action: "accept", pressed: false, title: "AI 曾建议，已移除；点击恢复" }));
    }
    if (!selected.length && !suggested.length && !editing) field.append(h("span.chip-empty", "无"));
    if (editing) {
      for (const term of terms(dimension.key)) {
        if (term.active === false || selected.includes(term.id) || suggested.includes(term.id)) continue;
        field.append(chip({ label: term.label, className: "option", field: dimension.key, term: term.id, action: "accept", pressed: false, title: `添加「${term.label}」` }));
      }
      // An explicit "nothing applies" control, distinct from resetting.
      field.append(
        h("button.link-btn.small", { type: "button", dataset: { field: dimension.key, term: "", action: "set_empty" }, title: "明确标记这一项都不适用" }, "都不适用"),
        h("button.link-btn.small", { type: "button", dataset: { field: dimension.key, term: "", action: "reset" }, title: "撤销人工修改，使用自动结果" }, "恢复自动"));
    }
    field.append(editToggle(dimension.key, editing, dimension.label));
    rows.push(row(dimension.key, dimension.label, field, { id: `v2-${dimension.key}` }));
  }
  const topics = selection.topics || [];
  if (topics.length > 2) {
    rows.push(h("p.curate-note#v2-folded-note", "列表卡片最多展示 2 个标签；其余标签仍然保留。"));
  }
  return rows;
}

function renderV1Rows(session, item) {
  const selection = session.v1.selection || v1Selection(item);
  const unreviewed = !item?.classification_reviewed && !session.v1.selection;
  const rows = [];
  for (const dimension of V1_DIMENSIONS) {
    const selected = dimension.multi ? selection[dimension.key] : [selection[dimension.key]].filter(Boolean);
    const editing = session.editing.has(dimension.key);
    const field = h("div.curate-field.chips");
    for (const term of selected) {
      field.append(chip({
        label: termLabel(dimension.vocabulary, term), className: "on", field: dimension.key, term, action: "v1-toggle",
        pressed: true, title: "点击移除", ai: unreviewed
      }));
    }
    if (!selected.length && !editing) field.append(h("span.chip-empty", "未指定"));
    if (editing) {
      const full = dimension.multi && selected.length >= dimension.max;
      for (const term of terms(dimension.key === "topics" ? "topics" : dimension.key)) {
        if (!term.active || selected.includes(term.id)) continue;
        const option = chip({ label: term.label, className: "option", field: dimension.key, term: term.id, action: "v1-toggle", pressed: false, title: full ? `最多 ${dimension.max} 个` : `添加「${term.label}」` });
        option.disabled = full;
        field.append(option);
      }
    }
    field.append(editToggle(dimension.key, editing, dimension.label));
    rows.push(row(dimension.key, dimension.multi ? `${dimension.label} ${selected.length}/${dimension.max}` : dimension.label, field, { id: `v1-${dimension.key}` }));
  }
  return rows;
}

function renderEntityRow(session) {
  if (session.entities.status !== "ready") return null;
  const payload = session.entities.payload || {};
  const entities = Array.isArray(payload.entities) ? payload.entities : [];
  const human = new Set(Array.isArray(payload.human) ? payload.human : []);
  const field = h("div.curate-field.chips#v2-entity-list");
  for (const entity of entities) {
    const remove = h("button.chip-remove", { type: "button", "aria-label": `移除实体 ${entity}`, title: "移除" }, icon("x", 11));
    remove.addEventListener("click", () => submitEntity(session, "reject", entity));
    field.append(h("span.chip.entity.v2-entity", { title: human.has(entity) ? "人工添加" : "自动识别" },
      human.has(entity) ? icon("user", 12) : null, h("span", entity), remove));
  }
  const state = payload.stale ? "stale" : payload.state || "not_run";
  const stateText = ENTITY_STATE_LABELS[state];
  if (!entities.length || state === "stale") field.append(h("span.chip-empty#v2-entity-state", stateText || "无"));
  const input = h("input.entity-input#v2-entity-input", { type: "text", maxLength: 80, placeholder: "添加实体", "aria-label": "人工添加实体" });
  const add = h("button.chip.chip-edit#v2-entity-add", { type: "button", title: "添加实体" }, icon("plus", 12), h("span", "添加"));
  const submit = () => {
    const value = input.value.trim();
    if (!value) { input.focus(); return; }
    input.value = "";
    submitEntity(session, "accept", value);
  };
  add.addEventListener("click", submit);
  input.addEventListener("keydown", (event) => {
    if (event.key === "Enter") { event.preventDefault(); submit(); }
    if (event.key === "Escape") { event.preventDefault(); input.value = ""; input.blur(); }
  });
  field.append(input, add);
  return row("entities", "实体", field, { id: "v2-entities" });
}

function renderFoot(session, item, compact = false) {
  const reviewed = Boolean(item?.classification_reviewed);
  const classification = item?.classification;
  const hasSuggestions = Boolean(classification?.topics?.length || classification?.form || classification?.use);
  const usingV2 = session.v2.status === "ready";
  els.confirm.hidden = reviewed || !hasSuggestions || session.v2.awaitingDetail;
  // In the multidimensional editor every field has its own reset; the global
  // v1 reset only restores topics/form/use and would read as "undo all".
  els.reset.hidden = !reviewed || usingV2;
  let text = "";
  if (compact) text = "";
  else if (!classification && item?.status !== "completed") text = "处理完成后会生成 AI 标签";
  else if (reviewed) text = "标签已人工确认";
  else if (needsReview(item)) text = "AI 标签待确认";
  else if (hasSuggestions) text = "AI 建议的标签";
  clear(els.review, reviewed ? icon("check", 13) : hasSuggestions ? icon("sparkles", 13) : null, text);
  els.review.classList.toggle("reviewed", reviewed);
}

function captureFocus() {
  const active = document.activeElement;
  if (!active || !els.tags.contains(active)) return null;
  return { field: active.dataset.field, term: active.dataset.term, action: active.dataset.action, edit: active.dataset.edit, id: active.id,
    value: active instanceof HTMLInputElement ? active.value : undefined, start: active.selectionStart, end: active.selectionEnd };
}

function restoreFocus(saved) {
  if (!saved) return;
  let target = null;
  if (saved.id) target = byId(saved.id);
  else if (saved.edit) target = els.tags.querySelector(`[data-edit="${saved.edit}"]`);
  else if (saved.field) {
    target = [...els.tags.querySelectorAll(`[data-field="${saved.field}"]`)].find((node) => node.dataset.term === saved.term)
      || els.tags.querySelector(`[data-edit="${saved.field}"]`);
  }
  if (target && saved.value !== undefined) {
    target.value = saved.value;
    target.setSelectionRange(saved.start, saved.end);
  }
  target?.focus({ preventScroll: true });
}

function hasAnyTags(session, item, mode) {
  if (mode === "v2") {
    const lists = [session.v2.selection, session.v2.automatic].filter(Boolean);
    return lists.some((selection) => V2_DIMENSIONS.some(({ key }) => Array.isArray(selection[key]) && selection[key].length));
  }
  const selection = session.v1.selection || v1Selection(item);
  return Boolean(selection.topics.length || selection.form || selection.use);
}

export function renderTags() {
  if (tagSystem.renderTagSystem(currentId)) { renderSummary(); return; }
  const session = sessions.get(currentId);
  if (!session) return;
  const item = getItem(currentId);
  const saved = captureFocus();
  const rows = [];
  const mode = vocab.v2Available === false || session.v2.status === "unavailable" ? "v1"
    : session.v2.status === "ready" && vocab.v2Available ? "v2" : "loading";
  const compact = mode !== "loading" && session.editing.size === 0 && !hasAnyTags(session, item, mode);
  if (compact) {
    // Nothing to show yet: one line instead of a stack of empty dimensions.
    rows.push(row("tags", "标签", h("div.curate-field.chips",
      h("span.chip-empty", item?.status === "completed" || !item ? "还没有标签" : "还没有标签，处理完成后会自动生成"),
      h("button.chip.chip-edit", { type: "button", dataset: { editAll: "" }, title: "手动添加标签 (T)" }, icon("plus", 12), h("span", "手动添加"))), { id: "tags-empty" }));
  } else if (mode === "v2") rows.push(...renderV2Rows(session, item));
  else if (mode === "v1") rows.push(...renderV1Rows(session, item));
  else rows.push(h("div.curate-row.tag-row.loading", h("span.curate-label", "标签"), h("div.curate-field", h("span.sk.sk-chips"))));
  const entityRow = renderEntityRow(session);
  if (entityRow) rows.push(entityRow);
  els.tags.replaceChildren(...rows);
  renderFoot(session, item, compact);
  restoreFocus(saved);
  renderSummary();
}

function renderWhy(session, item, { switched = false } = {}) {
  const pending = session.why.dirty || session.why.saving;
  if (switched) {
    // A different bookmark: show its own unsaved draft if it has one.
    els.why.value = pending ? session.why.draft ?? item?.why ?? "" : item?.why || "";
  } else if (!pending && document.activeElement !== els.why) {
    // Same bookmark refreshed: never overwrite a draft being typed or saved.
    els.why.value = item?.why || "";
  }
  const suggestion = item?.classification?.why_suggestion || "";
  els.suggestion.textContent = suggestion;
  els.suggestionBlock.hidden = !suggestion || els.why.value.trim() === suggestion.trim();
  renderWhyCounter();
  autoGrow();
  renderSummary();
}

// show renders the curation card for a bookmark and starts its reads. A deep
// link can arrive before the bookmark itself is loaded; the card then appears
// on refresh(); editor reads wait until their disclosure is opened.
export function show(id) {
  const switched = currentId !== id;
  if (currentId && switched) { flushPending(currentId); suspendRemote(); }
  currentId = id;
  if (switched) els.section.open = Boolean(byId("inspector")?.getClientRects().length && byId("inspector-tabs")?.active === "curation");
  tagSystem.showTagSystem(id);
  pruneSessions();
  const session = sessionFor(id);
  if (switched || !session.remoteController || session.remoteController.signal.aborted) session.remoteController = new AbortController();
  const item = getItem(id);
  els.section.hidden = !item;
  els.saveState.textContent = session.saveState;
  els.saveState.classList.toggle("error", Boolean(session.saveError));
  renderConflict(session);
  if (item) {
    renderWhy(session, item, { switched });
    renderTags();
  } else if (switched) {
    els.why.value = session.why.draft ?? "";
  }
  renderSummary();
  // Remote reads wait a beat so skimming past a bookmark costs nothing.
  clearTimeout(remoteTimer);
  remoteTimer = setTimeout(() => {
    if (id !== currentId) return;
    if (!els.section.open && !byId("diagnostics")?.open) return;
    if (vocab.v2Available !== false && (session.v2.status === "idle" || session.v2.status === "ready")) loadV2(session);
    if (vocab.v2Available !== false && session.entities.status !== "loading") loadEntities(session);
  }, session.v2.status === "idle" ? 60 : 120);
}

// refresh re-renders from the latest item without discarding drafts.
export function refresh(id) {
  if (id !== currentId) return;
  tagSystem.refreshTagSystem(id);
  const session = sessionFor(id);
  const item = getItem(id);
  if (!item) return;
  if (session.v2.awaitingDetail && !session.v2.inFlight && !session.v2.queue.length &&
      Number(item.cache_identity?.personal_revision) > session.v2.awaitingFromRevision &&
      Number(item.cache_identity?.personal_revision) >= session.v2.revision) {
    session.v2.awaitingDetail = false;
  }
  const first = els.section.hidden;
  els.section.hidden = false;
  renderWhy(session, item, { switched: first && !session.why.dirty && !session.why.saving });
  renderTags();
}

export function reloadRemote(id) {
  if (!els.section.open && !byId("diagnostics")?.open) return;
  const session = sessions.get(id);
  if (!session) return;
  // A changed detail already filled the version-keyed caches from one Worker
  // snapshot. The normal read uses those values and still falls back on older
  // Workers whose detail route has no combined selection/entity response.
  loadV2(session);
  loadEntities(session);
}

export function suspendRemote() {
  clearTimeout(remoteTimer);
  tagSystem.suspendTagSystem();
  const session = sessions.get(currentId);
  if (!session) return;
  session.remoteController?.abort();
  for (const entry of [session.v2, session.entities]) { entry.readEpoch++; if (entry.status === "loading") entry.status = "idle"; }
}
export function resumeRemote() {
  const session = sessions.get(currentId);
  if (!session) return;
  if (session.remoteController?.signal.aborted) session.remoteController = new AbortController();
  tagSystem.showTagSystem(currentId); reloadRemote(currentId);
}

export function toggleEditingAll() {
  if (!openPanel()) return;
  if (tagSystem.editTagSystem(currentId)) return;
  const session = sessions.get(currentId);
  if (!session) return;
  const keys = session.v2.status === "ready" ? visibleDimensions().map((entry) => entry.key) : V1_DIMENSIONS.map((entry) => entry.key);
  const open = keys.some((key) => !session.editing.has(key));
  session.editing = new Set(open ? keys : []);
  renderTags();
  if (open) els.tags.querySelector(".chip")?.focus({ preventScroll: true });
}

export function initCuration() {
  tagSystem.setTagSupplement(() => {
    const session = sessions.get(currentId);
    if (!session) return;
    const secondary = session.v2.status === "ready"
      ? renderV2Rows(session, getItem(currentId)).filter((entry) => entry.dataset?.dimension && !["topics", "resource_kinds", "content_functions"].includes(entry.dataset.dimension)) : [];
    const entity = renderEntityRow(session);
    if (entity) secondary.push(entity);
    if (secondary.length) els.tags.append(h("details.tag-secondary", h("summary", "更多内容属性与实体"), ...secondary));
  });
  on("tag-system:fallback", (id) => { if (id === currentId) renderTags(); });
  on("tag-system:render", (id) => { if (id === currentId) renderSummary(); });
  on("taxonomy", () => {
    renderSummary();
    if (els.section?.open) renderTags();
  });
  Object.assign(els, {
    section: byId("curate"), fields: byId("curate-fields"), why: byId("curation-why"), whyCount: byId("why-count"),
    suggestion: byId("why-suggestion"), suggestionBlock: byId("why-suggestion-block"), useSuggestion: byId("use-suggestion"),
    tags: byId("tag-rows"), review: byId("review-state"), confirm: byId("confirm-classification"),
    reset: byId("reset-classification"), saveState: byId("save-state"), conflict: byId("v2-conflict"),
    summaryTags: byId("curate-summary-tags"), summaryWhy: byId("curate-summary-why"), summaryState: byId("curate-summary-state")
  });
  els.section.addEventListener("toggle", () => {
    if (els.section.open) { autoGrow(); resumeRemote(); }
    else if (!byId("diagnostics")?.open) suspendRemote();
    else tagSystem.suspendTagSystem();
  });
  on("detail:aux-needed", id => { if (id === currentId) resumeRemote(); });

  els.why.addEventListener("input", () => {
    const session = sessionFor(currentId);
    session.why.dirty = true;
    session.why.draft = els.why.value;
    setSaveState(session, "未保存");
    renderWhyCounter();
    autoGrow();
    scheduleWhySave(session);
  });
  els.why.addEventListener("blur", () => flushPending(currentId));
  els.why.addEventListener("keydown", (event) => {
    if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
      event.preventDefault();
      const session = sessionFor(currentId);
      if (session.why.dirty) saveWhy(session);
      els.why.blur();
    } else if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      els.why.blur();
    }
  });
  els.useSuggestion.addEventListener("click", () => {
    const item = getItem(currentId);
    const suggestion = item?.classification?.why_suggestion || "";
    if (!suggestion) return;
    els.why.value = [...suggestion].slice(0, MAX_WHY).join("");
    const session = sessionFor(currentId);
    session.why.dirty = true;
    session.why.draft = els.why.value;
    els.suggestionBlock.hidden = true;
    saveWhy(session);
  });

  els.tags.addEventListener("click", (event) => {
    const target = event.target.closest("button");
    if (!target || !els.tags.contains(target) || target.disabled) return;
    const session = sessionFor(currentId);
    if (target.dataset.editAll !== undefined) {
      toggleEditingAll();
      return;
    }
    if (target.dataset.edit) {
      const key = target.dataset.edit;
      if (session.editing.has(key)) session.editing.delete(key);
      else session.editing.add(key);
      renderTags();
      return;
    }
    const { field, term, action } = target.dataset;
    if (!action || !field) return;
    if (action === "v1-toggle") {
      const dimension = V1_DIMENSIONS.find((entry) => entry.key === field);
      if (dimension) toggleV1(session, dimension, term);
      return;
    }
    enqueueOverride(currentId, field, term || "", action);
  });

  els.confirm.addEventListener("click", () => emit("confirm-request", currentId));
  els.reset.addEventListener("click", () => resetV1(sessionFor(currentId)));

  window.addEventListener("beforeunload", (event) => {
    if (currentId) flushPending(currentId);
    if (hasUnsavedWork()) {
      event.preventDefault();
      event.returnValue = "";
    }
  });
}
