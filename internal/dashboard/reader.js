(() => {
  "use strict";

  const ui = window.CairnUI;
  const match = window.location.pathname.match(/^\/bookmarks\/([1-9][0-9]*)$/);
  const bookmarkID = match ? Number(match[1]) : 0;
  let current = null;
  let loading = false;

  function languageLabel(value) {
    const labels = {
      ar: "阿拉伯语",
      de: "德语",
      en: "英语",
      es: "西班牙语",
      fr: "法语",
      ja: "日语",
      ko: "韩语",
      pt: "葡萄牙语",
      ru: "俄语",
      "zh-CN": "简体中文",
      "zh-TW": "繁体中文",
      zh: "中文"
    };
    return labels[value] || value || "未知语言";
  }

  function fallbackSummary(item) {
    if (item.status === "unsupported") return "此收藏不是 X 链接，不参与自动处理。";
    if (item.status === "processing") return "正在生成双语正文、摘要、标题和图片归档。";
    if (item.status === "completed") return "这条内容由旧版本生成，重新处理后可补齐新版阅读内容。";
    return "尚未生成摘要。";
  }

  function fallbackText(item, translated) {
    if (item.status === "unsupported") return translated ? "此收藏不参与翻译处理。" : "此收藏不是 X 链接，没有可提取的 X 原文。";
    if (item.status === "processing") return translated ? "正在生成简体中文全文。" : "正在从 X 原帖及相关评论提取原文。";
    if (item.status === "completed") return "这条内容由旧版本生成，重新处理后会补齐此字段。";
    return translated ? "处理完成后将在这里显示简体中文全文。" : "处理完成后将在这里显示原始语言全文。";
  }

  function renderGallery(item) {
    const section = ui.byId("reader-gallery-section");
    const gallery = ui.byId("reader-gallery");
    gallery.replaceChildren();
    const images = Array.isArray(item.images) ? item.images : [];
    section.hidden = images.length === 0;
    gallery.classList.toggle("single", images.length === 1);
    images.forEach((imageRef, index) => {
      const figure = document.createElement("figure");
      const link = document.createElement("a");
      link.href = ui.imagePath(imageRef.key);
      link.target = "_blank";
      link.rel = "noopener noreferrer";
      const image = document.createElement("img");
      image.src = ui.imagePath(imageRef.key);
      image.alt = `${item.ai_title || "收藏内容"} 图片 ${index + 1}`;
      image.loading = index === 0 ? "eager" : "lazy";
      image.decoding = "async";
      image.addEventListener("error", () => figure.remove());
      link.append(image);
      figure.append(link);
      gallery.append(figure);
    });
  }

  function renderLinks(item) {
    const section = ui.byId("reader-links-section");
    const links = ui.byId("reader-links");
    links.replaceChildren();
    const values = Array.isArray(item.related_links) ? item.related_links : [];
    section.hidden = values.length === 0;
    for (const value of values) {
      const link = ui.element("a", "", value);
      link.href = value;
      link.target = "_blank";
      link.rel = "noopener noreferrer";
      links.append(link);
    }
  }

  function render(item) {
    current = item;
    ui.byId("reader-loading").hidden = true;
    ui.byId("reader-error").hidden = true;
    ui.byId("reader-article").hidden = false;

    const status = ui.byId("reader-status");
    status.className = `badge ${item.status}`;
    status.textContent = ui.statusLabels[item.status] || item.status;
    const metaParts = [`#${item.id}`, `收藏于 ${ui.formatDate(item.created_at)}`];
    if (item.enriched_at) metaParts.push(`处理于 ${ui.formatDate(item.enriched_at)}`);
    if (item.model) metaParts.push(item.model);
    ui.byId("reader-meta").textContent = metaParts.join(" · ");

    const title = ui.byId("reader-title");
    title.textContent = item.ai_title || (item.status === "unsupported" ? "非 X 收藏" : "AI 标题待生成");
    title.classList.toggle("placeholder", !item.ai_title);
    document.title = `${item.ai_title || "收藏详情"} · Cairn 收藏阅读库`;

    const note = ui.byId("reader-note");
    note.hidden = !item.note;
    ui.byId("reader-note-text").textContent = item.note || "";

    const source = ui.byId("reader-source");
    source.href = item.url;
    source.textContent = item.status === "unsupported" ? "查看原链接" : "查看 X 原帖";

    const process = ui.byId("reader-process");
    const processable = item.processable !== false && item.status !== "unsupported";
    process.hidden = !processable;
    process.disabled = item.status === "processing" || loading;
    process.textContent = item.status === "processing" ? "处理中" : (item.status === "completed" ? "重新处理" : "立即处理");

    const error = ui.byId("reader-error-message");
    error.hidden = !item.error;
    error.textContent = item.error || "";

    renderGallery(item);
    const summary = ui.byId("reader-summary");
    summary.textContent = item.summary || fallbackSummary(item);
    summary.classList.toggle("empty", !item.summary);

    ui.byId("reader-language").textContent = languageLabel(item.original_language);
    const original = ui.byId("reader-original");
    original.textContent = item.original_text || fallbackText(item, false);
    original.classList.toggle("empty", !item.original_text);
    const translated = ui.byId("reader-translated");
    translated.textContent = item.translated_text || fallbackText(item, true);
    translated.classList.toggle("empty", !item.translated_text);
    renderLinks(item);
  }

  async function loadBookmark(silent = false) {
    if (!bookmarkID || loading) return;
    loading = true;
    if (!silent) ui.byId("reader-loading").hidden = false;
    try {
      const response = await fetch(`/api/bookmarks/${bookmarkID}`, { cache: "no-store" });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      render(await response.json());
    } catch (_) {
      if (!silent) {
        ui.byId("reader-loading").hidden = true;
        ui.byId("reader-error").hidden = false;
      } else {
        ui.showToast("刷新收藏内容失败", true);
      }
    } finally {
      loading = false;
      if (current) render(current);
    }
  }

  async function processCurrent() {
    if (!current) return;
    if (current.status === "completed" && !window.confirm("重新处理会再次调用模型并更新现有内容。确认继续？")) {
      return;
    }
    const button = ui.byId("reader-process");
    button.disabled = true;
    try {
      const result = await ui.submitProcessing([current.id]);
      if (result.accepted.length) {
        ui.showToast("已提交处理请求");
        current.status = "processing";
        render(current);
        setTimeout(() => loadBookmark(true), 800);
      } else {
        const code = result.rejected[0]?.error;
        ui.showToast(ui.errorLabels[code] || "处理请求未被接受", true);
      }
    } catch (_) {
      ui.showToast("提交处理请求失败", true);
    } finally {
      if (current) render(current);
    }
  }

  ui.byId("reader-process").addEventListener("click", processCurrent);
  ui.refreshServiceStatus();
  if (bookmarkID) {
    loadBookmark();
  } else {
    ui.byId("reader-loading").hidden = true;
    ui.byId("reader-error").hidden = false;
  }
  setInterval(() => {
    if (document.hidden) return;
    ui.refreshServiceStatus();
    if (current?.status === "processing") loadBookmark(true);
  }, 5000);
})();
