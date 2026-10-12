// Copies the shared Cairn design tokens and fonts from a cairn-share checkout
// (design/tokens/generated/tokens.css, design/fonts). The web workspace keeps
// legacy variable names, so the shared `--surface` (white card) is renamed to
// `--raised`; everything else keeps its shared name.
//   node scripts/sync-design-tokens.mjs [../cairn-share]
import { copyFile, readFile, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { execFileSync } from "node:child_process";
const share = resolve(process.argv[2] || "../cairn-share");
const revision = execFileSync("git", ["-C", share, "rev-parse", "--short=12", "HEAD"], { encoding: "utf8" }).trim();
const source = await readFile(join(share, "design/tokens/generated/tokens.css"), "utf8");
const body = source.replace(/^\/\*.*?\*\/\n/s, "").replace(/--surface:/g, "--raised:");
const web = "internal/dashboard/web";
await writeFile(join(web, "cairn-tokens.css"), `/* Shared Cairn design tokens (cairn-share ${revision}, design/tokens). Synced by scripts/sync-design-tokens.mjs; do not edit. */\n${body}`);
for (const name of ["dm-sans-latin-wght-normal.woff2", "source-serif-4-latin-wght-normal.woff2", "source-serif-4-latin-wght-italic.woff2", "OFL-DM-Sans.txt", "OFL-Source-Serif-4.txt"])
  await copyFile(join(share, "design/fonts", name), join(web, "fonts", name));
console.log(`synced tokens and fonts from cairn-share ${revision}`);
