(() => {
  "use strict";

  const errorLabels = Object.freeze({
    job_busy: "这条正在处理中",
    not_found: "这条收藏不存在",
    backend_error: "Cloudflare 后端暂时不可用",
    queue_full: "本机处理队列已满",
    invalid_ids: "所选收藏无效",
    invalid_source: "原文不能为空或过长"
  });

  const waitingText = Object.freeze({
    pending: "正在排队，稍后会生成中文标题与译文",
    processing: "正在生成中文标题与译文",
    failed: "上次没有读取成功，稍后会自动重试",
    exhausted: "这条没能读取，去后台可以再试一次",
    unsupported: "不是 X 链接，只保留了链接和备注",
    completed: "由旧版本处理，重新处理可以补齐内容"
  });

  const byId = (id) => document.getElementById(id);

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function parseDate(value) {
    if (!value) return null;
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? null : date;
  }

  function formatDate(value) {
    const date = parseDate(value);
    if (!date) return "-";
    return new Intl.DateTimeFormat("zh-CN", {
      year: "numeric",
      month: "long",
      day: "numeric"
    }).format(date);
  }

  function formatDateTime(value) {
    const date = parseDate(value);
    if (!date) return "-";
    return new Intl.DateTimeFormat("zh-CN", {
      month: "long",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit"
    }).format(date);
  }

  function startOfDay(date) {
    return new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
  }

  function bucketLabel(value) {
    const date = parseDate(value);
    if (!date) return "更早";
    const days = Math.round((startOfDay(new Date()) - startOfDay(date)) / 86400000);
    if (days <= 0) return "今天";
    if (days < 7) return "近七天";
    if (days < 30) return "近三十天";
    return new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "long" }).format(date);
  }

  function shortURL(value) {
    try {
      const parsed = new URL(value);
      return parsed.hostname.replace(/^www\./, "") + parsed.pathname;
    } catch (_) {
      return value;
    }
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

  async function fetchJSON(path) {
    const response = await fetch(path, { cache: "no-store" });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
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
    bucketLabel,
    byId,
    displaySummary,
    displayTitle,
    element,
    errorLabels,
    fetchJSON,
    fetchStatus,
    firstImage,
    formatDate,
    formatDateTime,
    imagePath,
    needsAttention,
    shortURL,
    showToast,
    submitProcessing,
    submitSource,
    waitingText
  });
})();
