// A small DOM for app tests: herdsman-blocks' linked-list fake DOM (so views,
// templates and lists run on it) plus what the app's forms use: form
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

/** A bubbling event with what form and key handlers read: the submitter, the key, its code and modifiers, and preventDefault. */
export class FakeEvent extends BaseEvent {
  /** The button that submitted a form: any element, a view's included. */
  submitter: BaseElement | null = null;
  key = "";
  code = "";
  isComposing = false;
  ctrlKey = false;
  metaKey = false;
  shiftKey = false;
  altKey = false;
  defaultPrevented = false;
  preventDefault(): void {
    this.defaultPrevented = true;
  }
  /** Only the four plain modifiers are ever held; AltGraph, CapsLock and the rest read as off. */
  getModifierState(name: string): boolean {
    return name === "Control"
      ? this.ctrlKey
      : name === "Meta"
        ? this.metaKey
        : name === "Shift"
          ? this.shiftKey
          : name === "Alt"
            ? this.altKey
            : false;
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
  // Form properties the app reads and writes.
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
    this.dispatch("close");
  }
  showModal(): void {
    this.open = true;
  }
  /**
   * The first descendant matching "tag", "tag[name=x]" or ".class" (all a
   * test needs), a view's elements included: those are the base fake DOM's,
   * so the name and class are read as attributes or the name property.
   */
  querySelector(sel: string): BaseElement | null {
    const m = /^([a-z]*)(?:\[name=([^\]]+)\])?(?:\.([\w-]+))?$/.exec(sel);
    const [, tag, name, cls] = m ?? [];
    const hit = (e: BaseElement): boolean =>
      (!tag || e.tagName.toLowerCase() === tag) &&
      (!name || e.getAttribute("name") === name || e["name"] === name) &&
      (!cls || e.className.split(/\s+/).includes(cls));
    const walk = (e: BaseElement): BaseElement | null => {
      for (const c of elementsOf(e)) {
        const found = hit(c) ? c : walk(c);
        if (found) return found;
      }
      return null;
    };
    return m ? walk(this) : null;
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

/**
 * The element methods the app calls on elements a view renders (a dialog
 * made with element(), the form in it), given to the base fake DOM's
 * elements too: views clone those, not this file's FakeElement.
 */
const shared = ["append", "replaceChildren", "remove", "focus", "close", "showModal", "querySelector", "dispatch"];
function shareMethods(): void {
  const base = BaseElement.prototype as unknown as Record<string, unknown>;
  const own = FakeElement.prototype as unknown as Record<string, unknown>;
  for (const name of shared)
    if (!(name in base)) Object.defineProperty(base, name, { value: own[name], configurable: true, writable: true });
}

/** Installs the fake document and Node on globalThis. */
export function installDom(): void {
  installFakeDocument(); // Node
  shareMethods();
  const g = globalThis as unknown as Record<string, unknown>;
  g["document"] = {
    activeElement: null,
    createElement: (tag: string) => new FakeElement(tag.toUpperCase()),
    createElementNS: (ns: string, tag: string) => new (ns === SVG_NS ? BaseElement : FakeElement)(tag, ns),
    createTextNode: (s: string) => new FakeText(s),
    body: new FakeElement("body"),
  };
}

/**
 * Every element under root, depth first, root included: the ones a view
 * rendered (clones of its template, the base fake DOM's elements) as well
 * as the ones createElement made.
 */
export function all(root: BaseElement): BaseElement[] {
  const out: BaseElement[] = [];
  const walk = (e: BaseElement): void => {
    out.push(e);
    for (const c of elementsOf(e)) walk(c);
  };
  walk(root);
  return out;
}

export function find(root: BaseElement, pred: (e: BaseElement) => boolean): BaseElement | undefined {
  return all(root).find(pred);
}

/** The element whose name (attribute, or property set by a view) is `name`. */
export function byName(root: BaseElement, name: string): BaseElement {
  const el = find(root, (e) => e.getAttribute("name") === name || e["name"] === name);
  if (!el) throw new Error(`no element named ${name}`);
  return el;
}

/** What a test can set on a dispatched event. */
export interface EventExtras {
  submitter?: BaseElement | null;
  key?: string;
  code?: string;
  isComposing?: boolean;
  ctrlKey?: boolean;
  metaKey?: boolean;
  shiftKey?: boolean;
  altKey?: boolean;
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
  if (extra.key !== undefined) e.key = extra.key;
  if (extra.code !== undefined) e.code = extra.code;
  e.isComposing = extra.isComposing ?? false;
  e.ctrlKey = extra.ctrlKey ?? false;
  e.metaKey = extra.metaKey ?? false;
  e.shiftKey = extra.shiftKey ?? false;
  e.altKey = extra.altKey ?? false;
  target.dispatchEvent(e);
  return e;
}

/**
 * A node as a string for comparing DOM built two ways: tags (with the
 * namespace for SVG), attributes sorted, text. Left out: empty text (a view
 * slot's anchor) and the order of attributes.
 * Properties a view sets that the old code set as attributes (`checked`) are
 * read as attributes, as a browser serializes the state they show.
 */
export function dump(n: FakeNode): string {
  if (n.nodeType === 3) return n.textContent;
  if (!(n instanceof BaseElement)) return "";
  const attrs = n.attributes.map((a) => `${a.name}="${a.value}"`);
  if (n["checked"] === true && !n.hasAttribute("checked")) attrs.push(`checked=""`);
  const tag = n.namespaceURI === SVG_NS ? `svg:${n.tagName}` : n.tagName.toLowerCase();
  return `<${[tag, ...attrs.sort()].join(" ")}>${n.childNodes.map(dump).join("")}</${tag}>`;
}

/**
 * Expected markup to compare with a dump(), written across lines as it nests:
 * a line break and the indentation after it are layout, not content, so break
 * a line only where no space in the text touches the break. Escapes
 * stand for what they do in a string (`\n` is a line break in text, `\ue0a0`
 * a character that does not show), and a hole's string goes in as it is.
 */
export function markup(strings: TemplateStringsArray, ...holes: string[]): string {
  const cook = (s: string): string =>
    s.replace(/\n[ \t]*/g, "").replace(/\\(u\{[0-9a-f]+\}|u[0-9a-f]{4}|.)/gi, (_, c: string) => {
      if (c.length > 1) return String.fromCodePoint(parseInt(c.replace(/[u{}]/g, ""), 16));
      const out = ({ n: "\n", t: "\t", r: "\r", "\\": "\\", "`": "`", $: "$" } as Record<string, string>)[c];
      if (out === undefined) throw new Error(`markup: unknown escape \\${c}`);
      return out;
    });
  return strings.raw.reduce((out, s, i) => out + cook(s) + (holes[i] ?? ""), "");
}
