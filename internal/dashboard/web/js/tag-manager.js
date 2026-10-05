import {h,clear,byId} from "./dom.js";
import {openDialog,toast} from "./ui.js";
import {loadV1,loadV2,loadCustomTags} from "./taxonomy.js";
import {invalidateTaxonomyReads} from "./api.js";
import {emit} from "./store.js";
const dimensions={topics:"主题",resource_kinds:"资源类型",content_functions:"内容特征",carriers:"载体",affordances:"潜在用途",forms:"旧版形态",uses:"旧版用途"};
const labels={tag_collision:"名称或别名已被其他标签使用",invalid_tag_definition:"请填写名称和含义，检查字段长度",last_ai_tag:"这个分类至少需要保留一个可供 AI 判断的标签",revision_conflict:"标签已在别处修改，请重新核对后保存",tag_limit:"这个分类的标签数量已达上限",personal_use_human_only:"反对意见仅供人工使用"};
const uuid=()=>{const b=crypto.getRandomValues(new Uint8Array(16));b[6]=(b[6]&15)|64;b[8]=(b[8]&63)|128;const h=[...b].map(x=>x.toString(16).padStart(2,'0')).join('');return `${h.slice(0,8)}-${h.slice(8,12)}-${h.slice(12,16)}-${h.slice(16,20)}-${h.slice(20)}`;};
const key="cairn.tag-manager.pending.v1";
async function request(path,body,method){const r=await fetch(path,{method:method||(body?"POST":"GET"),headers:{"Content-Type":"application/json"},body:body?JSON.stringify(body):undefined});const data=await r.json();if(!r.ok){const e=new Error(labels[data.error]||data.error||"保存失败");e.status=r.status;throw e;}return data;}
const error=e=>toast(e.message||"暂时无法连接服务",{tone:"error"});
const button=(label,run)=>h("button.btn",{type:"button",onclick:()=>Promise.resolve().then(run).catch(error)},label);
function pending(){try{return JSON.parse(localStorage.getItem(key)||"null");}catch{return null;}}
function retain(value){localStorage.setItem(key,JSON.stringify(value));}
function remove(){localStorage.removeItem(key);}
export async function managedTagChoices(){const [data,custom]=await Promise.all([request('/api/tag-catalog'),request('/api/custom-tags')]);return [...Object.keys(dimensions).flatMap(d=>(data.catalog[d]||[]).filter(t=>t.active&&!t.deprecated).map(t=>({ref:`system/${d}/${t.id}`,label:t.label,aliases:t.aliases||[],dimension:dimensions[d]}))),(custom.items||custom.tags||[]).filter(t=>t.status==='active').map(t=>({ref:t.tag_ref,label:t.label,dimension:'个人标签'}))];}
export function tagPicker(choices,selected=new Set()) {
 const query=h('input.collection-input',{type:'search',placeholder:'查找标签或别名','aria-label':'查找自动收录标签'}),rows=h('div.tag-manager-choices');
 const render=()=>clear(rows,choices.filter(t=>(t.label+' '+t.dimension+' '+(t.aliases||[]).join(' ')).toLowerCase().includes(query.value.toLowerCase())).map(t=>{
  const check=h('input',{type:'checkbox',checked:selected.has(t.ref)});check.addEventListener('change',()=>{if(check.checked)selected.add(t.ref);else selected.delete(t.ref);});
  return h('label.collection-pick',check,h('span',t.label),h('small',t.dimension));
 }));query.addEventListener('input',render);render();return h('div',query,rows);
}
export async function browseTags(){
 let data;try{data=await request('/api/tag-catalog');}catch(e){error(e);return;}
 if(!data.catalog){toast('当前服务还未升级标签管理');return;}
 const query=h('input.collection-input',{type:'search',placeholder:'查找名称、别名或含义','aria-label':'查找标签'}),mode=h('select.collection-input',{'aria-label':'标签状态'},h('option',{value:'active'},'使用中'),h('option',{value:'archived'},'已停用'));
 const dim=h('select.collection-input',{'aria-label':'标签类别'},h('option',{value:'all'},'全部类别'),Object.entries(dimensions).map(([value,label])=>h('option',{value},label)));
 const rows=h('div.collection-list'),recover=h('div');
 const refresh=async()=>{data=await request('/api/tag-catalog');invalidateTaxonomyReads();emit('custom-tags:changed');await Promise.all([loadV1(),loadV2(),loadCustomTags()]);emit('taxonomy');emit('tags:catalog-changed');render();};
 const send=async (body,path="/api/tag-catalog/operations",method="POST")=>{
  const old=pending();if(old&&JSON.stringify(old.body||old)!==JSON.stringify(body)){toast('还有一项未确认的修改，请先核对');return false;}
  try{retain({body,path,method});}catch{toast('浏览器无法保存修改，请允许本站本地存储');return false;}
  try{const next=await request(path,body,method);remove();data=next;await refresh();return true;}catch(e){
   if(e.status && e.status<500){remove();await refresh();}error(e);render();return false;
  }
 };
 const render=()=>{
  const receipt=pending();clear(recover,receipt?h('div.tag-manager-pending',h('p','一项修改尚未确认。重试会沿用原操作，不会重复创建标签。'),button('重试',()=>send(receipt.body||receipt,receipt.path,receipt.method)),button('核对远端',refresh)):null);
  const counts=new Map((data.counts||[]).map(c=>[`${c.field}:${c.term}`,c.n]));
  clear(rows,Object.entries(dimensions).filter(([d])=>dim.value==='all'||dim.value===d).flatMap(([d,label])=>(data.catalog[d]||[]).filter(t=>mode.value==='active'?t.active&&!t.deprecated:!t.active||t.deprecated).filter(t=>(t.label+' '+(t.aliases||[]).join(' ')+' '+t.description).toLowerCase().includes(query.value.toLowerCase())).map(t=>h('div.tag-manager-row',
   h('div.tag-manager-copy',h('strong',t.label),h('small',`${label} · ${counts.get(`${d==='forms'?'form':d==='uses'?'use':d}:${t.id}`)||0} 条 · ${d==='uses'&&t.id==='contra'||t.ai_enabled===false?'仅手动':'AI 自动打标'}`),h('p',t.description)),button('管理',()=>editTag(d,t))))));
  if(dim.value==='all')for(const t of (data.custom_tags||[]).filter(t=>mode.value==='active'?t.status==='active':t.status!=='active').filter(t=>t.label.toLowerCase().includes(query.value.toLowerCase())))rows.append(h('div.tag-manager-row',h('div.tag-manager-copy',h('strong',t.label),h('small',`个人标签 · ${t.link_count} 条 · 仅手动`)),button('管理',()=>editCustom(t))));
  if(!rows.childElementCount)rows.append(h('p.management-empty','没有符合条件的标签'));
 };
 function editCustom(term){
  const input=h('input.collection-input',{value:term.label,maxLength:80,'aria-label':'个人标签名称'});let body=null;
  openDialog({title:'管理个人标签',body:[h('label.collection-field','名称',input),h('p.collection-hint','个人标签由你手动使用。停用会保留已打标签及变更记录。')],actions:[{label:term.status==='active'?'停用':'恢复',run:()=>{body??={operation_key:uuid(),expected_revision:term.revision,label:term.label,archived:term.status==='active'};return send(body,`/api/custom-tags/${term.id}`,'PATCH');}},{label:'保存',primary:true,run:()=>{body??={operation_key:uuid(),expected_revision:term.revision,label:input.value};return send(body,`/api/custom-tags/${term.id}`,'PATCH');}}]});
 }
 function editTag(d,term){
  const name=h('input.collection-input',{value:term?.label||'',maxLength:80,'aria-label':'标签名称'}),desc=h('textarea.collection-input',{value:term?.description||'',rows:3,maxLength:1000,'aria-label':'标签含义'});
  const aliases=h('input.collection-input',{value:(term?.aliases||[]).join('，'),'aria-label':'标签别名'}),include=h('textarea.collection-input',{value:(term?.includes||[]).join('\n'),rows:2,'aria-label':'正例'}),exclude=h('textarea.collection-input',{value:(term?.excludes||[]).join('\n'),rows:2,'aria-label':'反例'});
  const ai=h('input',{type:'checkbox',checked:term?.ai_enabled!==false&&!(d==='uses'&&term?.id==='contra'),disabled:d==='uses'&&term?.id==='contra'}),granularity=h('select.collection-input',{'aria-label':'标签粒度'},h('option',{value:'specific'},'细分主题'),h('option',{value:'broad'},'宽主题'));granularity.value=term?.granularity||'specific';
  let savedBody=null;const expected=data.revision;
  const make=(type,definition)=>({operation_key:uuid(),expected_revision:expected,dimension:d,...(term?{id:term.id}:{}),type,...(definition?{definition}:{})});
  const history=async()=>{const response=await request(`/api/tag-catalog/history?dimension=${d}&id=${term.id}`);openDialog({title:'标签变更',body:(response.items||[]).length?response.items.map(x=>h('p',`${x.created_at} · ${x.action} · 版本 ${x.revision}`)):h('p','暂无管理变更记录')});};
  openDialog({title:term?'管理标签':'新建主题标签',body:[h('label.collection-field','名称',name),h('label.collection-field','别名（逗号分隔）',aliases),h('label.collection-field','含义：这个标签应该匹配什么',desc),d==='topics'?h('label.collection-field','粒度',granularity):null,h('label.management-check',ai,'参与 AI 自动打标'),h('details.management-advanced',h('summary','匹配示例与排除条件'),h('label.collection-field','正例（每行一项）',include),h('label.collection-field','反例（每行一项）',exclude)),h('p.collection-hint','更改含义和 AI 开关作用于待处理与新收藏。停用后保留历史标签及合集成员。'),term?button('查看变更',history):null],actions:[...(term?[{label:term.active&&!term.deprecated?'停用':'恢复',run:async()=>{savedBody??=make(term.active&&!term.deprecated?'archive':'restore');return send(savedBody);}}]:[]),{label:'保存',primary:true,run:async()=>{
   const split=v=>v.split(/[\n,，]/).map(x=>x.trim()).filter(Boolean);
   const lines=v=>v.split(/\n/).map(x=>x.trim()).filter(Boolean);
   const definition={label:name.value,aliases:split(aliases.value),description:desc.value,includes:lines(include.value),excludes:lines(exclude.value),ai_enabled:ai.checked,...(d==='topics'?{granularity:granularity.value}:{})};
   const draft=JSON.stringify(definition);if(savedBody&&JSON.stringify(savedBody.definition)!==draft&&pending()){toast('请先确认上一项修改');return false;}
   if(!savedBody||JSON.stringify(savedBody.definition)!==draft)savedBody=make(term?'edit':'create',definition);
   const ok=await send(savedBody);if(!ok&&!pending())savedBody=null;return ok;
  }}]});
 }
 query.addEventListener('input',render);mode.addEventListener('change',render);dim.addEventListener('change',render);
 openDialog({title:'标签管理',wide:true,body:[h('div.management-toolbar',query,h('div.collection-tools',dim,mode,h('div.management-actions',button('新建标签',()=>editTag('topics',null))))),recover,rows]});render();
}
export function initTagManager(){byId('browse-tags')?.addEventListener('click',()=>browseTags());}
