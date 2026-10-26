// A small DOM for app tests: herdsman-blocks' linked-list fake DOM (so views,
// templates and morph run on it) plus what the app's forms use: form
// properties, classList, readable dataset, bubbling events with extras.
// Bun ships no DOM, and the repository takes no npm packages.
import {
  FakeElement as BaseElement,
  FakeEvent as BaseEvent,
  type FakeNode,
  FakeText,
  installFakeDocument,
  SVG_NS,
} from "./herdsman-blocks.testdom.ts";

/** A bubbling event with what form handlers read: the submitter and preventDefault. */
export class FakeEvent extends BaseEvent {
  submitter: FakeElement | null = null;
  defaultPrevented = false;
  preventDefault(): void {
    this.defaultPrevented = true;
  }
}

class ClassList {
  constructor(private el: BaseElement) {}
  private get list(): string[] {
    return this.el.className.split(/\s+/).filter(Boolean);
  }
  contains(c: string): boolean {
    return this.list.includes(c);
  }
  add(c: string): void {
    if (!this.contains(c)) this.el.className = [...this.list, c].join(" ");
  }
  remove(c: string): void {
    this.el.className = this.list.filter((x) => x !== c).join(" ");
  }
  toggle(c: string, on?: boolean): boolean {
    const want = on ?? !this.contains(c);
    if (want) this.add(c);
    else this.remove(c);
    return want;
  }
}

const dataName = (k: string): string => "data-" + k.replace(/[A-Z]/g, (c) => "-" + c.toLowerCase());

export class FakeElement extends BaseElement {
  // Properties h() assigns directly ("k in el").
  value = "";
  checked = false;
  disabled = false;
  selected = false;
  name = "";
  id = "";
  type = "";
  open = false;
  returnValue = "";
  readonly classList: ClassList = new ClassList(this);

  /** data-* attributes, readable and writable. */
  override get dataset(): Record<string, string> {
    return new Proxy({} as Record<string, string>, {
      get: (_t, k) => this.getAttribute(dataName(String(k))) ?? undefined,
      set: (_t, k, v) => {
        this.setAttribute(dataName(String(k)), String(v));
        return true;
      },
    });
  }
  override getAttribute(k: string): string | null {
    if (k === "name") return this.name || super.getAttribute("name");
    return super.getAttribute(k);
  }
  dispatch(type: string, extra: EventExtras = {}): FakeEvent {
    return dispatch(this, type, extra);
  }
  append(...cs: (FakeNode | string)[]): void {
    for (const c of cs) this.appendChild(typeof c === "string" ? new FakeText(c) : c);
  }
  replaceChildren(...cs: FakeNode[]): void {
    this.textContent = "";
    this.append(...cs);
  }
  remove(): void {
    this.parentNode?.removeChild(this);
  }
  focus(): void {}
  close(): void {
    this.open = false;
  }
  get children(): BaseElement[] {
    return elementsOf(this);
  }
}

function elementsOf(n: FakeNode): BaseElement[] {
  const out: BaseElement[] = [];
  for (let c = n.firstChild; c; c = c.nextSibling) if (c instanceof BaseElement) out.push(c);
  return out;
}

/** Installs the fake document and Node on globalThis. */
export function installDom(): void {
  installFakeDocument(); // Node
  const g = globalThis as unknown as Record<string, unknown>;
  g["document"] = {
    activeElement: null,
    createElement: (tag: string) => new FakeElement(tag.toUpperCase()),
    createElementNS: (ns: string, tag: string) => new (ns === SVG_NS ? BaseElement : FakeElement)(tag, ns),
    createTextNode: (s: string) => new FakeText(s),
  };
}

/** Every element under root, depth first, root included. */
export function all(root: BaseElement): FakeElement[] {
  const out: FakeElement[] = [];
  const walk = (e: BaseElement): void => {
    if (e instanceof FakeElement) out.push(e);
    for (const c of elementsOf(e)) walk(c);
  };
  walk(root);
  return out;
}

export function find(root: BaseElement, pred: (e: FakeElement) => boolean): FakeElement | undefined {
  return all(root).find(pred);
}

export function byName(root: BaseElement, name: string): FakeElement {
  const el = find(root, (e) => e.name === name);
  if (!el) throw new Error(`no element named ${name}`);
  return el;
}

/** What a test can set on a dispatched event. */
export interface EventExtras {
  submitter?: FakeElement | null;
}

/**
 * Fires a bubbling event at any fake element, including one a view rendered:
 * on the way up, each element's listeners and on* handler run, so a view's
 * delegated handlers (a listener on its render() container) see it as the
 * browser's would. Tests fire events only through this.
 */
export function dispatch(target: BaseElement, type: string, extra: EventExtras = {}): FakeEvent {
  const e = new FakeEvent(type);
  if (extra.submitter !== undefined) e.submitter = extra.submitter;
  target.dispatchEvent(e);
  return e;
}

/**
 * A node as a string for comparing DOM built two ways: tags (with the
 * namespace for SVG), attributes sorted, text. Left out: empty text (a view
 * slot's anchor), the bridge's data-key stamps, and the order of attributes.
 * Properties a view sets that the old code set as attributes (`checked`) are
 * read as attributes, as a browser serializes the state they show.
 */
export function dump(n: FakeNode): string {
  if (n.nodeType === 3) return n.textContent;
  if (!(n instanceof BaseElement)) return "";
  const attrs = n.attributes.filter((a) => a.name !== "data-key").map((a) => `${a.name}="${a.value}"`);
  if (n["checked"] === true && !n.hasAttribute("checked")) attrs.push(`checked=""`);
  const tag = n.namespaceURI === SVG_NS ? `svg:${n.tagName}` : n.tagName.toLowerCase();
  return `<${[tag, ...attrs.sort()].join(" ")}>${n.childNodes.map(dump).join("")}</${tag}>`;
}
