// Real browser regression over the embedded application. Synthetic APIs only;
// no Worker mutation or model invocation. Run: node internal/dashboard/tag-browser-test.mjs
import assert from "node:assert/strict";
import { chromium } from "playwright";
import { createFixtureState, startFixtureServer } from "../../tests/browser/fixture-server.mjs";
import { taxonomyV2 } from "../../tests/browser/fixture-data.mjs";

const fixture = createFixtureState();
const item = fixture.items.find((entry) => entry.curation_status === "inbox");
const tag = (id,label,aliases=[]) => ({id,label,aliases,active:true});
const catalog = { ...taxonomyV2(), topics:[tag("image_creation","图像生成"),tag("ai_coding","AI编程"),tag("video_creation","视频制作")],
  resource_kinds:[tag("skill","Skill"),tag("prompt","提示词",["Prompt"])] };
const initial = {id:item.id,revision:4,content_revision:1,decision_id:3,selection:{topics:["image_creation","ai_coding"],resource_kinds:["skill"],content_functions:["method","opinion"],carriers:[],affordances:[]},
  automatic:{topics:["image_creation","ai_coding"],resource_kinds:["skill"],content_functions:["method","opinion"]},custom_tags:[],state:{fields:{
    topics:{status:"completed_nonempty",values:[{term:"image_creation",origin:"automatic"},{term:"ai_coding",origin:"automatic"}],actions:[],candidates:[{term_id:"video_creation",verdict:"abstained"}]},
    resource_kinds:{status:"completed_nonempty",values:[{term:"skill",origin:"automatic"}],actions:[],candidates:[]},
    content_functions:{status:"completed_nonempty",values:[{term:"method",origin:"automatic"},{term:"opinion",origin:"automatic"}],actions:[],candidates:[]}}}};
