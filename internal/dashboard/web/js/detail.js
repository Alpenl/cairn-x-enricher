import { failureReason } from "./process-status.js";
// The reading pane: one bookmark with its status control, curation card,
// images, translation, lazily rendered original text and diagnostics.
import { relatedReadingLinks } from "./reading-links.js";
import { renderReading, readingVersions } from "./reading.js";
import { renderMedia, resetMedia } from './media.js';
import { fetchJSON } from "./api.js";
import { api, errorLabel, imagePath, prepareSourceSubmission } from "./api.js";
import { queueImage, prioritizeReading } from "./image-loader.js";
import * as curation from "./curation.js";
import * as diagnostics from "./diagnostics.js";
import { byId, clear, h } from "./dom.js";
import { displaySummary, displayTitle, formatFull, isWorking, paragraphsOf, sourceLabels, sourceLine } from "./format.js";
import { icon } from "./icons.js";
import { forgetOffline, offlineItem, rememberOffline } from "./offline.js";
import { STATUSES } from "./query.js";
import { emit, getItem, mergeItem, on, state } from "./store.js";
import { confirmAction, openDialog, openMenu, toast } from "./ui.js";

const POLL_INTERVAL = 8000;
const IDENTITY_INTERVAL = 15000;
// A job that never advances (worker down, lease stuck) must not poll the LAN
// server for as long as the tab stays open.
const MAX_POLLS = 30;

const els = {};
let hooks = {};
let aiFormatting = false;
let currentId = 0;
let polls = 0;
let loadToken = 0;
let fetchTimer = 0;
let identityBusy = false;
let detailController = null;
let fetchBusy = false;
let identityDue = 0, identityDelay = IDENTITY_INTERVAL;
let wasVisible = false;
const renderedText = new WeakMap();
let renderedImages = "";
let showUnformatted = false;
let formattedItemId = 0;
const versionLabels=new Map();
let renderedLinks = "";

export function currentItemId() {
  return currentId;
}

function sourceURL(item) {
  try {
    const value = String(item?.url || "");
    return ["http:", "https:"].includes(new URL(value).protocol) ? value : null;
  } catch { return null; }
}

export function openSource() {
  const url = sourceURL(getItem(currentId));
  if (url) window.open(url, "_blank", "noopener,noreferrer");
}

// --- Rendering -------------------------------------------------------------------

function paragraphs(container, text, clean = true) {
  const key = JSON.stringify([text, clean, getItem(currentId)?.images?.map(image => image.key)]);
  const previous = renderedText.get(container);
  if (previous?.key === key) return previous.result;
  const result = renderReading(container,text,{url:getItem(currentId)?.url,clean,image:(index,alt)=>{
    const ref=getItem(currentId)?.images?.[index];if(!ref)return h("p","图片未归档");
    const img=h("img",{alt,loading:"lazy",decoding:"async"});queueImage(img,imagePath(ref.key),{priority:0});
    const a=h("a",img);a.href=imagePath(ref.key);a.target="_blank";a.rel="noopener";
    return h("figure.reading-figure",a,...(alt?[h("figcaption",alt)]:[]));
  }});
  renderedText.set(container, { key, result });
  return result;
}

function renderFigures(item, body, inlineImages) {
  const images = Array.isArray(item.images) ? item.images : [];
  const key = JSON.stringify([images.map((image) => image.key),body]);
  if (key === renderedImages) return;
  renderedImages = key;
  els.figures.replaceChildren();
  els.figures.dataset.count = String(images.length);
  images.forEach((ref, index) => {
    if (inlineImages.has(index)) return;
    const image = h("img", { alt: "", decoding: "async" });
    queueImage(image, imagePath(ref.key), { priority: 0 });
    const figure = h("button.figure", { type: "button", "aria-label": `查看第 ${index + 1} 张图片` }, image);
    if (image.complete && image.naturalWidth) image.classList.add("ready");
    image.addEventListener("load", () => image.classList.add("ready"));
    image.addEventListener("error", () => figure.remove());
    figure.addEventListener("click", () => openLightbox(images, index));
    els.figures.append(figure);
  });
  els.figures.hidden = els.figures.childElementCount === 0;
}

