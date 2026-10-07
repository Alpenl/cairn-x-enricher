// Light, dark or follow-the-system theme. The choice is a per-browser
// convenience kept in localStorage; theme-boot.js applies it before first
// paint so there is no flash of the wrong theme.
import { byId } from "./dom.js";
import { openMenu } from "./ui.js";

const KEY = "cairn.theme";
const LABELS = Object.freeze({ system: "跟随系统", light: "浅色", dark: "深色" });

function stored() {
  try {
    const value = localStorage.getItem(KEY);
    return value === "light" || value === "dark" ? value : "system";
  } catch {
    return "system";
  }
}

function apply(choice) {
  const root = document.documentElement;
  if (choice === "system") root.removeAttribute("data-theme");
  else root.setAttribute("data-theme", choice);
  const button = byId("theme-button");
  if (button) button.title = `外观：${LABELS[choice]}`;
}

export function saveTheme(choice) {
  try {
    if (choice === "system") localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, choice);
  } catch {
    // Without storage the choice lasts for this page only.
  }
  apply(choice);
}

export function initTheme() {
  apply(stored());
  const button = byId("theme-button");
  button?.addEventListener("click", () => {
    const current = stored();
    openMenu(button, [
      { heading: "外观" },
      ...["system", "light", "dark"].map((choice) => ({
        label: LABELS[choice] + (choice === current ? "  ✓" : ""),
        icon: choice === "system" ? "monitor" : choice === "light" ? "sun" : "moon",
        run: () => saveTheme(choice)
      }))
    ], { align: "start" });
  });
}
