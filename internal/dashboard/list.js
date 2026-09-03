(() => {
  "use strict";

  const ui = window.CairnUI;
  const state = {
    items: [],
    selected: new Set(),
    status: "all",
    search: "",
    nextBeforeId: null,
    loading: false
  };

  function updateCounts(counts) {
    ui.byId("count-total").textContent = counts.total ?? 0;
    ui.byId("count-pending").textContent = counts.pending ?? 0;
    ui.byId("count-processing").textContent = counts.processing ?? 0;
    ui.byId("count-completed").textContent = counts.completed ?? 0;
    ui.byId("count-failed").textContent = counts.failed ?? 0;
    ui.byId("count-exhausted").textContent = counts.exhausted ?? 0;
    ui.byId("count-unsupported").textContent = counts.unsupported ?? 0;
  }

  function isProcessable(item) {
    return item.processable !== false && item.status !== "unsupported";
  }

  function syncSelectionControls() {
    const processableIDs = state.items.filter(isProcessable).map((item) => item.id);
    const visibleSet = new Set(processableIDs);
    for (const id of state.selected) {
      if (!visibleSet.has(id)) state.selected.delete(id);
    }
    const selectedVisible = processableIDs.filter((id) => state.selected.has(id)).length;
    const selectableCount = Math.min(processableIDs.length, 10);
    const allSelected = selectableCount > 0 && selectedVisible === selectableCount;
    ui.byId("select-all").checked = allSelected;
    ui.byId("select-all").indeterminate = selectedVisible > 0 && !allSelected;
    ui.byId("select-all").disabled = processableIDs.length === 0 || state.loading;
    ui.byId("process-selected").disabled = state.selected.size === 0 || state.loading;
    ui.byId("process-selected").textContent = `处理选中（${state.selected.size}）`;
  }

  function summaryText(item) {
    if (item.summary) return item.summary;
    if (item.status === "unsupported") return "此收藏不是 X 链接，不参与自动处理。";
    if (item.status === "processing") return "正在生成双语正文、摘要和标题。";
    if (item.status === "completed") return "这条内容由旧版本处理，可重新处理以补齐新版字段。";
    return "尚未生成 AI 内容。";
  }

  function addMedia(card, item) {
    if (!Array.isArray(item.images) || item.images.length === 0) {
      card.classList.add("no-image");
      return;
    }
    const media = ui.element("div", "card-media");
    const image = document.createElement("img");
    image.src = ui.imagePath(item.images[0].key);
    image.alt = `${item.ai_title || "收藏内容"} 图片 1`;
    image.loading = "lazy";
    image.decoding = "async";
    image.addEventListener("error", () => {
      media.remove();
      card.classList.add("no-image");
    });
    media.append(image);
    if (item.images.length > 1) {
      media.append(ui.element("span", "image-count", `${item.images.length} 图`));
    }
    card.append(media);
  }

  function createCard(item) {
    const processable = isProcessable(item);
    const selected = state.selected.has(item.id);
    const card = ui.element("article", `bookmark-card${selected ? " selected" : ""}${processable ? "" : " unsupported"}`);
    card.dataset.id = String(item.id);

    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.disabled = !processable;
    checkbox.checked = selected;
    checkbox.setAttribute("aria-label", processable ? `选择收藏 ${item.id}` : `收藏 ${item.id} 不参与 X 处理`);
    checkbox.addEventListener("change", () => {
      if (checkbox.checked && state.selected.size >= 10) {
        checkbox.checked = false;
        ui.showToast("每次最多选择 10 条收藏", true);
        return;
      }
      if (checkbox.checked) state.selected.add(item.id);
      else state.selected.delete(item.id);
      card.classList.toggle("selected", checkbox.checked);
      syncSelectionControls();
    });
    card.append(checkbox);
    addMedia(card, item);

    const body = ui.element("div", "card-body");
    const meta = ui.element("div", "card-meta");
    meta.append(ui.element("span", `badge ${item.status}`, ui.statusLabels[item.status] || item.status));
    meta.append(ui.element("span", "meta", `#${item.id}`));
    meta.append(ui.element("span", "meta", ui.formatDate(item.created_at)));
    if (item.attempts > 0) meta.append(ui.element("span", "meta", `尝试 ${item.attempts} 次`));
    body.append(meta);

    const title = item.ai_title || (processable ? "AI 标题待生成" : "非 X 收藏");
    const heading = ui.element("h2", `card-title${item.ai_title ? "" : " placeholder"}`);
    const readerLink = ui.element("a", "", title);
    readerLink.href = `/bookmarks/${item.id}`;
    heading.append(readerLink);
    body.append(heading);

    if (item.note) {
      const note = ui.element("p", "manual-note");
      note.append(ui.element("span", "", "手动备注"));
      note.append(document.createTextNode(item.note));
      body.append(note);
    }

    body.append(ui.element("p", `summary-preview${item.summary ? "" : " empty"}`, summaryText(item)));
    const source = ui.element("a", "source-url", ui.shortURL(item.url));
    source.href = item.url;
    source.target = "_blank";
    source.rel = "noopener noreferrer";
    source.title = item.url;
    body.append(source);
    if (item.error) body.append(ui.element("p", "error", item.error));
    if (item.status === "failed" && item.next_retry_at) {
      body.append(ui.element("p", "meta", `下次重试：${ui.formatDate(item.next_retry_at)}`));
    }
    card.append(body);

    const actions = ui.element("div", "card-actions");
    const read = ui.element("a", "button secondary compact", "阅读");
    read.href = `/bookmarks/${item.id}`;
    actions.append(read);
    if (processable) {
      const process = ui.element("button", "button compact", item.status === "completed" ? "重新处理" : "立即处理");
      process.type = "button";
      process.disabled = item.status === "processing";
      if (item.status === "processing") process.textContent = "处理中";
      process.addEventListener("click", () => processIDs([item.id]));
      actions.append(process);
    }
    card.append(actions);
    return card;
  }

  function renderItems() {
    const list = ui.byId("bookmark-list");
    list.replaceChildren(...state.items.map(createCard));
    ui.byId("empty").hidden = state.items.length > 0 || state.loading;
    syncSelectionControls();
  }

  async function loadBookmarks(reset = true, silent = false) {
    if (state.loading) return;
    state.loading = true;
    if (!silent) ui.byId("loading").hidden = false;
    ui.byId("refresh").disabled = true;
    syncSelectionControls();
    try {
      const params = new URLSearchParams({ limit: "20" });
      if (state.status !== "all") params.set("status", state.status);
      if (state.search) params.set("q", state.search);
      if (!reset && state.nextBeforeId) params.set("before_id", String(state.nextBeforeId));
      const response = await fetch(`/api/bookmarks?${params}`, { cache: "no-store" });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const page = await response.json();
      state.items = reset ? (page.items || []) : state.items.concat(page.items || []);
      state.nextBeforeId = page.next_before_id;
      updateCounts(page.counts || {});
      ui.byId("load-more").hidden = !state.nextBeforeId;
      ui.byId("updated").textContent = `更新于 ${new Date().toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", second: "2-digit" })}`;
      renderItems();
    } catch (_) {
      if (!silent) ui.showToast("读取收藏失败，请检查 Cloudflare 后端", true);
    } finally {
      state.loading = false;
      ui.byId("loading").hidden = true;
      ui.byId("refresh").disabled = false;
      syncSelectionControls();
    }
  }

  async function processIDs(ids) {
    const chosen = state.items.filter((item) => ids.includes(item.id));
    if (chosen.some((item) => item.status === "completed") && !window.confirm("选中内容包含已完成项目，继续会重新调用模型。确认处理？")) {
      return;
    }
    try {
      const result = await ui.submitProcessing(ids);
      for (const id of result.accepted) state.selected.delete(id);
      if (result.accepted.length) ui.showToast(`已提交 ${result.accepted.length} 条处理请求`);
      if (result.rejected.length) {
        const reasons = result.rejected.map((item) => ui.errorLabels[item.error] || "请求失败");
        ui.showToast(`${result.rejected.length} 条未提交：${[...new Set(reasons)].join("、")}`, true);
      }
      await loadBookmarks(true, true);
    } catch (_) {
      ui.showToast("提交处理请求失败", true);
    }
  }

  ui.byId("search-form").addEventListener("submit", (event) => {
    event.preventDefault();
    state.search = ui.byId("search-input").value.trim();
    ui.byId("clear-search").hidden = !state.search;
    state.selected.clear();
    loadBookmarks(true);
  });
  ui.byId("clear-search").addEventListener("click", () => {
    ui.byId("search-input").value = "";
    state.search = "";
    ui.byId("clear-search").hidden = true;
    state.selected.clear();
    loadBookmarks(true);
  });
  ui.byId("refresh").addEventListener("click", () => loadBookmarks(true));
  ui.byId("load-more").addEventListener("click", () => loadBookmarks(false));
  ui.byId("process-selected").addEventListener("click", () => processIDs([...state.selected]));
  ui.byId("select-all").addEventListener("change", (event) => {
    if (event.target.checked) {
      for (const item of state.items) {
        if (state.selected.size >= 10) break;
        if (isProcessable(item)) state.selected.add(item.id);
      }
    } else {
      state.selected.clear();
    }
    renderItems();
  });
  ui.byId("status-tabs").addEventListener("click", (event) => {
    const tab = event.target.closest("[data-status]");
    if (!tab || tab.dataset.status === state.status) return;
    state.status = tab.dataset.status;
    state.selected.clear();
    for (const node of document.querySelectorAll("[data-status]")) {
      const active = node === tab;
      node.classList.toggle("active", active);
      node.setAttribute("aria-selected", String(active));
    }
    loadBookmarks(true);
  });

  ui.refreshServiceStatus();
  loadBookmarks(true);
  setInterval(() => {
    if (document.hidden) return;
    ui.refreshServiceStatus();
    if (state.items.some((item) => item.status === "processing")) loadBookmarks(true, true);
  }, 5000);
})();
