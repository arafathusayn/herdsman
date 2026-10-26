// A small linked-list DOM for tests and profiling: just what herdsman-dom-diff
// touches (template parsing, cloneNode, sibling pointers, insertBefore,
// removeChild, text data, classes, attributes). Bun ships no DOM and the
// repository takes no npm packages. Every operation is O(1) pointer work, so
// a CPU profile over it shows the library's own cost, not a browser's.

/** Counts the DOM work done; tests assert on it. */
export const ops = { inserts: 0, moves: 0, stateMoves: 0, removes: 0, clones: 0, classWrites: 0 };
export function resetOps(): void {
  ops.inserts = ops.moves = ops.stateMoves = ops.removes = ops.clones = ops.classWrites = 0;
}

export class FakeNode {
  parentNode: FakeNode | null = null;
  firstChild: FakeNode | null = null;
  lastChild: FakeNode | null = null;
  nextSibling: FakeNode | null = null;
  previousSibling: FakeNode | null = null;
  readonly nodeType: number;
  constructor(nodeType: number) {
    this.nodeType = nodeType;
  }
  get nodeName(): string {
    return "#node";
  }
  get nodeValue(): string | null {
    return null;
  }
  set nodeValue(_v: string | null) {}
  readonly ownerDocument: { activeElement: unknown } | null = null;

  /** True on the fake document root: nodes under it are "in the document". */
  isDocument = false;
  get isConnected(): boolean {
    let n: FakeNode = this;
    while (n.parentNode) n = n.parentNode;
    return n.isDocument;
  }

  /** Like Chrome's moveBefore: keeps state, throws when either node is off-document. */
  moveBefore(n: FakeNode, ref: FakeNode | null): void {
    if (!this.isConnected || !n.isConnected) throw new Error("HierarchyRequestError");
    ops.stateMoves++;
    this.insertBefore(n, ref);
  }

  insertBefore(n: FakeNode, ref: FakeNode | null): FakeNode {
    if (n.parentNode) {
      ops.moves++;
      n.parentNode.unlink(n);
    } else ops.inserts++;
    n.parentNode = this;
    if (ref === null) {
      n.previousSibling = this.lastChild;
      n.nextSibling = null;
      if (this.lastChild) this.lastChild.nextSibling = n;
      else this.firstChild = n;
      this.lastChild = n;
    } else {
      if (ref.parentNode !== this) throw new Error("ref is not a child");
      n.nextSibling = ref;
      n.previousSibling = ref.previousSibling;
      if (ref.previousSibling) ref.previousSibling.nextSibling = n;
      else this.firstChild = n;
      ref.previousSibling = n;
    }
    return n;
  }
  appendChild(n: FakeNode): FakeNode {
    return this.insertBefore(n, null);
  }
  removeChild(n: FakeNode): FakeNode {
    if (n.parentNode !== this) throw new Error("not a child");
    ops.removes++;
    this.unlink(n);
    return n;
  }
  replaceChild(n: FakeNode, old: FakeNode): FakeNode {
    this.insertBefore(n, old);
    this.unlink(old);
    return old;
  }
  private unlink(n: FakeNode): void {
    if (n.previousSibling) n.previousSibling.nextSibling = n.nextSibling;
    else this.firstChild = n.nextSibling;
    if (n.nextSibling) n.nextSibling.previousSibling = n.previousSibling;
    else this.lastChild = n.previousSibling;
    n.parentNode = n.nextSibling = n.previousSibling = null;
  }
  get childNodes(): FakeNode[] {
    const out: FakeNode[] = [];
    for (let c = this.firstChild; c; c = c.nextSibling) out.push(c);
    return out;
  }
  get textContent(): string {
    return this.childNodes.map((c) => c.textContent).join("");
  }
  set textContent(v: string) {
    for (let c = this.firstChild; c;) {
      const next: FakeNode | null = c.nextSibling;
      ops.removes++;
      c.parentNode = c.nextSibling = c.previousSibling = null;
      c = next;
    }
    this.firstChild = this.lastChild = null;
    if (v !== "") this.appendChild(new FakeText(v));
  }
  cloneNode(_deep?: boolean): FakeNode {
    throw new Error("abstract");
  }
}

export class FakeText extends FakeNode {
  data: string;
  constructor(data: string) {
    super(3);
    this.data = data;
  }
  override get nodeName(): string {
    return "#text";
  }
  override get nodeValue(): string {
    return this.data;
  }
  override set nodeValue(v: string | null) {
    this.data = v ?? "";
  }
  override get textContent(): string {
    return this.data;
  }
  override cloneNode(): FakeText {
    return new FakeText(this.data);
  }
}