let current = structuredClone(initial);
let custom = [];
let conflict = false;
let delayNext = false;
let release;
const calls = [];
const events = [];
const beforeByOperation = new Map();
const {server,url} = await startFixtureServer({state:fixture});
const browser = await chromium.launch({executablePath:process.env.CHROME_PATH || undefined,args:["--no-sandbox"]});
const context = await browser.newContext({viewport:{width:1024,height:800}});
const page = await context.newPage();
const pageErrors=[];
page.on("pageerror",error=>pageErrors.push(error.message));
const send = (route,payload,status=200) => route.fulfill({status,contentType:"application/json",body:JSON.stringify(payload)});
await page.route("**/api/v2-taxonomy",route=>send(route,catalog));
await page.route("**/api/tag-counts?*",route=>send(route,{total:1,topics:catalog.topics.map(entry=>({id:entry.id,count:1})),resource_kinds:[{id:"skill",count:1}],custom_tags:[]}));
await page.route("**/api/custom-tags",async route=>{
  if(route.request().method()==="GET") return send(route,{tags:custom});
  const body=route.request().postDataJSON();
  const created={id:"123e4567-e89b-12d3-a456-426614174000",tag_ref:"custom/default/123e4567-e89b-12d3-a456-426614174000",label:body.label,revision:1,status:"active",link_count:0};
  custom.push(created); return send(route,{tag:created});
});
await page.route(`**/api/bookmarks/${item.id}/tag-history?*`,route=>send(route,{events,next_before_id:null}));
await page.route(`**/api/bookmarks/${item.id}/tags`,async route=>{
  if(route.request().method()==="GET") return send(route,current);
  const body=route.request().postDataJSON(); calls.push(body);
  if(delayNext){delayNext=false;await new Promise(resolve=>{release=resolve;});}
  if(conflict){conflict=false;current.revision++;return send(route,{error:"revision_conflict",revision:current.revision},409);}
  assert.equal(body.expected_revision,current.revision);
  beforeByOperation.set(body.operation_key,structuredClone(current));
  for(const action of body.actions){
    if(action.action==="undo") { const previous=beforeByOperation.get(action.operation_id);if(previous)current={...structuredClone(previous),revision:current.revision};continue; }
    if(action.action==="replace") {
      const from=action.from_tag_ref.split("/");const to=action.to_tag_ref.split("/");
      current.selection[from[1]]=current.selection[from[1]].filter(term=>term!==from[2]);current.selection[to[1]].push(to[2]);
      current.state.fields[to[1]].values.push({term:to[2],origin:"human",confirmed:false});continue;
    }
    if(action.action==="set_empty"){current.selection[action.dimension]=[];current.state.fields[action.dimension].values=[];current.state.fields[action.dimension].cleared_automatic={origin:"human"};continue;}
    if(action.action==="reset_group"){current.selection[action.dimension]=[...current.automatic[action.dimension]];current.state.fields[action.dimension].cleared_automatic=null;continue;}
    const parts=action.tag_ref.split("/");
    if(parts[0]==="custom") {if(action.action==="attach")current.custom_tags.push(custom.find(tag=>tag.id===parts[2]));else current.custom_tags=current.custom_tags.filter(tag=>tag.id!==parts[2]);continue;}
    const [_,field,term]=parts;
    const values=current.state.fields[field];
    current.selection[field]=current.selection[field].filter(id=>id!==term);
    values.values=values.values.filter(value=>value.term!==term);
    if(["accept","confirm"].includes(action.action)) {current.selection[field].push(term);values.values.push({term,origin:"human",confirmed:action.action==="confirm"});}
    else if(action.action==="reset" && current.automatic[field].includes(term)){current.selection[field].push(term);values.values.push({term,origin:"automatic"});}
    values.actions=values.actions.filter(value=>value.term!==term);
    if(action.action==="reject")values.actions.push({term,action:"reject",origin:"human"});
  }
  current.revision++;
  const action=body.actions[0];events.unshift({id:events.length+1,operation_id:body.operation_key,action:action.action,tag_ref:action.tag_ref,actions:body.actions,created_at:"2026-09-30T03:00:00Z",actor_type:"human"});
  return send(route,{...current,operation_id:body.operation_key,replayed:false});
});
let checks=0;
const checked=(name)=>{checks++;process.stdout.write(`ok   ${name}\n`);};
try {
  await page.goto(`${url}/bookmarks/${item.id}`);
  await page.locator('.tag-system-row[data-dimension="resource_kinds"]').waitFor({state:"attached"});
  assert.equal(await page.locator("#curate").evaluate(node=>node.open),false);
  assert.equal(await page.locator("#curation-why").isVisible(),false);
  assert.equal(await page.locator(".tag-suggestions").isVisible(),false);
  assert.deepEqual(await page.locator(".curate-summary-tag").allTextContents(),["图像生成","AI编程","Skill","方法","观点"]);
  assert.equal(await page.locator(".curate-summary-more").count(),0);
  assert.equal(calls.length,0);checked("reading opens with a compact effective-tag summary and no editor or write");
  await page.locator("#detail-scroll").focus();await page.keyboard.press("r");
  assert.equal(await page.locator("#curate").evaluate(node=>node.open),true);
  assert.equal(await page.evaluate(()=>document.activeElement.id),"curation-why");checked("reason shortcut opens the panel before focusing");
  await page.locator("#curation-why").fill("留给下周复盘");
  await page.waitForFunction(()=>document.querySelector("#save-state")?.textContent==="已保存");
  assert.equal(await page.locator("#curate").evaluate(node=>node.open),true);
  assert.equal(await page.evaluate(()=>document.activeElement.id),"curation-why");
  assert.equal(await page.locator("#curate-summary-why").textContent(),"留给下周复盘");
  assert.equal(calls.length,0);checked("reason auto-save keeps the panel open and preserves focus without tag writes");
  await page.locator(".tag-secondary > summary").click();
  await page.locator("#v2-entity-input").fill("尚未提交的实体");
  await page.evaluate(async id=>{const curation=await import("/assets/js/curation.js");curation.refresh(id);},item.id);
  assert.equal(await page.locator(".tag-secondary").evaluate(node=>node.open),true);
  assert.equal(await page.locator("#v2-entity-input").inputValue(),"尚未提交的实体");
  assert.equal(await page.evaluate(()=>document.activeElement.id),"v2-entity-input");
  assert.equal(calls.length,0);checked("same-bookmark refresh preserves a nested editor's draft, disclosure and focus");
  await page.locator("#v2-entity-input").fill("");
  await page.locator("#curate > summary").click();
  await page.locator("#detail-scroll").focus();await page.keyboard.press("t");
  await page.getByRole("searchbox",{name:"搜索标签",exact:true}).waitFor();
  assert.equal(await page.locator("#curate").evaluate(node=>node.open),true);
  assert.equal(calls.length,0);checked("tag shortcut opens the panel and search without writing");
  await page.getByRole("button",{name:"关闭",exact:true}).click();
  await page.locator("#detail-scroll").focus();await page.keyboard.press("j");
  await page.waitForFunction(id=>!location.pathname.endsWith("/"+id),item.id);
  assert.equal(await page.locator("#curate").evaluate(node=>node.open),false);
  await page.keyboard.press("k");
  await page.waitForFunction(id=>location.pathname.endsWith("/"+id),item.id);
  assert.equal(await page.locator("#curate").evaluate(node=>node.open),false);
  assert.equal(await page.locator("#curate-summary-why").textContent(),"留给下周复盘");checked("switching bookmarks resets disclosure while retaining the saved reason");
  await page.locator("#curate > summary").click();
  await page.locator('.tag-system-row[data-dimension="resource_kinds"]').waitFor();
  assert.equal(await page.locator('.tag-system-chip.custom').count(),0);checked("custom section absent when unused");
  const functions = page.locator('.tag-system-row[data-dimension="content_functions"]');
  assert.equal(await functions.isVisible(),true);
  assert.deepEqual(await functions.locator('.tag-name').allTextContents(),["方法","观点"]);
  assert.equal(await page.locator('.tag-secondary [data-dimension="content_functions"]').count(),0);
  checked("content features are visible identity-bearing tags without a duplicate advanced editor");
  await functions.getByRole("button",{name:"移除观点",exact:true}).click();
  await page.waitForFunction(()=>document.querySelectorAll('.tag-system-row[data-dimension="content_functions"] .tag-name').length===1);
  assert.deepEqual(calls.at(-1).actions,[{action:"reject",tag_ref:"system/content_functions/opinion"}]);
  assert.equal(await functions.locator('.tag-origin').textContent(),"自动标签");
  await page.locator('.toast-action').last().click();
  await page.waitForFunction(()=>document.querySelectorAll('.tag-system-row[data-dimension="content_functions"] .tag-name').length===2);
  checked("content feature removal and causal undo preserve the other automatic tag's origin");
  assert.equal(await page.locator('.tag-system-row[data-dimension="topics"] .tag-name').count(),2);
  assert.equal(await page.locator('.tag-suggestions').count(),1);checked("boundary suggestion is separate from effective labels");
  const before=calls.length;
  await page.locator('.tag-system-row[data-dimension="topics"] .tag-name').filter({hasText:"图像生成"}).click();
  assert.equal(calls.length,before);assert.match(page.url(),/topics=image_creation/);checked("tag label filters without removing");
  await page.goto(`${url}/bookmarks/${item.id}`);
  await page.locator("#curate > summary").click();
  await page.locator('.tag-system-row[data-dimension="resource_kinds"]').waitFor();
  await page.getByRole("button",{name:"移除AI编程",exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('.tag-save-state')?.textContent==="");
  assert.deepEqual(calls.at(-1).actions,[{action:"reject",tag_ref:"system/topics/ai_coding"}]);
  assert.equal(await page.locator('.tag-system-row[data-dimension="topics"] .tag-origin').textContent(),"自动标签");checked("single removal leaves other AI label unconfirmed");
  await page.locator("#curate > summary").click();
  await page.locator('.toast-action').last().click();
  await page.waitForFunction(()=>document.querySelectorAll('.tag-system-row[data-dimension="topics"] .tag-name').length===2);
  assert.equal(calls.at(-1).actions[0].action,"undo");checked("undo uses causal operation rather than inverse toggle");
  assert.equal(await page.locator("#curate").evaluate(node=>node.open),false);checked("undo remains available while closed and does not open the editor");
  await page.locator("#curate > summary").click();
  await page.getByRole("button",{name:"编辑图像生成",exact:true}).click();
  await page.getByRole("menuitem",{name:"确认这个标签"}).click();
  await page.waitForFunction(()=>document.querySelector('.tag-system-row[data-dimension="topics"]')?.textContent.includes("你已确认"));
  assert.equal(await page.locator("#curate").evaluate(node=>node.open),true);
  assert.equal(await page.evaluate(()=>document.activeElement.getAttribute("aria-label")),"编辑图像生成");checked("per-tag save preserves the disclosure and editing focus");
  assert.equal(calls.at(-1).expected_content_revision,1);
  assert.equal(calls.at(-1).expected_decision_id,3);checked("per-tag confirmation binds viewed content and decision");
  await page.getByRole("button",{name:"添加标签",exact:true}).click();
  await page.getByRole("searchbox",{name:"搜索标签",exact:true}).fill("Prompt");
  assert.equal(await page.getByRole("button",{name:/创建自定义标记/}).count(),0);checked("explicit alias reuses system tag");
  await page.getByRole("searchbox",{name:"搜索标签",exact:true}).fill("我的项目");
  await page.getByRole("button",{name:"创建自定义标记「我的项目」"}).click();
  await page.locator('.tag-system-chip.custom').waitFor();assert.equal(current.custom_tags[0].label,"我的项目");checked("custom creation attaches optional stable marker");
  conflict=true;
  await page.getByRole("button",{name:"移除AI编程",exact:true}).click();
  await page.locator('.tag-system-conflict').waitFor();
  assert.equal(await page.getByRole("button",{name:"移除AI编程",exact:true}).count(),0);checked("CAS conflict preserves user's local removal draft");
  await page.locator("#curate > summary").click();
  assert.equal(await page.locator("#curate-summary-state").textContent(),"有修改待处理");
  await page.locator("#curate > summary").click();checked("closed summary preserves access to a pending conflict");
  await page.getByRole("button",{name:"重新应用我的修改"}).click();
  await page.waitForFunction(()=>!document.querySelector('.tag-system-conflict'));checked("explicit reapply uses current revision");
  await page.getByRole("button",{name:"编辑图像生成",exact:true}).click();
  await page.getByRole("menuitem",{name:"替换为其他标签"}).click();
  await page.getByRole("button",{name:"视频制作 主题",exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('.tag-system-row[data-dimension="topics"]')?.textContent.includes("视频制作"));
  assert.equal(calls.at(-1).actions.length,1);assert.equal(calls.at(-1).actions[0].action,"replace");checked("replacement is one atomic API action");
  await page.getByRole("button",{name:"查看变更",exact:true}).click();
  await page.locator('.tag-history-entry').first().waitFor();assert.match(await page.locator('.tag-history').textContent(),/替换/);checked("real operation history is readable");
  await page.getByRole("button",{name:"关闭",exact:true}).click();
  await page.setViewportSize({width:320,height:740});
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth),true);checked("320px layout keeps tag actions in viewport");
  assert.deepEqual(pageErrors,[]);checked("no browser runtime errors");
  process.stdout.write(`${checks} tag browser checks passed\n`);
} finally {await browser.close();await new Promise(resolve=>server.close(resolve));}
