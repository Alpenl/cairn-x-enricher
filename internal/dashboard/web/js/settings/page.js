// Full-page settings: #/settings/<section>[/<sub>]. The workspace stays
// mounted (hidden) underneath, so list scroll, selection, filters and open
// panels survive a round trip; 「返回收藏库」 steps back through history to
// the entry we came from instead of stacking a new one.
import { byId, clear, h } from "../dom.js";
import { icon } from "../icons.js";
import { toast } from "../ui.js";
import { TABS, GROUPS, SETTING_INDEX, tabById, closePopovers, fold, marked, readReceipt, dropReceipt, send } from "./core.js";
import { db, loadCollectionsData } from "./data.js";

const sections = {};
export function registerSection(id, impl) {
  sections[id] = impl;
}
const page = () => byId("settings-page");
const app = () => byId("app");
const router = { onExit: null, open: false, section: null, sub: "", idx: history.state?.cairnIdx ?? 0, wsIdx: null, title: "", ws: null, rendered: null };
const SETTINGS = /^#\/settings(?:\/|$)/;

export const isSettingsHash = (hash = location.hash) => SETTINGS.test(hash);
export const settingsOpen = () => router.open;
export const currentSection = () => router.section;
export const sectionPath = (id, sub) => `#/settings/${id}${sub ? `/${encodeURIComponent(sub)}` : ""}`;
const baseURL = () => location.pathname + location.search;