function renderLinks(item, extracted = []) {
  const links = relatedReadingLinks(item, extracted);
  const key = JSON.stringify(links);
  if (key === renderedLinks) return;
  renderedLinks = key;
  els.links.replaceChildren();
  els.links.hidden = links.length === 0;
  if (!links.length) return;
  els.links.append(h("h3.section-label", "相关链接"));
  for (const value of links) {
    els.links.append(h("a.related-link", { href: value.url, target: "_blank", rel: "noopener noreferrer" }, icon("link", 14), h("span", value.title || value.url)));
  }
}

function renderStatusControl(item) {
  const status = item.curation_status || "inbox";
  for (const button of els.statusButtons) {
    const active = button.dataset.status === status;
    button.setAttribute("aria-checked", String(active));
    button.tabIndex = active ? 0 : -1;
  }
  for (const button of els.mobileStatus.querySelectorAll("button[data-status]")) {
    button.setAttribute("aria-pressed", String(button.dataset.status === status));
  }
}

function renderProcessBanner(item) {
  const banner = els.banner;
  const actions = [];
  let tone = "info";
  let text = "";
  if (item.offline_cached_at) {
    text = `离线副本 · ${formatFull(new Date(item.offline_cached_at).toISOString())} · 内容可能已更新，编辑需要联网。`;
  } else if (item.paid_call_unresolved) {
    tone = "danger";
    text = failureReason(item);
    actions.push(h("button.btn.btn-sm", { type: "button", onclick: () => pasteSource(item.id) }, icon("clipboard", 14), "粘贴原文"));
  } else if (item.status === "processing") text = item.original_text ? "正在根据已存正文生成标题、译文与摘要…" : "正在读取原帖并生成中文标题、译文与摘要…";
  else if (item.status === "pending") text = item.original_text ? "正文已保存，可直接阅读；标题、译文和摘要等待 AI 增强。" : "已排队，等待后台读取原帖。";
  else if (item.status === "failed") {
    tone = "warn";
    text = failureReason(item);
    actions.push(h("button.btn.btn-sm", { type: "button", onclick: () => processItem(item.id) }, icon("refresh", 14), "立即重试"));
  } else if (item.status === "exhausted") {
    tone = "danger";
    text = failureReason(item);
    actions.push(h("button.btn.btn-sm", { type: "button", onclick: () => processItem(item.id) }, icon("refresh", 14), "再试一次"));
    actions.push(h("button.btn.btn-sm", { type: "button", onclick: () => pasteSource(item.id) }, icon("clipboard", 14), "粘贴原文"));
  } else if (item.status === "completed" && !item.translated_text && item.content_loaded !== false && item.processable !== false) {
    text = "这条由旧版本处理，重新处理可以补齐译文与图片。";
    actions.push(h("button.btn.btn-sm", { type: "button", onclick: () => processItem(item.id) }, "重新处理"));
  }
  banner.hidden = !text;
  if (!text) return;
  banner.className = `banner banner-${tone}`;
  clear(banner, item.status === "processing" && !item.paid_call_unresolved ? icon("loader", 16, "spin") : icon(tone === "info" ? "clock" : "alert", 16),
    h("p", text), actions.length ? h("div.banner-actions", actions) : null);
}

function renderPosition() {
  const index = state.order.indexOf(currentId);
  els.position.textContent = index >= 0 ? `${index + 1} / ${state.total ?? state.order.length}` : "";
  els.prev.disabled = index <= 0;
  els.next.disabled = index === -1 || (index >= state.order.length - 1 && !state.nextBeforeID);
  const nextId = index >= 0 ? state.order[index + 1] : 0;
  const next = nextId ? getItem(nextId) : null;
  els.nextLink.hidden = !next;
  if (next) {
    els.nextLink.href = hooks.href(next.id);
    els.nextTitle.textContent = displayTitle(next).text;
  }
}

