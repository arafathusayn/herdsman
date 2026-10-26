// An element that is a view's root and sits among siblings the view does not
// own: the topbar and the sidebar next to each other in the app frame, a copy
// button at the end of a <pre>. render() puts a view's root inside a
// container and owns all of the container's content, so it cannot place such
// an element. ElementView renders the view into a holder of its own, which is
// never attached, and hands out the root for the caller to place once; later
// updates patch the root where it stands, keeping its nodes, focus and
// scroll.
//
// Every update must describe the same view as the first (the type parameter
// N, a description type exact in its value tuple, holds it to that): another
// view would replace the root, and `el` would no longer be on the page.
//
// render() keeps no reference to its container while it patches, so the root
// may live anywhere. If the view runtime starts delegating events at each
// render() root, the root's handlers would stay behind on the holder: this
// class is then the one place to change (see plans/views-migration.md).
import { render, type ViewNode } from "../herdsman-blocks-view.ts";

const HTML_NS = "http://www.w3.org/1999/xhtml";

// A checked narrowing, not a cast: the node is an HTML element with the tag.
const isTag = <K extends keyof HTMLElementTagNameMap>(n: Node, tag: K): n is HTMLElementTagNameMap[K] =>
  n.nodeType === 1 && "namespaceURI" in n && n.namespaceURI === HTML_NS && n.nodeName.toLowerCase() === tag;

export class ElementView<K extends keyof HTMLElementTagNameMap, N extends ViewNode> {
  /** The view's root: place it once; it stays the same element. */
  readonly el: HTMLElementTagNameMap[K];
  readonly #holder: Node;

  constructor(tag: K, d: N) {
    this.#holder = document.createElement("div");
    render(this.#holder, d);
    const root = this.#holder.firstChild;
    if (root === null || !isTag(root, tag)) throw new Error(`ElementView: the view's root is not a <${tag}>`);
    this.el = root;
  }

  /** Patches the element toward the description, of the same view as the first. */
  update(d: N): void {
    render(this.#holder, d);
  }
}
