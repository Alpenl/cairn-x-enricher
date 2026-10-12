// Keyboard-first reading. Single keys act on the open bookmark, "g" starts a
// two-key jump, and "?" lists everything. Keys never fire while the
// user is typing, and modified keys are left to the browser.
import { h, isEditable } from "./dom.js";
import { isMenuOpen } from "./ui.js";
import { openDialog } from "./ui.js";

// Keys are listed as tokens; "/", "+" and "然后" between keys are separators
// meaning "or", "together" and "then".
export const SHORTCUTS = Object.freeze([
  { group: "浏览", items: [
    [["J", "/", "↓"], "下一条"], [["K", "/", "↑"], "上一条"], [["Enter"], "打开 / 聚焦阅读区"],
    [["Space"], "向下翻阅正文"], [["/"], "搜索与标签筛选"], [["Esc"], "返回 / 关闭"], [["F"], "专注阅读"], [["Ctrl / ⌘", "+", "K"], "搜索命令"], [["I"], "整理面板"]
  ] },
  { group: "整理", items: [
    [["R"], "写收藏原因"], [["A"], "确认 AI 标签"], [["T"], "编辑标签"], [["Z"], "撤销上一次操作"]
  ] },
  { group: "其他", items: [
    [["V"], "打开原帖"], [["E"], "导出当前收藏"], [[","], "设置"]
  ] },
  { group: "跳转", items: [
    [["G", "然后", "A"], "全部收藏"], [["G", "然后", "B"], "服务状态"]
  ] }
]);

const SEPARATORS = new Set(["/", "+", "然后"]);

let pendingG = 0;

export function showHelp() {
  const body = h("div.shortcut-grid", SHORTCUTS.map((section) => h("section.shortcut-group",
    h("h3", section.group),
    h("dl", section.items.map(([keys, label]) => [
      h("dt", keys.map((key, index) => (index > 0 && SEPARATORS.has(key) ? h("span.key-sep", key) : h("kbd", key)))),
      h("dd", label)
    ])))));
  openDialog({ title: "键盘快捷键", body, wide: true });
}

export function initShortcuts(actions) {
  document.addEventListener("keydown", (event) => {
    if (event.defaultPrevented || event.isComposing) return;
    if (event.metaKey || event.ctrlKey || event.altKey) return;
    if (document.body.classList.contains("settings-open")) return;
    if (document.querySelector("wa-dialog[open], dialog[open], #sidebar[aria-modal='true']") || isMenuOpen()) return;
    const key = event.key;
    const target = event.target;

    if (key === "Escape") {
      if (isEditable(target)) return;
      if (actions.escape()) event.preventDefault();
      return;
    }
    if (isEditable(target) || document.querySelector("#app.route-management")) return;

    if (pendingG) {
      clearTimeout(pendingG);
      pendingG = 0;
      if (key.toLowerCase() === "a") { event.preventDefault(); actions.view("all"); return; }
      if (key.toLowerCase() === "b") { event.preventDefault(); actions.backstage(); return; }
    }

    // Space scrolls the reading pane, unless a control has focus and would be
    // activated by it.
    if (key === " ") {
      if (target instanceof Element && target.closest("button, wa-button, wa-checkbox, wa-switch, wa-tab, input[type=checkbox], input[type=radio], summary, [role='button'], [role='checkbox'], a[href]:not(.row-main)")) return;
      event.preventDefault();
      actions.scrollDetail(event.shiftKey ? -1 : 1);
      return;
    }
    const inList = target instanceof Element && Boolean(target.closest("#list-pane"));
    const onBody = target === document.body || target === document.documentElement;
    switch (key) {
      case "j": case "J": event.preventDefault(); actions.step(1); return;
      case "k": case "K": event.preventDefault(); actions.step(-1); return;
      case "ArrowDown": if (inList || onBody) { event.preventDefault(); actions.step(1); } return;
      case "ArrowUp": if (inList || onBody) { event.preventDefault(); actions.step(-1); } return;
      case "Enter":
        if (target instanceof Element && target.closest("button, summary, select, [role='button'], a[href]:not(.row-main)")) return;
        if (inList || onBody) { event.preventDefault(); actions.open(); }
        return;
      case "/": event.preventDefault(); actions.search(); return;
      case "?": event.preventDefault(); showHelp(); return;
      case "r": case "R": event.preventDefault(); actions.why(); return;
      case "a": case "A": event.preventDefault(); actions.confirm(); return;
      case "t": case "T": event.preventDefault(); actions.editTags(); return;
      case "z": case "Z": case "u": case "U": event.preventDefault(); actions.undo(); return;
      case ",": if (actions.settings) { event.preventDefault(); actions.settings(); } return;
      case "v": case "V": event.preventDefault(); actions.openSource(); return;
      case "e": case "E": event.preventDefault(); actions.exportCurrent(); return;
      case "f": case "F": event.preventDefault(); actions.focus(); return;
      case "g": case "G":
        pendingG = setTimeout(() => { pendingG = 0; }, 1200);
        return;
      default:
    }
  });
}
