(() => {
  "use strict";

  const ui = window.CairnUI;
  const REFRESH_INTERVAL = 15000;

  function retryIcon() {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("width", "15");
    svg.setAttribute("height", "15");
    svg.setAttribute("viewBox", "0 0 20 20");
    svg.setAttribute("fill", "none");
    svg.setAttribute("aria-hidden", "true");
    const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
    path.setAttribute("d", "M15.8 7.8A6 6 0 1 0 16 10m0-5v3.1h-3.1");
    path.setAttribute("stroke", "currentColor");
    path.setAttribute("stroke-width", "1.6");
    path.setAttribute("stroke-linecap", "round");
    path.setAttribute("stroke-linejoin", "round");
    svg.append(path);
    return svg;
  }

  function sourceIcon() {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("width", "15");
    svg.setAttribute("height", "15");
    svg.setAttribute("viewBox", "0 0 20 20");
    svg.setAttribute("fill", "none");
    svg.setAttribute("aria-hidden", "true");
    const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
    path.setAttribute("d", "M7 4.5h6M7 8h6M7 11.5h3.5M5.5 2.8h9A1.7 1.7 0 0 1 16.2 4.5v11a1.7 1.7 0 0 1-1.7 1.7h-9a1.7 1.7 0 0 1-1.7-1.7v-11a1.7 1.7 0 0 1 1.7-1.7Z");
    path.setAttribute("stroke", "currentColor");
    path.setAttribute("stroke-width", "1.5");
    path.setAttribute("stroke-linecap", "round");
    path.setAttribute("stroke-linejoin", "round");
    svg.append(path);
    return svg;
  }

  function setRetryLabel(button, label) {
    button.replaceChildren(retryIcon(), ui.element("span", "", label));
  }

  function setSourceLabel(button, label) {
    button.replaceChildren(sourceIcon(), ui.element("span", "", label));
  }

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
    const shell = ui.element("div", "back-item");
    const row = ui.element("div", "back-row");
    const body = ui.element("div", "u");
    const link = ui.element("a", "back-url", ui.shortURL(item.url));
    link.href = `/bookmarks/${item.id}`;
    body.append(link);
    body.append(ui.element("div", "back-why", reasonFor(item)));
    row.append(body);

    const actions = ui.element("div", "back-actions");
    const action = ui.element("button", "retry-btn");
    action.type = "button";
    setRetryLabel(action, "再试一次");
    action.addEventListener("click", async () => {
      action.disabled = true;
      try {
        const result = await ui.submitProcessing([item.id]);
        if (result.accepted.length > 0) {
          setRetryLabel(action, "已提交");
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
    actions.append(action);

    const sourceAction = ui.element("button", "retry-btn retry-btn-secondary");
    sourceAction.type = "button";
    setSourceLabel(sourceAction, "粘贴原文");
    actions.append(sourceAction);
    row.append(actions);

    const sourceForm = ui.element("form", "source-form");
    sourceForm.hidden = true;
    const textarea = ui.element("textarea", "source-input");
    textarea.name = "original_text";
    textarea.maxLength = 100000;
    textarea.placeholder = "粘贴原帖原文";
    textarea.rows = 8;
    const sourceSubmit = ui.element("button", "retry-btn");
    sourceSubmit.type = "submit";
    setSourceLabel(sourceSubmit, "提交生成");
    const sourceCancel = ui.element("button", "text-btn", "取消");
    sourceCancel.type = "button";
    const sourceFooter = ui.element("div", "source-actions");
    sourceFooter.append(sourceSubmit, sourceCancel);
    sourceForm.append(textarea, sourceFooter);

    sourceAction.addEventListener("click", () => {
      sourceForm.hidden = !sourceForm.hidden;
      if (!sourceForm.hidden) textarea.focus();
    });
    sourceCancel.addEventListener("click", () => {
      sourceForm.hidden = true;
    });
    sourceForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      const sourceText = textarea.value.trim();
      if (!sourceText) {
        ui.showToast("原文不能为空", true);
        return;
      }
      sourceSubmit.disabled = true;
      sourceAction.disabled = true;
      try {
        const result = await ui.submitSource(item.id, sourceText);
        if (result.accepted.length > 0) {
          setSourceLabel(sourceSubmit, "已提交");
          ui.showToast("已提交原文生成请求");
          setTimeout(refresh, 900);
        } else {
          const code = result.rejected[0]?.error;
          ui.showToast(ui.errorLabels[code] || "原文生成请求未被接受", true);
          sourceSubmit.disabled = false;
          sourceAction.disabled = false;
        }
      } catch (_) {
        ui.showToast("提交原文失败", true);
        sourceSubmit.disabled = false;
        sourceAction.disabled = false;
      }
    });

    shell.append(row, sourceForm);
    return shell;
  }

  function attentionText(total, shown) {
    if (total > shown) return `需要你看一眼的 ${total} 条（显示最近 ${shown} 条）`;
    return `需要你看一眼的 ${shown} 条`;
  }

  async function refresh() {
    let summary = null;
    try {
      summary = await ui.fetchJSON("/api/backstage");
    } catch (_) {
      ui.byId("back-title").textContent = "读取失败";
      ui.byId("back-state").textContent = "无法读取 Cloudflare 后端，请检查网络和 CAIRN_ENRICHER_TOKEN。";
      return;
    }

    const counts = summary.counts || {};
    const attention = Array.isArray(summary.attention) ? summary.attention : [];
    const title = ui.byId("back-title");
    title.textContent = summary.title || "一切正常";

    const state = ui.byId("back-state");
    state.textContent = summary.state || "";
    if (summary.last_error) {
      state.append(document.createElement("br"));
      state.append(document.createTextNode(summary.last_error));
    }

    const sub = ui.byId("back-sub");
    const list = ui.byId("back-list");
    list.replaceChildren();
    if (attention.length === 0) {
      sub.hidden = true;
    } else {
      const total = Number.isFinite(summary.attention_total) ? summary.attention_total : attention.length;
      sub.textContent = attentionText(total, attention.length);
      sub.hidden = false;
      for (const item of attention) list.append(attentionRow(item));
    }

    const build = summary.build || {};
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