function render(item) {
  const title = displayTitle(item);
  els.title.textContent = title.text;
  els.title.classList.toggle("raw", title.raw);
  document.title = `${title.raw ? "阅读" : item.ai_title} · Cairn 收藏`;
  clear(els.meta,
    h("span.meta-source", sourceLine(item.url) || sourceLabels[item.source] || ""),
    h("span.meta-sep", "·"),
    h("time", { dateTime: item.created_at || "" }, formatFull(item.created_at)),
    item.source ? [h("span.meta-sep", "·"), h("span", sourceLabels[item.source] || item.source)] : null,
    item.note ? [h("span.meta-sep", "·"), h("span.meta-note", { title: "来自 App 的收藏备注" }, icon("pencil", 12), h("span", item.note))] : null);
  const source = sourceURL(item);
  els.source.hidden = !source;
  if (source) els.source.href = source;
  else els.source.removeAttribute("href");
  renderStatusControl(item);
  renderProcessBanner(item);

  const summary = displaySummary(item);
  els.lede.textContent = summary.text;
  els.lede.classList.toggle("wait", summary.wait);
  // A banner already explains a waiting or failed read; the placeholder
  // summary would only repeat it.
  els.lede.hidden = summary.wait && !els.banner.hidden;

  const full = item.content_loaded !== false;
  els.bodyLoading.hidden = full;
  if (full) {
    if (formattedItemId !== item.id) { showUnformatted=false;formattedItemId=item.id; }
    const { body, label } = readingVersions(item, showUnformatted);
    const reading = paragraphs(els.body, body, !showUnformatted);
    renderFigures(item, body, reading.images);
    void renderMedia(byId('detail-media'),item,els.body);
    const cleaned = !showUnformatted && els.body.dataset.cleaned === "true";
    const processed=showUnformatted ? (versionLabels.get(item.id) || "整理版") : (cleaned ? "净读版" : label);
    if(!showUnformatted) versionLabels.set(item.id,processed);
    byId("reading-version").textContent=processed;
    byId("reading-version").setAttribute("aria-checked",String(!showUnformatted));
    byId("toggle-formatted").hidden=!item.formatted_content && !cleaned && !showUnformatted;
    byId("toggle-formatted").textContent="原内容";
    byId("toggle-formatted").setAttribute("aria-checked",String(showUnformatted));
    const waiting=["pending","processing"].includes(item.formatting_status);
    byId("format-body").hidden=!aiFormatting;
    byId("format-body").disabled=waiting || !body;
    byId("format-body").textContent=waiting ? "AI 精排中" : item.formatting_status === "failed" ? "重试 AI 精排" : item.formatted_content ? "重新 AI 精排" : "AI 精排";
    els.originalBlock.hidden = !item.original_text || item.original_text === body;
    if (!els.original.hidden) paragraphs(els.original, item.original_text || "", false);
    renderLinks(item, reading.links);
  }
  renderPosition();
  els.exportButton.disabled = false;
}

// --- Loading ---------------------------------------------------------------------

async function fetchDetail(id, { silent = false } = {}) {
  if (silent && fetchBusy) return false;
  detailController?.abort();
  const controller = new AbortController(); detailController = controller;
  fetchBusy = true;
  if (!silent && readerVisible()) prioritizeReading(true);
  const token = ++loadToken;
  try {
    const item = await (silent ? api.detailFresh(id, { signal: controller.signal }) : api.detail(id, { signal: controller.signal }));
    if (!item) return false;
    if (token !== loadToken || id !== currentId) {
      return false;
    }
    const merged = mergeItem(item);
    delete merged.offline_cached_at;
    void rememberOffline(merged);
    els.bodyLoading.hidden = true;
    els.error.hidden = true;
    els.article.hidden = false;
    render(merged);
    curation.refresh(id);
    emit("item", id);
    return true;
  } catch (error) {
    if (error.name === "AbortError") return false;
    if (token !== loadToken || id !== currentId) return false;
    if ([401, 403, 404].includes(error?.status)) void forgetOffline(id);
    if (!error?.status || error.status >= 500) {
      const copy = await offlineItem(id);
      if (token !== loadToken || id !== currentId) return false;
      if (copy) {
        els.bodyLoading.hidden = true;
        els.error.hidden = true;
        els.article.hidden = false;
        render(mergeItem(copy));
        return true;
      }
    }
    els.bodyLoading.hidden = true;
    if (silent) {
      toast("刷新内容失败", { tone: "error" });
      return false;
    }
    if (!getItem(id)) {
      els.article.hidden = true;
      els.error.hidden = false;
      els.errorText.textContent = error?.message === "not_found" ? "这条收藏不存在或已删除" : `无法读取这条收藏：${errorLabel(error?.message)}`;
    } else {
      toast(`读取全文失败：${errorLabel(error?.message)}`, { tone: "error" });
    }
    return false;
  } finally {
    if (detailController === controller) { detailController = null; fetchBusy = false; prioritizeReading(false); }
  }
}

