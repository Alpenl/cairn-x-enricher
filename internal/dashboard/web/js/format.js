// Pure formatting helpers. Nothing here touches the DOM, so the module can be
// imported and tested directly in Node.

const DAY = 86400000;

// Intl.DateTimeFormat construction is far more expensive than reuse, and these
// run for every rendered row, so one formatter per option set is cached.
const formatters = new Map();
function formatterFor(options) {
  const key = JSON.stringify(options);
  let formatter = formatters.get(key);
  if (!formatter) {
    formatter = new Intl.DateTimeFormat("zh-CN", options);
    formatters.set(key, formatter);
  }
  return formatter;
}

const CACHE_LIMIT = 2000;
const dateCache = new Map();
export function parseDate(value) {
  if (!value) return null;
  let date = dateCache.get(value);
  if (date === undefined) {
    date = new Date(value);
    if (Number.isNaN(date.getTime())) date = null;
    if (dateCache.size >= CACHE_LIMIT) dateCache.clear();
    dateCache.set(value, date);
  }
  return date;
}

const labelCache = new Map();
function memo(key, compute) {
  let value = labelCache.get(key);
  if (value === undefined) {
    if (labelCache.size >= CACHE_LIMIT) labelCache.clear();
    value = compute();
    labelCache.set(key, value);
  }
  return value;
}

export function startOfDay(date) {
  return new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
}

export function formatDate(value) {
  const date = parseDate(value);
  if (!date) return "-";
  return memo(`d:${date.getTime()}`, () => formatterFor({ year: "numeric", month: "long", day: "numeric" }).format(date));
}

