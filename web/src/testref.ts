// Reference builders for markup tests: the shape of the h() DOM builder the
// app used before views, kept so tests can state the markup a view must
// render as the old code built it, and compare dump()s. Tests only; the app
// renders everything with views.
//
// Nothing here is the old machinery: no morph, no keyed patching, no handler
// maps. h() builds a fresh element; each() and unkeyed() are plain lists; a
// view description given as a child is rendered once into a scratch element
// and its nodes are moved out (its handlers are not kept: a reference is for
// its markup).
import { child, html, type ListNode, render, type RunsNode, type ViewNode } from "./herdsman-blocks-view.ts";

/** A child of a reference element. */
export type Child =
  Node | string | number | false | null | undefined | ViewNode | ListNode | RunsNode | readonly Child[];

/** A list in a reference tree (the old builder's unkeyed list). */
export type Unkeyed = readonly Child[];

type Props = {
  class?: string | undefined;
  style?: string | undefined;
  dataset?: Record<string, string> | undefined;
  [attr: string]: unknown;
};

/** An element as the old builder made it: props as properties when the element has them, else attributes. */
export function h<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  props: Props | null = null,
  ...children: Child[]
): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  if (props) {
    for (const [k, v] of Object.entries(props)) {
      if (v === undefined || v === null || v === false) continue;
      if (k === "class") el.className = String(v);
      else if (k === "dataset") Object.assign(el.dataset, v);
      else if (k.startsWith("on") && typeof v === "function")
        continue; // markup only
      else if (k in el && k !== "style" && k !== "list" && k !== "form") {
        (el as unknown as Record<string, unknown>)[k] = v;
      } else el.setAttribute(k, v === true ? "" : String(v));
    }
  }
  append(el, children);
  return el;
}

/** Replaces el's children, as the old mount() did. */
export function mount(el: Element, ...children: Child[]): void {
  el.replaceChildren();
  append(el, children);
}

/** The old keyed list, as a plain list: the key only mattered to patch(). */
export function each<T>(items: readonly T[], _key: (item: T) => string | number, render: (item: T) => Child): Unkeyed {
  return items.map(render);
}

/** The old unkeyed list: a plain list. */
export function unkeyed(nodes: readonly Child[]): Unkeyed {
  return nodes;
}

function append(el: Node, children: readonly Child[]): void {
  for (const c of children) {
    if (c === null || c === undefined || c === false) continue;
    if (Array.isArray(c)) append(el, c);
    else if (c instanceof Node) el.appendChild(c);
    else if (typeof c === "string" || typeof c === "number") el.appendChild(document.createTextNode(String(c)));
    else for (const n of materialize(c as ViewNode | ListNode | RunsNode)) el.appendChild(n);
  }
}

const Scratch = html`<div>${child()}</div>`;

function materialize(c: ViewNode | ListNode | RunsNode): Node[] {
  const box = document.createElement("div");
  render(box, Scratch([c]));
  const holder = box.firstChild;
  const out: Node[] = [];
  if (holder === null) return out;
  // Everything but empty text (a slot's anchor).
  for (let n = holder.firstChild; n; n = n.nextSibling) if (n.nodeType !== 3 || n.nodeValue !== "") out.push(n);
  for (const n of out) holder.removeChild(n);
  return out;
}
