// Parsing and extraction are provided by Marked and Defuddle; DOMPurify is the
// final safety boundary. Only Cairn archive identities are resolved into images.
import { h } from "./dom.js";
import { Defuddle, Marked, DOMPurify } from "./vendor/reader.js";
import { adaptLegacyCapture } from "./reading-legacy.js";

const parser = new Marked({gfm:true, breaks:true});
// Archive HTML is source text, never executable markup, even in the raw view.
parser.use({renderer:{html({text}){return text.replace(/&/g,"&amp;").replace(/</g,"&lt;").replace(/>/g,"&gt;");}}});
const allowedTags=['p','br','hr','h1','h2','h3','h4','h5','h6','ul','ol','li','blockquote','pre','code','strong','em','del','a','img','table','thead','tbody','tr','th','td','sup','sub','div','span'];
function archiveMarkdown(text) {
 // Compatibility for linked images produced by the old browser collector.
 return text.replace(/^\[!([^\[\]\n]*)\(cairn-image:(\d+)\)\]\([^\n]*\)$/gm,'![$1](cairn-image:$2)')
  .replace(/^\[(!\[[^\]]*\]\(cairn-image:\d+\))\]\([^\n]*\)$/gm,'$1');
}
export function renderReading(container,text,{image,url='',clean=true}={}) {
 const original=archiveMarkdown(text||'').replace(/(^>[^\n]*)\n\n(?=>)/gm,'$1\n>\n');
 // Preserve our private URI scheme during sanitization, then remove all src
 // attributes before mounting anything into the live document.
 const html=parser.parse(original.replace(/\[([^\]]*)\]\(cairn-media:(\d+)\)/g,'[$1](https://cairn.invalid/media-placeholder/$2)')).replace(/src="cairn-image:(\d+)"/g,'src="https://cairn.invalid/archive/$1"');
 const doc=new DOMParser().parseFromString('<!doctype html><html><head><title></title></head><body><article id="cairn-article"></article></body></html>','text/html');
 const root=doc.querySelector('article');
 root.append(DOMPurify.sanitize(html,{RETURN_DOM_FRAGMENT:true,ALLOWED_TAGS:allowedTags,ALLOWED_ATTR:['href','src','alt','title','start','colspan','rowspan'],ALLOW_DATA_ATTR:false,ALLOW_ARIA_ATTR:false}));
 for(const img of root.querySelectorAll('img')) {
  const match=(img.getAttribute('src')||'').match(/^https:\/\/cairn\.invalid\/archive\/(\d+)$/);
  if(match) {img.setAttribute('src',`cairn-image:${match[1]}`);img.setAttribute('width','800');img.setAttribute('height','600');}
  else img.replaceWith(doc.createTextNode(img.alt||'图片未归档'));
 }
 let changed=false;
 if(clean) {
  changed=adaptLegacyCapture(root,url);
  // The archive is already an article: score its blocks, never fetch another
  // page or third-party extractor API. Keep uncertain images and source text.
  try {
   const result=new Defuddle(doc.cloneNode(true),{url:'https://cairn.invalid/article',contentSelector:'#cairn-article',useAsync:false,includeReplies:false,removeSmallImages:false,removeHiddenElements:false}).parse();
   if(result.content?.trim()) {
    const extracted=new DOMParser().parseFromString(result.content,'text/html').body;
    const beforeImages=[...root.querySelectorAll('img')].map(n=>n.getAttribute('src'));
    const afterImages=[...extracted.querySelectorAll('img')].map(n=>n.getAttribute('src'));
    // Never silently lose an archived image in a generic extraction heuristic.
    const headings=[...root.querySelectorAll('h1,h2,h3,h4,h5,h6')].map(n=>n.textContent.trim());
    const keptHeadings=[...extracted.querySelectorAll('h1,h2,h3,h4,h5,h6')].map(n=>n.textContent.trim());
    const code=[...root.querySelectorAll('pre')].map(n=>n.textContent);
    const keptCode=[...extracted.querySelectorAll('pre')].map(n=>n.textContent);
    const media=[...root.querySelectorAll('a[href^="https://cairn.invalid/media-placeholder/"]')].map(n=>n.getAttribute('href'));
    const keptMedia=[...extracted.querySelectorAll('a[href^="https://cairn.invalid/media-placeholder/"]')].map(n=>n.getAttribute('href'));
    if(beforeImages.every(src=>afterImages.includes(src))&&headings.every(t=>keptHeadings.includes(t))&&code.every(t=>keptCode.includes(t))&&media.every(t=>keptMedia.includes(t))){changed ||= root.textContent.trim()!==extracted.textContent.trim();root.replaceChildren(...extracted.childNodes);}
   }
  } catch { /* The sanitized archived document remains readable. */ }
 }
 // DOMPurify's default URL policy rejects Cairn's private URI; use a temporary
 // inert HTTPS identity and resolve it only after the final sanitization.
 const finalHTML=root.innerHTML.replace(/src="cairn-image:(\d+)"/g,'src="https://cairn.invalid/archive/$1"');
 const fragment=DOMPurify.sanitize(finalHTML,{RETURN_DOM_FRAGMENT:true,ALLOWED_TAGS:allowedTags,ALLOWED_ATTR:['href','src','alt','title','start','colspan','rowspan'],ALLOW_DATA_ATTR:false,ALLOW_ARIA_ATTR:false});
 const renderedImages=new Set();
 for(const img of fragment.querySelectorAll('img')) {
  const match=(img.getAttribute('src')||'').match(/^https:\/\/cairn\.invalid\/archive\/(\d+)$/);
  img.removeAttribute('src');
  const index=match?Number(match[1]):-1;
  const alt=/^(图片|图像|image)$/i.test(img.alt||'')?'':img.alt;
  const node=index>=0&&image?image(index,alt):null;
  if(node){const parent=img.closest('a')||img;parent.replaceWith(node);renderedImages.add(index);}
  else img.replaceWith(h('span','图片未归档'));
 }
 for(const a of fragment.querySelectorAll('a')) {
  const media=(a.getAttribute('href')||'').match(/^https:\/\/cairn\.invalid\/media-placeholder\/(\d+)(\?poster=1)?$/);
  if(media){const slot=h('div.reading-media-slot',{dataset:{mediaIndex:media[1],poster:String(Boolean(media[2]))}},h('a',{href:url||'#'},'媒体未归档，查看原文'));const parent=a.parentElement?.tagName==='P'&&a.parentElement.childNodes.length===1?a.parentElement:a;parent.replaceWith(slot);continue;}

  try {const u=new URL(a.getAttribute('href'));if(!/^https?:$/.test(u.protocol)||u.username||u.password)throw 0;a.href=u.href;a.target='_blank';a.rel='noopener noreferrer';}
  catch {a.replaceWith(...a.childNodes);}
 }
 for(const heading of fragment.querySelectorAll('h1,h2,h3,h4,h5,h6')) {
  const level=Math.min(4,Number(heading.tagName.slice(1))+1),replacement=h(`h${level}`);replacement.append(...heading.childNodes);heading.replaceWith(replacement);
 }
 for(const table of fragment.querySelectorAll('table')){const wrapper=h('div.reading-table');table.replaceWith(wrapper);wrapper.append(table);}
 for(const p of fragment.querySelectorAll('p'))if(p.childNodes.length===1&&p.firstChild.tagName==='FIGURE')p.replaceWith(p.firstChild);
 const links=[...fragment.querySelectorAll("a[href]")].map(a=>({url:a.href,title:a.textContent.trim()}));
 container.replaceChildren(fragment);container.dataset.cleaned=String(changed);
 return {images:renderedImages,links,cleaned:changed};
}

// A Chinese source is already readable. A generated Chinese rewrite must not
// replace its archived structure, links or inline image positions.
export function readingVersions(item, unformatted = false) {
 const original = item.original_text || "", translated = item.translated_text || "";
 const chinese = /^(zh(?:[-_].*)?|chinese|中文|简体中文|繁体中文)$/i.test((item.original_language || "").trim());
 const base = chinese && original ? original : translated || original;
 const formatted = !unformatted && item.formatted_content;
 return { body: formatted || base, label: formatted ? "整理版" : chinese && original ? "原文" : translated && translated !== original ? "译文" : "原内容" };
}
