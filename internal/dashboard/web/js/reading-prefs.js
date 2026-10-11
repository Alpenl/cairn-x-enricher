// Per-device reading preferences (decision: never synced). Default version
// decides which side of 原文 / 整理版 a bookmark opens on; font size only
// changes the reading pane.
const VERSION_KEY = "cairn.reader.default-version";
const SIZE_KEY = "cairn.reader.font-size";
export const FONT_SIZES = Object.freeze([16, 17, 18, 20, 22]);
export const DEFAULT_SIZE = 17;
export const DEFAULT_VERSION = "organized";
const get = (key) => { try { return localStorage.getItem(key); } catch { return null; } };
const put = (key, value) => { try { if (value === null) localStorage.removeItem(key); else localStorage.setItem(key, String(value)); } catch {} };
export function defaultVersion() {
  return get(VERSION_KEY) === "original" ? "original" : DEFAULT_VERSION;
}
export function setDefaultVersion(value) {
  put(VERSION_KEY, value === DEFAULT_VERSION ? null : value);
}
export function fontSize() {
  const n = Number(get(SIZE_KEY));
  return FONT_SIZES.includes(n) ? n : DEFAULT_SIZE;
}
export function setFontSize(n) {
  put(SIZE_KEY, n === DEFAULT_SIZE ? null : n);
  applyFontSize();
}
export function applyFontSize() {
  document.documentElement.style.setProperty("--reader-font-size", `${fontSize()}px`);
}
