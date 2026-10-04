import assert from "node:assert/strict";
import { chromium } from "playwright";
import { createFixtureState, startFixtureServer } from "../../tests/browser/fixture-server.mjs";
import { taxonomyV2 } from "../../tests/browser/fixture-data.mjs";

const catalog = taxonomyV2();
catalog.topics = [
  { id: "ai_coding", label: "AI编程", navigation: true },
  { id: "agent_workflow", label: "Agent配置与自动化（Workflow automation）", navigation: true },
  { id: "knowledge", label: "信息采集与知识库", navigation: true },
  { id: "portrait", label: "AI写真", granularity: "specific", navigation: false }
].map(term => ({ ...term, active: true }));
catalog.resource_kinds = [{ id: "reference", label: "参考资料", active: true }];
const { server, url } = await startFixtureServer({ state: createFixtureState({ count: 96 }) });
const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || "/usr/bin/google-chrome", args: ["--no-sandbox"] });
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 860 } });
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.route("**/api/v2-taxonomy", route => route.fulfill({ json: catalog, headers: { "X-Cairn-Topic-Granularity": "1", "X-Cairn-Tag-System": "1" } }));
  let count = 3, specific = 1;
  await page.route("**/api/tag-counts?*", async route => {
    const value = count, refinement = specific;
    await new Promise(resolve => setTimeout(resolve, 300));
    await route.fulfill({ json: { topics: catalog.topics.map(term => ({ id: term.id, count: term.id === "portrait" ? refinement : value })),
      resource_kinds: [{ id: "reference", count: value }], content_functions: [], custom_tags: [] } }).catch(() => {});
  });
  await page.goto(`${url}/?curation_status=all`);
  await page.waitForFunction(() => document.querySelector('[data-value="agent_workflow"]'));
  await page.locator('[data-group="topics"] > summary').click();
  await page.locator('[data-group="resource_kinds"] > summary').click();
  await page.waitForFunction(() => document.querySelector('[data-value="agent_workflow"] .facet-chip-count')?.textContent === "3");
  // Mixed CJK/Latin wording remains multiline even on runners without CJK fonts.
  // Keep the explicit wrapping assertion before sampling either font size.
  for (const fontSize of [12.5, 17]) {
    await page.evaluate(size => { document.querySelector("#facets").style.width = "210px";
      for (const node of document.querySelectorAll(".facet-chip")) node.style.fontSize = `${size}px`;
    }, fontSize);
    await page.evaluate(() => {
      const nodes = [...document.querySelectorAll('[data-topic-section="navigation"] .facet-chip')];
      const rects = () => nodes.map(node => { const r = node.getBoundingClientRect(); return [r.x, r.y, r.width, r.height]; });
      const baseline = rects();
      window.stability = { nodes, baseline, samples: [], removed: 0, running: true };
      const frame = () => { if (!window.stability.running) return; window.stability.samples.push(rects()); requestAnimationFrame(frame); };
      requestAnimationFrame(frame);
      window.stability.observer = new MutationObserver(records => {
        for (const record of records) for (const removed of record.removedNodes)
          if (nodes.some(node => removed === node || removed.contains?.(node))) window.stability.removed++;
      });
      window.stability.observer.observe(document.querySelector('#facets'), { childList: true, subtree: true });
    });
    const long = page.locator('[data-value="agent_workflow"]');
    const handle = await long.elementHandle();
    assert.ok(await long.evaluate(node => node.querySelector('.facet-chip-label').getBoundingClientRect().height > parseFloat(getComputedStyle(node).lineHeight)), "fixture must wrap onto multiple lines");
    for (const value of ["agent_workflow", "ai_coding", "knowledge", "agent_workflow"]) {
      count = count === 3 ? 12 : 3;
      await page.evaluate(async () => (await import("/assets/js/api.js")).invalidateQueryReads());
      const target = page.locator(`[data-facet="topics"][data-value="${value}"]`);
      await target.focus(); await target.click();
      await page.waitForFunction(expected => document.querySelector('[data-value="agent_workflow"] .facet-chip-count').textContent === String(expected), count);
      assert.equal(await target.evaluate(node => document.activeElement === node), true);
    }
    const measured = await page.evaluate(() => {
      const s = window.stability; s.running = false; s.observer.disconnect();
      return { baseline: s.baseline, firstMoved: s.samples.find(rects => rects.some((r,i) => r.some((v,j) => Math.abs(v - s.baseline[i][j]) > 0.1))), removed: s.removed, samples: s.samples.length,
        moved: s.samples.some(rects => rects.some((r,i) => r.some((v,j) => Math.abs(v - s.baseline[i][j]) > 0.1))) };
    });
    assert.ok(measured.samples > 10);
    assert.equal(measured.removed, 0, "navigation nodes stay mounted throughout selection and count updates");
    if (measured.moved) console.log(JSON.stringify(measured));
    assert.equal(measured.moved, false, "every animation frame keeps wrapped row geometry stable");
    assert.equal(await handle.evaluate(node => node.isConnected), true);
    console.log(`Stable wrapped labels at ${fontSize}px: ${measured.samples} frames, zero removals or layout movement`);
  }
  const resource = page.locator('[data-facet="resource_kinds"][data-value="reference"]');
  const resourceHandle = await resource.elementHandle();
  await resource.click();
  assert.equal(await resourceHandle.evaluate(node => node.isConnected && node.getAttribute('aria-pressed') === 'true'), true);
  await resource.click();
  assert.equal(await resourceHandle.evaluate(node => node.isConnected && node.getAttribute('aria-pressed') === 'false'), true);

  // A surviving specific-topic button changes semantics as selection changes.
  await page.locator('.topic-show-all').click();
  if (await page.locator('#clear-filters').isVisible()) await page.locator('#clear-filters').click();
  const portrait = page.locator('[data-value="portrait"]');
  const portraitHandle = await portrait.elementHandle();
  assert.equal(await portrait.getAttribute('data-facet'), 'topics');
  await page.locator('[data-value="ai_coding"]').click();
  assert.equal(await portraitHandle.evaluate(node => node.isConnected && node.dataset.facet === 'topic_refinements'), true);
  await portrait.click();
  assert.equal(new URL(page.url()).searchParams.get('topic_refinements'), 'portrait');
  await portrait.click();
  assert.equal(new URL(page.url()).searchParams.has('topic_refinements'), false);
  // Search input and caret survive an unrelated selection/count update.
  const search = page.locator('#topic-search');
  const searchHandle = await search.elementHandle();
  await search.fill('Agent');
  await page.locator('[data-value="agent_workflow"]').click();
  assert.equal(await searchHandle.evaluate(node => node.isConnected && node.value === 'Agent'), true);
  await search.fill('');
  await page.locator('.topic-show-all').click();
  specific = 0; count = 31;
  await page.evaluate(async () => (await import("/assets/js/api.js")).invalidateQueryReads());
  await page.locator('[data-value="knowledge"]').click();
  await page.waitForFunction(() => document.querySelector('[data-value="agent_workflow"] .facet-chip-count').textContent === '31');
  assert.equal(await page.locator('[data-topic-section="specific"]').count(), 0);
  const more = page.locator('[data-group="more"]');
  if (!await more.evaluate(node => node.open)) await more.locator(':scope > summary').click();
  const since = page.locator('[data-group="since"]');
  if (!await since.evaluate(node => node.open)) await since.locator(':scope > summary').click();
  await page.locator('#filter-since').fill('2026-01-01');
  await page.locator('#filter-uncertain').click();
  await page.locator('#clear-filters').click();
  assert.equal(await page.locator('#filter-since').inputValue(), '');
  await page.locator('.nav-item[data-view="all"]').click();
  assert.equal(await page.locator('#filter-uncertain').isChecked(), false);
  assert.deepEqual(errors, []);
  console.log('Resource toggle, dynamic refinement, search preservation and changed count sections passed');
} finally { await browser.close(); await new Promise(resolve => server.close(resolve)); }
