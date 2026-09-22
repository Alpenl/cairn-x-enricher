// Real browser HTTP cache exercise; never intercept requests or disable cache.
import assert from "node:assert/strict";
import { chromium } from "playwright";
const base=process.env.CAIRN_IMAGE_BASE, key=process.env.CAIRN_IMAGE_KEY;
const browser=await chromium.launch({executablePath:process.env.CHROME_PATH||undefined,args:["--no-sandbox"]});
let checks=0;
function check(name,actual,expected){assert.deepEqual(actual,expected,name);console.log(`ok ${++checks} ${name}`);}
try {
 const page=await browser.newPage();
 await page.goto(base+"/privacy-probe");
 const read=async url=>page.evaluate(async url=>{const r=await fetch(url);return {status:r.status,body:await r.text(),cache:r.headers.get("cache-control")};},url);
 const stats=async()=> (await fetch(base+"/privacy-control")).json();
 const oldURL="/api/images/"+key;
 check("prewarm old release URL",(await read(oldURL)).body,"old-cached-private-image");
 check("browser actually reuses old response",(await read(oldURL)).body,"old-cached-private-image");
 check("old cached response avoided a second HTTP request",(await stats()).legacy,1);
 const url=await page.evaluate(key=>window.CairnUI.imagePath(key),key);
 assert.notEqual(url,oldURL,"production imagePath must migrate the old cache key");checks++; console.log(`ok ${checks} production URL escapes old cache`);
 check("current image comes from actual Go proxy",(await read(url)).body,"synthetic-image-1");
 check("private cache policy",(await read(url)).cache,"private, no-store");
 await fetch(base+"/privacy-control",{method:"POST"});
 check("same current URL sees changed bytes",(await read(url)).body,"synthetic-image-2");
 check("all current reads reached upstream",(await stats()).images,3);
 await fetch(base+"/privacy-control?delete=1",{method:"POST"});
 const gone=await read(url);
 check("deleted owner rejects cached or orphan image",gone.status,404);
 assert(!gone.body.includes("synthetic-image"));checks++;console.log(`ok ${checks} no image bytes on deletion`);
 check("deletion checked before orphan image fetch",(await stats()).images,3);
 await page.reload();
 check("page recreation still rejects deleted image",(await read(url)).status,404);
 console.log(`PASS: ${checks} real-browser cache checks; zero paid calls`);
}finally{await browser.close();}
