(() => {
  "use strict";

  const errorLabels = Object.freeze({
    job_busy: "这条正在处理中",
    not_found: "这条收藏不存在",
    backend_error: "Cloudflare 后端暂时不可用",
    queue_full: "本机处理队列已满",
    invalid_ids: "所选收藏无效",
    invalid_source: "原文不能为空或过长",
    invalid_curation: "整理内容无效，请检查标签和收藏原因",
    invalid_id: "收藏编号无效",
    invalid_query: "筛选条件无效",
    invalid_json: "请求内容格式不对，请重试",
    invalid_content_type: "请求类型不受支持，请刷新页面重试"
  });

  // Unknown codes are surfaced verbatim rather than hidden, so a new backend
  // error is visible instead of being replaced by a generic message.
  function errorLabel(code) {
    return errorLabels[code] || code || "请求失败，请重试";
  }

  const waitingText = Object.freeze({
    pending: "正在排队，稍后会生成中文标题与译文",
    processing: "正在生成中文标题与译文",
    failed: "上次没有读取成功，稍后会自动重试",
    exhausted: "这条没能读取，去后台可以再试一次",
    unsupported: "尚未归档正文",
    completed: "由旧版本处理，重新处理可以补齐内容"
  });

  const byId = (id) => document.getElementById(id);
  const curationLabels = Object.freeze({ inbox: "收件箱", kept: "精选", compiled: "已编入笔记", drop: "搁置" });
  const sourceLabels = Object.freeze({ x: "X", wechat: "公众号", other: "其他来源" });

  // Intl.DateTimeFormat construction is roughly 30x the cost of reusing an
  // existing formatter, and these are built once per rendered card. Cache one
  // instance per option set instead of allocating in the render path.
  const formatters = new Map();
  function formatterFor(options) {
    const key = JSON.stringify(options);
    let formatter = formatters.get(key);
    if (!formatter) {
      formatter = new Intl.DateTimeFormat("zh-CN", options);
      formatters.set(key, formatter);
    }
    return formatter;
  }

  // Dates arrive as ISO strings and are displayed at day or minute granularity,
  // so round-trip parsing and formatting results are memoised per raw value.
  const dateCache = new Map();
  const DATE_CACHE_LIMIT = 2000;
  function parseDate(value) {
    if (!value) return null;
    let date = dateCache.get(value);
    if (date === undefined) {
      date = new Date(value);
      if (Number.isNaN(date.getTime())) date = null;
      if (dateCache.size >= DATE_CACHE_LIMIT) dateCache.clear();
      dateCache.set(value, date);
    }
    return date;
  }

  const labelCache = new Map();
  function memoLabel(key, compute) {
    let value = labelCache.get(key);
    if (value === undefined) {
      if (labelCache.size >= DATE_CACHE_LIMIT) labelCache.clear();
      value = compute();
      labelCache.set(key, value);
    }
    return value;
  }
  let catalog = null;
  let catalogPromise = null;
  // termLabel runs once per topic/form/use on every rendered card, so resolve
  // IDs through a lookup table instead of scanning the term array each time.
  const termLabels = { topics: new Map(), forms: new Map(), uses: new Map() };

  async function loadTaxonomy() {
    if (!catalogPromise) {
      catalogPromise = fetchJSON("/api/taxonomy").then((value) => {
        catalog = value;
        for (const dimension of ["topics", "forms", "uses"]) {
          const index = termLabels[dimension];
          index.clear();
          for (const term of value?.[dimension] || []) index.set(term.id, term.label);
        }
        return value;
      }).catch((error) => { catalogPromise = null; throw error; });
    }
    return catalogPromise;
  }

  function termLabel(dimension, id) {
    return termLabels[dimension]?.get(id) || id;
  }

  function fillTerms(select, terms, emptyLabel, includeInactive = false) {
    select.replaceChildren(new Option(emptyLabel, ""));
    for (const term of terms) {
      if (term.active || includeInactive) select.append(new Option(term.label + (term.active ? "" : "（停用）"), term.id));
    }
  }

  function metadata(item) {
    const holder = element("div", "bookmark-meta");
    holder.append(element("span", "curation-state", curationLabels[item.curation_status || "inbox"]));
    if (item.source) holder.append(element("span", "", sourceLabels[item.source] || item.source));
    const classification = item.classification;
    for (const topic of classification?.topics || []) holder.append(element("span", "topic-label", termLabel("topics", topic)));
    if (classification?.form) holder.append(element("span", "", termLabel("forms", classification.form)));
    if (classification?.use) holder.append(element("span", "", termLabel("uses", classification.use)));
    if (!item.classification_reviewed && (!classification || classification.uncertainty)) holder.append(element("span", "review-state", "待确认"));
    return holder;
  }

  function bookmarkPath(id) {
    return `/bookmarks/${id}${window.location.search}`;
  }

  // Yields to the event loop so a large export does not freeze the page. The
  // build is chunked because a full-text export can involve megabytes of
  // string work, and this runs on the same thread as scrolling and input.
  function yieldToBrowser() {
    return new Promise((resolve) => setTimeout(resolve, 0));
  }

  const EXPORT_CHUNK_SIZE = 40;

  async function exportMarkdown(items, hasMore = false) {
    // Summary pages hydrate only when the user exports. Capture this list so
    // changing filters during an export cannot mix two different selections.
    items = items.slice();
    const escape = (value) => String(value || "").replace(/[\\`*_{}\[\]()<>#!|]/g, "\\$&");
    const line = (value) => escape(value).replace(/[\r\n]+/g, " ");
    const link = (value) => {
      try {
        const url = new URL(value);
        return /^(https?:)$/.test(url.protocol) ? `<${url.href.replace(/[<>]/g, encodeURIComponent)}>` : "";
      } catch (_) { return ""; }
    };
    const lines = ["# Cairn 收藏摘录", "", `导出时间：${new Date().toISOString()}`, `条目数量：${items.length}`,
      `范围：当前已加载的收藏${hasMore ? "（还有未加载的结果）" : ""}`, `筛选地址：${link(window.location.href)}`, ""];
    for (let index = 0; index < items.length; index++) {
      const summary = items[index];
      const item = summary.content_loaded === false
        ? await fetchJSON(`/api/bookmarks/${summary.id}`)
        : summary;
      const classification = item.classification || {};
      lines.push(`## ${line(displayTitle(item).text)}`, "", `收藏 ID：${item.id}`, `来源：${link(item.url)}`,
        `收藏时间：${line(item.created_at)}`, `整理状态：${curationLabels[item.curation_status || "inbox"]}`,
        `主题：${(classification.topics || []).map((id) => line(termLabel("topics", id))).join(" / ")}`,
        `形态：${line(termLabel("forms", classification.form || ""))}`, `用途：${line(termLabel("uses", classification.use || ""))}`,
        `分类确认：${item.classification_reviewed ? "已确认" : "未确认"}`, "");
      for (const [label, value] of [["收藏原因", item.why], ["收藏备注", item.note], ["用途建议（AI）", classification.why_suggestion],
        ["摘要", item.summary], ["中文全文", item.translated_text], ["原文", item.original_text]]) {
        if (value) lines.push(`### ${label}`, "", escape(value), "");
      }
      if (classification.entities?.length) lines.push(`实体：${classification.entities.map(line).join(" / ")}`, "");
      if (item.related_links?.length) lines.push("### 相关链接", "", ...item.related_links.map((value) => `- ${link(value)}`), "");
      if ((index + 1) % EXPORT_CHUNK_SIZE === 0) await yieldToBrowser();
    }
    const blob = new Blob([lines.join("\n")], { type: "text/markdown;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `cairn-${new Date().toISOString().slice(0, 10)}.md`;
    document.body.append(anchor);
    anchor.click();
    anchor.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function formatDate(value) {
    const date = parseDate(value);
    if (!date) return "-";
    const time = date.getTime();
    return memoLabel(`d:${time}`, () => formatterFor({
      year: "numeric",
      month: "long",
      day: "numeric"
    }).format(date));
  }

  function formatDateTime(value) {
    const date = parseDate(value);
    if (!date) return "-";
    const time = date.getTime();
    return memoLabel(`dt:${time}`, () => formatterFor({
      month: "long",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit"
    }).format(date));
  }

  // Relative time reads faster than a clock time for "when does this retry"
  // or "how old is this failure". Fall back to the absolute form for anything
  // beyond a day, where a date is more useful than a long countdown.
  function formatRelative(value, now = Date.now()) {
    const date = parseDate(value);
    if (!date) return "-";
    const delta = date.getTime() - now;
    const minutes = Math.round(delta / 60000);
    const abs = Math.abs(minutes);
    const rtf = new Intl.RelativeTimeFormat("zh-CN", { numeric: "auto" });
    if (abs < 60) return rtf.format(minutes, "minute");
    if (abs < 60 * 24) return rtf.format(Math.round(minutes / 60), "hour");
    return formatDateTime(value);
  }

  function startOfDay(date) {
    return new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
  }

  // The bucket only depends on the calendar day, so compute it once per day
  // rather than once per card.
  function bucketLabel(value) {
    const date = parseDate(value);
    if (!date) return "更早";
    const day = startOfDay(date);
    const today = startOfDay(new Date());
    return memoLabel(`b:${day}:${today}`, () => {
      const days = Math.round((today - day) / 86400000);
      if (days <= 0) return "今天";
      if (days < 7) return "近七天";
      if (days < 30) return "近三十天";
      return formatterFor({ year: "numeric", month: "long" }).format(date);
    });
  }

  // Hostnames repeat heavily across a bookmark list, so cache the parsed form
  // instead of constructing a URL object per card.
  const shortURLCache = new Map();
  function shortURL(value) {
    let cached = shortURLCache.get(value);
    if (cached === undefined) {
      try {
        const parsed = new URL(value);
        cached = parsed.hostname.replace(/^www\./, "") + parsed.pathname;
      } catch (_) {
        cached = value;
      }
      if (shortURLCache.size >= DATE_CACHE_LIMIT) shortURLCache.clear();
      shortURLCache.set(value, cached);
    }
    return cached;
  }

  function imagePath(key) {
    return "/api/images/" + String(key).split("/").map(encodeURIComponent).join("/");
  }

  function firstImage(item) {
    const images = Array.isArray(item.images) ? item.images : [];
    return images.length > 0 ? imagePath(images[0].key) : "";
  }

  function displayTitle(item) {
    if (item.ai_title) return { text: item.ai_title, raw: false };
    return { text: shortURL(item.url), raw: true };
  }

  function displaySummary(item) {
    if (item.summary) return { text: item.summary, wait: false };
    return { text: waitingText[item.status] || "还没有生成内容", wait: true };
  }

  function needsAttention(item) {
    return item.status === "failed" || item.status === "exhausted";
  }

  function showToast(message, bad = false) {
    const toast = byId("toast");
    if (!toast) return;
    toast.textContent = message;
    toast.className = bad ? "toast bad" : "toast";
    toast.hidden = false;
    clearTimeout(showToast.timer);
    showToast.timer = setTimeout(() => { toast.hidden = true; }, 4200);
  }

  async function fetchJSON(path, options = {}) {
    const response = await fetch(path, { cache: "no-store", ...options });
    if (!response.ok) {
      const payload = await response.json().catch(() => ({}));
      throw new Error(payload.error || `HTTP ${response.status}`);
    }
    return response.json();
  }

  async function fetchStatus() {
    return fetchJSON("/status");
  }

  async function submitProcessing(ids) {
    const response = await fetch("/api/bookmarks/process", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ids })
    });
    let payload = {};
    try {
      payload = await response.json();
    } catch (_) {
      payload = {};
    }
    if (!response.ok && !Array.isArray(payload.rejected)) {
      throw new Error(payload.error || `HTTP ${response.status}`);
    }
    return {
      accepted: Array.isArray(payload.accepted) ? payload.accepted : [],
      rejected: Array.isArray(payload.rejected) ? payload.rejected : []
    };
  }

  async function submitSource(id, originalText) {
    const response = await fetch(`/api/bookmarks/${id}/source`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ original_text: originalText })
    });
    let payload = {};
    try {
      payload = await response.json();
    } catch (_) {
      payload = {};
    }
    if (!response.ok && !Array.isArray(payload.rejected)) {
      throw new Error(payload.error || `HTTP ${response.status}`);
    }
    return {
      accepted: Array.isArray(payload.accepted) ? payload.accepted : [],
      rejected: Array.isArray(payload.rejected) ? payload.rejected : []
    };
  }

  window.CairnUI = Object.freeze({
    bookmarkPath,
    curationLabels,
    exportMarkdown,
    fillTerms,
    loadTaxonomy,
    metadata,
    termLabel,
    bucketLabel,
    byId,
    displaySummary,
    displayTitle,
    element,
    errorLabel,
    errorLabels,
    fetchJSON,
    fetchStatus,
    firstImage,
    formatDate,
    formatDateTime,
    formatRelative,
    imagePath,
    needsAttention,
    shortURL,
    showToast,
    submitProcessing,
    submitSource,
    waitingText
  });
})();