function parse(hash) {
  const parts = hash.replace(/^#\/?/, "").split("/");
  const dec = (x) => { try { return decodeURIComponent(x); } catch { return x; } };
  const id = parts[1] || "";
  const known = TABS.some((t) => t.id === id);
  return { section: known ? id : router.section || "tags", sub: known && parts[2] ? dec(parts.slice(2).join("/")) : "", unknown: Boolean(id) && !known };
}

/** Public entry: open a settings section (last visited by default). */
export function openSettings(section, sub = "", { replace = false } = {}) {
  section = section || router.section || "tags";
  const hash = sectionPath(section, sub);
  if (router.open && router.section === section && (!sub || sub === router.sub)) {
    if (sub) sections[section]?.select?.(sub);
    focusHeading();
    return;
  }
  if (!router.open) {
    router.ws = { url: baseURL(), focus: document.activeElement, scroll: scrollSnapshot() };
    router.wsIdx = router.idx;
  }
  if (replace) history.replaceState({ ...(history.state || {}), cairnIdx: router.idx }, "", baseURL() + hash);
  else {
    router.idx += 1;
    history.pushState({ cairnIdx: router.idx }, "", baseURL() + hash);
  }
  applyRoute();
}
/** 「返回收藏库」 */
export function leaveSettings() {
  if (!router.open) return Promise.resolve();
  commitPending();
  if (router.wsIdx != null && router.wsIdx < router.idx) {
    const done = new Promise((resolve) => { router.onExit = resolve; setTimeout(resolve, 1000); });
    history.go(router.wsIdx - router.idx);
    return done;
  }
  {
    router.idx += 1;
    history.pushState({ cairnIdx: router.idx }, "", baseURL());
    applyRoute();
  }
  return Promise.resolve();
}
function scrollSnapshot() {
  return ["list-scroll", "detail-scroll", "inspector-scroll"].map((id) => [id, byId(id)?.scrollTop ?? 0]);
}

export function applyRoute() {
  if (!isSettingsHash()) {
    if (router.open) exit();
    router.wsIdx = null;
    return;
  }
  const r = parse(location.hash);
  const entering = !router.open;
  if (entering) enter();
  else closePopovers();
  const changed = entering || r.section !== router.section;
  if (changed && !entering) commitPending();
  router.section = r.section;
  router.sub = r.sub;
  document.title = `${tabById(r.section).title} · 设置 · Cairn`;
  if (r.unknown) toast(`没有这个设置分区，已打开${tabById(r.section).title}`);
  if (changed) render();
  else if (r.sub) sections[r.section]?.select?.(r.sub);
}
function enter() {
  router.open = true;
  router.title = router.title || document.title;
  if (!router.ws) router.ws = { url: baseURL(), focus: document.activeElement, scroll: scrollSnapshot() };
  closePopovers();
  for (const d of document.querySelectorAll("wa-dialog[open]")) d.open = false;
  app().classList.add("route-settings");
  app().hidden = true;
  document.body.classList.add("settings-open");
  page().hidden = false;
  playIn(page());
  syncService();
  refreshBadge();
}
function exit() {
  commitPending();
  closePopovers();
  closeResults();
  router.open = false;
  router.rendered = null;
  page().hidden = true;
  app().hidden = false;
  app().classList.remove("route-settings");
  document.body.classList.remove("settings-open");
  document.title = router.title || "Cairn";
  sections[router.section]?.leave?.();
  const ws = router.ws;
  router.ws = null;
  for (const [id, top] of ws?.scroll || []) { const n = byId(id); if (n) n.scrollTop = top; }
  playIn(app());
  const f = ws?.focus;
  if (f && f !== document.body && document.contains(f) && f.getClientRects().length) f.focus({ preventScroll: true });
  else byId("settings-button")?.focus({ preventScroll: true });
  const done = router.onExit;
  router.onExit = null;
  done?.();
}
function playIn(node) {
  if (matchMedia("(prefers-reduced-motion: reduce)").matches) return;
  node.classList.remove("view-in");
  void node.offsetWidth;
  node.classList.add("view-in");
  node.addEventListener("animationend", () => node.classList.remove("view-in"), { once: true });
}
function commitPending() {
  sections[router.section]?.commit?.();
}
const focusHeading = () => byId("sp-title")?.focus({ preventScroll: true });

/** Keep the address bar on the canonical route (selection included) without new entries. */
export function syncUrl() {
  if (!router.open) return;
  const sub = sections[router.section]?.canonicalSub?.() || "";
  router.sub = sub;
  const want = sectionPath(router.section, sub);
  if (location.hash !== want) history.replaceState(history.state, "", baseURL() + want);
}

function render() {
  const tab = tabById(router.section);
  renderNav();
  byId("sp-title").textContent = tab.title;
  byId("sp-desc").textContent = tab.desc;
  byId("sp-crumb").textContent = tab.title;
  const dd = byId("sp-section-dd");
  dd.value = tab.id;
  const body = byId("sp-body");
  body.className = `sp-body${tab.md ? " md-body" : ""} sec-${tab.id}`;
  page().classList.toggle("sp-single", !tab.md);
  body.scrollTop = 0;
  setActions([]);
  const impl = sections[tab.id];
  router.rendered = tab.id;
  if (!impl) {
    clear(body, h("div.sp-loading", "正在载入…"));
    return;
  }
  impl.render(body, { sub: router.sub, syncUrl, setActions, refreshBadge, openSettings, leaveSettings });
  focusHeading();
}
export function setActions(nodes) {
  const box = byId("sp-actions");
  const receipt = readReceipt();
  const items = [nodes, receipt ? receiptBar(receipt) : null].flat().filter(Boolean);
  clear(box, items);
  box.hidden = !items.length;
}
function receiptBar(receipt) {
  return h("div.sp-note-warn", { role: "status" }, icon("alert", 14),
    h("span", "有一项修改还没有收到服务确认。"),
    h("button.btn.btn-ghost.btn-sm", { type: "button", onclick: async () => {
      try { await send(receipt.path, receipt.body, receipt.method); toast("已确认保存", { tone: "ok" }); } catch {}
      rerender();
    } }, "重试"),
    h("button.btn.btn-ghost.btn-sm", { type: "button", onclick: () => { dropReceipt(); rerender(); } }, "放弃"));
}
export function rerender() {
  if (router.open) render();
}

function renderNav() {
  const pend = pendingTotal();
  clear(byId("sp-nav-list"), GROUPS.map((g, gi) => h("div.nav-group", { role: "group", "aria-labelledby": `spg-${gi}` },
    h("div.nav-label", { id: `spg-${gi}` }, g),
    TABS.filter((t) => t.group === g).map((t) => {
      const on = t.id === router.section, n = t.id === "rules" ? pend : 0;
      return h("a.nav-item.sp-link", { href: sectionPath(t.id), class: on ? "active" : "", dataset: { tab: t.id }, "aria-current": on ? "page" : null, "aria-description": n ? `${n} 条待审核` : null,
        onclick: (e) => { if (e.metaKey || e.ctrlKey || e.shiftKey) return; e.preventDefault(); openSettings(t.id); } },
        icon(t.icon, 16), h("span.nav-name", t.label), n ? h("span.pend", { title: `${n} 条收藏按规则等待审核` }, String(n)) : null);
    }))));
}
const pendingTotal = () => db.collections.filter((c) => !c.deleted).reduce((s, c) => s + (Number(c.pending_review_count) || 0), 0);
export async function refreshBadge() {
  try { await loadCollectionsData(); } catch { return; }
  if (!router.open) return;
  const link = byId("sp-nav-list").querySelector('.sp-link[data-tab="rules"]');
  if (!link) return;
  const n = pendingTotal();
  let p = link.querySelector(".pend");
  if (!n) { p?.remove(); link.removeAttribute("aria-description"); return; }
  if (!p) { p = h("span.pend"); link.append(p); }
  p.textContent = String(n);
  p.title = `${n} 条收藏按规则等待审核`;
  link.setAttribute("aria-description", `${n} 条待审核`);
}
function syncService() {
  const dot = document.querySelector("#service-link .status-dot");
  const text = byId("service-link")?.textContent?.trim();
  const mine = byId("sp-service");
  if (dot) mine.querySelector(".status-dot").dataset.tone = dot.dataset.tone || "ok";
  if (text) byId("sp-service-text").textContent = text;
}

/* ---------- 搜索设置 ---------- */
let hits = [], hl = -1;
function results(q) {
  const ql = fold(q.trim());
  if (!ql) return [];
  const out = SETTING_INDEX.filter((s) => fold(`${s.label} ${s.kw} ${tabById(s.section).title}`).includes(ql)).map((s) => ({ kind: "setting", ...s }));
  for (const t of db.tags) if (!t.more && fold([t.label, ...t.aliases].join(" ")).includes(ql)) out.push({ kind: "tag", section: "tags", sub: `${t.d}:${t.id}`, label: t.label, note: t.archived ? "已归档" : "标签", dot: t.d });
  for (const c of db.collections) if (!c.deleted && fold(c.name).includes(ql)) out.push({ kind: "coll", section: "rules", sub: c.id, label: c.name, note: "合集规则" });
  return out.slice(0, 30);
}
function renderResults() {
  const box = byId("sp-results"), input = byId("sp-search"), q = input.value;
  hits = results(q);
  if (!q.trim()) return closeResults();
  hl = hits.length ? Math.max(0, Math.min(hl, hits.length - 1)) : -1;
  const groups = [["setting", "设置项"], ["tag", "标签"], ["coll", "合集"]];
  let i = -1;
  clear(box, hits.length
    ? groups.map(([k, label]) => {
        const list = hits.filter((x) => x.kind === k);
        if (!list.length) return null;
        return [h("div.sp-res-group", label), list.map((x) => {
          i = hits.indexOf(x);
          const idx = i;
          return h("button.sp-res", { type: "button", role: "option", id: `sp-res-${idx}`, class: idx === hl ? "hl" : "", "aria-selected": String(idx === hl),
            onmousemove: () => { if (hl !== idx) { hl = idx; paintHl(); } }, onclick: () => pickResult(idx) },
            x.dot ? h("span.type-dot", { class: `type-${x.dot === "custom" ? "custom" : x.dot}` }) : icon(x.kind === "coll" ? "folder" : tabById(x.section).icon, 15),
            h("span.main", h("span.lbl", marked(x.label, q.trim())), x.note ? h("span.note", x.note) : null),
            h("span.where", tabById(x.section).title));
        })];
      })
    : h("div.sp-res-empty", `没有找到「${q.trim()}」相关的设置`));
  box.hidden = false;
  input.setAttribute("aria-expanded", "true");
  input.setAttribute("aria-activedescendant", hl >= 0 ? `sp-res-${hl}` : "");
}
function paintHl() {
  for (const b of byId("sp-results").querySelectorAll(".sp-res")) {
    const on = b.id === `sp-res-${hl}`;
    b.classList.toggle("hl", on);
    b.setAttribute("aria-selected", String(on));
    if (on) b.scrollIntoView({ block: "nearest" });
  }
  byId("sp-search").setAttribute("aria-activedescendant", hl >= 0 ? `sp-res-${hl}` : "");
}
function closeResults() {
  const box = byId("sp-results");
  if (!box) return;
  box.hidden = true;
  box.replaceChildren();
  byId("sp-search").setAttribute("aria-expanded", "false");
  hl = -1;
}
function pickResult(i) {
  const x = hits[i];
  if (!x) return;
  const input = byId("sp-search");
  input.value = "";
  closeResults();
  if (x.kind === "setting") {
    openSettings(x.section);
    highlight(x.key);
  } else openSettings(x.section, x.sub);
  if (x.kind !== "setting") sections[x.section]?.select?.(x.sub, { focus: true });
}
/** Scroll to and briefly highlight a [data-setting] anchor, focusing its control. */
export function highlight(key) {
  requestAnimationFrame(() => {
    const node = page().querySelector(`[data-setting="${CSS.escape(key)}"]`);
    if (!node) return;
    node.scrollIntoView({ block: "center", behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
    node.classList.remove("sp-hl");
    void node.offsetWidth;
    node.classList.add("sp-hl");
    node.addEventListener("animationend", () => node.classList.remove("sp-hl"), { once: true });
    const control = node.matches("button, input, textarea, select, a") ? node : node.querySelector("button:not(:disabled), input, textarea, select");
    control?.focus({ preventScroll: true });
  });
}

export function initSettingsPage() {
  const dd = byId("sp-section-dd");
  clear(dd, GROUPS.map((g) => h("optgroup", { label: g }, TABS.filter((t) => t.group === g).map((t) => h("option", { value: t.id }, t.title)))));
  dd.addEventListener("change", () => openSettings(dd.value));
  byId("sp-back").addEventListener("click", (e) => { e.preventDefault(); leaveSettings(); });
  page().querySelector(".brand").addEventListener("click", (e) => { e.preventDefault(); leaveSettings(); });
  const input = byId("sp-search");
  input.addEventListener("input", () => { hl = 0; renderResults(); });
  input.addEventListener("focus", () => { if (input.value.trim()) renderResults(); });
  input.addEventListener("blur", () => setTimeout(() => { if (!byId("sp-search-wrap").contains(document.activeElement)) closeResults(); }, 120));
  input.addEventListener("keydown", (e) => {
    if (e.isComposing) return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (!hits.length) return;
      hl = (hl + (e.key === "ArrowDown" ? 1 : -1) + hits.length) % hits.length;
      paintHl();
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (hl >= 0) pickResult(hl);
    } else if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      if (input.value) { input.value = ""; closeResults(); } else input.blur();
    }
  });
  document.addEventListener("keydown", (e) => {
    if (!router.open || e.defaultPrevented || e.isComposing) return;
    if (document.querySelector("wa-dialog[open], dialog[open]")) return;
    const t = e.target;
    const typing = t instanceof Element && (t.matches("input, textarea, select, [contenteditable='true']"));
    if (e.key === "Escape" && !typing) {
      if (document.querySelector(".sp-menu:not([hidden]), .pick-pop:not([hidden])")) return;
      e.preventDefault();
      leaveSettings();
    } else if (e.key === "/" && !typing && !e.metaKey && !e.ctrlKey) {
      e.preventDefault();
      input.focus();
      input.select();
    }
  });
  window.addEventListener("popstate", (e) => {
    router.idx = e.state?.cairnIdx ?? 0;
    applyRoute();
  });
  window.addEventListener("hashchange", () => applyRoute());
  applyRoute();
}
