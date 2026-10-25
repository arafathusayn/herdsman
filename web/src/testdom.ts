// A small DOM for tests: just what the app's h() builder and the New route
// form use (elements, text, attributes, classes, events). Bun ships no DOM,
// and the repository takes no npm packages, so tests install this instead.

type Listener = (e: FakeEvent) => void;

export interface FakeEvent {
  type: string;
  target: FakeNode | null;
  submitter?: FakeElement | null;
  defaultPrevented: boolean;
  preventDefault(): void;
}

export class FakeNode {
  parentNode: FakeElement | null = null;
  childNodes: FakeNode[] = [];
  get textContent(): string {
    return this.childNodes.map((c) => c.textContent).join("");
  }
  set textContent(v: string) {
    for (const c of this.childNodes) c.parentNode = null;
    this.childNodes = v === "" ? [] : [new FakeText(v)];
    for (const c of this.childNodes) c.parentNode = this as unknown as FakeElement;
  }
  remove(): void {
    if (!this.parentNode) return;
    const p = this.parentNode;
    p.childNodes = p.childNodes.filter((c) => c !== this);
    this.parentNode = null;
  }
}

export class FakeText extends FakeNode {
  constructor(private data: string) {
    super();
  }
  override get textContent(): string {
    return this.data;
  }
  override set textContent(v: string) {
    this.data = v;
  }
}

class ClassList {
  constructor(private el: FakeElement) {}
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

export class FakeElement extends FakeNode {
  className = "";
  dataset: Record<string, string> = {};
  attributes: Record<string, string> = {};
  readonly classList = new ClassList(this);
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
  private listeners: Record<string, Listener[]> = {};
  constructor(readonly tagName: string) {
    super();
  }
  setAttribute(k: string, v: string): void {
    this.attributes[k] = v;
  }
  getAttribute(k: string): string | null {
    if (k === "name") return this.name || (this.attributes["name"] ?? null);
    return this.attributes[k] ?? null;
  }
  appendChild(c: FakeNode): FakeNode {
    c.remove();
    c.parentNode = this;
    this.childNodes.push(c);
    return c;
  }
  append(...cs: (FakeNode | string)[]): void {
    for (const c of cs) this.appendChild(typeof c === "string" ? new FakeText(c) : c);
  }
  replaceChildren(...cs: FakeNode[]): void {
    for (const c of this.childNodes) c.parentNode = null;
    this.childNodes = [];
    this.append(...cs);
  }
  addEventListener(type: string, fn: Listener): void {
    (this.listeners[type] ??= []).push(fn);
  }
  dispatch(type: string, extra: Partial<FakeEvent> = {}): FakeEvent {
    const e: FakeEvent = {
      type,
      target: this,
      defaultPrevented: false,
      preventDefault() {
        this.defaultPrevented = true;
      },
      ...extra,
    };
    // Events bubble to the ancestors, as submit and input do.
    for (let n: FakeElement | null = this; n; n = n.parentNode) for (const fn of n.listeners[type] ?? []) fn(e);
    return e;
  }
  focus(): void {}
  close(): void {
    this.open = false;
  }
  get children(): FakeElement[] {
    return this.childNodes.filter((c): c is FakeElement => c instanceof FakeElement);
  }
}

/** Installs the fake document and Node on globalThis. */
export function installDom(): void {
  const g = globalThis as unknown as Record<string, unknown>;
  g["Node"] = FakeNode;
  g["document"] = {
    activeElement: null,
    createElement: (tag: string) => new FakeElement(tag),
    createElementNS: (_ns: string, tag: string) => new FakeElement(tag),
    createTextNode: (s: string) => new FakeText(s),
  };
}

/** Every element under root, depth first, root included. */
export function all(root: FakeElement): FakeElement[] {
  return [root, ...root.children.flatMap(all)];
}

export function find(root: FakeElement, pred: (e: FakeElement) => boolean): FakeElement | undefined {
  return all(root).find(pred);
}

export function byName(root: FakeElement, name: string): FakeElement {
  const el = find(root, (e) => e.name === name);
  if (!el) throw new Error(`no element named ${name}`);
  return el;
}
