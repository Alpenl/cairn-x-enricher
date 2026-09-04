(() => {
  "use strict";

  const ui = window.CairnUI;
  const match = window.location.pathname.match(/^\/bookmarks\/([1-9][0-9]*)$/);
  const bookmarkID = match ? Number(match[1]) : 0;
  const POLL_INTERVAL = 8000;
  let current = null;
  let loading = false;

  function paragraphs(container, text) {
    container.replaceChildren();
    for (const block of String(text).split(/\n+/)) {
      const line = block.trim();
      if (line) container.append(ui.element("p", "", line));
    }
  }

  function renderFigures(item) {
    const holder = ui.byId("read-figures");
    holder.replaceChildren();
    const images = Array.isArray(item.images) ? item.images : [];
    images.forEach((imageRef, index) => {
      const figure = ui.element("div", "read-figure");
      const image = document.createElement("img");
      image.src = ui.imagePath(imageRef.key);
      image.alt = "";
      image.loading = index === 0 ? "eager" : "lazy";
      image.decoding = "async";
      image.addEventListener("error", () => figure.remove());
      figure.append(image);
      holder.append(figure);
    });
  }

  function renderLinks(item) {
    const holder = ui.byId("read-links");
    holder.replaceChildren();
    for (const value of Array.isArray(item.related_links) ? item.related_links : []) {
      const link = ui.element("a", "", value);
      link.href = value;
      link.target = "_blank";
      link.rel = "noopener noreferrer";
      holder.append(link);
    }
  }

  function render(item) {
    current = item;
    ui.byId("read-loading").hidden = true;
    ui.byId("read-error").hidden = true;
    ui.byId("read-article").hidden = false;

    const title = ui.displayTitle(item);
    const heading = ui.byId("read-title");
    heading.textContent = title.text;
    heading.classList.toggle("raw", title.raw);
    document.title = `${title.raw ? "阅读" : item.ai_title} · Cairn 收藏`;

    ui.byId("read-date").textContent = ui.formatDate(item.created_at);

    const note = ui.byId("read-note");
    note.hidden = !item.note;
    note.textContent = item.note || "";

    const summary = ui.displaySummary(item);
    const lede = ui.byId("read-lede");
    lede.textContent = summary.text;
    lede.style.fontStyle = summary.wait ? "italic" : "normal";

    renderFigures(item);

    const body = ui.byId("read-body");
    if (item.translated_text) {
      paragraphs(body, item.translated_text);
    } else if (item.error) {
      paragraphs(body, item.error);
    } else {
      body.replaceChildren();
    }

    const originalBlock = ui.byId("read-original-block");
    originalBlock.hidden = !item.original_text;
    if (item.original_text) paragraphs(ui.byId("read-original"), item.original_text);

    renderLinks(item);

    const source = ui.byId("read-source");
    source.href = item.url;

    const process = ui.byId("read-process");
    const processable = item.processable !== false && item.status !== "unsupported";
    process.hidden = !processable;
    process.disabled = item.status === "processing" || loading;
    if (item.status === "processing") process.textContent = "处理中";
    else process.textContent = item.translated_text ? "重新处理" : "立即处理";
  }

  async function loadNeighbour() {
    try {
      const page = await ui.fetchJSON(`/api/bookmarks?limit=1&before_id=${bookmarkID}`);
      const next = Array.isArray(page.items) ? page.items[0] : null;
      if (!next) return;
      const link = ui.byId("read-next");
      link.href = `/bookmarks/${next.id}`;
      ui.byId("read-next-title").textContent = ui.displayTitle(next).text;
      link.hidden = false;
    } catch (_) {
      // 相邻收藏只是便利入口，读取失败时保持隐藏。
    }
  }

  async function loadBookmark(silent = false) {
    if (!bookmarkID || loading) return;
    loading = true;
    if (!silent) ui.byId("read-loading").hidden = false;
    try {
      render(await ui.fetchJSON(`/api/bookmarks/${bookmarkID}`));
    } catch (_) {
      if (silent) {
        ui.showToast("刷新内容失败", true);
      } else {
        ui.byId("read-loading").hidden = true;
        ui.byId("read-error").hidden = false;
      }
    } finally {
      loading = false;
    }
  }

  async function processCurrent() {
    if (!current) return;
    if (current.translated_text && !window.confirm("重新处理会再次调用模型并覆盖现有内容。确认继续？")) {
      return;
    }
    const button = ui.byId("read-process");
    button.disabled = true;
    try {
      const result = await ui.submitProcessing([current.id]);
      if (result.accepted.length > 0) {
        ui.showToast("已提交处理请求");
        current.status = "processing";
        render(current);
        setTimeout(() => loadBookmark(true), 900);
      } else {
        const code = result.rejected[0]?.error;
        ui.showToast(ui.errorLabels[code] || "处理请求未被接受", true);
        button.disabled = false;
      }
    } catch (_) {
      ui.showToast("提交处理请求失败", true);
      button.disabled = false;
    }
  }

  ui.byId("read-process").addEventListener("click", processCurrent);
  ui.byId("read-toggle").addEventListener("click", (event) => {
    const panel = ui.byId("read-original");
    const open = panel.hidden;
    panel.hidden = !open;
    event.currentTarget.classList.toggle("open", open);
    event.currentTarget.setAttribute("aria-expanded", String(open));
    ui.byId("read-toggle-label").textContent = open ? "收起原文" : "展开原文";
  });

  if (bookmarkID) {
    loadBookmark();
    loadNeighbour();
  } else {
    ui.byId("read-loading").hidden = true;
    ui.byId("read-error").hidden = false;
  }

  setInterval(() => {
    if (document.hidden) return;
    if (current?.status === "processing" || current?.status === "pending") loadBookmark(true);
  }, POLL_INTERVAL);
})();
