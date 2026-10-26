// An element that is a view's root and sits among siblings the view does not
// own: the topbar and the sidebar next to each other in the app frame, a copy
// button at the end of a <pre>. render() puts a view's root inside a
// container and owns all of the container's content, so it cannot place such
// an element. ElementView renders the view into a holder of its own, which is
// never attached, and hands out the root for the caller to place once; later
// updates patch the root where it stands, keeping its nodes, focus and
// scroll. The view is fixed, so the root stays the same element.
//
// render() keeps no reference to its container while it patches, so the root
// may live anywhere. If the view runtime starts delegating events at each
// render() root, the root's handlers would stay behind on the holder: this
// class is then the one place to change (see plans/views-migration.md).
import { render, type View } from "../herdsman-blocks-view.ts";

const HTML_NS = "http://www.w3.org/1999/xhtml";

// A checked narrowing, not a cast: the node is an HTML element with the tag.
const isTag = <K extends keyof HTMLElementTagNameMap>(n: Node, tag: K): n is HTMLElementTagNameMap[K] =>
  n.nodeType === 1 && "namespaceURI" in n && n.namespaceURI === HTML_NS && n.nodeName.toLowerCase() === tag;

/** The value tuple of a view: `ViewValues<typeof Row>`. */
export type ViewValues<T> = T extends View<infer V> ? V : never;

export class ElementView<K extends keyof HTMLElementTagNameMap, V extends readonly unknown[]> {
  /** The view's root: place it once; it stays the same element. */
  readonly el: HTMLElementTagNameMap[K];
  readonly #holder: Node;
  readonly #view: View<V>;

  constructor(tag: K, view: View<V>, vals: V) {
    this.#view = view;
    this.#holder = document.createElement("div");
    render(this.#holder, view(vals));
    const root = this.#holder.firstChild;
    if (root === null || !isTag(root, tag)) throw new Error(`ElementView: the view's root is not a <${tag}>`);
    this.el = root;
  }

  /** Patches the element toward the view with these values. */
  update(vals: V): void {
    render(this.#holder, this.#view(vals));
  }
}
