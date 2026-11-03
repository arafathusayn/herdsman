import { attach } from "@reactre/core";
import type { Child } from "@reactre/core/jsx-runtime";

function isTag<K extends keyof HTMLElementTagNameMap>(node: Node | null, tag: K): node is HTMLElementTagNameMap[K] {
  return (
    node !== null &&
    node.nodeType === 1 &&
    "namespaceURI" in node &&
    node.namespaceURI === "http://www.w3.org/1999/xhtml" &&
    node.nodeName.toLowerCase() === tag
  );
}

/** Mount an owned Reactre root that its caller places in a native DOM container. */
export function mountRoot<K extends keyof HTMLElementTagNameMap>(tag: K, view: () => Child) {
  const holder = document.createElement("div");
  const dispose = attach(view, holder);
  const el = holder.firstChild;
  if (!isTag(el, tag)) {
    dispose();
    throw new Error(`The component's root is not a <${tag}>`);
  }
  return { el, dispose };
}
