// Component-library adapter for forms, preserving native value/checked/event APIs.
// Hot list rows and the IME search field deliberately use dom.h instead.
import "./vendor/components.js";
import { h } from "./dom.js";
let serial = 0;
export function control(spec, props, ...children) {
  if (
    props &&
    (typeof props !== "object" || props instanceof Node || Array.isArray(props))
  ) {
    children.unshift(props);
    props = null;
  }
  const tag = spec.match(/^[a-z0-9-]*/i)[0];
  const mapped =
    tag === "input"
      ? props?.type === "checkbox"
        ? "wa-checkbox"
        : "wa-input"
      : {
          button: "wa-button",
          textarea: "wa-textarea",
          select: "wa-select",
          option: "wa-option",
        }[tag];
  const node = h(
    mapped
      ? spec.replace(tag, mapped)
      : tag === "label"
        ? spec.replace(tag, "div")
        : spec,
    props,
    ...children,
  );
  if (mapped && ["wa-input", "wa-textarea", "wa-select"].includes(mapped)) {
    if (props?.["aria-label"]) {
      node.label = props["aria-label"];
      node.removeAttribute("aria-label");
      node.classList.add("sr-label");
    }
    if (props?.maxLength) node.maxlength = props.maxLength;
  }
  if (mapped) {
    if (mapped === "wa-input" || mapped === "wa-textarea")
      node.value = props?.value ?? "";
    node.size = "s";
    if (mapped === "wa-button") {
      node.type = "button";
      node.appearance =
        props?.appearance ??
        (node.matches(".link-btn,.nav-item,.collection-title")
          ? "plain"
          : "outlined");
    }
    if (mapped === "wa-select" && !props?.value)
      node.value = node.querySelector("wa-option")?.value || "";
  }
  if (tag === "label") {
    const field = [...node.children].find((el) =>
      el.matches("wa-input,wa-textarea,wa-select,wa-checkbox,wa-switch"),
    );
    if (field) {
      if (field.matches("wa-checkbox,wa-switch")) {
        for (const child of [...node.childNodes])
          if (child !== field) field.append(child);
      } else if (!field.label && !field.hasAttribute("aria-label")) {
        const label = h("span", { id: `field-label-${++serial}` });
        for (const child of [...node.childNodes])
          if (child !== field) label.append(child);
        node.prepend(label);
        field.setAttribute("aria-labelledby", label.id);
      }
    }
  }
  return node;
}
