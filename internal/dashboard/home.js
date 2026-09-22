(() => {
  "use strict";

  const ui = window.CairnUI;
  const PAGE_SIZE = 40;
  const FEATURE_COUNT = 4;
  const NARROW_FEATURE_COUNT = 1;
  const POLL_INTERVAL = 10000;
  const multiFilterKeys = ["topics", "content_functions", "carriers", "affordances", "entity_state"];
  const filterKeys = ["curation_status", ...multiFilterKeys, "form", "use", "source", "since", "uncertain"];
  const initialParams = new URLSearchParams(window.location.search);

  const state = {
    search: new URLSearchParams(window.location.search).get("q")?.trim() || "",
    items: [],
    nextBeforeID: null,
    loading: false,
    bucket: null,
    column: null
  };
  state.filters = Object.fromEntries(filterKeys.map((key) => [key, initialParams.get(key) || ""]));
  // Preserve shared URLs using the original single-topic parameter.
  if (initialParams.get("topic")) state.filters.topics = [...new Set([
    ...state.filters.topics.split(",").filter(Boolean), initialParams.get("topic")
  ])].join(",");
  const needsFilterContract = () => [...multiFilterKeys, "form", "use"].some(key => state.filters[key]);
  let activeRequest = null;
  let requestVersion = 0;
  let firstPageSnapshot = "";

  function filtered() {
    return Boolean(state.search) || Object.values(state.filters).some(Boolean);
  }

  function shot(item, className, blankClass) {
    const source = ui.firstImage(item);
    if (!source) return ui.element("div", blankClass);
    const box = ui.element("div", className);
    const image = document.createElement("img");
    image.src = source;
    image.alt = "";
    image.loading = "lazy";
    image.decoding = "async";
    // Fade in once decoded so a slow image does not pop into view.
    if (image.complete) image.classList.add("ready");
    image.addEventListener("load", () => image.classList.add("ready"));
    image.addEventListener("error", () => {
      box.replaceWith(ui.element("div", blankClass));
    });
    box.append(image);
    return box;
  }

  function highlight(text, terms) {
    const fragment = document.createDocumentFragment();
    if (terms.length === 0) {
      fragment.append(document.createTextNode(text));
      return fragment;
    }
    const lower = text.toLowerCase();
    // Lowercasing can change length for a few characters, which would shift
    // every subsequent slice. Fall back to an unmarked node when that happens
    // rather than highlighting the wrong span.
    if (lower.length !== text.length) {
      fragment.append(document.createTextNode(text));
      return fragment;
    }
    let cursor = 0;
    while (cursor < text.length) {
      let at = -1;
      let width = 0;
      for (const term of terms) {
        const found = lower.indexOf(term, cursor);
        if (found !== -1 && (at === -1 || found < at)) {
          at = found;
          width = term.length;
        }
      }
      if (at === -1) break;
      if (at > cursor) fragment.append(document.createTextNode(text.slice(cursor, at)));
      fragment.append(ui.element("mark", "", text.slice(at, at + width)));
      cursor = at + width;
    }
    if (cursor < text.length) fragment.append(document.createTextNode(text.slice(cursor)));
    return fragment;
  }

  function searchTerms() {
    return state.search
      .toLowerCase()
      .split(/\s+/)
      .filter((term) => term.length > 0);
  }

  function searchExcerpt(item, terms) {
    const summary = ui.displaySummary(item);
    if (!terms.length) return summary;
    for (const value of [item.summary, item.translated_text, item.original_text, item.classification?.entities?.join(" / "), item.classification?.why_suggestion, item.note, item.why]) {
      if (!value) continue;
      const lower = value.toLowerCase();
      const positions = terms.map((term) => lower.indexOf(term)).filter((at) => at >= 0);
      if (!positions.length) continue;
      const start = Math.max(0, Math.min(...positions) - 60);
      return { text: (start ? "…" : "") + value.slice(start, start + 220) + (start + 220 < value.length ? "…" : ""), wait: false };
    }
    return summary;
  }

  function featureCard(item) {
    const title = ui.displayTitle(item);
    const summary = ui.displaySummary(item);
    const card = ui.element("a", "fcard");
    card.href = ui.bookmarkPath(item.id);
    card.append(shot(item, "fshot", "fshot fshot-blank"));
    card.append(ui.element("h2", `fcard-title${title.raw ? " raw" : ""}`, title.text));
    card.append(ui.metadata(item));
    if (item.why || item.note) card.append(ui.element("p", "fcard-note", item.why || item.note));
    card.append(ui.element("p", `fcard-sum${summary.wait ? " wait" : ""}`, summary.text));
    return card;
  }

  function compactItem(item) {
    const title = ui.displayTitle(item);
    const summary = ui.displaySummary(item);
    const row = ui.element("a", "item");
    row.href = ui.bookmarkPath(item.id);
    row.append(shot(item, "item-shot", "item-blank"));
    const body = ui.element("div");
    body.append(ui.element("h3", `item-title${title.raw ? " raw" : ""}`, title.text));
    body.append(ui.element("p", `item-sum${summary.wait ? " wait" : ""}`, item.why || item.note || summary.text));
    body.append(ui.metadata(item));
    row.append(body);
    return row;
  }

  function resultEntry(item, terms) {
    const title = ui.displayTitle(item);
    const summary = searchExcerpt(item, terms);
    const entry = ui.element("a", "entry");
    entry.href = ui.bookmarkPath(item.id);
    const body = ui.element("div");
    const heading = ui.element("h2", `entry-title${title.raw ? " raw" : ""}`);
    heading.append(highlight(title.text, terms));
    body.append(heading);
    body.append(ui.metadata(item));
    if (item.why || item.note) {
      const note = ui.element("p", "entry-note");
      note.append(highlight(item.why || item.note, terms));
      body.append(note);
    }
    const text = ui.element("p", `entry-sum${summary.wait ? " wait" : ""}`);
    text.append(highlight(summary.text, terms));
    body.append(text);
    entry.append(body);
    entry.append(shot(item, "entry-shot", "entry-shot entry-blank"));
    return entry;
  }

  function appendStream(items) {
    const stream = ui.byId("stream");
    // Build the new rows in a detached fragment and attach them once. Appending
    // each card to a live subtree forces the browser to revisit style and
    // layout for every item during a scroll-triggered page load.
    const fragment = document.createDocumentFragment();
    if (filtered()) {
      const terms = searchTerms();
      let list = stream.querySelector(".results");
      if (!list) {
        list = ui.element("div", "results");
        fragment.append(list);
      }
      const rows = document.createDocumentFragment();
      for (const item of items) rows.append(resultEntry(item, terms));
      list.append(rows);
      stream.append(fragment);
      return;
    }
    for (const item of items) {
      const bucket = ui.bucketLabel(item.created_at);
      if (bucket !== state.bucket) {
        state.bucket = bucket;
        const band = ui.element("div", "band");
        band.append(document.createTextNode(bucket));
        band.append(ui.element("i"));
        fragment.append(band);
        state.column = ui.element("div", "cols");
        fragment.append(state.column);
      }
      state.column.append(compactItem(item));
    }
    stream.append(fragment);
  }

  function renderPage(page, append) {
    const items = Array.isArray(page.items) ? page.items : [];
    const counts = page.counts || {};
    const stream = ui.byId("stream");
    const feature = ui.byId("feature");

    if (!append) {
      state.items = items;
      state.bucket = null;
      state.column = null;
      stream.replaceChildren();
      feature.replaceChildren();
      ui.byId("feature-band").hidden = true;
      ui.byId("result-count").hidden = true;
      ui.byId("empty").hidden = items.length > 0;

      if (filtered()) {
        const count = ui.byId("result-count");
        count.textContent = page.next_before_id
          ? `找到 ${items.length} 条以上`
          : `找到 ${items.length} 条`;
        count.hidden = items.length === 0;
        appendStream(items);
      } else if (items.length > 0) {
        const band = ui.byId("feature-band");
        band.firstChild.textContent = ui.bucketLabel(items[0].created_at);
        ui.byId("feature-band-count").textContent = `共 ${counts.total ?? items.length} 条收藏`;
        band.hidden = false;
        const featured = window.innerWidth < 720 ? NARROW_FEATURE_COUNT : FEATURE_COUNT;
        for (const item of items.slice(0, featured)) feature.append(featureCard(item));
        appendStream(items.slice(featured));
      }
    } else {
      state.items = state.items.concat(items);
      appendStream(items);
    }

    const attention = (counts.failed ?? 0) + (counts.exhausted ?? 0) > 0;
    ui.byId("backstage-link").classList.toggle("attention", attention);
    if (filtered()) {
      ui.byId("result-count").textContent = `已显示 ${state.items.length} 条${page.next_before_id ? "，还有更多" : ""}`;
      ui.byId("result-count").hidden = state.items.length === 0;
    }
    ui.byId("export-markdown").title = `导出已加载的 ${state.items.length} 条收藏`;
  }

  async function load({ append = false, silent = false } = {}) {
    if (append && state.loading) return;
    if (append && !state.nextBeforeID) return;
    activeRequest?.abort();
    activeRequest = new AbortController();
    const version = ++requestVersion;
    state.loading = true;
    ui.byId("export-markdown").disabled = true;
    ui.byId("load-error").hidden = true;
    if (!silent) ui.byId("loading").hidden = false;
    ui.byId("tail").hidden = true;
    try {
      const params = new URLSearchParams({ limit: String(PAGE_SIZE) });
      if (!state.search) params.set("view", "summary");
      if (state.search) params.set("q", state.search);
      for (const [key, value] of Object.entries(state.filters)) if (value) params.set(key, value);
      if (needsFilterContract()) params.set("filter_contract_version", "1");
      if (append && state.nextBeforeID) params.set("before_id", String(state.nextBeforeID));
      const page = await ui.fetchJSON(`/api/bookmarks?${params}`, { signal: activeRequest.signal });
      if (version !== requestVersion) return;
      if (needsFilterContract() && page.filter_contract_version !== 1) throw new Error("unsupported_filter_contract");
      if (!append) {
        const snapshot = JSON.stringify(page);
        if (silent && snapshot === firstPageSnapshot) return;
        firstPageSnapshot = snapshot;
      }
      state.nextBeforeID = page.next_before_id ?? null;
      renderPage(page, append);
      ui.byId("tail").hidden = Boolean(state.nextBeforeID) || state.items.length === 0;
    } catch (error) {
      if (version !== requestVersion || error.name === "AbortError") return;
      if (!silent) {
        ui.byId("load-error").hidden = false;
        ui.byId("load-error-text").textContent = error.message === "unsupported_filter_contract"
          ? "服务暂不支持完整筛选，请更新服务或清除筛选后浏览。" : "读取收藏失败";
      }
    } finally {
      if (version === requestVersion) {
        state.loading = false;
        ui.byId("loading").hidden = true;
        ui.byId("export-markdown").disabled = state.items.length === 0;
      }
    }
  }

  function runSearch(value) {
    const next = value.trim();
    if (next === state.search) return;
    state.search = next;
    changeFilters();
  }

  function changeFilters() {
    state.nextBeforeID = null;
    state.items = [];
    firstPageSnapshot = "";
    ui.byId("stream").replaceChildren();
    ui.byId("feature").replaceChildren();
    ui.byId("feature-band").hidden = true;
    ui.byId("result-count").hidden = true;
    ui.byId("empty").hidden = true;
    const params = new URLSearchParams();
    if (state.search) params.set("q", state.search);
    for (const [key, value] of Object.entries(state.filters)) if (value) params.set(key, value);
    window.history.replaceState(null, "", params.size ? `/?${params}` : "/");
    ui.byId("clear-filters").hidden = !filtered();
    load();
  }

  for (const key of filterKeys) {
    const control = ui.byId(`filter-${key}`);
    if (key === "uncertain") control.checked = state.filters[key] === "true";
    else if (multiFilterKeys.includes(key)) {
      for (const option of control.options) option.selected = state.filters[key].split(",").includes(option.value);
    } else if (key === "since" && state.filters[key]) {
      const date = new Date(state.filters[key]);
      if (Number.isFinite(date.getTime())) control.value = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
    } else control.value = state.filters[key];
    control.addEventListener("change", () => {
      if (key === "uncertain") state.filters[key] = control.checked ? "true" : "";
      else if (key === "since") state.filters[key] = control.value ? new Date(`${control.value}T00:00:00`).toISOString() : "";
      else if (multiFilterKeys.includes(key)) state.filters[key] = [...control.selectedOptions].map(option => option.value).join(",");
      else state.filters[key] = control.value;
      changeFilters();
    });
  }
  ui.byId("clear-filters").addEventListener("click", () => {
    clearTimeout(searchTimer);
    state.search = "";
    ui.byId("find").value = "";
    for (const key of filterKeys) {
      state.filters[key] = "";
      const control = ui.byId(`filter-${key}`);
      control.value = "";
      if (multiFilterKeys.includes(key)) for (const option of control.options) option.selected = false;
      if (key === "uncertain") control.checked = false;
    }
    changeFilters();
  });
  ui.byId("export-markdown").addEventListener("click", async () => {
    // The server-side export carries the full effective v2 dimensions, the
    // human origin and partial counts under the current filters. The local
    // builder stays as a fallback for a backend without the endpoint.
    const button = ui.byId("export-markdown");
    button.disabled = true;
    try {
      const params = new URLSearchParams({ limit: String(Math.max(state.items.length, PAGE_SIZE)) });
      if (state.search) params.set("q", state.search);
      for (const [key, value] of Object.entries(state.filters)) if (value) params.set(key, value);
      if (needsFilterContract()) params.set("filter_contract_version", "1");
      await ui.exportServerMarkdown(params);
    } catch (error) {
      if (error?.message === "export_unsupported") {
        try {
          await ui.exportMarkdown(state.items, Boolean(state.nextBeforeID));
        } catch (_) {
          ui.showToast("导出失败，请重试", true);
        }
      } else {
        ui.showToast("导出失败，请重试", true);
      }
    } finally {
      button.disabled = state.items.length === 0;
    }
  });
  ui.byId("retry-load").addEventListener("click", () => load({ append: state.items.length > 0 }));

  let searchTimer = 0;
  ui.byId("find").addEventListener("input", (event) => {
    clearTimeout(searchTimer);
    const value = event.target.value;
    searchTimer = setTimeout(() => runSearch(value), 260);
  });
  ui.byId("find-form").addEventListener("submit", (event) => {
    event.preventDefault();
    clearTimeout(searchTimer);
    runSearch(ui.byId("find").value);
  });
  ui.byId("find").addEventListener("keydown", (event) => {
    if (event.key !== "Escape") return;
    event.target.value = "";
    clearTimeout(searchTimer);
    runSearch("");
  });

  const sentinel = ui.byId("sentinel");
  if (typeof IntersectionObserver === "function") {
    new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) load({ append: true });
    }, { rootMargin: "600px" }).observe(sentinel);
  }

  // Back-to-top: reveal only after the user has scrolled past the featured
  // strip, and hide again near the top so it never floats over empty space.
  const toTop = ui.byId("to-top");
  if (toTop) {
    const prefersReduced = typeof window.matchMedia === "function" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    const onScroll = () => toTop.classList.toggle("visible", window.scrollY > 560);
    onScroll();
    toTop.hidden = false;
    window.addEventListener("scroll", onScroll, { passive: true });
    toTop.addEventListener("click", () => {
      if (typeof window.scrollTo === "function") window.scrollTo({ top: 0, behavior: prefersReduced ? "auto" : "smooth" });
    });
  }

  if (state.search) ui.byId("find").value = state.search;
  ui.byId("clear-filters").hidden = !filtered();
  ui.loadTaxonomy().then((catalog) => {
    for (const [key, dimension, label] of [["form", "forms", "全部形态"], ["use", "uses", "全部用途"]]) {
      const control = ui.byId(`filter-${key}`);
      ui.fillTerms(control, catalog[dimension], label, true);
      control.value = state.filters[key];
      control.disabled = false;
    }
    const byPath = new Map(state.items.map((item) => [`/bookmarks/${item.id}`, item]));
    for (const holder of document.querySelectorAll(".bookmark-meta")) {
      // Labels become available after the independent vocabulary request.
      const anchor = holder.closest("a");
      const item = byPath.get(new URL(anchor.href).pathname);
      if (item) holder.replaceWith(ui.metadata(item));
    }
  }).catch(() => ui.showToast("读取标签词表失败", true));
  ui.fetchJSON("/api/v2-taxonomy").then(catalog => {
    if (catalog.available === false) throw new Error("unsupported");
    for (const dimension of ["topics", "content_functions", "carriers", "affordances"]) {
      if (!Array.isArray(catalog[dimension])) throw new Error("unsupported");
      const control = ui.byId(`filter-${dimension}`);
      control.replaceChildren();
      const requested = state.filters[dimension].split(",").filter(Boolean);
      const known = new Set();
      for (const term of catalog[dimension]) {
        const option = document.createElement("option");
        option.value = term.id;
        option.textContent = `${term.label || term.id}${term.deprecated || term.active === false ? "（已停用）" : ""}`;
        option.selected = requested.includes(term.id);
        control.append(option); known.add(term.id);
      }
      // A saved URL never silently loses an unknown requested ID.
      for (const id of requested.filter(id => !known.has(id))) {
        const option = document.createElement("option");
        option.value = id; option.textContent = `${id}（词表不可用）`; option.selected = true;
        control.append(option);
      }
      control.disabled = false;
    }
  }).catch(() => {
    ui.byId("filter-capability").hidden = false;
    ui.byId("filter-capability").textContent = "多维词表暂不可用；已选筛选条件仍保留，可重试或清除。";
  });
  load();
  setInterval(() => {
    if (document.hidden || filtered() || state.loading) return;
    if (state.items.length > PAGE_SIZE) return;
    if (window.scrollY > 240) return;
    const waiting = state.items
      .slice(0, PAGE_SIZE)
      .some((item) => item.status === "pending" || item.status === "processing");
    if (waiting) load({ silent: true });
  }, POLL_INTERVAL);
})();
