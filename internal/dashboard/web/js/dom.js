// Tiny DOM helpers. The application renders with plain elements so that user
// content only ever reaches the page through textContent, never as markup.

const SVG_NS = "http://www.w3.org/2000/svg";

// h("button.btn.btn-ghost#save", { type: "button", onclick }, "label", child)
// The selector part accepts classes and one id in any order.
export function h(spec, props, ...children) {
  const tag = spec.match(/^[a-z0-9-]*/i)[0] || "div";
  const node = document.createElement(tag);
  const classes = [];
  for (const [, kind, name] of spec.slice(tag.length).matchAll(/([.#])([^.#]+)/g)) {
    if (kind === "#") node.id = name;
    else classes.push(name);
  }
  if (classes.length) node.className = classes.join(" ");
  if (props && (typeof props !== "object" || props instanceof Node || Array.isArray(props))) {
    children.unshift(props);
    props = null;
  }
  if (props) applyProps(node, props);
  append(node, children);
  return node;
}

export function applyProps(node, props) {
  for (const [key, value] of Object.entries(props)) {
    if (value === undefined || value === null || value === false) {
      if (key === "hidden" || key === "disabled") node[key] = false;
      continue;
    }
    if (key === "class") node.className = [node.className, value].filter(Boolean).join(" ");
    else if (key === "text") node.textContent = value;
    else if (key === "dataset") Object.assign(node.dataset, value);
    else if (key.startsWith("on") && typeof value === "function") node.addEventListener(key.slice(2), value);
    else if (key.startsWith("aria-") || key === "role" || key === "for" || key === "tabindex") node.setAttribute(key, String(value));
    else if (key in node && typeof node[key] !== "function") node[key] = value;
    else node.setAttribute(key, value === true ? "" : String(value));
  }
}

export function append(node, children) {
  for (const child of children.flat(Infinity)) {
    if (child === null || child === undefined || child === false || child === "") continue;
    node.append(child instanceof Node ? child : document.createTextNode(String(child)));
  }
  return node;
}

export function clear(node, ...children) {
  node.replaceChildren();
  return append(node, children);
}

export function byId(id) {
  return document.getElementById(id);
}

export function svg(tag, attrs = {}) {
  const node = document.createElementNS(SVG_NS, tag);
  for (const [key, value] of Object.entries(attrs)) node.setAttribute(key, String(value));
  return node;
}

// highlightInto appends text with <mark> around the given ranges.
export function highlightInto(node, text, ranges) {
  let cursor = 0;
  for (const [start, end] of ranges) {
    if (start > cursor) node.append(document.createTextNode(text.slice(cursor, start)));
    node.append(h("mark", null, text.slice(start, end)));
    cursor = end;
  }
  if (cursor < text.length) node.append(document.createTextNode(text.slice(cursor)));
  return node;
}

export function isEditable(target) {
  if (!(target instanceof Element)) return false;
  if (target.closest("[contenteditable='true']")) return true;
  const tag = target.tagName;
  if (tag === "TEXTAREA" || tag === "SELECT") return true;
  if (tag !== "INPUT") return false;
  return !["checkbox", "radio", "button", "submit", "reset"].includes(target.type);
}

export function nextFrame() {
  return new Promise((resolve) => requestAnimationFrame(() => resolve()));
}

export function prefersReducedMotion() {
  return typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
}