function readerVisible() {
  return !document.hidden && !byId("app").classList.contains("route-management") && currentId > 0 && state.route.name !== "backstage" &&
    (state.layout !== "narrow" || state.route.name === "bookmark");
}
export function syncReaderVisibility() {
  if (!els.article) return;
  const visible = readerVisible();
  diagnostics.syncVisibility();
  if (!visible) {
    clearTimeout(fetchTimer); fetchTimer = 0;
    detailController?.abort(); detailController = null; fetchBusy = false; loadToken++;
    curation.suspendRemote(); prioritizeReading(false);
  } else if (!wasVisible) {
    identityDue = 0; identityDelay = IDENTITY_INTERVAL;
    if (getItem(currentId)?.content_loaded === false && !fetchBusy && !fetchTimer) fetchDetail(currentId);
    curation.resumeRemote();
  }
  wasVisible = visible;
}

export function showItem(id) {
  if (id !== currentId) { detailController?.abort(); detailController = null; clearTimeout(fetchTimer); fetchTimer = 0; fetchBusy = false; loadToken++; identityDue = 0; identityDelay = IDENTITY_INTERVAL; }
  if (!id) {
    currentId = 0;
    els.empty.hidden = false;
    els.article.hidden = true;
    els.error.hidden = true;
    document.title = "Cairn 收藏";
    prioritizeReading(false);
    return;
  }
  const changed = id !== currentId;
  currentId = id;
  polls = 0;
  els.empty.hidden = true;
  els.error.hidden = true;
  if (changed) {
    els.source.hidden = true;
    els.source.removeAttribute("href");
    renderedImages = "";
    renderedLinks = "";
    els.original.hidden = true;
    els.original.replaceChildren();
    renderedText.delete(els.original);
    els.toggle.setAttribute("aria-expanded", "false");
    els.toggle.classList.remove("open");
    els.toggleLabel.textContent = "展开原文";
    els.body.replaceChildren();
    resetMedia();byId('detail-media').replaceChildren();byId('detail-media').hidden=true;
    renderedText.delete(els.body);
    els.figures.replaceChildren();
    els.links.replaceChildren();
    els.scroll.scrollTop = 0;
  }
  const cached = getItem(id);
  prioritizeReading(cached?.content_loaded === false || !cached);
  if (cached) {
    els.article.hidden = false;
    render(cached);
    els.bodyLoading.hidden = cached.content_loaded !== false;
  } else {
    els.article.hidden = false;
    clear(els.title, "");
    els.lede.textContent = "";
    els.bodyLoading.hidden = false;
  }
  curation.show(id);
  diagnostics.show(id);
  // While skimming with J/K the cached summary renders at once; the full read
  // waits a beat so passing over a row does not cost a request.
  clearTimeout(fetchTimer);
  fetchTimer = setTimeout(() => { fetchTimer = 0; if (id === currentId && readerVisible()) fetchDetail(id); }, cached ? 90 : 0);
}

export function refreshCurrent() {
  if (currentId) fetchDetail(currentId, { silent: true });
}

// --- Actions -----------------------------------------------------------------------

