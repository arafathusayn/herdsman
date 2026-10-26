// A small DOM for app tests: herdsman-blocks' linked-list fake DOM (so views,
// templates and morph run on it) plus what the app's forms use: form
// properties, classList, readable dataset, bubbling events with extras.
// Bun ships no DOM, and the repository takes no npm packages.
import {
  FakeElement as BaseElement,
  type FakeNode,
  FakeText,
  installFakeDocument,
  SVG_NS,
} from "./herdsman-blocks.testdom.ts";

type Listener = (e: FakeEvent) => void;

export interface FakeEvent {
  type: string;
  target: FakeNode;
  submitter?: FakeElement | null;
  defaultPrevented: boolean;
  preventDefault(): void;
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
  private handlers: Record<string, Listener[]> = {};
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
  override addEventListener(type: string, fn: Listener): void {
    (this.handlers[type] ??= []).push(fn);
  }
  /** The listeners `dispatch` calls, by event type. */
  listenersOf(type: string): readonly Listener[] {
    return this.handlers[type] ?? [];
  }
  dispatch(type: string, extra: Partial<FakeEvent> = {}): FakeEvent {
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

/**
 * Fires an event at any fake element, including one a view rendered: it
 * bubbles to the ancestors (as submit and input do) and runs, on each,
 * listeners and the on* handler a view or template set. This is the one place
 * to change if the view runtime starts delegating events.
 */
export function dispatch(target: BaseElement, type: string, extra: Partial<FakeEvent> = {}): FakeEvent {
  const e: FakeEvent = {
    type,
    target,
    defaultPrevented: false,
    preventDefault() {
      this.defaultPrevented = true;
    },
    ...extra,
  };
  for (let n: FakeNode | null = target; n; n = n.parentNode) {
    if (!(n instanceof BaseElement)) continue;
    if (n instanceof FakeElement) for (const fn of n.listenersOf(type)) fn(e);
    else n.fire(type);
    const handler = n["on" + type];
    if (typeof handler === "function") handler.call(n, e);
  }
  return e;
}
