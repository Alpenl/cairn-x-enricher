// Small, safe Markdown subset. Source HTML is always text; remote images are never embedded.
import { h } from "./dom.js";
function inline(parent,text) {
 const tokens=/(!?\[([^\]\n]*)\]\(([^\s]+)\)|\*\*([^*]+)\*\*|`([^`]+)`)/g;
 let last=0;
 for(const m of text.matchAll(tokens)) {
  parent.append(document.createTextNode(text.slice(last,m.index)));
  if(m[4])parent.append(h("strong",m[4]));
  else if(m[5])parent.append(h("code",m[5]));
  else {
   let url;try{url=new URL(m[3]);}catch{}
   if(url&&/^https?:$/.test(url.protocol)&&!url.username&&!url.password) {
    const a=h("a",m[2]||url.host);a.href=url.href;a.target="_blank";a.rel="noopener noreferrer";parent.append(a);
   }else parent.append(document.createTextNode(m[2]||m[0]));
  }
  last=m.index+m[0].length;
 }
 parent.append(document.createTextNode(text.slice(last)));
}
export function renderReading(container,text,{image}={}) {
 const fragment=document.createDocumentFragment();
 const lines=(text||"").replace(/\r/g,"").split("\n");
 let paragraph=[],list=null,code=null;
 const flush=()=>{if(paragraph.length){const p=h("p");inline(p,paragraph.join("\n"));fragment.append(p);paragraph=[];}list=null;};
 for(let i=0;i<lines.length;i++) {
  const line=lines[i];
  if(/^\s*```/.test(line)){flush();if(code){fragment.append(h("pre",h("code",code.join("\n"))));code=null;}else code=[];continue;}
  if(code){code.push(line);continue;}
  if(!line.trim()){flush();continue;}
  const asset=line.trim().match(/^!\[([^\]]*)\]\(cairn-image:(\d+)\)$/);
  if(asset&&image){flush();const node=image(Number(asset[2]),asset[1]);if(node)fragment.append(node);continue;}
  const heading=line.match(/^(#{1,6})\s+(.+)$/);
  if(heading){flush();const node=h(`h${Math.min(4,heading[1].length+1)}`);inline(node,heading[2]);fragment.append(node);continue;}
  const bullet=line.match(/^\s*(?:([-*+•])|([0-9]+)[.)])\s+(.+)$/);
  if(bullet){if(paragraph.length)flush();const tag=bullet[2]?"ol":"ul";if(!list||list.tagName.toLowerCase()!==tag){list=h(tag);if(bullet[2])list.start=Number(bullet[2]);fragment.append(list);}const li=h("li");inline(li,bullet[3]);list.append(li);continue;}
  if(/^>\s?/.test(line)){flush();const q=h("blockquote");inline(q,line.replace(/^>\s?/,""));fragment.append(q);continue;}
  if(line.includes("|")&&/^\s*\|?\s*:?-{3,}/.test(lines[i+1]||"")) {
   flush();const table=h("table"),head=h("thead"),body=h("tbody");
   const cells=s=>s.trim().replace(/^\||\|$/g,"").split("|");
   const row=(values,tag)=>{const tr=h("tr");for(const value of values){const td=h(tag);inline(td,value.trim());tr.append(td);}return tr;};
   head.append(row(cells(line),"th"));i++;while(i+1<lines.length&&lines[i+1].includes("|")){body.append(row(cells(lines[++i]),"td"));}table.append(head,body);fragment.append(h("div.reading-table",table));continue;
  }
  list=null;paragraph.push(line);
 }
 flush();if(code)fragment.append(h("pre",h("code",code.join("\n"))));container.replaceChildren(fragment);
}
