// Private, bounded session cache. No media is written into persistent browser
// caches; account changes and explicit mutations clear the in-memory copies.
import { on } from './store.js';
const entries = new Map(), flights = new Map();
const MAX_BYTES = 96 * 1024 * 1024, MAX_ITEMS = 96, TTL = 5 * 60_000;
let bytes = 0, epoch = 0;
function remove(key) { const item=entries.get(key); if(item)bytes-=item.blob.size; entries.delete(key); }
function keep(key,blob) {
  if(blob.size>MAX_BYTES)return;
  remove(key);entries.set(key,{blob,until:Date.now()+TTL});bytes+=blob.size;
  while(bytes>MAX_BYTES||entries.size>MAX_ITEMS)remove(entries.keys().next().value);
}
export function cachedBlob(source, signal) {
  if(signal?.aborted)return Promise.reject(new DOMException('Aborted','AbortError'));
  const item=entries.get(source);
  if(item&&item.until>Date.now()){entries.delete(source);entries.set(source,item);return Promise.resolve(item.blob);}
  remove(source);
  let flight=flights.get(source);
  if(!flight){
    const generation=epoch,controller=new AbortController();flight={controller,users:new Set()};
    flight.promise=fetch(source,{signal:controller.signal,cache:'no-store'}).then(r=>{if(!r.ok)throw new Error('media_unavailable');return r.blob();}).then(blob=>{if(generation===epoch&&!controller.signal.aborted)keep(source,blob);return blob;}).finally(()=>{if(flights.get(source)===flight)flights.delete(source);});
    flights.set(source,flight);
  }
  const user={};flight.users.add(user);
  return new Promise((resolve,reject)=>{
    const release=()=>{flight.users.delete(user);signal?.removeEventListener('abort',abort);};
    const abort=()=>{release();if(!flight.users.size){flight.controller.abort();if(flights.get(source)===flight)flights.delete(source);}reject(new DOMException('Aborted','AbortError'));};
    signal?.addEventListener('abort',abort,{once:true});
    flight.promise.then(value=>{release();resolve(value);},error=>{release();reject(error);});
  });
}
export function clearBlobCache(){epoch++;entries.clear();bytes=0;for(const f of flights.values())f.controller.abort();flights.clear();}
export function blobCacheStats(){return {items:entries.size,bytes,inflight:flights.size,max_bytes:MAX_BYTES};}
on('account:changed',clearBlobCache);
on('library:changed',clearBlobCache);