export async function processItem(id) {
  const item = getItem(id);
  if (!item) return;
  if (item.processable === false || item.status === "unsupported") {
    toast("只有 X 链接可以自动读取；可以粘贴原文生成", { tone: "error" });
    return;
  }
  if (item.translated_text || item.ai_title) {
    const confirmed = await confirmAction({
      title: "重新处理这条收藏？",
      message: "会再次调用模型读取原帖，并覆盖现有的标题、译文和摘要。人工整理（状态、收藏原因、人工标签）不受影响。",
      confirmLabel: "重新处理"
    });
    if (!confirmed) return;
  }
  try {
    const result = await api.process([id]);
    if (result.accepted.length) {
      toast("已提交处理请求", { tone: "ok" });
      mergeItem({ ...getItem(id), status: "processing" });
      emit("item", id);
      if (id === currentId) {
        render(getItem(id));
        setTimeout(() => refreshCurrent(), 900);
      }
    } else {
      toast(errorLabel(result.rejected[0]?.error), { tone: "error" });
    }
  } catch (error) {
    toast(`提交处理请求失败：${errorLabel(error?.message)}`, { tone: "error" });
  }
}

export function pasteSource(id) {
  const textarea = h("textarea.source-input#source-text", { rows: 10, maxLength: 100000, placeholder: "把原帖正文粘贴到这里。会直接根据这段文字生成标题、译文和摘要，不再搜索 X。" });
  let submission = null;
  let baseRevision = getItem(id)?.cache_identity?.content_revision;
  openDialog({
    title: "粘贴原文生成",
    body: [textarea, h("p.dialog-detail", "会调用一次模型生成阅读内容；不会改动人工整理。")],
    initialFocus: () => textarea,
    actions: [
      { id: "cancel", label: "取消" },
      {
        id: "submit-source", label: "提交生成", primary: true,
        run: async () => {
          const text = textarea.value;
          if (!text.trim()) {
            toast("原文不能为空", { tone: "error" });
            textarea.focus();
            return false;
          }
          try {
            submission = await prepareSourceSubmission(id, text, submission, baseRevision);
            const result = await api.submitSource(id, submission);
            if (!result.accepted.length) {
              if (["input_changed", "operation_conflict"].includes(result.rejected[0]?.error)) {
                submission = null;
                baseRevision = null;
              }
              toast(errorLabel(result.rejected[0]?.error), { tone: "error" });
              return false;
            }
            toast("原文已保存，阅读内容已排队", { tone: "ok" });
            const item = getItem(id);
            if (item) {
              mergeItem({ ...item, status: "pending" });
              emit("item", id);
            }
            if (id === currentId) setTimeout(() => refreshCurrent(), 900);
            return true;
          } catch (error) {
            toast(`提交原文失败：${errorLabel(error?.message)}`, { tone: "error" });
            return false;
          }
        }
      }
    ]
  });
}

function openLightbox(images, start) {
  let index = start;
  const image = h("img.lightbox-img", { alt: "" });
  const counter = h("span.lightbox-count");
  const show = () => {
    queueImage(image, imagePath(images[index].key), { priority: -1 });
    counter.textContent = images.length > 1 ? `${index + 1} / ${images.length}` : "";
  };
  const step = (delta) => { index = (index + delta + images.length) % images.length; show(); };
  const body = h("div.lightbox", image,
    images.length > 1 ? h("button.icon-btn.lightbox-prev", { type: "button", "aria-label": "上一张", onclick: () => step(-1) }, icon("chevronLeft", 22)) : null,
    images.length > 1 ? h("button.icon-btn.lightbox-next", { type: "button", "aria-label": "下一张", onclick: () => step(1) }, icon("chevronRight", 22)) : null,
    counter);
  const { dialog } = openDialog({ title: "图片", body, wide: true });
  dialog.classList.add("dialog-image");
  dialog.addEventListener("keydown", (event) => {
    if (event.key === "ArrowLeft") step(-1);
    if (event.key === "ArrowRight") step(1);
  });
  show();
}

