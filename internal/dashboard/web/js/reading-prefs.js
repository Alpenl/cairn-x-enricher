// Per-device reading preferences (decision: never synced). Default version
// decides which side of 原文 / 整理版 a bookmark opens on; font size only
// changes the reading pane.
const VERSION_KEY = "cairn.reader.default-version";
const SIZE_KEY = "cairn.reader.font-size";
// Prototype 小 / 中 / 大. Older builds stored 18/20/22; those snap to the nearest step.
export const FONT_SIZES = Object.freeze([16, 17, 19]);
export const FONT_SIZE_LABELS = Object.freeze({ 16: "小", 17: "中", 19: "大" });
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
  if (!Number.isFinite(n) || n <= 0) return DEFAULT_SIZE;
  return FONT_SIZES.reduce((best, x) => (Math.abs(x - n) < Math.abs(best - n) ? x : best), DEFAULT_SIZE);
}
export function setFontSize(n) {
  put(SIZE_KEY, n === DEFAULT_SIZE ? null : n);
  applyFontSize();
}
export function applyFontSize() {
  document.documentElement.style.setProperty("--reader-font-size", `${fontSize()}px`);
}
