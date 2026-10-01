// Compact navigation is a modal surface. Keep native controls usable and do
// not leave off-screen navigation in the keyboard order after it closes.
export function createSidebarModal(app) {
  const panel = app.querySelector("#sidebar");
  const backdrop = app.querySelector("#sidebar-backdrop");
  let opened = false;
  let previousFocus = null;
  let background = [];
  const focusable = () => [...panel.querySelectorAll("a[href], button, input, select, summary, [tabindex]")]
    .filter((node) => !node.disabled && node.tabIndex >= 0 && !node.closest("[inert]") && node.getClientRects().length);

  function setOpen(requested, { compact = true } = {}) {
    const next = Boolean(requested && compact);
    if (next && !opened) {
      previousFocus = document.activeElement;
      background = [...app.children].filter((node) => node !== panel && node !== backdrop)
        .map((node) => [node, node.inert]);
      for (const [node] of background) node.inert = true;
    } else if (!next && opened) {
      for (const [node, wasInert] of background) node.inert = wasInert;
      background = [];
    }
    const wasOpen = opened;
    opened = next;
    app.classList.toggle("sidebar-open", next);
    backdrop.hidden = !next;
    panel.inert = compact && !next;
    if (next) {
      panel.setAttribute("role", "dialog");
      panel.setAttribute("aria-modal", "true");
    } else {
      panel.removeAttribute("role");
      panel.removeAttribute("aria-modal");
    }
    for (const button of app.querySelectorAll("[data-open-sidebar]")) {
      button.setAttribute("aria-expanded", String(next));
      button.setAttribute("aria-controls", "sidebar");
    }
    if (next && !wasOpen) focusable()[0]?.focus({ preventScroll: true });
    if (!next && wasOpen) {
      if (previousFocus?.isConnected && previousFocus.getClientRects().length) previousFocus.focus({ preventScroll: true });
      previousFocus = null;
    }
  }

  panel.addEventListener("keydown", (event) => {
    if (!opened || event.defaultPrevented || event.isComposing) return;
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      setOpen(false);
    } else if (event.key === "Tab") {
      const nodes = focusable();
      const first = nodes[0];
      const last = nodes.at(-1);
      if (!first) { event.preventDefault(); return; }
      if (event.shiftKey && (document.activeElement === first || !nodes.includes(document.activeElement))) {
        event.preventDefault(); last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault(); first.focus();
      }
    }
  });
  return setOpen;
}
