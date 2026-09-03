(() => {
  "use strict";

  const statusLabels = Object.freeze({
    pending: "待处理",
    processing: "处理中",
    completed: "已完成",
    failed: "待重试",
    exhausted: "已终止",
    unsupported: "其他"
  });

  const errorLabels = Object.freeze({
    job_busy: "该收藏正在处理中",
    not_found: "收藏不存在",
    backend_error: "Cloudflare 后端暂时不可用",
    queue_full: "本机处理队列已满",
    invalid_ids: "所选收藏无效"
  });

  const byId = (id) => document.getElementById(id);

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function formatDate(value) {
    if (!value) return "-";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "-";
    return new Intl.DateTimeFormat("zh-CN", {
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit"
    }).format(date);
  }

  function shortURL(value) {
    try {
      const parsed = new URL(value);
      return parsed.hostname + parsed.pathname;
    } catch (_) {
      return value;
    }
  }

  function imagePath(key) {
    return "/api/images/" + String(key).split("/").map(encodeURIComponent).join("/");
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

  function setServiceState(kind, label) {
    const dot = byId("state-dot");
    const stateLabel = byId("state-label");
    if (dot) dot.className = `state-dot ${kind}`;
    if (stateLabel) stateLabel.textContent = label;
  }

  async function refreshServiceStatus() {
    try {
      const response = await fetch("/status", { cache: "no-store" });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const status = await response.json();
      const build = status.build || {};
      const commit = build.commit && build.commit !== "none" ? build.commit.slice(0, 8) : "local";
      const version = build.version || "dev";
      if (byId("current-version")) byId("current-version").textContent = version;
      if (byId("service-meta")) byId("service-meta").textContent = `提交 ${commit}`;
      if (byId("build-info")) byId("build-info").textContent = `Cairn X Enricher ${version}`;
      if (!status.ready) setServiceState("bad", "未就绪");
      else if (status.last_error) setServiceState("warn", "需要检查");
      else setServiceState("good", "运行正常");
    } catch (_) {
      setServiceState("bad", "连接失败");
      if (byId("current-version")) byId("current-version").textContent = "未知";
      if (byId("service-meta")) byId("service-meta").textContent = "无法读取服务状态";
    }
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

  window.CairnUI = Object.freeze({
    byId,
    element,
    errorLabels,
    formatDate,
    imagePath,
    refreshServiceStatus,
    shortURL,
    showToast,
    statusLabels,
    submitProcessing
  });
})();
