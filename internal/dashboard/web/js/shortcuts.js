// Keyboard-first triage. Single keys act on the current bookmark, "g" starts a
// two-key jump to a view, and "?" lists everything. Keys never fire while the
// user is typing, and modified keys are left to the browser.
import { h, isEditable } from "./dom.js";
import { isMenuOpen } from "./ui.js";
import { openDialog } from "./ui.js";

// Keys are listed as tokens; "/", "+" and "然后" between keys are separators
// meaning "or", "together" and "then".
export const SHORTCUTS = Object.freeze([
  { group: "浏览", items: [
    [["J", "/", "↓"], "下一条"], [["K", "/", "↑"], "上一条"], [["Enter"], "打开 / 聚焦阅读区"],
    [["Space"], "向下翻阅正文"], [["/"], "搜索"], [["Esc"], "返回 / 取消选择"], [["F"], "专注阅读"]
  ] },
  { group: "整理", items: [
    [["1"], "收件箱"], [["2"], "精选"], [["3"], "已编入笔记"], [["4"], "搁置"],
    [["R"], "写收藏原因"], [["A"], "确认 AI 标签"], [["T"], "编辑标签"], [["Z"], "撤销上一次状态修改"]
  ] },
  { group: "批量与其他", items: [
    [["X"], "选中 / 取消选中"], [["Shift", "+", "点击"], "连续多选"], [["V"], "打开原帖"], [["E"], "导出当前或所选"]
  ] },
  { group: "跳转", items: [
    [["G", "然后", "I"], "收件箱"], [["G", "然后", "S"], "精选"], [["G", "然后", "C"], "已编入笔记"], [["G", "然后", "D"], "搁置"],
    [["G", "然后", "A"], "全部收藏"], [["G", "然后", "U"], "待确认分类"], [["G", "然后", "B"], "服务状态"]
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
    if (document.querySelector("dialog[open]") || isMenuOpen()) return;
    const key = event.key;
    const target = event.target;

    if (key === "Escape") {
      if (isEditable(target)) return;
      if (actions.escape()) event.preventDefault();
      return;
    }
    if (isEditable(target)) return;

    if (pendingG) {
      clearTimeout(pendingG);
      pendingG = 0;
      const view = { i: "inbox", s: "kept", c: "compiled", d: "drop", a: "all", u: "uncertain" }[key.toLowerCase()];
      if (view) { event.preventDefault(); actions.view(view); return; }
      if (key.toLowerCase() === "b") { event.preventDefault(); actions.backstage(); return; }
    }

    // Space scrolls the reading pane, unless a control has focus and would be
    // activated by it.
    if (key === " ") {
      if (target instanceof Element && target.closest("button, summary, [role='button'], a[href]:not(.row-main)")) return;
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
        if (inList || onBody) { event.preventDefault(); actions.open(); }
        return;
      case "/": event.preventDefault(); actions.search(); return;
      case "?": event.preventDefault(); showHelp(); return;
      case "1": case "2": case "3": case "4":
        event.preventDefault();
        actions.status(["inbox", "kept", "compiled", "drop"][Number(key) - 1]);
        return;
      case "r": case "R": event.preventDefault(); actions.why(); return;
      case "a": case "A": event.preventDefault(); actions.confirm(); return;
      case "t": case "T": event.preventDefault(); actions.editTags(); return;
      case "z": case "Z": case "u": case "U": event.preventDefault(); actions.undo(); return;
      case "x": case "X": event.preventDefault(); actions.check(); return;
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
