// Keep citations separate from the prose without losing their inline context.
export function relatedReadingLinks(item, extracted = []) {
 const links=new Map();let origin;try{origin=new URL(item.url);}catch{}
 for(const entry of [...extracted,...(item.related_links||[]).map(url=>({url,title:''}))]) {
  let u;try{u=new URL(entry.url);}catch{continue;}
  if(!/^https?:$/.test(u.protocol)||u.username||u.password)continue;
  if(origin&&u.origin===origin.origin&&u.pathname===origin.pathname)continue;
  if(u.hostname==='cairn.invalid'||/\/api\/(?:images|media)\//.test(u.pathname))continue;
  if(/(^|\.)(x|twitter)\.com$/.test(u.hostname)) {
   if(/\/status\/\d+\/(analytics|quotes)|^\/i\/(premium|subscribe)|\/media\/\d+/.test(u.pathname))continue;
   if(origin&&u.pathname.replace(/\/$/,'')===`/${origin.pathname.split('/')[1]}`)continue;
  }
  if(/\.(?:mp4|webm|mov|m3u8|mp3|wav|png|jpe?g|gif|webp)(?:$)/i.test(u.pathname))continue;
  const key=u.host+u.pathname+u.search+u.hash;
  if(!links.has(key)||u.protocol==='https:'&&links.get(key).url.startsWith('http:'))links.set(key,{url:u.href,title:entry.title&&entry.title!==u.href?entry.title:u.href});
 }
 return [...links.values()];
}
