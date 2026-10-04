import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {chromium} from 'playwright';
import {createFixtureState,startFixtureServer} from './fixture-server.mjs';
const state=createFixtureState(),item=state.items[0];
item.url='https://x.com/Reader/status/123';item.original_language='zh';item.images=[];item.status='completed';
item.original_text='第一段演示：\n\nGIF\n\n视频之后的完整正文。';item.related_links=[];
const {server,url}=await startFixtureServer({state});
const bytes=await readFile(new URL('./fixtures/preview.webm',import.meta.url));
const browser=await chromium.launch({executablePath:process.env.CHROME_PATH||undefined,args:['--no-sandbox']});
try {
 const page=await browser.newPage({viewport:{width:1280,height:960}});let downloads=0;const errors=[];
 page.on('pageerror',e=>errors.push(e.message));
 await page.route('**/api/bookmarks/*/media',r=>r.fulfill({headers:{'X-Cairn-Offline-Scope':'b'.repeat(64)},json:{items:[{id:'a'.repeat(32),title:'演示视频',kind:'video',status:'ready',content_type:'video/webm',size:bytes.length}]}}));
 await page.route('**/api/media/*',r=>{downloads++;return r.fulfill({contentType:'video/webm',body:bytes});});
 await page.goto(`${url}/bookmarks/${item.id}`);
 const player=page.locator('#detail-body .reading-media-slot video');
 try {await player.waitFor({timeout:10000});} catch(error){console.log(await page.locator('#detail-body').innerHTML(),await page.locator('#detail-media').innerHTML(),errors);throw error;}
 await player.scrollIntoViewIfNeeded();
 try {await page.waitForFunction(()=>document.querySelector('#detail-body video')?.readyState>=2,{},{timeout:10000});}catch(error){console.log(await player.evaluate(v=>({src:v.src,network:v.networkState,ready:v.readyState,error:v.error?.message,html:v.parentElement.outerHTML})),{downloads,errors});throw error;}
 assert.equal(await player.evaluate(v=>v.paused),true);assert.equal(await player.evaluate(v=>v.videoWidth),64);
 assert.equal(await page.locator('#detail-media video').count(),0);
 assert.ok((await player.boundingBox()).height>100);
 await player.evaluate(v=>v.play());await page.waitForFunction(()=>document.querySelector('#detail-body video')?.currentTime>0);await player.evaluate(v=>v.pause());
 assert.equal(downloads,1);
 await page.locator('#toggle-formatted').click();await page.locator('#detail-media video').waitFor();
 await page.locator('#toggle-formatted').click();await page.locator('#detail-body video').waitFor();assert.equal(downloads,1);
 await page.setViewportSize({width:390,height:844});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
 assert.deepEqual(errors,[]);console.log('PASS: visible video first frame, inline placement, native play, raw toggle, bounded cache and mobile');
}finally{await browser.close();await new Promise(resolve=>server.close(resolve));}
