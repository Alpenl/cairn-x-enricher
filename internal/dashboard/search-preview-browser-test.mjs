import assert from "node:assert/strict";
import { chromium } from "playwright";
import { createFixtureState, startFixtureServer } from "../../tests/browser/fixture-server.mjs";
import { taxonomyV2 } from "../../tests/browser/fixture-data.mjs";
const fixture = createFixtureState({ count: 12 });
for (const item of fixture.items) { item.ai_title = "普通收藏"; item.summary = "普通摘要"; item.original_text = "普通正文"; item.translated_text = "普通正文"; }
fixture.items[0].ai_title = "布局实践";
fixture.items[1].original_text = "只在完整正文中出现的布局技巧";
const { server, url } = await startFixtureServer({ state: fixture });
const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || "/usr/bin/google-chrome", args: ["--no-sandbox"] });
try {
 const page = await browser.newPage({ viewport: { width: 1280, height: 860 } });
 const errors = [], searches = []; page.on('pageerror', e=>errors.push(e.message));
 await page.route('**/api/v2-taxonomy', r=>r.fulfill({ json: taxonomyV2(), headers:{'X-Cairn-Tag-System':'1','X-Cairn-Topic-Granularity':'1'} }));
 let release; const gate = new Promise(resolve=>release=resolve);
 await page.route('**/api/bookmarks?*', async route=>{
  const q=new URL(route.request().url()).searchParams.get('q');
  const response=await route.fetch(); const body=await response.json();
  if(q){ searches.push(q); await gate; }
  await route.fulfill({response,json:{...body,local_filter_version:1}}).catch(()=>{});
 });
 await page.goto(`${url}/?curation_status=all`);
 await page.waitForFunction(()=>document.querySelector('#list-pane')?.dataset.loading==='false');
 const input=page.locator('#search');
 await input.focus();
 await input.evaluate(node=>{
  node.dispatchEvent(new CompositionEvent('compositionstart',{bubbles:true}));node.value='bu';
  node.dispatchEvent(new InputEvent('input',{bubbles:true,isComposing:true}));
 });
 await page.waitForTimeout(400);assert.deepEqual(searches,[]);
 await input.evaluate(node=>{
  node.value='布局';node.dispatchEvent(new CompositionEvent('compositionend',{bubbles:true,data:'布局'}));
  node.dispatchEvent(new InputEvent('input',{bubbles:true}));
 });
 await page.waitForFunction(()=>document.querySelector('#list-count').textContent.includes('全文搜索中'));
 assert.deepEqual(await page.locator('#rows li.row[data-id]').evaluateAll(nodes=>nodes.map(n=>Number(n.dataset.id))),[fixture.items[0].id]);
 assert.equal(await input.evaluate(node=>node===document.activeElement),true);
 assert.equal(await page.locator('#list-pane').getAttribute('aria-busy'),'true');
 release();await page.waitForFunction(()=>document.querySelector('#list-pane').dataset.loading==='false');
 assert.deepEqual(await page.locator('#rows li.row[data-id]').evaluateAll(nodes=>nodes.map(n=>Number(n.dataset.id))),[fixture.items[0].id,fixture.items[1].id]);
 assert.deepEqual(searches,['布局']);
 assert.equal(await page.locator('#list-count').textContent(),'2 条');
 // The preview is never substituted for server results after edits/account changes.
 const preview=await page.evaluate(async()=>{
  const {previewSearch,invalidateQueryReads}=await import('/assets/js/api.js');
  invalidateQueryReads();return previewSearch(new URLSearchParams({q:'布局',view:'summary',limit:'60',curation_status:'all'}));
 });
 assert.equal(preview,null);assert.deepEqual(errors,[]);
 console.log('Search preview paints before the response; body-only results arrive; IME sends one query; invalidation passed');
}finally{await browser.close();await new Promise(resolve=>server.close(resolve));}
