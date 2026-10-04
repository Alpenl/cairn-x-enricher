// Narrow migration adapter for old X captures that lost DOM roles. This does
// not select article content: Defuddle owns extraction, Marked owns parsing.
export function adaptLegacyCapture(root, sourceURL) {
 let source;try{source=new URL(sourceURL);}catch{return false;}
 if(!/(^|\.)(x|twitter)\.com$/.test(source.hostname))return false;
 const author=source.pathname.split("/")[1],post=source.pathname.match(/\/status\/(\d+)/)?.[1];
 if(!author||!post)return false;
 let changed=false;
 const remove=node=>{node.remove();changed=true;};
 const text=node=>(node?.textContent||"").trim();
 const ownURL=href=>{try{const u=new URL(href);return /(^|\.)(x|twitter)\.com$/.test(u.hostname)?u:null;}catch{return null;}};
 // The old collector put the same author/profile twice ahead of the content.
 for(const p of [...root.children].slice(0,3)){
  const link=p.querySelector("a");const u=link&&ownURL(link.href);
  if(p.tagName==="P"&&u&&u.pathname.replace(/\/$/,"").toLowerCase()===`/${author.toLowerCase()}`&&text(p)===text(link))remove(p);
  else break;
 }
 for(const p of [...root.children].slice(0,2))if(/^翻译自\s*\S+$|^Translated from .+$/i.test(text(p)))remove(p);
 for(const a of [...root.querySelectorAll("a[href]")]){
  if(a.closest("pre,code,blockquote,li"))continue;
  const u=ownURL(a.href),p=a.closest("p");if(!u||!p||text(p)!==text(a))continue;
  if(new RegExp(`^/${author}/status/${post}/(?:analytics|quotes)/?$`,"i").test(u.pathname)){remove(p);continue;}
  if(u.pathname===`/${author}/status/${post}`&&/\d{1,2}:\d{2}.+\d{4}|\d{4}.+\d{1,2}:\d{2}/.test(text(a))){remove(p);continue;}
  if(u.pathname==="/i/premium_sign_up"){
   const prev=p.previousElementSibling;
   if(/^(想发布自己的文章[？?]|Want to publish your own Article[？?])$/i.test(text(prev)))remove(prev);
   remove(p);
  }
 }
 for(const p of [...root.children]){
  if(p.tagName!=="P")continue;
  if(/^(评价此翻译[:：]?|Rate this translation[:：]?)$/i.test(text(p))){
   const next=p.nextElementSibling;if(/^\d{1,2}:\d{2}$/.test(text(next)))remove(next);remove(p);
  }
 }
 // Marked correctly leaves invalid multiline link syntax as text. Repair only
 // legacy analytics links, never arbitrary multiline prose or quoted examples.
 for(const p of [...root.children]){
  if(p.tagName!=="P")continue;
  if(new RegExp(`^查看\\]\\(https://(?:x|twitter)\\.com/${author}/status/${post}/analytics\\)$`,"i").test(text(p))){
   const prev=p.previousElementSibling;if(/^\[[\d.,]+[万亿kKmM]?$/.test(text(prev)))remove(prev);remove(p);
  }
 }
 // A standalone inline link split by the previous collector rejoins only an
 // unfinished sentence. Headings, lists, quotes and finished sentences stay.
 for(const p of [...root.children]){
  const prev=p.previousElementSibling,next=p.nextElementSibling;
  if(p.tagName!=="P"||p.children.length!==1||p.firstElementChild.tagName!=="A"||text(p)!==text(p.firstElementChild)||prev?.tagName!=="P"||next?.tagName!=="P")continue;
  if(!text(prev)||/[。！？.!?:：；;]$/.test(text(prev))||prev.querySelector("img")||next.querySelector("img")||/^https?:/.test(text(p)))continue;
  prev.append(" ",...p.childNodes," ",...next.childNodes);remove(p);remove(next);
 }
 // The old X collector collapsed every newline in tweetText. Intl.Segmenter
 // restores readable sentence boundaries without altering or inventing words.
 const prose=[...root.children].filter(p=>p.tagName==="P"&&text(p).length>120&&!p.querySelector("img,code"));
 if(prose.length===1&&!root.querySelector("h1,h2,h3,h4,blockquote,pre,ul,ol")&&typeof Intl.Segmenter==="function"){
  const p=prose[0];if(!p.children.length){
   const sentences=[...new Intl.Segmenter("zh",{granularity:"sentence"}).segment(text(p))].map(x=>x.segment.trim()).filter(Boolean);
   if(sentences.length>=3){p.replaceWith(...sentences.map(s=>{const node=root.ownerDocument.createElement("p");node.textContent=s;return node;}));changed=true;}
  }
 }
 let mediaIndex=0;
 for(const p of [...root.children])if(p.tagName==="P"&&text(p)==="GIF"){
  const poster=Boolean(p.previousElementSibling?.querySelector("img"));
  const a=root.ownerDocument.createElement("a");a.href=`https://cairn.invalid/media-placeholder/${mediaIndex++}${poster?'?poster=1':''}`;a.textContent="动画未归档，查看原文";p.replaceChildren(a);changed=true;
 }
 return changed;
}
