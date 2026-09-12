(() => {
  "use strict";

  const ui = window.CairnUI;
  const match = window.location.pathname.match(/^\/bookmarks\/([1-9][0-9]*)$/);
  const bookmarkID = match ? Number(match[1]) : 0;
  const POLL_INTERVAL = 8000;
  let current = null;
  let loading = false;
  let catalog = null;
  let dirty = false;
  let saving = false;
  const renderedText = new WeakMap();
  ui.byId("read-back").href = `/${window.location.search}`;

  function markDirty(value = true) {
    dirty = value;
    ui.byId("curation-dirty").hidden = !dirty;
  }

  function selection() {
    return {
      topics: [...ui.byId("curation-topics").querySelectorAll("input:checked")].map((input) => input.value),
      form: ui.byId("curation-form-value").value,
      use: ui.byId("curation-use").value
    };
  }

  function updateTopicCount() {
    const inputs = [...ui.byId("curation-topics").querySelectorAll("input")];
    const count = inputs.filter((input) => input.checked).length;
    ui.byId("topic-count").textContent = `${count}/3`;
    for (const input of inputs) input.disabled = !input.checked && (count >= 3 || input.dataset.inactive === "true");
  }

  function renderCuration(item) {
    ui.byId("read-meta").replaceChildren(ui.metadata(item));
    const why = ui.byId("read-why");
    why.textContent = item.why || "";
    why.hidden = !item.why;
    const entities = item.classification?.entities || [];
    ui.byId("read-entities").textContent = entities.join(" / ");
    ui.byId("read-entities").hidden = entities.length === 0;
    if (!catalog || dirty || saving) return;
    ui.byId("curation-fields").disabled = false;
    ui.byId("curation-why").value = item.why || "";
    ui.byId("curation-status").value = item.curation_status || "inbox";
    const classification = item.classification || {};
    ui.byId("why-suggestion").textContent = classification.why_suggestion || "";
    ui.byId("why-suggestion-block").hidden = !classification.why_suggestion;
    ui.byId("reset-classification").hidden = !item.classification_reviewed;
    const topics = ui.byId("curation-topics");
    topics.replaceChildren();
    for (const term of catalog.topics) {
      if (!term.active && !classification.topics?.includes(term.id)) continue;
      const label = ui.element("label", "topic-option");
      const input = document.createElement("input");
      input.type = "checkbox";
      input.value = term.id;
      input.checked = classification.topics?.includes(term.id) || false;
      input.dataset.inactive = String(!term.active);
      label.append(input, document.createTextNode(term.label + (term.active ? "" : "（停用）")));
      topics.append(label);
    }
    for (const [id, dimension, value] of [["curation-form-value", "forms", classification.form], ["curation-use", "uses", classification.use]]) {
      const control = ui.byId(id);
      ui.fillTerms(control, catalog[dimension].filter((term) => term.active || term.id === value), "未指定", true);
      control.value = value || "";
    }
    updateTopicCount();
  }

  async function loadTaxonomy() {
    try {
      catalog = await ui.loadTaxonomy();
      ui.byId("curation-error").hidden = true;
      if (current) renderCuration(current);
    } catch (_) {
      ui.byId("curation-error").hidden = false;
    }
  }

  async function saveCuration(reset = false) {
    if (!current || saving || !catalog) return;
    const whyDraft = ui.byId("curation-why").value;
    const statusDraft = ui.byId("curation-status").value;
    const update = reset ? { classification: null } : { why: whyDraft, curation_status: statusDraft };
    if (!reset) {
      const before = current.classification || { topics: [], form: "", use: "" };
      const chosen = selection();
      const sameTopics = JSON.stringify([...chosen.topics].sort()) === JSON.stringify([...(before.topics || [])].sort());
      if (!current.classification_reviewed || !sameTopics || chosen.form !== before.form || chosen.use !== before.use) {
        update.classification = chosen;
      }
    }
    saving = true;
    ui.byId("curation-fields").disabled = true;
    try {
      const item = await ui.fetchJSON(`/api/bookmarks/${bookmarkID}/curation`, {
        method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify(update)
      });
      saving = false;
      markDirty(false);
      render(item);
      if (reset && (whyDraft !== (item.why || "") || statusDraft !== item.curation_status)) {
        ui.byId("curation-why").value = whyDraft;
        ui.byId("curation-status").value = statusDraft;
        markDirty();
      }
      ui.showToast(reset ? "已恢复自动分类" : "已保存整理");
    } catch (error) {
      ui.showToast(ui.errorLabel(error.message), true);
    } finally {
      saving = false;
      ui.byId("curation-fields").disabled = false;
    }
  }

  function paragraphs(container, text) {
    if (renderedText.get(container) === text) return;
    renderedText.set(container, text);
    container.replaceChildren();
    const fragment = document.createDocumentFragment();
    for (const block of String(text).split(/\n+/)) {
      const line = block.trim();
      if (line) fragment.append(ui.element("p", "", line));
    }
    container.append(fragment);
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
      if (image.complete) image.classList.add("ready");
      image.addEventListener("load", () => image.classList.add("ready"));
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
    const previous = current;
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
    renderCuration(item);
    ui.byId("read-export").disabled = false;

    const summary = ui.displaySummary(item);
    const lede = ui.byId("read-lede");
    lede.textContent = summary.text;
    lede.style.fontStyle = summary.wait ? "italic" : "normal";

    if (JSON.stringify(previous?.images) !== JSON.stringify(item.images)) renderFigures(item);

    const body = ui.byId("read-body");
    paragraphs(body, item.translated_text || item.error || "");

    const originalBlock = ui.byId("read-original-block");
    originalBlock.hidden = !item.original_text;
    const original = ui.byId("read-original");
    if (!original.hidden) paragraphs(original, item.original_text || "");

    if (JSON.stringify(previous?.related_links) !== JSON.stringify(item.related_links)) renderLinks(item);

    const source = ui.byId("read-source");
    source.href = item.url;

    const process = ui.byId("read-process");
    const processable = item.processable !== false && item.status !== "unsupported";
    process.hidden = !processable;
    process.disabled = item.status === "processing";
    if (item.status === "processing") process.textContent = "处理中";
    else process.textContent = item.translated_text ? "重新处理" : "立即处理";
  }

  async function loadNeighbour() {
    try {
      const params = new URLSearchParams(window.location.search);
      params.set("limit", "1");
      params.set("view", "summary");
      params.set("before_id", String(bookmarkID));
      const page = await ui.fetchJSON(`/api/bookmarks?${params}`);
      const next = Array.isArray(page.items) ? page.items[0] : null;
      if (!next) return;
      const link = ui.byId("read-next");
      link.href = ui.bookmarkPath(next.id);
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
        render({ ...current, status: "processing" });
        setTimeout(() => loadBookmark(true), 900);
      } else {
        const code = result.rejected[0]?.error;
        ui.showToast(ui.errorLabel(code), true);
        button.disabled = false;
      }
    } catch (_) {
      ui.showToast("提交处理请求失败", true);
      button.disabled = false;
    }
  }

  ui.byId("read-process").addEventListener("click", processCurrent);
  ui.byId("read-export").addEventListener("click", async () => {
    if (!current) return;
    const button = ui.byId("read-export");
    button.disabled = true;
    try {
      await ui.exportMarkdown([current]);
    } catch (_) {
      ui.showToast("导出失败，请重试", true);
    } finally {
      button.disabled = !current;
    }
  });
  ui.byId("curation-form").addEventListener("input", () => markDirty());
  ui.byId("curation-form").addEventListener("change", () => { markDirty(); updateTopicCount(); });
  ui.byId("curation-form").addEventListener("submit", (event) => { event.preventDefault(); saveCuration(); });
  ui.byId("reset-classification").addEventListener("click", () => saveCuration(true));
  ui.byId("retry-taxonomy").addEventListener("click", loadTaxonomy);
  ui.byId("use-suggestion").addEventListener("click", () => {
    ui.byId("curation-why").value = current?.classification?.why_suggestion || "";
    markDirty();
  });
  window.addEventListener("beforeunload", (event) => {
    if (dirty || saving) { event.preventDefault(); event.returnValue = ""; }
  });
  ui.byId("read-toggle").addEventListener("click", (event) => {
    const panel = ui.byId("read-original");
    const open = panel.hidden;
    if (open) paragraphs(panel, current?.original_text || "");
    panel.hidden = !open;
    event.currentTarget.classList.toggle("open", open);
    event.currentTarget.setAttribute("aria-expanded", String(open));
    ui.byId("read-toggle-label").textContent = open ? "收起原文" : "展开原文";
  });

  if (bookmarkID) {
    loadTaxonomy();
    loadBookmark();
    loadNeighbour();
  } else {
    ui.byId("read-loading").hidden = true;
    ui.byId("read-error").hidden = false;
  }

  // Poll while a job is in flight, but stop after a bounded number of attempts.
  // A job that never advances (worker down, lease stuck) would otherwise poll
  // a LAN server every 8 seconds for as long as the tab stays open.
  const MAX_POLLS = 30;
  let polls = 0;
  setInterval(() => {
    if (document.hidden || dirty || saving) return;
    if (current?.status !== "processing" && current?.status !== "pending") { polls = 0; return; }
    if (polls++ >= MAX_POLLS) return;
    loadBookmark(true);
  }, POLL_INTERVAL);
})();
