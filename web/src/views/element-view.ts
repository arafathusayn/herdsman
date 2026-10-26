// An element that is a view's root and sits among siblings the view does not
// own: the topbar and the sidebar next to each other in the app frame, a copy
// button at the end of a <pre>. render() puts a view's root inside a
// container and owns all of the container's content, so it cannot place such
// an element. ElementView mounts the view's root on its own (herdsman-blocks'
// mount()) and hands it out for the caller to place once; later updates patch
// the root where it stands (update()), keeping its nodes, focus and scroll.
//
// Every update must describe the same view as the first (the type parameter
// N, a description type exact in its value tuple, holds it to that): update()
// refuses another view, which would need another root element.
//
// The root is its own render root: it delegates its own and its descendants'
// handlers, wherever it is placed (inside another render root too, where each
// handler still runs once).
import { mount, update, type ViewNode } from "../herdsman-blocks-view.ts";

const HTML_NS = "http://www.w3.org/1999/xhtml";

// A checked narrowing, not a cast: the node is an HTML element with the tag.
const isTag = <K extends keyof HTMLElementTagNameMap>(n: Node, tag: K): n is HTMLElementTagNameMap[K] =>
  n.nodeType === 1 && "namespaceURI" in n && n.namespaceURI === HTML_NS && n.nodeName.toLowerCase() === tag;

export class ElementView<K extends keyof HTMLElementTagNameMap, N extends ViewNode> {
  /** The view's root: place it once; it stays the same element. */
  readonly el: HTMLElementTagNameMap[K];

  constructor(tag: K, d: N) {
    const root = mount(d);
    if (!isTag(root, tag)) throw new Error(`ElementView: the view's root is not a <${tag}>`);
    this.el = root;
  }

  /** Patches the element toward the description, of the same view as the first. */
  update(d: N): void {
    update(this.el, d);
  }
}
