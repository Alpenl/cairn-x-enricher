import {h} from './dom.js';
import {fetchJSON} from './api.js';
import {cachedBlob} from './blob-cache.js';
import {on} from './store.js';
const listings=new Map();
let generation=0,currentKey='',objectURLs=[],players=[];
function clear(){for(const p of players){p.pause();p.removeAttribute('src');p.load();}players=[];generation++;currentKey='';for(const u of objectURLs)URL.revokeObjectURL(u);objectURLs=[];}
export async function renderMedia(container,item){
 const key=`${item.id}:${item.cache_identity?.body_revision ?? '?'}`;
 if(currentKey===key)return;
 clear();currentKey=key;const token=generation;container.replaceChildren();container.hidden=true;
 let result=listings.get(key);
 try{
  if(!result||result.until<Date.now()){
   const data=await fetchJSON(`/api/bookmarks/${item.id}/media`);result={items:data.items||[],until:Date.now()+300000};listings.set(key,result);
   while(listings.size>40)listings.delete(listings.keys().next().value);
  }
  if(generation!==token)return;
  for(const m of result.items){
   const title=h('p.media-title',m.title||(m.kind==='audio'?'音频':'视频'));
   const box=h('section.archived-media',title);
   if(m.status!=='ready')box.append(h('p.muted','媒体尚未归档完成，请在浏览器插件的待上传队列中继续上传。'));
   else if(m.content_type==='application/zip'){
    box.append(h('p.muted','已归档完整播放列表和媒体分片。'),h('a.btn',{href:`/api/media/${m.id}`,download:'video-hls.zip'},'下载完整流式媒体包'));
   }else{
    const player=h(m.kind==='audio'?'audio':'video',{controls:true,preload:'none',playsInline:true});
    const button=h('button.btn',{type:'button'},`播放已归档${m.kind==='audio'?'音频':'视频'} · ${(m.size/1024/1024).toFixed(1)} MB`);
    button.addEventListener('click',async()=>{
     button.disabled=true;button.textContent='正在读取媒体…';
     try{
      let source=`/api/media/${m.id}`;
      if(m.size<=64*1024*1024){const blob=await cachedBlob(source);if(generation!==token)return;source=URL.createObjectURL(blob);objectURLs.push(source);}
      player.src=source;button.hidden=true;player.hidden=false;await player.play();
     }catch{button.disabled=false;button.textContent='读取失败，点击重试';}
    });
    players.push(player);player.hidden=true;box.append(button,player,h('a.media-download',{href:`/api/media/${m.id}`,download:''},'下载原始文件'));
   }
   container.append(box);
  }
  container.hidden=!result.items.length;
 }catch{
  if(generation!==token)return;
  // Older backends have no media route; all existing reading remains usable.
  currentKey='';
 }
}
export function resetMedia(){clear();}
on('account:changed',()=>{clear();listings.clear();});
on('library:changed',()=>{listings.clear();});
