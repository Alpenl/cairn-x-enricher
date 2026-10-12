// Reader reason card (prototype "reason-block"): an empty prompt, the saved
// reason with an edit button, or an inline editor. It edits the same draft as
// the organize panel's textarea through curation.js, so both stay in step.
import * as curation from "./curation.js";
import { byId, clear, h } from "./dom.js";
import { icon } from "./icons.js";
import { on } from "./store.js";
import { toast } from "./ui.js";

let currentId = 0;
let editing = false;
let saving = false;
let box = null;

function autoGrow(textarea) {
  textarea.style.height = "auto";
  textarea.style.height = `${Math.max(64, textarea.scrollHeight)}px`;
}

function render({ focus = false } = {}) {
  if (!box) return;
  if (!currentId) { box.replaceChildren(); return; }
  const text = curation.reasonText(currentId);
  if (editing) {
    const area = h("textarea", {
      rows: "2", maxlength: String(curation.REASON_MAX), "aria-label": "收藏原因",
      placeholder: "为什么收藏它？之后会在哪里用到？"
    });
    area.value = text;
    const count = h("span.reason-count");
    const sync = () => {
      const n = [...area.value].length;
      count.textContent = `${n}/${curation.REASON_MAX}`;
      count.hidden = n < curation.REASON_MAX - 40;
      autoGrow(area);
    };
    area.addEventListener("input", sync);
    area.addEventListener("keydown", (event) => {
      if (event.isComposing) return;
      if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) { event.preventDefault(); void commit(area.value); }
      else if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); cancel(); }
    });
    clear(box, h("div.reason-edit", area, h("div.reason-edit-foot",
      h("span.hint", h("kbd", "⌘"), h("kbd", "↵"), " 保存　", h("kbd", "Esc"), " 取消"),
      count,
      h("button.btn.btn-ghost.btn-sm", { type: "button", onclick: cancel }, "取消"),
      h("button.btn.btn-primary.btn-sm", { type: "button", disabled: saving, onclick: () => commit(area.value) }, saving ? "保存中…" : "保存"))));
    sync();
    if (focus) { area.focus(); area.setSelectionRange(area.value.length, area.value.length); }
    return;
  }
  if (text.trim()) {
    clear(box, h("div.reason-card",
      h("div.reason-body", h("div.reason-label", "收藏原因"), h("div.reason-text", text)),
      h("button.btn.btn-ghost.btn-sm", { type: "button", "aria-label": "编辑收藏原因", title: "编辑收藏原因 (R)", onclick: () => startEdit() }, icon("pencil", 14), "编辑")));
    return;
  }
  clear(box, h("button.reason-empty", { type: "button", title: "写收藏原因 (R)", onclick: () => startEdit() }, icon("pencil", 14), "写一句收藏原因"));
}

async function commit(value) {
  if (saving) return;
  const id = currentId;
  const before = curation.reasonText(id);
  if (value === before) { editing = false; render(); return; }
  saving = true;
  render();
  let ok = false;
  try { ok = await curation.saveReason(id, value); } finally { saving = false; }
  if (id !== currentId) return;
  if (ok) {
    editing = false;
    toast(value.trim() ? "收藏原因已保存" : "已清除收藏原因", { tone: "ok" });
  }
  render({ focus: editing });
  if (!editing) box.querySelector("button")?.focus({ preventScroll: true });
}

function cancel() {
  editing = false;
  render();
  box.querySelector("button")?.focus({ preventScroll: true });
}

export function startEdit() {
  if (!currentId || !box) return;
  editing = true;
  render({ focus: true });
  box.scrollIntoView({ block: "nearest" });
}

export function show(id) {
  if (id !== currentId) { editing = false; saving = false; }
  currentId = id;
  if (!editing) render();
}

export function initReason() {
  box = byId("reason-block");
  on("item", (id) => { if (id === currentId && !editing) render(); });
}
