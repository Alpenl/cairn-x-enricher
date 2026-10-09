// Shared interaction primitives: toasts (with an optional undo action), popover
// menus and modal dialogs built on <dialog>.
import "./vendor/components.js";
import { append, byId, clear, h } from "./dom.js";
import { icon } from "./icons.js";

// --- Toasts -----------------------------------------------------------------

let lastAction = null;

export function toast(
  message,
  { tone = "info", action = null, duration } = {},
) {
  const host = byId("toasts");
  if (!host) return null;
  const node = h(
    "div.toast",
    { class: `toast-${tone}`, role: tone === "error" ? "alert" : "status" },
    tone === "error"
      ? icon("alert", 16)
      : tone === "ok"
        ? icon("check", 16)
        : null,
    h("span.toast-text", message),
  );
  let timer = 0;
  const close = () => {
    clearTimeout(timer);
    node.classList.add("leaving");
    setTimeout(() => node.remove(), 160);
    if (lastAction?.node === node) lastAction = null;
  };
  if (action) {
    const button = h(
      "button.toast-action",
      { type: "button" },
      action.label,
      action.key ? h("kbd", action.key) : null,
    );
    button.addEventListener("click", () => {
      close();
      action.run();
    });
    node.append(button);
    lastAction = {
      node,
      run: () => {
        close();
        action.run();
      },
    };
  }
  node.append(
    h(
      "button.toast-close",
      { type: "button", "aria-label": "关闭提示", onclick: close },
      icon("x", 14),
    ),
  );
  host.append(node);
  while (host.children.length > 3) host.firstElementChild.remove();
  timer = setTimeout(
    close,
    duration ?? (action ? 7000 : tone === "error" ? 6000 : 3200),
  );
  return close;
}

// runLastToastAction lets the undo shortcut reach the most recent toast.
export function runLastToastAction() {
  if (!lastAction) return false;
  lastAction.run();
  return true;
}

// --- Menus and dialogs: Web Awesome owns positioning, focus and dismissal. ---
let openMenuState = null;
export function closeMenu() {
  if (!openMenuState) return;
  const { menu, anchor, placeholder, onClose } = openMenuState;
  openMenuState = null;
  placeholder.replaceWith(anchor);
  anchor.removeAttribute("slot");
  anchor.setAttribute("aria-expanded", "false");
  menu.remove();
  onClose?.();
}
export function isMenuOpen() {
  return Boolean(openMenuState?.menu.open);
}
export function openMenu(anchor, items, { align = "end", onClose } = {}) {
  if (openMenuState?.anchor === anchor) {
    closeMenu();
    return;
  }
  closeMenu();
  const placeholder = document.createComment("menu-trigger");
  anchor.before(placeholder);
  const menu = h("wa-dropdown.menu", {
    placement: `bottom-${align}`,
    size: "s",
  });
  menu.style.display = "contents";
  anchor.slot = "trigger";
  placeholder.after(menu);
  menu.append(anchor);
  const actions = new Map();
  let index = 0;
  for (const item of items) {
    if (!item) continue;
    if (item === "separator") {
      menu.append(h("div.menu-sep", { role: "separator" }));
      continue;
    }
    if (item.heading) {
      menu.append(h("div.menu-heading", item.heading));
      continue;
    }
    const value = String(index++);
    actions.set(value, item);
    menu.append(
      h(
        "wa-dropdown-item",
        {
          value,
          disabled: !!item.disabled,
          class: item.danger ? "danger" : "",
        },
        item.icon ? h("span", { slot: "icon" }, icon(item.icon, 16)) : null,
        h(
          "span.menu-label",
          h("span", item.label),
          item.hint ? h("small", item.hint) : null,
        ),
        item.kbd ? h("kbd", { slot: "details" }, item.kbd) : null,
      ),
    );
  }
  openMenuState = { menu, anchor, placeholder, onClose };
  menu.addEventListener("wa-select", (event) => {
    const action = actions.get(event.detail.item.value);
    closeMenu();
    action?.run?.();
  });
  menu.addEventListener("wa-after-hide", () => {
    if (openMenuState?.menu === menu) closeMenu();
  });
  menu.updateComplete.then(() => {
    if (openMenuState?.menu === menu) menu.open = true;
  });
}
export function openDialog({
  title,
  body,
  actions = [],
  wide = false,
  onClose,
  initialFocus,
}) {
  const dialog = h("wa-dialog.dialog", {
    label: title,
    class: wide ? "dialog-wide" : "",
  });
  let closing = false;
  const close = (value = "") => {
    if (closing || !dialog.isConnected) return;
    closing = true;
    dialog.returnValue = value;
    dialog.open = false;
  };
  dialog.close = close;
  const closeButton = h(
    "wa-button",
    {
      slot: "header-actions",
      appearance: "plain",
      size: "s",
      "aria-label": "关闭",
      onclick: () => close(),
    },
    icon("x", 18),
  );
  const footer = actions.length
    ? h(
        "footer.dialog-foot",
        { slot: "footer" },
        actions.map((action) => {
          const button = h(
            "wa-button",
            {
              size: "s",
              variant: action.danger
                ? "danger"
                : action.primary
                  ? "brand"
                  : "neutral",
              appearance:
                action.primary || action.danger ? "accent" : "outlined",
              dataset: action.id ? { action: action.id } : undefined,
            },
            action.label,
          );
          button.addEventListener("click", async () => {
            if (button.disabled) return;
            button.disabled = true;
            try {
              if (action.run && (await action.run()) === false) {
                button.disabled = false;
                return;
              }
            } catch {
              button.disabled = false;
              return;
            }
            close(action.value ?? action.id ?? "ok");
          });
          return button;
        }),
      )
    : null;
  const bodyNode = h("div.dialog-body", { tabindex: "-1" }, body);
  append(dialog, [closeButton, bodyNode, footer]);
  dialog.addEventListener("wa-after-hide", (event) => {
    if (event.target !== dialog) return;
    dialog.remove();
    onClose?.(dialog.returnValue || "");
  });
  dialog.addEventListener("wa-after-show", (event) => {
    if (event.target !== dialog) return;
    (initialFocus?.() || bodyNode)?.focus({ preventScroll: true });
  });
  document.body.append(dialog);
  dialog.updateComplete.then(() => {
    if (dialog.isConnected && !closing) dialog.open = true;
  });
  return { dialog, close };
}

// confirmAction resolves true only on an explicit confirmation.
export function confirmAction({
  title,
  message,
  confirmLabel = "确认",
  danger = false,
  detail,
}) {
  return new Promise((resolve) => {
    let confirmed = false;
    openDialog({
      title,
      body: [
        h("p.dialog-text", message),
        detail ? h("p.dialog-detail", detail) : null,
      ],
      actions: [
        { id: "cancel", label: "取消" },
        {
          id: "confirm",
          label: confirmLabel,
          primary: !danger,
          danger,
          run: () => {
            confirmed = true;
          },
        },
      ],
      onClose: () => resolve(confirmed),
    });
  });
}

export function setBusy(button, busy, label) {
  if (!button) return;
  button.disabled = busy;
  button.classList.toggle("busy", busy);
  if (label !== undefined) clear(button, label);
}