function copyLink(item) {
  const done = () => toast("已复制原帖链接", { tone: "ok" });
  if (navigator.clipboard?.writeText) navigator.clipboard.writeText(item.url).then(done, () => toast("复制失败", { tone: "error" }));
  else toast("浏览器不支持复制", { tone: "error" });
}

function openDetailMenu(anchor) {
  const item = getItem(currentId);
  if (!item) return;
  const processable = item.processable !== false && item.status !== "unsupported";
  openMenu(anchor, [
    { label: "打开原帖", icon: "external", kbd: "V", disabled: !sourceURL(item), run: openSource },
    { label: "复制原帖链接", icon: "copy", run: () => copyLink(item) },
    { label: "导出这条（Markdown）", icon: "download", run: () => hooks.exportItems([item.id]) },
    "separator",
    { heading: "阅读内容" },
    processable ? {
      label: item.translated_text ? "重新处理" : "立即处理", icon: "refresh", hint: "会调用模型读取原帖",
      disabled: item.status === "processing" || item.paid_call_unresolved, run: () => processItem(item.id)
    } : null,
    { label: "粘贴原文生成", icon: "clipboard", hint: "跳过 X 搜索，直接用粘贴的正文", run: () => pasteSource(item.id) },
    { label: "重新抓取原文", icon: "download", hint: "会重新读取来源", run: () => diagnostics.refreshSource(item.id) },
    "separator",
    { heading: "分类" },
    { label: "只重试分类", icon: "tag", hint: "0 次模型调用，只重新入队", run: () => diagnostics.retryClassification(item.id) },
    { label: "按当前策略重算", icon: "sparkles", hint: "0 次模型调用，先预览结果", run: () => diagnostics.replayPolicy(item.id) },
    { label: "查看分类依据与诊断", icon: "fileText", run: () => diagnostics.openDiagnostics() },
    "separator",
    { label: hooks.isFocusMode() ? "退出专注阅读" : "专注阅读", icon: hooks.isFocusMode() ? "minimize" : "maximize", kbd: "F", run: () => hooks.toggleFocus() }
  ]);
}

// --- Wiring --------------------------------------------------------------------------