export class FakeComment extends FakeNode {
  data: string;
  constructor(data: string) {
    super(8);
    this.data = data;
  }
  override get nodeName(): string {
    return "#comment";
  }
  override get nodeValue(): string {
    return this.data;
  }
  override set nodeValue(v: string | null) {
    this.data = v ?? "";
  }
  override get textContent(): string {
    return "";
  }
  override cloneNode(): FakeComment {
    return new FakeComment(this.data);
  }
}

export class FakeElement extends FakeNode {
  private cls = "";
  get className(): string {
    return this.cls;
  }
  set className(v: string) {
    ops.classWrites++;
    this.cls = v;
  }
  private attrs: Map<string, string> | null = null;
  [prop: string]: unknown;

  private listeners: Map<string, ((e: { type: string; target: FakeNode }) => void)[]> | null = null;

  readonly tagName: string;
  constructor(tagName: string) {
    super(1);
    this.tagName = tagName;
  }
  override get nodeName(): string {
    return this.tagName;
  }
  get attributes(): { name: string; value: string }[] {
    const out = this.attrs ? [...this.attrs].map(([name, value]) => ({ name, value })) : [];
    return this.cls ? [{ name: "class", value: this.cls }, ...out] : out;
  }
  hasAttribute(k: string): boolean {
    return k === "class" ? this.cls !== "" : (this.attrs?.has(k) ?? false);
  }
  /** data-* attributes as a plain object view, enough for h()'s Object.assign. */
  get dataset(): Record<string, string> {
    return new Proxy({} as Record<string, string>, {
      set: (_t, k, v) => {
        this.setAttribute("data-" + String(k).replace(/[A-Z]/g, (c) => "-" + c.toLowerCase()), String(v));
        return true;
      },
    });
  }
  addEventListener(type: string, fn: (e: { type: string; target: FakeNode }) => void): void {
    const map = (this.listeners ??= new Map());
    map.set(type, [...(map.get(type) ?? []), fn]);
  }
  /** Calls this element's listeners for `type` with this element as `this`. */
  fire(type: string): void {
    for (const fn of this.listeners?.get(type) ?? []) fn.call(this, { type, target: this });
  }
  setAttribute(k: string, v: string): void {
    if (k === "class") this.className = v;
    else (this.attrs ??= new Map()).set(k, v);
  }
  getAttribute(k: string): string | null {
    return k === "class" ? this.className || null : (this.attrs?.get(k) ?? null);
  }
  removeAttribute(k: string): void {
    if (k === "class") this.className = "";
    else this.attrs?.delete(k);
  }
  override cloneNode(deep?: boolean): FakeElement {
    ops.clones++;
    const e = new FakeElement(this.tagName);
    e.cls = this.cls;
    if (this.attrs) e.attrs = new Map(this.attrs);
    if (deep) for (let c = this.firstChild; c; c = c.nextSibling) e.appendRaw(c.cloneNode(true));
    return e;
  }
  // Clone-time append that does not count as an insert of the list under test.
  private appendRaw(n: FakeNode): void {
    n.parentNode = this;
    n.previousSibling = this.lastChild;
    if (this.lastChild) this.lastChild.nextSibling = n;
    else this.firstChild = n;
    this.lastChild = n;
  }
  set innerHTML(src: string) {
    this.textContent = "";
    parse(src, this);
  }
  get content(): FakeElement {
    return this;
  }
}

const TOKEN =
  /<!--([\s\S]*?)-->|<\/([a-z0-9-]+)\s*>|<([a-z0-9-]+)((?:\s+[^\s=>/]+(?:=(?:"[^"]*"|'[^']*'|[^\s>]+))?)*)\s*(\/?)>|([^<]+)/gi;
const ATTR = /([^\s=>/]+)(?:=(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?/g;

function parse(src: string, root: FakeElement): void {
  const stack: FakeElement[] = [root];
  for (const m of src.matchAll(TOKEN)) {
    const top = stack[stack.length - 1]!;
    if (m[1] !== undefined) top.appendChild(new FakeComment(m[1]));
    else if (m[2]) stack.pop();
    else if (m[3]) {
      const el = new FakeElement(m[3].toUpperCase());
      for (const a of (m[4] ?? "").matchAll(ATTR)) el.setAttribute(a[1]!, a[2] ?? a[3] ?? a[4] ?? "");
      top.appendChild(el);
      if (!m[5]) stack.push(el);
    } else if (m[6]) top.appendChild(new FakeText(m[6]));
  }
  resetOps();
}

/** Installs a global `document` and `Node` with what the library and h() call. */
export function installFakeDocument(): void {
  const g = globalThis as unknown as { document: unknown; Node: unknown };
  g.document = {
    createElement: (tag: string) => new FakeElement(tag.toUpperCase()),
    createTextNode: (data: string) => new FakeText(data),
  };
  g.Node = FakeNode;
}

/** Casts a fake node to the DOM type the library's signatures expect. */
export const asDom = <T extends Node = Node>(n: FakeNode): T => n as unknown as T;
