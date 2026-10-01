import assert from "node:assert/strict";
import { chromium } from "playwright";
import { createFixtureState, startFixtureServer } from "./fixture-server.mjs";

const fixture = createFixtureState({ count: 5000 });
for (const item of fixture.items) { item.status = "completed"; item.curation_status = "inbox"; }
const { server, url } = await startFixtureServer({ state: fixture });
const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || undefined, args: ["--no-sandbox"] });
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  await page.goto(url);
  await page.waitForFunction(() => document.querySelectorAll("li.row").length >= 40);
  const metrics = await page.evaluate(async () => {
    const list = await import("/assets/js/list.js");
    const { state } = await import("/assets/js/store.js");
    const started = performance.now();
    let attempts = 0;
    while (state.nextBeforeID && attempts++ < 160) await list.loadMore();
    const elapsed = performance.now() - started;
    const samples = [];
    let selected = true;
    for (const index of [0, 400, 999, 1499, 2499, 3499, 4499, 4999, 0]) {
      const id = state.order[index];
      const start = performance.now();
      list.markSelected(id);
      await new Promise(requestAnimationFrame);
      samples.push(performance.now() - start);
      selected &&= document.querySelector("li.row.selected")?.dataset.id === String(id);
    }
    return { rows: state.order.length, domRows: document.querySelectorAll("li.row").length,
      loadAllMs: Math.round(elapsed), selectionFrameMs: samples.map(Math.round), selected,
      heapMiB: performance.memory ? Math.round(performance.memory.usedJSHeapSize / 1024 / 1024) : null };
  });
  assert.equal(metrics.rows, 5000);
  assert.equal(metrics.domRows, 5000);
  assert.equal(metrics.selected, true);
  console.log(JSON.stringify(metrics));
} finally {
  await browser.close();
  await new Promise((resolve) => server.close(resolve));
}