export function initDetail(options) {
  hooks = options;
  byId("format-body").hidden=true;
  fetchJSON("/api/reading-capabilities").then(result=>{aiFormatting=result.ai_formatting===true;if(currentId&&getItem(currentId))render(getItem(currentId));}).catch(()=>{});
  byId("toggle-formatted").addEventListener("click",()=>{showUnformatted=!showUnformatted;render(getItem(currentId));});
  byId("reading-version").addEventListener("click",()=>{if(showUnformatted){showUnformatted=false;render(getItem(currentId));}});
  byId("format-body").addEventListener("click",async()=>{
    const id=currentId;const button=byId("format-body");button.disabled=true;
    try {
      await fetchJSON(`/api/bookmarks/${id}/presentation`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({force:Boolean(getItem(id)?.formatted_content)})});
      toast("已加入正文整理队列；完成前仍可阅读原内容。");
      if(currentId===id) await fetchDetail(id);
    }catch(error){toast(errorLabel(error.message));}
    finally {if(currentId===id)button.disabled=false;}
  });
  Object.assign(els, {
    pane: byId("detail-pane"), empty: byId("detail-empty"), article: byId("detail"), error: byId("detail-error"),
    errorText: byId("detail-error-text"), scroll: byId("detail-scroll"), back: byId("detail-back"),
    prev: byId("prev-item"), next: byId("next-item"), position: byId("detail-position"),
    statusButtons: [...byId("status-seg").querySelectorAll("button[data-status]")], mobileStatus: byId("mobile-actions"),
    source: byId("open-source"), menuButton: byId("detail-menu"), exportButton: byId("detail-export"),
    meta: byId("detail-meta"), title: byId("detail-title"), banner: byId("process-banner"), lede: byId("detail-lede"),
    figures: byId("figures"), bodyLoading: byId("body-loading"), body: byId("detail-body"),
    originalBlock: byId("original-block"), toggle: byId("original-toggle"), toggleLabel: byId("original-toggle-label"),
    original: byId("original"), links: byId("related-links"), nextLink: byId("detail-next"), nextTitle: byId("detail-next-title")
  });

  for (const button of [...els.statusButtons, ...els.mobileStatus.querySelectorAll("button[data-status]")]) {
    button.addEventListener("click", () => hooks.setStatus(currentId, button.dataset.status));
  }
  // Arrow keys move within the radio group, as the ARIA pattern expects.
  byId("status-seg").addEventListener("keydown", (event) => {
    if (!["ArrowLeft", "ArrowRight"].includes(event.key)) return;
    event.preventDefault();
    event.stopPropagation();
    const current = els.statusButtons.findIndex((button) => button === document.activeElement);
    const next = els.statusButtons[(current + (event.key === "ArrowRight" ? 1 : -1) + STATUSES.length) % STATUSES.length];
    next.focus();
    next.click();
  });
  els.prev.addEventListener("click", () => hooks.step(-1));
  els.next.addEventListener("click", () => hooks.step(1));
  els.back.addEventListener("click", () => hooks.closeDetail());
  els.menuButton.addEventListener("click", () => openDetailMenu(els.menuButton));
  els.exportButton.addEventListener("click", () => hooks.exportItems([currentId]));
  byId("mobile-more").addEventListener("click", (event) => openDetailMenu(event.currentTarget));
  els.nextLink.addEventListener("click", (event) => {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) return;
    event.preventDefault();
    hooks.step(1);
  });
  els.toggle.addEventListener("click", () => {
    const open = els.original.hidden;
    if (open) paragraphs(els.original, getItem(currentId)?.original_text || "", false);
    els.original.hidden = !open;
    els.toggle.classList.toggle("open", open);
    els.toggle.setAttribute("aria-expanded", String(open));
    els.toggleLabel.textContent = open ? "收起原文" : "展开原文";
  });
  byId("detail-retry").addEventListener("click", () => showItem(currentId));

  on("item", (id) => {
    if (id !== currentId) return;
    const item = getItem(id);
    if (item) render(item);
  });
  on("list:loaded", () => { if (currentId) renderPosition(); });
  on("list:more", () => { if (currentId) renderPosition(); });
  on("classification:changed", async (id) => {
    if (id === currentId && readerVisible() && await fetchDetail(id, { silent: true }) && id === currentId && readerVisible()) {
      curation.reloadRemote(id);
    }
  });

  setInterval(() => {
    if (!readerVisible()) return;
    const item = getItem(currentId);
    if (!isWorking(item)) { polls = 0; return; }
    if (polls++ >= MAX_POLLS) return;
    fetchDetail(currentId, { silent: true });
  }, POLL_INTERVAL);

  // A visible, idle detail can change in another client without any queue
  // activity. Only fetch the full article after its small identity changes.
  setInterval(async () => {
    if (!readerVisible() || identityBusy || Date.now() < identityDue) return;
    const id = currentId;
    const before = getItem(id);
    if (!before?.cache_identity || (isWorking(before) && polls < MAX_POLLS)) return;
    identityBusy = true;
    try {
      const remote = await api.identity(id);
      if (id !== currentId || !readerVisible()) return;
      const current = getItem(id);
      if (!current) return;
      const versionChanged = JSON.stringify(remote.cache_identity) !== JSON.stringify(current.cache_identity);
      const statusChanged = current.processable !== false && remote.status !== current.status;
      if (!versionChanged && !statusChanged && remote.updated_at === current.updated_at &&
          remote.paid_call_unresolved === current.paid_call_unresolved) { identityDelay = Math.min(60_000, identityDelay * 2); return; }
      identityDelay = IDENTITY_INTERVAL;
      if (await fetchDetail(id, { silent: true }) && id === currentId && readerVisible() && versionChanged) {
        curation.reloadRemote(id);
      }
    } catch {
      // Identity checks are best effort; the next visible tick retries.
    } finally {
      identityBusy = false;
      identityDue = Date.now() + identityDelay;
    }
  }, IDENTITY_INTERVAL);
  document.addEventListener("visibilitychange", syncReaderVisibility);

}
