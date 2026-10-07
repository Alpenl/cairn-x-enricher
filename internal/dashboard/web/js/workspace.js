// Workspace composition. Controllers keep their caches and drafts mounted while pages change.
import { byId, clear, h, isEditable } from "./dom.js";
import { control } from "./controls.js";
import { icon } from "./icons.js";
import { state, on, emit, getItem } from "./store.js";
import { openDialog, toast } from "./ui.js";
import { fetchJSON, errorLabel } from "./api.js";
import { facetFilterCount } from "./query.js";
import { displayTitle } from "./format.js";
const pages = new Map();
let current = null,
  editor = null,
  hooks = {},
  drawer = null,
  membershipGeneration = 0,
  membershipID = 0;
const descriptions = {
  tags: "统一维护标签含义、别名与自动打标范围。人工整理与历史记录保留。",
  collections: "用少量合集组织收藏，挂上标签后可自动收录新内容。",
  organize: "对已有合集生成归属预选，默认由你审核后加入。",
  settings: "调整这台设备的阅读体验。",
};
export function workspaceOpen() {
  return Boolean(current);
}
export function closeWorkspace({ navigate = true } = {}) {
  if (!current) return;
  const previous = current;
  current = null;
  editor = null;
  previous.onClose?.();
  byId("management-page").hidden = true;
  byId("app").classList.remove("route-management");
  if (navigate && location.hash)
    history.replaceState(
      history.state,
      "",
      location.pathname + location.search,
    );
  updateNav();
  setInspectorTab(byId("inspector-tabs").active || "curation");
  emit("workspace:visibility");
}
export function restoreWorkspace(key) {
  const saved = pages.get(key);
  if (!saved) return false;
  if (current === saved) return true;
  current?.onClose?.();
  if (drawer) closeInspector();
  emit("workspace:navigate");
  current = saved;
  editor = saved.editor;
  const page = byId("management-page");
  clear(page, saved.root);
  page.hidden = false;
  byId("app").classList.add("route-management");
  if (location.hash !== `#${key}`)
    history.pushState(
      history.state,
      "",
      `${location.pathname}${location.search}#${key}`,
    );
  setInspectorTab(byId("inspector-tabs").active || "curation");
  updateNav();
  emit("workspace:visibility");
  Promise.resolve()
    .then(() => saved.activate?.())
    .catch((error) => toast(errorLabel(error.message), { tone: "error" }));
  return true;
}
export function openWorkspace({ key, title, body, onClose, split = false }) {
  if (current) current.onClose?.();
  if (drawer) closeInspector();
  emit("workspace:navigate");
  const page = byId("management-page");
  page.hidden = false;
  byId("app").classList.add("route-management");
  const content = h("div.page-content"),
    editorHost = split
      ? h(
          "section.manager-editor",
          { "aria-label": `${title}编辑` },
          h("p.manager-placeholder", "从左侧选择一项查看与编辑"),
        )
      : null;
  clear(
    page,
    h(
      "div.page-inner",
      h(
        "header.page-head",
        h(
          "div.page-head-copy",
          h("h1", title),
          h("p", descriptions[key] || ""),
        ),
      ),
      split
        ? h("div.manager-split", h("div.manager-list", content), editorHost)
        : content,
    ),
  );
  if (body) clear(content, body);
  editor = editorHost;
  const token = {
    key,
    onClose,
    content,
    editor: editorHost,
    root: page.firstElementChild,
  };
  current = token;
  if (["tags", "collections"].includes(key)) pages.set(key, token);
  if (location.hash !== `#${key}`)
    history.pushState(
      history.state,
      "",
      `${location.pathname}${location.search}#${key}`,
    );
  setInspectorTab(byId("inspector-tabs").active || "curation");
  updateNav();
  emit("workspace:visibility");
  return {
    dialog: page,
    isCurrent: () => current === token,
    onActivate: (fn) => {
      token.activate = fn;
    },
    setBody: (body) => {
      if (current === token) clear(content, body);
    },
    discard: () => {
      if (pages.get(key) === token) pages.delete(key);
    },
    close: () => {
      if (current === token) closeWorkspace();
    },
  };
}
export function openEditor({
  title,
  body,
  actions = [],
  initialFocus,
  onClose,
  ...rest
}) {
  if (!editor || !current)
    return openDialog({ title, body, actions, initialFocus, onClose, ...rest });
  const host = editor,
    page = current;
  const identity = Symbol("editor");
  page.editorIdentity = identity;
  let closed = false;
  const close = () => {
    if (closed) return;
    closed = true;
    if (page.editorIdentity === identity)
      clear(host, h("p.manager-placeholder", "选择一项继续编辑"));
    onClose?.();
  };
  const buttons = actions.map((action) => {
    const button = control(
      "button.btn",
      {
        variant: action.danger
          ? "danger"
          : action.primary
            ? "brand"
            : "neutral",
        appearance: action.primary || action.danger ? "accent" : "outlined",
      },
      action.label,
    );
    button.addEventListener("click", async () => {
      if (button.disabled) return;
      button.disabled = true;
      try {
        if (action.run && (await action.run()) === false) return;
        close();
      } catch (error) {
        toast(errorLabel(error.message), { tone: "error" });
      } finally {
        button.disabled = false;
      }
    });
    return button;
  });
  clear(
    host,
    h("h2", title),
    h("div.editor-body", body),
    h("footer.editor-actions", buttons),
  );
  if (matchMedia("(max-width:759.98px)").matches)
    host.scrollIntoView({ block: "start", behavior: "instant" });
  return { dialog: host, close };
}
function updateNav() {
  for (const [id, key] of [
    ["browse-tags", "tags"],
    ["browse-collections", "collections"],
    ["organize-button", "organize"],
    ["settings-button", "settings"],
  ]) {
    const node = byId(id);
    if (current?.key === key) node.setAttribute("aria-current", "page");
    else node.removeAttribute("aria-current");
  }
  for (const button of byId("tabbar").children) {
    const active = current
      ? button.dataset.page === current.key
      : state.route.name === "backstage"
        ? button.dataset.page === "service"
        : button.dataset.page === "library";
    active
      ? button.setAttribute("aria-current", "page")
      : button.removeAttribute("aria-current");
  }
}
function filterState() {
  const n = facetFilterCount(state.filters),
    button = byId("filter-button"),
    count = byId("filter-count");
  button.dataset.on = String(n > 0);
  count.hidden = !n;
  count.textContent = String(n);
}
function setInspectorTab(name) {
  byId("inspector-tabs").active = name;
  byId("diagnostics").open = name === "processing" && inspectorVisible();
  byId("run-history").open = name === "history" && inspectorVisible();
  byId("curate").open = name === "curation" && inspectorVisible();
  emit("inspector:visibility");
}
export function showInspector(name = "curation") {
  if (!state.selectedId) return;
  byId("library").classList.remove("insp-off");
  byId("inspector-toggle").setAttribute("aria-pressed", "true");
  if (matchMedia("(max-width:1320px)").matches && !drawer) {
    drawer = h("wa-dialog.inspector-drawer", {
      label: "整理面板",
      withoutHeader: true,
    });
    const instance = drawer;
    instance.append(byId("inspector"));
    document.body.append(instance);
    instance.addEventListener("wa-after-hide", (event) => {
      if (event.target !== instance || instance.open) return;
      byId("library").append(byId("inspector"));
      instance.remove();
      if (drawer === instance) drawer = null;
      byId("inspector-toggle").setAttribute("aria-pressed", "false");
      emit("inspector:visibility");
    });
    instance.updateComplete.then(() => {
      if (drawer === instance) {
        instance.open = true;
        // The disclosures must observe the dialog's open state. Before the
        // first reactive update the dialog is still closed, so opening a
        // disclosure earlier would leave the drawer's content collapsed.
        setInspectorTab(name);
      }
    });
  } else if (drawer) drawer.open = true;
  setInspectorTab(name);
  void memberships();
}
export function closeInspector() {
  if (drawer) drawer.open = false;
  else byId("library").classList.add("insp-off");
  byId("curate").open = false;
  byId("inspector-toggle").setAttribute("aria-pressed", "false");
  byId("diagnostics").open = false;
  byId("run-history").open = false;
  emit("inspector:visibility");
}
function inspectorVisible() {
  return (
    !current &&
    state.route.name !== "backstage" &&
    Boolean(
      drawer?.open ||
        (matchMedia("(min-width:1321px)").matches &&
          !byId("library").classList.contains("insp-off")),
    )
  );
}
async function memberships(force = false) {
  const id = state.selectedId;
  if (!id || !inspectorVisible() || (id === membershipID && !force)) return;
  membershipID = id;
  const generation = ++membershipGeneration,
    host = byId("inspector-memberships");
  clear(host, h("p.collection-hint", "正在读取合集…"));
  try {
    const data = await fetchJSON(`/api/collections?link_ids=${id}`);
    if (generation !== membershipGeneration || id !== state.selectedId) return;
    const selected = data.items.filter(
      (c) => c.selected_count > 0 && !c.deleted,
    );
    clear(
      host,
      selected.length
        ? selected.map((c) =>
            h(
              "button.collection-membership",
              { type: "button", onclick: () => hooks.collection(c.id) },
              icon("folder", 12),
              c.name,
            ),
          )
        : h("p.collection-hint", "还没有加入合集"),
    );
  } catch {
    if (generation === membershipGeneration) {
      membershipID = 0;
      clear(
        host,
        h(
          "button.link-btn",
          { onclick: () => memberships(true) },
          "合集暂时无法读取，重试",
        ),
      );
    }
  }
}
async function navigate(key) {
  try {
    if (key === "tags") {
      const { browseTags } = await import("./tag-manager.js");
      await browseTags();
    } else if (key === "collections") {
      const { browse } = await import("./collections.js");
      await browse();
    } else if (key === "organize") {
      const { organize } = await import("./collections.js");
      await organize();
    } else if (key === "settings") settings();
    else {
      closeWorkspace();
      if (key === "service") hooks.service();
      else hooks.library();
    }
  } catch (error) {
    toast(errorLabel(error.message), { tone: "error" });
  }
}
function settings() {
  const theme = control(
    "select",
    { "aria-label": "外观" },
    ...[
      ["system", "跟随系统"],
      ["light", "浅色"],
      ["dark", "深色"],
    ].map(([value, label]) => control("option", { value }, label)),
  );
  theme.value = localStorage.getItem("cairn.theme") || "system";
  theme.addEventListener("change", () => {
    hooks.theme(theme.value);
  });
  const size = control(
    "select",
    { "aria-label": "正文字号" },
    ...[16, 17, 18, 20, 22].map((value) =>
      control("option", { value: String(value) }, `${value} px`),
    ),
  );
  size.value = String(readSize());
  size.addEventListener("change", () => {
    localStorage.setItem("cairn.reader.font-size", size.value);
    document.documentElement.style.setProperty(
      "--reader-font-size",
      `${size.value}px`,
    );
  });
  const card = (title, ...body) =>
    h("section.settings-card", h("h2", title), body);
  openWorkspace({
    key: "settings",
    title: "设置与快捷键",
    body: [
      card(
        "阅读与外观",
        h(
          "label.settings-row",
          h("span", "外观", h("small", "选择浅色、深色或跟随设备")),
          theme,
        ),
        h(
          "label.settings-row",
          h("span", "正文字号", h("small", "仅影响这台设备的阅读字号")),
          size,
        ),
      ),
      card(
        "离线与导出",
        h(
          "p.collection-hint",
          "阅读过的文章会保存文字副本。媒体使用现有归档与图片缓存策略。",
        ),
        h(
          "div.collection-tools",
          control("button", { onclick: hooks.offline }, "下载离线阅读文件"),
          control("button", { onclick: hooks.export }, "导出当前结果"),
          control("button", { onclick: hooks.forget }, "清除本机离线副本"),
        ),
      ),
      card(
        "键盘操作",
        h(
          "p.collection-hint",
          "⌘ / Ctrl K 打开命令搜索；J / K 浏览；1–4 整理；I 打开整理面板。",
        ),
        control("button", { onclick: hooks.help }, "查看全部快捷键"),
      ),
    ],
  });
}
function readSize() {
  try {
    const n = Number(localStorage.getItem("cairn.reader.font-size"));
    return [16, 17, 18, 20, 22].includes(n) ? n : 17;
  } catch {
    return 17;
  }
}
function commandPalette() {
  const query = control("input", {
      type: "search",
      placeholder: "查找收藏、合集或执行操作",
      "aria-label": "搜索命令",
    }),
    rows = h("div.command-list");
  let dialog;
  const commands = [
    [
      "搜索全部收藏",
      "search",
      () => {
        closeWorkspace();
        hooks.library();
        byId("search").focus();
      },
    ],
    ["标签库", "tag", () => navigate("tags")],
    ["管理全部合集", "folder", () => navigate("collections")],
    ["AI 整理", "sparkles", () => navigate("organize")],
    ["服务状态", "activity", () => navigate("service")],
    ["设置与快捷键", "keyboard", () => navigate("settings")],
  ];
  const render = () => {
    const q = query.value.trim().toLowerCase();
    const choices = [
      ...commands.map(([label, ic, run]) => ({ label, ic, run })),
      ...state.order.slice(0, 100).map((id) => ({
        label: displayTitle(getItem(id)).text,
        ic: "book",
        run: () => {
          closeWorkspace();
          emit("select-request", id);
        },
      })),
    ]
      .filter((c) => c.label.toLowerCase().includes(q))
      .slice(0, 20);
    clear(
      rows,
      choices.length
        ? choices.map((c) =>
            h(
              "button.command-option",
              {
                onclick: () => {
                  dialog.close();
                  c.run();
                },
              },
              icon(c.ic, 16),
              h("span", c.label),
            ),
          )
        : h(
            "p.collection-hint",
            "没有匹配的命令或已加载收藏。可使用列表搜索查找全文。",
          ),
    );
  };
  query.addEventListener("input", render);
  query.addEventListener("keydown", (event) => {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      rows.querySelector("button")?.focus();
    }
    if (event.key === "Enter" && !event.isComposing) {
      event.preventDefault();
      rows.querySelector("button")?.click();
    }
  });
  dialog = openDialog({
    title: "搜索或执行命令",
    body: [query, rows],
    initialFocus: () => query,
  });
  render();
}
export function initWorkspace(options) {
  hooks = options;
  document.documentElement.style.setProperty(
    "--reader-font-size",
    `${readSize()}px`,
  );
  byId("inspector-toggle").addEventListener("click", () =>
    inspectorVisible() ? closeInspector() : showInspector(),
  );
  byId("inspector-close").addEventListener("click", closeInspector);
  byId("inspector-tabs").addEventListener("wa-tab-show", (event) =>
    setInspectorTab(event.detail.name),
  );
  byId("inspector-add-collection").addEventListener("click", () =>
    hooks.pick(),
  );
  byId("organize-button").addEventListener("click", () => navigate("organize"));
  byId("settings-button").addEventListener("click", settings);
  byId("command-button").addEventListener("click", commandPalette);
  const filters = byId("filter-panel"),
    filter = byId("filter-button");
  byId("filter-done").addEventListener("click", () => {
    filters.open = false;
  });
  for (const event of ["wa-after-show", "wa-after-hide"])
    filters.addEventListener(event, () => {
      filter.setAttribute("aria-expanded", String(filters.open));
      emit("filter:visibility");
    });
  for (const [key, label, ic] of [
    ["library", "阅读", "inbox"],
    ["collections", "合集", "folder"],
    ["tags", "标签库", "tag"],
    ["organize", "AI 整理", "sparkles"],
    ["service", "服务", "activity"],
  ])
    byId("tabbar").append(
      h(
        "button",
        {
          type: "button",
          dataset: { page: key },
          onclick: () => navigate(key),
        },
        icon(ic, 20),
        h("span", label),
      ),
    );
  document.addEventListener("keydown", (event) => {
    if (event.defaultPrevented || event.isComposing) return;
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
      event.preventDefault();
      commandPalette();
      return;
    }
    if (
      !event.metaKey &&
      !event.ctrlKey &&
      !event.altKey &&
      event.key.toLowerCase() === "i" &&
      !isEditable(event.target) &&
      !document.querySelector("wa-dialog[open]")
    ) {
      event.preventDefault();
      inspectorVisible() ? closeInspector() : showInspector();
    }
  });
  on("inspector:open", showInspector);
  on("selection", () => {
    setInspectorTab(byId("inspector-tabs").active || "curation");
    void memberships();
    updateNav();
  });
  on("collections:changed", () => void memberships(true));
  on("list:loaded", () => {
    filterState();
    updateNav();
  });
  on("inspector:visibility", () => {
    if (inspectorVisible()) void memberships();
  });
  const hashRoute = () => {
    const key = location.hash.slice(1);
    if (["tags", "collections", "organize", "settings"].includes(key)) {
      if (current?.key !== key) void navigate(key);
    } else closeWorkspace({ navigate: false });
  };
  matchMedia("(min-width:1321px)").addEventListener("change", () => {
    if (drawer) drawer.open = false;
    setInspectorTab(byId("inspector-tabs").active || "curation");
  });
  window.addEventListener("popstate", hashRoute);
  window.addEventListener("hashchange", hashRoute);
  hashRoute();
  updateNav();
  filterState();
}
