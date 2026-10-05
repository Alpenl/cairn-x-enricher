import {managedTagChoices,tagPicker} from "./tag-manager.js";
import { fetchJSON, errorLabel, invalidateQueryReads } from "./api.js";
import { byId, h, clear } from "./dom.js";
import { state, on, emit } from "./store.js";
import { openDialog, confirmAction, toast } from "./ui.js";
import { openOrganizing } from "./collection-organizing.js";
import { emptyFilters } from "./query.js";
import { icon } from "./icons.js";

let catalog = [], hooks = {}, loading = null, pinnedSignature = "", loadEpoch = 0;
// randomUUID is unavailable on plain HTTP NAS addresses; getRandomValues works there.
const uuid = () => {
 const bytes = crypto.getRandomValues(new Uint8Array(16));
 bytes[6] = (bytes[6] & 15) | 64; bytes[8] = (bytes[8] & 63) | 128;
 const hex = [...bytes].map(b=>b.toString(16).padStart(2,"0")).join("");
 return `${hex.slice(0,8)}-${hex.slice(8,12)}-${hex.slice(12,16)}-${hex.slice(16,20)}-${hex.slice(20)}`;
};
const labels = {collections_unsupported:"服务暂不支持合集，请先更新后端",invalid_name:"名称不能为空，最多 80 个字符",invalid_note:"备注最多 2000 个字符",collection_deleted:"这个合集已被删除，请到已删除中恢复",member_not_found:"这条收藏已移出合集，请刷新后再操作",collection_limit:"合集或成员数量已达上限",collection_write_failed:"保存未完成，请刷新合集后重试"};
const failure = e => toast(labels[e.message] || errorLabel(e.message), {tone:"error"});
const button = (text, run, cls = "btn") => h(`button.${cls}`, {type:"button", onclick:async event=>{const node=event.currentTarget;node.disabled=true;try{await run();}catch(e){failure(e);}finally{node.disabled=false;}}},text);
const live = () => catalog.filter(c=>!c.deleted);
export const collectionName = id => catalog.find(c=>c.id===id)?.name || "合集";
async function load() {
 if(loading) return loading;
 const epoch=++loadEpoch;
 const flight=fetchJSON("/api/collections").then(data=>{if(epoch===loadEpoch){catalog=data.items;renderNav();emit("collections:loaded");}return catalog;}).finally(()=>{if(loading===flight)loading=null;});
 loading=flight;return flight;
}
function select(id) {hooks.select({...emptyFilters(),curation_status:"all",collection_id:id});}
function renderNav(){
 const nav=byId("pinned-collections");if(!nav)return;
 const pinned=live().filter(c=>c.pinned&&!c.archived).slice(0,6);
 const signature=JSON.stringify([state.filters.collection_id,pinned.map(c=>[c.id,c.name,c.item_count])]);
 nav.hidden=!pinned.length;
 if(signature!==pinnedSignature){
  pinnedSignature=signature;
  clear(nav,pinned.map(c=>h("button.nav-item.collection-shortcut",{
   type:"button",title:c.name,"aria-current":state.filters.collection_id===c.id?"page":null,
   onclick:()=>select(c.id)
  },icon("folder",16),h("span.nav-label",c.name),h("span.nav-count",String(c.item_count)))));
 }
 renderContext();
}
function renderContext(){
 const id=state.filters.collection_id,c=catalog.find(x=>x.id===id),box=byId("collection-context");
 if(!box)return;box.hidden=!id;
 clear(box,id?[h("span",c?.description || (c?.archived?"已归档合集":"按你安排的顺序阅读")),button("管理",()=>edit(id),"link-btn")]:[]);
}
// One closure owns one operation key. A lost response retries the same receipt.
function mutation(id, revision, type, initial){
 let body=null,last="",expected=revision;
 return async values=>{
  const fields=values??initial,signature=JSON.stringify(fields);
  if(!body||signature!==last){last=signature;body={operation_key:uuid(),expected_revision:expected,type,...fields};}
  try{
   const result=await fetchJSON(`/api/collections/${id}/operations`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
   expected=result.collection.revision;body=null;invalidateQueryReads();loadEpoch++;loading=null;
   catalog=[...catalog.filter(c=>c.id!==id),result.collection];renderNav();emit("collections:loaded");emit("collections:changed",id);
   // A failed catalog refresh must not turn a committed write into a new operation.
   load().catch(()=>{});return result.collection;
  }catch(e){
   if(e.message==="revision_conflict"){
    const current=await fetchJSON(`/api/collections/${id}`);expected=current.collection.revision;
    if(await confirmAction({title:"合集已在其他设备更新",message:`最新名称：${current.collection.name}。${type==="note"?"最新备注："+(current.items.find(item=>item.link_id===fields.link_id)?.note||"无") : "最新说明："+(current.collection.description||"无")}。你的编辑仍保留，按最新版本重新提交这次操作？`,confirmLabel:"重新提交"})) {body=null;return mutation(id,expected,type,fields)();}
   }
   throw e;
  }
 };
}
function create(onCreated){
 const name=h("input.collection-input",{placeholder:"例如：个人网站改版",maxLength:80,"aria-label":"合集名称"});
 const description=h("textarea.collection-input",{placeholder:"这个合集用来做什么？（可选）",maxLength:2000,rows:3,"aria-label":"合集说明"});
 const id=uuid(),save=mutation(id,0,"create");
 openDialog({title:"新建合集",body:[h("label.collection-field","名称",name),h("label.collection-field","说明",description)],initialFocus:()=>name,
 actions:[{label:"创建",primary:true,run:async()=>{if(!name.value.trim()){name.focus();return false;}try{await save({name:name.value.trim(),description:description.value});await onCreated?.(id);}catch(e){failure(e);return false;}}}]});
}
export async function browse(){
 try{await load();}catch(e){failure(e);return;}
 const query=h("input.collection-input",{placeholder:"查找合集","aria-label":"查找合集",type:"search"});
 const mode=h("select.collection-input",{"aria-label":"合集范围"},[ ["active","使用中"],["archived","已归档"],["deleted","已删除"] ].map(([value,label])=>h("option",{value},label)));
 const rows=h("div.collection-list");let dialog;
 const render=()=>clear(rows,catalog.filter(c=>mode.value==="deleted"?c.deleted:!c.deleted&&Boolean(c.archived)===(mode.value==="archived")).filter(c=>(c.name+" "+c.description).toLowerCase().includes(query.value.toLowerCase())).map(c=>h("div.collection-row",
  button(`${c.pinned?"☆ ":""}${c.name} · ${c.item_count}`,()=>{if(c.deleted)return;dialog.close();select(c.id);},"collection-title"),
  c.deleted?button("恢复",async()=>{await mutation(c.id,c.revision,"restore",{})();render();}):button("管理",()=>edit(c.id,render),"link-btn"))));
 query.addEventListener("input",render);mode.addEventListener("change",render);
 dialog=openDialog({title:"合集",body:[h("div.management-toolbar",query,h("div.collection-tools",mode,h("div.management-actions",button("新建",()=>create(render)),button("自动整理",()=>{dialog.close();return openOrganizing(catalog,uuid,()=>{load().catch(()=>{});hooks.reload();});})))),rows],wide:true});render();
}
export async function pick(ids){
 ids=[...new Set(ids.filter(Boolean))];if(!ids.length)return;
 if(ids.length>100){toast("一次最多选择 100 条收藏");return;}
 let definitions;
 try{definitions=(await fetchJSON(`/api/collections?link_ids=${ids.join(',')}`)).items.filter(c=>!c.deleted&&!c.archived);}catch(e){failure(e);return;}
 const touched=new Map(),requests=new Map();
 const query=h("input.collection-input",{type:"search",placeholder:"查找合集","aria-label":"查找合集"}),rows=h("div.collection-list");
 const render=()=>clear(rows,definitions.filter(c=>c.name.toLowerCase().includes(query.value.toLowerCase())).map(c=>{
  const input=h("input",{type:"checkbox",checked:touched.has(c.id)?touched.get(c.id):c.selected_count===ids.length,"aria-label":c.name});
  input.indeterminate=!touched.has(c.id)&&c.selected_count>0&&c.selected_count<ids.length;
  input.addEventListener("change",()=>{touched.set(c.id,input.checked);requests.delete(c.id);});
  return h("label.collection-pick",input,h("span",c.name),h("small",`${c.item_count} 条`));
 }));
 query.addEventListener("input",render);
 openDialog({title:ids.length===1?"加入合集":`将 ${ids.length} 条收藏加入合集`,body:[h("div.collection-tools",query,button("新建",()=>create(async id=>{
   definitions=(await fetchJSON(`/api/collections?link_ids=${ids.join(',')}`)).items.filter(c=>!c.deleted&&!c.archived);touched.set(id,true);render();
 }))),rows,h("p.collection-hint","可加入多个合集，移出不会删除收藏。")],actions:[{label:"保存",primary:true,run:async()=>{
  for(const [id,selected] of touched){
   const c=definitions.find(c=>c.id===id);if(!requests.has(id))requests.set(id,mutation(id,c.revision,selected?"add":"remove",{link_ids:ids}));
   try{await requests.get(id)();touched.delete(id);c.selected_count=selected?ids.length:0;}catch(e){failure(e);render();return false;}
  }
  toast("合集已更新",{tone:"ok"});
 }}]});render();
}
async function edit(id,after){
 let detail;
 try{detail=await fetchJSON(`/api/collections/${id}`);}catch(e){failure(e);return;}
 let c=detail.collection,items=detail.items;
 const name=h("input.collection-input",{value:c.name,maxLength:80,"aria-label":"合集名称"}),description=h("textarea.collection-input",{value:c.description,maxLength:2000,rows:2,"aria-label":"合集说明"});
 const pinned=h("input",{type:"checkbox",checked:!!c.pinned}),archived=h("input",{type:"checkbox",checked:!!c.archived});
 const rows=h("div.collection-list"),ruleSummary=h("p.collection-rule-summary");let save=mutation(id,c.revision,"edit"),dragged=null;
 const reload=async()=>{detail=await fetchJSON(`/api/collections/${id}`);c=detail.collection;items=detail.items;save=mutation(id,c.revision,"edit");render();await after?.();};
 const move=async(link,before)=>{await mutation(id,c.revision,"move",{link_id:link,before_id:before})();await reload();};
 const render=()=>{ruleSummary.textContent=c.rule_enabled?`规则已启用 · 匹配${c.rule_mode==="all"?"全部":"任一"}标签`:"自动收录未启用";return clear(rows,items.length?items.map((item,index)=>h("div.collection-member",{draggable:true,ondragstart:()=>dragged=item.link_id,ondragover:e=>e.preventDefault(),ondrop:async e=>{e.preventDefault();if(dragged&&dragged!==item.link_id){try{await move(dragged,item.link_id);}catch(err){failure(err);}}dragged=null;}},
   h("div.collection-member-copy",h("span",item.title),item.note?h("small",item.note):null,item.origin==="rule"?h("small","按标签自动收录"):item.origin==="organize"?h("small","AI 整理加入"):null),
   h("div.collection-member-actions",index?button("↑",()=>move(item.link_id,items[index-1].link_id)):null,index<items.length-1?button("↓",()=>move(item.link_id,items[index+2]?.link_id??null)):null,
    button("备注",()=>note(c,item,reload)),button("移出",async()=>{await mutation(id,c.revision,"remove",{link_ids:[item.link_id]})();await reload();})))):h("p.collection-hint","还没有内容。在阅读页或列表多选后加入收藏。"));};
 const dialog=openDialog({title:"管理合集",wide:true,body:[h("label.collection-field","名称",name),h("label.collection-field","说明",description),h("div.collection-tools",h("label",pinned," 置顶"),h("label",archived," 归档")),button("自动收录标签",()=>editRule(c,reload)),ruleSummary,h("p.collection-hint","拖动或使用箭头调整顺序；备注只属于这个合集。"),rows],
 actions:[{label:"删除合集",danger:true,run:async()=>{
  if(!await confirmAction({title:"删除这个合集？",message:"收藏和原有备注会保留，可以在“已删除”中恢复合集。",confirmLabel:"删除合集",danger:true}))return false;
  try{const deleted=await mutation(id,c.revision,"delete",{})();toast("合集已删除，收藏仍保留",{action:{label:"撤销",run:async()=>{try{await mutation(id,deleted.revision,"restore",{})();await after?.();}catch(e){failure(e);}}}});await after?.();}catch(e){failure(e);return false;}
 }},{label:"保存",primary:true,run:async()=>{
  try{await save({name:name.value,description:description.value,pinned:pinned.checked,archived:archived.checked});await after?.();}catch(e){failure(e);return false;}
 }}]});
 render();return dialog;
}
async function editRule(c,after){
 const choices=await managedTagChoices(),selected=new Set(JSON.parse(c.rule_tags||"[]"));
 const enabled=h("input",{type:"checkbox",checked:!!c.rule_enabled}),mode=h("select.collection-input",{"aria-label":"标签匹配方式"},h("option",{value:"any"},"匹配任一标签"),h("option",{value:"all"},"匹配全部标签"));mode.value=c.rule_mode||"any";
 let save=mutation(c.id,c.revision,"rule"),latest=c;
 const summary=h("p.collection-hint","保存后，只自动加入新的收藏；已加入的内容会保留。手动移出后不会再次自动加入。"),preview=h("div");
 const known=new Set(choices.map(t=>t.ref));for(const ref of selected)if(!known.has(ref))choices.push({ref,label:ref,dimension:"已停用或已缺失，请移除"});
 const previewExisting=async()=>{
  const current=await fetchJSON(`/api/collections/${c.id}`);latest=current.collection;
  const result=await fetchJSON(`/api/collections/${c.id}/rules/preview`);
  clear(preview,result.rule_valid?h("div",h("p",`已保存规则可补收 ${result.items.length}${result.has_more?"+":""} 条`),result.items.map(i=>h("p.collection-hint",i.title)),result.items.length?button("按已保存规则补收",async()=>{
   if(!await confirmAction({title:"补收已有收藏？",message:`按已保存规则加入这 ${result.items.length} 条收藏，手动排除的内容会跳过。`,confirmLabel:"补收"}))return;
   await mutation(c.id,result.revision,"backfill")();await after();await previewExisting();
  }):null):h("p","规则中的标签尚未保存或已停用，请先修改并保存。"));
 };
 openDialog({title:"合集自动收录",body:[h("label",enabled," 按标签自动收录新收藏"),mode,tagPicker(choices,selected),summary,button("预览已保存规则的已有收藏",previewExisting),preview],actions:[{label:"保存规则",primary:true,run:async()=>{
  try{await save({enabled:enabled.checked,mode:mode.value,tag_refs:[...selected]});await after();}catch(e){failure(e);return false;}
 }}]});
}
function note(c,item,after){
 const input=h("textarea.collection-input",{value:item.note,rows:4,maxLength:2000,"aria-label":"合集内备注"});
 const save=mutation(c.id,c.revision,"note",{link_id:item.link_id});
 openDialog({title:"合集内备注",body:input,initialFocus:()=>input,actions:[{label:"保存",primary:true,run:async()=>{try{await save({link_id:item.link_id,note:input.value});await after();}catch(e){failure(e);return false;}}}]});
}
export function initCollections(options){
 hooks=options;
 byId("browse-collections")?.addEventListener("click",()=>browse());
 byId("add-to-collection")?.addEventListener("click",()=>pick([state.selectedId]));
 on("collections:changed",id=>{if(state.filters.collection_id===id)hooks.reload();renderContext();});
 on("list:loaded",renderNav);
 load().catch(()=>{});
 window.addEventListener("focus",()=>load().catch(()=>{}));
}
