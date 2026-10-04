import {build} from "esbuild";
import {readFile,writeFile} from "node:fs/promises";
const target="internal/dashboard/web/js/vendor/reader.js";
const result=await build({entryPoints:["internal/dashboard/reader/vendor.js"],bundle:true,format:"esm",platform:"browser",target:"es2022",minify:true,legalComments:"eof",write:false});
const output=result.outputFiles[0].text;
if(process.argv.includes("--check")){if(await readFile(target,"utf8")!==output)throw new Error("Reader bundle is stale: npm run build-reader");}
else await writeFile(target,output);
console.log(`Reader bundle: ${Buffer.byteLength(output)} bytes`);
