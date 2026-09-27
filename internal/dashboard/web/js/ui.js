// Shared interaction primitives: toasts (with an optional undo action), popover
// menus and modal dialogs built on <dialog>.
import { append, byId, clear, h } from "./dom.js";
import { icon } from "./icons.js";

// --- Toasts -----------------------------------------------------------------

let lastAction = null;

export function toast(message, { tone = "info", action = null, duration } = {}) {
  const host = byId("toasts");
  if (!host) return null;
  const node = h("div.toast", { class: `toast-${tone}`, role: tone === "error" ? "alert" : "status" },
    tone === "error" ? icon("alert", 16) : tone === "ok" ? icon("check", 16) : null,
    h("span.toast-text", message));
  let timer = 0;
  const close = () => {
    clearTimeout(timer);
    node.classList.add("leaving");
    setTimeout(() => node.remove(), 160);
    if (lastAction?.node === node) lastAction = null;
  };
  if (action) {
    const button = h("button.toast-action", { type: "button" }, action.label, action.key ? h("kbd", action.key) : null);
    button.addEventListener("click", () => { close(); action.run(); });
    node.append(button);
    lastAction = { node, run: () => { close(); action.run(); } };
  }
  node.append(h("button.toast-close", { type: "button", "aria-label": "关闭提示", onclick: close }, icon("x", 14)));
  host.append(node);
  while (host.children.length > 3) host.firstElementChild.remove();
  timer = setTimeout(close, duration ?? (action ? 7000 : tone === "error" ? 6000 : 3200));
  return close;
}

// runLastToastAction lets the undo shortcut reach the most recent toast.
export function runLastToastAction() {
  if (!lastAction) return false;
  lastAction.run();
  return true;
}

// --- Popover menus ------------------------------------------------------------

let openMenuState = null;

export function closeMenu() {
  if (!openMenuState) return;
  const { menu, anchor, onClose } = openMenuState;
  openMenuState = null;
  menu.remove();
  anchor?.setAttribute("aria-expanded", "false");
  document.removeEventListener("pointerdown", onOutside, true);
  onClose?.();
}

function onOutside(event) {
  if (!openMenuState) return;
  if (openMenuState.menu.contains(event.target) || openMenuState.anchor?.contains(event.target)) return;
  closeMenu();
}

export function isMenuOpen() {
  return Boolean(openMenuState);
}

// items: [{ label, icon, hint, kbd, danger, disabled, run } | "separator" | { heading }]
export function openMenu(anchor, items, { align = "end", onClose } = {}) {
  if (openMenuState?.anchor === anchor) { closeMenu(); return; }
  closeMenu();
  const menu = h("div.menu", { role: "menu" });
  const buttons = [];
  for (const item of items) {
    if (!item) continue;
    if (item === "separator") { menu.append(h("div.menu-sep", { role: "separator" })); continue; }
    if (item.heading) { menu.append(h("div.menu-heading", item.heading)); continue; }
    const button = h("button.menu-item", {
      type: "button", role: "menuitem", disabled: item.disabled || false, class: item.danger ? "danger" : ""
    },
    item.icon ? icon(item.icon, 16) : h("span.menu-icon-space"),
    h("span.menu-label", h("span", item.label), item.hint ? h("small", item.hint) : null),
    item.kbd ? h("kbd", item.kbd) : null);
    button.addEventListener("click", () => { closeMenu(); item.run?.(); });
    menu.append(button);
    buttons.push(button);
  }
  menu.addEventListener("keydown", (event) => {
    const enabled = buttons.filter((button) => !button.disabled);
    const index = enabled.indexOf(document.activeElement);
    if (event.key === "ArrowDown" || event.key === "j") { event.preventDefault(); enabled[(index + 1) % enabled.length]?.focus(); }
    else if (event.key === "ArrowUp" || event.key === "k") { event.preventDefault(); enabled[(index - 1 + enabled.length) % enabled.length]?.focus(); }
    else if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); closeMenu(); anchor?.focus(); }
    else if (event.key === "Tab") closeMenu();
  });
  document.body.append(menu);
  const rect = anchor.getBoundingClientRect();
  const width = menu.offsetWidth;
  const height = menu.offsetHeight;
  let left = align === "end" ? rect.right - width : rect.left;
  left = Math.max(8, Math.min(left, window.innerWidth - width - 8));
  let top = rect.bottom + 6;
  if (top + height > window.innerHeight - 8) top = Math.max(8, rect.top - height - 6);
  menu.style.left = `${left}px`;
  menu.style.top = `${top}px`;
  anchor.setAttribute("aria-expanded", "true");
  openMenuState = { menu, anchor, onClose };
  setTimeout(() => document.addEventListener("pointerdown", onOutside, true), 0);
  buttons.find((button) => !button.disabled)?.focus({ preventScroll: true });
}

// --- Dialogs ------------------------------------------------------------------

export function openDialog({ title, body, actions = [], wide = false, onClose, initialFocus }) {
  const dialog = h("dialog.dialog", { class: wide ? "dialog-wide" : "", "aria-label": title });
  const close = (value) => {
    if (!dialog.open) return;
    dialog.close(value ?? "");
  };
  const header = h("header.dialog-head", h("h2", title),
    h("button.icon-btn", { type: "button", "aria-label": "关闭", onclick: () => close("") }, icon("x", 18)));
  const footer = actions.length ? h("footer.dialog-foot", actions.map((action) => {
    const button = h(`button.btn${action.primary ? ".btn-primary" : ""}${action.danger ? ".btn-danger" : ""}`, {
      type: "button", dataset: action.id ? { action: action.id } : undefined
    }, action.label);
    button.addEventListener("click", async () => {
      if (action.run) {
        button.disabled = true;
        try {
          const keep = await action.run();
          if (keep === false) { button.disabled = false; return; }
        } catch {
          button.disabled = false;
          return;
        }
      }
      close(action.value ?? action.id ?? "ok");
    });
    return button;
  })) : null;
  const bodyNode = h("div.dialog-body", { tabindex: "-1" }, body);
  append(dialog, [header, bodyNode, footer]);
  dialog.addEventListener("close", () => { dialog.remove(); onClose?.(dialog.returnValue); });
  dialog.addEventListener("click", (event) => { if (event.target === dialog) close(""); });
  document.body.append(dialog);
  dialog.showModal();
  // Without an obvious first action, focus the content so the close button
  // does not light up with a focus ring the user never asked for.
  (initialFocus?.() || dialog.querySelector(".dialog-foot .btn-primary") || (actions.length ? dialog.querySelector(".dialog-foot button") : bodyNode))?.focus();
  return { dialog, close };
}

// confirmAction resolves true only on an explicit confirmation.
export function confirmAction({ title, message, confirmLabel = "确认", danger = false, detail }) {
  return new Promise((resolve) => {
    let confirmed = false;
    openDialog({
      title,
      body: [h("p.dialog-text", message), detail ? h("p.dialog-detail", detail) : null],
      actions: [
        { id: "cancel", label: "取消" },
        { id: "confirm", label: confirmLabel, primary: !danger, danger, run: () => { confirmed = true; } }
      ],
      onClose: () => resolve(confirmed)
    });
  });
}

export function setBusy(button, busy, label) {
  if (!button) return;
  button.disabled = busy;
  button.classList.toggle("busy", busy);
  if (label !== undefined) clear(button, label);
}
