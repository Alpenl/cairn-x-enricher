(() => {
  "use strict";

  const ui = window.CairnUI;
  const ATTENTION_STATUSES = ["failed", "exhausted"];
  const REFRESH_INTERVAL = 15000;

  function reasonFor(item) {
    if (item.status === "exhausted") {
      return `已经试过 ${item.attempts} 次仍然失败${item.error ? "：" + item.error : ""}`;
    }
    if (item.next_retry_at) {
      return `上次读取失败，${ui.formatDateTime(item.next_retry_at)} 会自动重试`;
    }
    return item.error || "上次读取失败，稍后会自动重试";
  }

  function attentionRow(item) {
    const row = ui.element("div", "back-row");
    const body = ui.element("div", "u");
    const link = ui.element("a", "back-url", ui.shortURL(item.url));
    link.href = `/bookmarks/${item.id}`;
    body.append(link);
    body.append(ui.element("div", "back-why", reasonFor(item)));
    row.append(body);

    const action = ui.element("button", "text-btn", "再试一次");
    action.type = "button";
    action.addEventListener("click", async () => {
      action.disabled = true;
      try {
        const result = await ui.submitProcessing([item.id]);
        if (result.accepted.length > 0) {
          ui.showToast("已提交处理请求");
          setTimeout(refresh, 900);
        } else {
          const code = result.rejected[0]?.error;
          ui.showToast(ui.errorLabels[code] || "处理请求未被接受", true);
          action.disabled = false;
        }
      } catch (_) {
        ui.showToast("提交处理请求失败", true);
        action.disabled = false;
      }
    });
    row.append(action);
    return row;
  }

  function describeService(status, counts) {
    const parts = [];
    const stats = status?.last_stats;
    if (stats) {
      parts.push(`最近一批领取 ${stats.claimed ?? 0} 条，完成 ${stats.completed ?? 0} 条，失败 ${stats.failed ?? 0} 条。`);
    }
    if ((counts.pending ?? 0) + (counts.processing ?? 0) > 0) {
      parts.push(`队列里还有 ${(counts.pending ?? 0) + (counts.processing ?? 0)} 条在等待处理。`);
    }
    parts.push("新收藏一般在几分钟内出现在列表里，平时不需要打开这一页。");
    return parts.join("");
  }

  async function refresh() {
    let status = null;
    try {
      status = await ui.fetchStatus();
    } catch (_) {
      status = null;
    }

    let counts = {};
    const attention = [];
    try {
      for (const name of ATTENTION_STATUSES) {
        const page = await ui.fetchJSON(`/api/bookmarks?limit=20&status=${name}`);
        counts = page.counts || counts;
        for (const item of Array.isArray(page.items) ? page.items : []) attention.push(item);
      }
    } catch (_) {
      ui.byId("back-title").textContent = "读取失败";
      ui.byId("back-state").textContent = "无法读取 Cloudflare 后端，请检查网络和 CAIRN_ENRICHER_TOKEN。";
      return;
    }

    const title = ui.byId("back-title");
    if (status && !status.ready) {
      title.textContent = "服务未就绪";
    } else if (status?.last_error) {
      title.textContent = "最近一批有错误";
    } else {
      title.textContent = "一切正常";
    }

    const state = ui.byId("back-state");
    state.textContent = describeService(status, counts);
    if (status?.last_error) {
      state.append(document.createElement("br"));
      state.append(document.createTextNode(status.last_error));
    }

    const sub = ui.byId("back-sub");
    const list = ui.byId("back-list");
    list.replaceChildren();
    if (attention.length === 0) {
      sub.hidden = true;
    } else {
      sub.textContent = `需要你看一眼的 ${attention.length} 条`;
      sub.hidden = false;
      for (const item of attention) list.append(attentionRow(item));
    }

    const build = status?.build || {};
    const version = build.version || "dev";
    const commit = build.commit && build.commit !== "none" ? build.commit.slice(0, 8) : "local";
    ui.byId("back-foot").textContent =
      `cairn-x-enricher ${version} · 提交 ${commit} · 共 ${counts.total ?? 0} 条收藏`;
  }

  refresh();
  setInterval(() => {
    if (document.hidden) return;
    refresh();
  }, REFRESH_INTERVAL);
})();
