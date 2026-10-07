import { build } from "esbuild";
import { readFile, writeFile, mkdir } from "node:fs/promises";
import { icons } from "@awesome.me/webawesome/dist/components/icon/library.system.js";
const check = process.argv.includes("--check");
async function output(path, content) {
  if (check) {
    if ((await readFile(path, "utf8")) !== content)
      throw Error(
        `Stale component asset: ${path}. Run npm run build-components`,
      );
  } else {
    await mkdir(path.slice(0, path.lastIndexOf("/")), { recursive: true });
    await writeFile(path, content);
  }
}
for (const [entry, target] of [
  ["vendor.js", "js/vendor/components.js"],
  ["vendor.css", "components.css"],
]) {
  const result = await build({
    entryPoints: [`internal/dashboard/components/${entry}`],
    bundle: true,
    format: "esm",
    platform: "browser",
    target: "es2022",
    minify: true,
    legalComments: "eof",
    write: false,
  });
  const content = result.outputFiles[0].text;
  await output(`internal/dashboard/web/${target}`, content);
  console.log(`${target}: ${Buffer.byteLength(content)} bytes`);
}
for (const [variant, items] of Object.entries(icons))
  for (const [name, svg] of Object.entries(items))
    await output(
      `internal/dashboard/web/icons/system/${variant}/${name}.svg`,
      svg,
    );
await output(
  "internal/dashboard/web/js/vendor/components-LICENSE.txt",
  await readFile("node_modules/@awesome.me/webawesome/LICENSE.md", "utf8"),
);
