(() => {
  "use strict";

  const ui = window.CairnUI;
  const PAGE_SIZE = 40;
  const FEATURE_COUNT = 4;
  const NARROW_FEATURE_COUNT = 1;
  const POLL_INTERVAL = 10000;

  const state = {
    search: new URLSearchParams(window.location.search).get("q")?.trim() || "",
    items: [],
    nextBeforeID: null,
    loading: false,
    bucket: null,
    column: null
  };

  function shot(item, className, blankClass) {
    const source = ui.firstImage(item);
    if (!source) return ui.element("div", blankClass);
    const box = ui.element("div", className);
    const image = document.createElement("img");
    image.src = source;
    image.alt = "";
    image.loading = "lazy";
    image.decoding = "async";
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
      const mark = document.createElement("mark");
      mark.textContent = text.slice(at, at + width);
      fragment.append(mark);
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

  function featureCard(item) {
    const title = ui.displayTitle(item);
    const summary = ui.displaySummary(item);
    const card = ui.element("a", "fcard");
    card.href = `/bookmarks/${item.id}`;
    card.append(shot(item, "fshot", "fshot fshot-blank"));
    card.append(ui.element("h2", `fcard-title${title.raw ? " raw" : ""}`, title.text));
    if (item.note) card.append(ui.element("p", "fcard-note", item.note));
    card.append(ui.element("p", `fcard-sum${summary.wait ? " wait" : ""}`, summary.text));
    return card;
  }

  function compactItem(item) {
    const title = ui.displayTitle(item);
    const summary = ui.displaySummary(item);
    const row = ui.element("a", "item");
    row.href = `/bookmarks/${item.id}`;
    row.append(shot(item, "item-shot", "item-blank"));
    const body = ui.element("div");
    body.append(ui.element("h3", `item-title${title.raw ? " raw" : ""}`, title.text));
    body.append(ui.element("p", `item-sum${summary.wait ? " wait" : ""}`, item.note || summary.text));
    row.append(body);
    return row;
  }

  function resultEntry(item, terms) {
    const title = ui.displayTitle(item);
    const summary = ui.displaySummary(item);
    const entry = ui.element("a", "entry");
    entry.href = `/bookmarks/${item.id}`;
    const body = ui.element("div");
    const heading = ui.element("h2", `entry-title${title.raw ? " raw" : ""}`);
    heading.append(highlight(title.text, terms));
    body.append(heading);
    if (item.note) {
      const note = ui.element("p", "entry-note");
      note.append(highlight(item.note, terms));
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
    if (state.search) {
      const terms = searchTerms();
      let list = stream.querySelector(".results");
      if (!list) {
        list = ui.element("div", "results");
        stream.append(list);
      }
      for (const item of items) list.append(resultEntry(item, terms));
      return;
    }
    for (const item of items) {
      const bucket = ui.bucketLabel(item.created_at);
      if (bucket !== state.bucket) {
        state.bucket = bucket;
        const band = ui.element("div", "band");
        band.append(document.createTextNode(bucket));
        band.append(ui.element("i"));
        stream.append(band);
        state.column = ui.element("div", "cols");
        stream.append(state.column);
      }
      state.column.append(compactItem(item));
    }
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

      if (state.search) {
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
  }

  async function load({ append = false, silent = false } = {}) {
    if (state.loading) return;
    if (append && !state.nextBeforeID) return;
    state.loading = true;
    if (!silent) ui.byId("loading").hidden = false;
    ui.byId("tail").hidden = true;
    try {
      const params = new URLSearchParams({ limit: String(PAGE_SIZE) });
      if (state.search) params.set("q", state.search);
      if (append && state.nextBeforeID) params.set("before_id", String(state.nextBeforeID));
      const page = await ui.fetchJSON(`/api/bookmarks?${params}`);
      state.nextBeforeID = page.next_before_id ?? null;
      renderPage(page, append);
      ui.byId("tail").hidden = Boolean(state.nextBeforeID) || state.items.length === 0;
    } catch (_) {
      if (!silent) ui.showToast("读取收藏失败，请检查 Cloudflare 后端", true);
    } finally {
      state.loading = false;
      ui.byId("loading").hidden = true;
    }
  }

  function runSearch(value) {
    const next = value.trim();
    if (next === state.search) return;
    state.search = next;
    state.nextBeforeID = null;
    const url = next ? `/?q=${encodeURIComponent(next)}` : "/";
    window.history.replaceState(null, "", url);
    load();
  }

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

  if (state.search) ui.byId("find").value = state.search;
  load();
  setInterval(() => {
    if (document.hidden || state.search || state.loading) return;
    if (window.scrollY > 240) return;
    const waiting = state.items
      .slice(0, PAGE_SIZE)
      .some((item) => item.status === "pending" || item.status === "processing");
    if (waiting) load({ silent: true });
  }, POLL_INTERVAL);
})();
