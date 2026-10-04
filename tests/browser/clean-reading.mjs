import assert from "node:assert/strict";
import {chromium} from "playwright";
import {createFixtureState,startFixtureServer} from "./fixture-server.mjs";
const state=createFixtureState(),item=state.items[0];
item.url="https://x.com/Reader/status/123";item.original_language="zh";item.images=[];item.status="completed";item.summary="正文去噪与引用链接测试";
item.related_links=["https://example.org/paper","https://example.org/extra","javascript:alert(1)","https://x.com/Reader/status/123/analytics","https://x.com/i/premium_sign_up"];
item.original_text=`[作者](https://x.com/Reader)\n\n[@Reader](https://x.com/Reader)\n\n翻译自 韩语\n\n# 实用工作流\n\n读完\n\n[原始论文](https://example.org/paper)\n\n可以继续实践。\n\n> 第一步保留全文。\n\n> 第二步验证图片。\n\n- 项目一\n  - 子项目\n\n\`\`\`text\n评价此翻译：\n这段是代码示例，不能删除。\n\`\`\`\n\n评价此翻译：\n\n0:03\n\n[下午3:00 · 2026年9月4日](https://x.com/Reader/status/123)\n\n[7.1万\n\n查看](https://x.com/Reader/status/123/analytics)\n\n想发布自己的文章？\n\n[升级为 Premium](https://x.com/i/premium_sign_up)\n\n<script>window.pwned=true</script>\n\n![remote](https://untrusted.example/tracker.png)`;
const {server,url}=await startFixtureServer({state});
const browser=await chromium.launch({executablePath:process.env.CHROME_PATH||undefined,args:["--no-sandbox"]});
try{
 const page=await browser.newPage({viewport:{width:1280,height:960}}),errors=[],remote=[];
 page.on("pageerror",e=>errors.push(e.message));await page.route("https://**/*",r=>{remote.push(r.request().url());return r.abort();});
 await page.goto(`${url}/bookmarks/${item.id}`);await page.locator("#detail-body h2").waitFor();
 assert.equal(await page.locator("#reading-version").textContent(),"净读版");
 const paragraphs=await page.locator("#detail-body > p").allTextContents();assert.ok(paragraphs.some(p=>p.includes("读完")&&p.includes("原始论文")&&p.includes("继续实践")));
 assert.ok(!paragraphs.some(p=>/作者|@Reader|评价此翻译|Premium|翻译自|7.1万/.test(p)));
 assert.match(await page.locator("#detail-body pre").textContent(),/评价此翻译/);
 assert.equal(await page.locator("#detail-body blockquote").count(),1);
 assert.equal(await page.locator("#detail-body ul ul li").count(),1);
 assert.deepEqual(await page.locator("#related-links a").evaluateAll(nodes=>nodes.map(a=>a.href)),["https://example.org/paper","https://example.org/extra"]);
 assert.equal(await page.locator("#detail-body script").count(),0);assert.equal(await page.evaluate(()=>window.pwned),undefined);assert.deepEqual(remote,[]);
 await page.locator("#toggle-formatted").click();assert.match(await page.locator("#detail-body").textContent(),/升级为 Premium/);
 await page.locator("#toggle-formatted").click();assert.doesNotMatch(await page.locator("#detail-body").textContent(),/升级为 Premium/);
 await page.setViewportSize({width:390,height:844});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
 const timing=await page.evaluate(async text=>{const {renderReading}=await import('/assets/js/reading.js');const times=[];for(let i=0;i<10;i++){const node=document.createElement('div'),start=performance.now();renderReading(node,text,{url:'https://x.com/Reader/status/123'});times.push(performance.now()-start);}return times.sort((a,b)=>a-b);},item.original_text);
 assert.ok(timing[5]<250,`unexpectedly slow parsing: ${timing[5]}ms`);assert.deepEqual(errors,[]);
 console.log(JSON.stringify({test:'Defuddle/Marked clean reading, links, quotes, code, raw fallback, mobile and no external requests',median_ms:timing[5]}));
}finally{await browser.close();await new Promise(resolve=>server.close(resolve));}