export function formatDateTime(value) {
  const date = parseDate(value);
  if (!date) return "-";
  return memo(`dt:${date.getTime()}`, () => formatterFor({ month: "long", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(date));
}

export function formatFull(value) {
  const date = parseDate(value);
  if (!date) return "-";
  return memo(`f:${date.getTime()}`, () => formatterFor({ year: "numeric", month: "long", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(date));
}

// Relative time reads faster than a clock time for "when does this retry" or
// "how old is this failure". Beyond a day an absolute date is more useful.
export function formatRelative(value, now = Date.now()) {
  const date = parseDate(value);
  if (!date) return "-";
  const minutes = Math.round((date.getTime() - now) / 60000);
  const rtf = new Intl.RelativeTimeFormat("zh-CN", { numeric: "auto" });
  if (Math.abs(minutes) < 60) return rtf.format(minutes, "minute");
  if (Math.abs(minutes) < 60 * 24) return rtf.format(Math.round(minutes / 60), "hour");
  return formatDateTime(value);
}

// Compact list timestamp: a clock time today, then day names, then dates.
export function listTime(value, now = new Date()) {
  const date = parseDate(value);
  if (!date) return "";
  const days = Math.round((startOfDay(now) - startOfDay(date)) / DAY);
  if (days <= 0) return memo(`t:${date.getTime()}`, () => formatterFor({ hour: "2-digit", minute: "2-digit" }).format(date));
  if (days === 1) return "昨天";
  if (days < 7) return `${days} 天前`;
  if (date.getFullYear() === now.getFullYear()) return memo(`md:${startOfDay(date)}`, () => `${date.getMonth() + 1}月${date.getDate()}日`);
  return memo(`ymd:${startOfDay(date)}`, () => `${date.getFullYear()}/${date.getMonth() + 1}/${date.getDate()}`);
}

// The bucket only depends on the calendar day, so it is memoised per day.
export function bucketLabel(value, now = new Date()) {
  const date = parseDate(value);
  if (!date) return "更早";
  const day = startOfDay(date);
  const today = startOfDay(now);
  return memo(`b:${day}:${today}`, () => {
    const days = Math.round((today - day) / DAY);
    if (days <= 0) return "今天";
    if (days === 1) return "昨天";
    if (days < 7) return "近七天";
    if (days < 30) return "近三十天";
    return formatterFor({ year: "numeric", month: "long" }).format(date);
  });
}

// Hostnames repeat heavily across a list, so the parsed form is cached.
const shortURLCache = new Map();
export function shortURL(value) {
  let cached = shortURLCache.get(value);
  if (cached === undefined) {
    try {
      const parsed = new URL(value);
      cached = parsed.hostname.replace(/^www\./, "") + parsed.pathname;
    } catch {
      cached = String(value ?? "");
    }
    if (shortURLCache.size >= CACHE_LIMIT) shortURLCache.clear();
    shortURLCache.set(value, cached);
  }
  return cached;
}

// sourceLine is the "who posted this" hint shown above a title.
export function sourceLine(value) {
  try {
    const parsed = new URL(value);
    const host = parsed.hostname.replace(/^www\./, "");
    if (host === "x.com" || host === "twitter.com") {
      const handle = parsed.pathname.split("/").filter(Boolean)[0];
      return handle ? `@${handle}` : host;
    }
    if (host === "mp.weixin.qq.com") return "微信公众号";
    return host;
  } catch {
    return "";
  }
}

export const curationLabels = Object.freeze({ inbox: "收件箱", kept: "精选", compiled: "已编入笔记", drop: "搁置" });
export const curationShort = Object.freeze({ inbox: "收件箱", kept: "精选", compiled: "已编入", drop: "搁置" });
export const sourceLabels = Object.freeze({ x: "X", wechat: "公众号", other: "网页" });

export const waitingText = Object.freeze({
  pending: "正在排队，稍后会生成中文标题与译文",
  processing: "正在生成中文标题与译文",
  failed: "上次没有读取成功，稍后会自动重试",
  exhausted: "这条没能读取，可以在后台再试一次或粘贴原文",
  unsupported: "尚未归档正文",
  completed: "由旧版本处理，重新处理可以补齐内容"
});

export function displayTitle(item) {
  if (item?.ai_title) return { text: item.ai_title, raw: false };
  return { text: shortURL(item?.url || ""), raw: true };
}

export function displaySummary(item) {
  if (item?.summary) return { text: item.summary, wait: false };
  return { text: waitingText[item?.status] || "还没有生成内容", wait: true };
}

export function needsAttention(item) {
  return item?.status === "failed" || item?.status === "exhausted";
}

export function isWorking(item) {
  return !item?.paid_call_unresolved && (item?.status === "pending" || item?.status === "processing");
}

// An unreviewed record with no suggestion, or an explicitly uncertain one,
// is what the "待确认" view lists.
export function needsReview(item) {
  return !item?.classification_reviewed && (!item?.classification || Boolean(item.classification.uncertainty));
}

export function searchTerms(query) {
  return String(query || "").toLowerCase().split(/\s+/).filter(Boolean);
}

// highlightRanges returns non-overlapping [start, end) spans of text that
// match any term, case-insensitively. Lowercasing can change the length of a
// few characters, which would shift every later span, so such text is left
// unmarked rather than highlighting the wrong characters.
export function highlightRanges(text, terms) {
  const value = String(text ?? "");
  if (!terms.length) return [];
  const lower = value.toLowerCase();
  if (lower.length !== value.length) return [];
  const ranges = [];
  let cursor = 0;
  while (cursor < value.length) {
    let at = -1;
    let width = 0;
    for (const term of terms) {
      const found = lower.indexOf(term, cursor);
      if (found !== -1 && (at === -1 || found < at || (found === at && term.length > width))) {
        at = found;
        width = term.length;
      }
    }
    if (at === -1) break;
    ranges.push([at, at + width]);
    cursor = at + width;
  }
  return ranges;
}

// searchExcerpt picks the first field that actually contains a term, so a hit
// in the original text or a note is visible without opening the bookmark.
export function searchExcerpt(item, terms, width = 160) {
  const summary = displaySummary(item);
  if (!terms.length) return summary;
  const fields = [item.search_excerpt, item.summary, item.translated_text, item.original_text, item.classification?.entities?.join(" / "),
    item.classification?.why_suggestion, item.note, item.why];
  for (const value of fields) {
    if (!value) continue;
    const lower = value.toLowerCase();
    const positions = terms.map((term) => lower.indexOf(term)).filter((at) => at >= 0);
    if (!positions.length) continue;
    const start = Math.max(0, Math.min(...positions) - 40);
    const text = (start ? "…" : "") + value.slice(start, start + width) + (start + width < value.length ? "…" : "");
    return { text, wait: false };
  }
  return summary;
}

export function paragraphsOf(text) {
  return String(text ?? "").split(/\n+/).map((line) => line.trim()).filter(Boolean);
}

export function clampText(value, max) {
  const chars = [...String(value ?? "")];
  return chars.length > max ? `${chars.slice(0, max - 1).join("")}…` : chars.join("");
}
