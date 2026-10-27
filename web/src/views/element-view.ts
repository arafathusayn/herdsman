// Elements that are a view's root and that their owner places itself: a
// dialog, a toast, a search field a class keeps, the topbar and the sidebar
// next to each other in the app frame, a copy button at the end of a <pre>.
// render() puts a view's root inside a container and owns all of the
// container's content, so it cannot place such an element. These mount the
// view's root on its own (herdsman-blocks' mount()) and hand it out for the
// caller to place; later updates patch the root where it stands (update()),
// keeping its nodes, focus and scroll.
//
// The root is its own render root: it delegates its own and its descendants'
// handlers, wherever it is placed (inside another render root too, where each
// handler still runs once).
import { mount, update, type ViewNode } from "../herdsman-blocks-view.ts";

const HTML_NS = "http://www.w3.org/1999/xhtml";

// A checked narrowing, not a cast: the node is an HTML element with the tag.
const isTag = <K extends keyof HTMLElementTagNameMap>(n: Node, tag: K): n is HTMLElementTagNameMap[K] =>
  n.nodeType === 1 && "namespaceURI" in n && n.namespaceURI === HTML_NS && n.nodeName.toLowerCase() === tag;

/**
 * Mounts a view's root, typed by its tag (`HTMLDialogElement` for "dialog").
 * Patch it later with herdsman-blocks' `update(el, d)` and a description of
 * the same view; another view needs another element.
 */
export function element<K extends keyof HTMLElementTagNameMap>(tag: K, d: ViewNode): HTMLElementTagNameMap[K] {
  const root = mount(d);
  if (!isTag(root, tag)) throw new Error(`element: the view's root is not a <${tag}>`);
  return root;
}

/**
 * An element made by `element`, with updates held to the same view: the type
 * parameter N, a description type exact in its value tuple, admits only
 * descriptions of the view it was made with.
 */
export class ElementView<K extends keyof HTMLElementTagNameMap, N extends ViewNode> {
  /** The view's root: place it once; it stays the same element. */
  readonly el: HTMLElementTagNameMap[K];

  constructor(tag: K, d: N) {
    this.el = element(tag, d);
  }

  /** Patches the element toward the description, of the same view as the first. */
  update(d: N): void {
    update(this.el, d);
  }
}
