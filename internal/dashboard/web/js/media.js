import {h} from './dom.js';
import {fetchJSON} from './api.js';
import {cachedBlob} from './blob-cache.js';
import {on} from './store.js';
const listings=new Map();
let generation=0,currentKey='',objectURLs=[],players=[],observer=null,controller=null,boxes=[];
function clear(){boxes=[];controller?.abort();controller=null;observer?.disconnect();observer=null;for(const p of players){p.pause();p.removeAttribute('src');p.load();}players=[];generation++;currentKey='';for(const u of objectURLs)URL.revokeObjectURL(u);objectURLs=[];}
function placeMedia(container,body){
 for(const [index,box] of boxes.entries()){
  const slot=body?.querySelector(`[data-media-index="${index}"]`);
  if(slot&&box.dataset.ready==='true'){
   for(const node of slot.children)if(node!==box)node.hidden=true;
   if(slot.dataset.poster==='true'&&slot.previousElementSibling?.tagName==='FIGURE')slot.previousElementSibling.hidden=true;
   if(box.parentElement!==slot)slot.append(box);
  }else if(box.parentElement!==container)container.append(box);
 }
 container.hidden=!container.children.length;
}
export async function renderMedia(container,item,body){
 const key=`${item.id}:${item.cache_identity?.body_revision ?? '?'}`;
 if(currentKey===key){placeMedia(container,body);return;}
 clear();currentKey=key;controller=new AbortController();const signal=controller.signal;const token=generation;container.replaceChildren();container.hidden=true;
 let result=listings.get(key);
 try{
  if(!result||result.until<Date.now()){
   const data=await fetchJSON(`/api/bookmarks/${item.id}/media`);result={items:data.items||[],until:Date.now()+300000};listings.set(key,result);
   while(listings.size>40)listings.delete(listings.keys().next().value);
  }
  if(generation!==token)return;
  for(const m of result.items){
   const title=h('p.media-title',m.title||(m.kind==='audio'?'音频':'视频'));
   const box=h('section.archived-media',{dataset:{ready:String(m.status==='ready')}},title);boxes.push(box);
   if(m.status!=='ready')box.append(h('p.muted','媒体尚未归档完成，请在浏览器插件的待上传队列中继续上传。'));
   else if(m.content_type==='application/zip'){
    box.append(h('p.muted','已归档完整播放列表和媒体分片。'),h('a.btn',{href:`/api/media/${m.id}`,download:'video-hls.zip'},'下载完整流式媒体包'));
   }else{
    const player=h(m.kind==='audio'?'audio':'video',{controls:true,preload:'metadata',playsInline:true,'aria-label':m.title||'归档视频'});
    const status=h('p.media-status','正在读取预览…');
    const frame=h('div.media-preview',player,status);
    const download=h('details.media-more',h('summary','下载原始文件'),h('a.media-download',{href:`/api/media/${m.id}`,download:''},'下载'));
    let started=false;
    const load=async()=>{
     if(started||signal.aborted)return;started=true;
     try{
      let source=`/api/media/${m.id}`;
      // Small clips keep the bounded session cache. Large videos use Range
      // requests directly, so displaying a frame does not fetch the whole file.
      if(m.size<=8*1024*1024){const blob=await cachedBlob(source,signal);if(generation!==token)return;source=URL.createObjectURL(blob);objectURLs.push(source);}
      if(generation!==token)return;
      player.src=source+(m.kind==='video'?'#t=0.001':'');player.load();
     }catch(error){if(signal.aborted)return;started=false;status.replaceChildren(h('button.btn',{type:'button',onclick:load},'预览读取失败，点击重试'));}
    };
    player.addEventListener('loadeddata',()=>{status.hidden=true;});
    player.addEventListener('loadedmetadata',()=>{if(m.kind==='audio')status.hidden=true;else if(player.videoWidth&&player.videoHeight)player.style.aspectRatio=`${player.videoWidth} / ${player.videoHeight}`;});
    player.addEventListener('error',()=>{if(signal.aborted)return;status.hidden=false;started=false;status.replaceChildren(h('button.btn',{type:'button',onclick:load},'预览读取失败，点击重试'));});
    players.push(player);box.append(frame,download);
    // The first frame becomes visible before playback; never autoplay sound.
    if('IntersectionObserver' in window){
     if(!observer)observer=new IntersectionObserver(entries=>{for(const e of entries)if(e.isIntersecting){observer?.unobserve(e.target);e.target.dispatchEvent(new Event('media:visible'));}},{rootMargin:'240px'});
     frame.addEventListener('media:visible',load,{once:true});observer.observe(frame);
    }else void load();
   }
   container.append(box);
  }
  placeMedia(container,body);
 }catch{
  if(generation!==token)return;
  // Older backends have no media route; all existing reading remains usable.
  currentKey='';
 }
}
export function resetMedia(){clear();}
on('account:changed',()=>{clear();listings.clear();});
on('library:changed',()=>{listings.clear();});
