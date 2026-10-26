// herdsman-dom-diff: a zero-dependency block renderer with a keyed list reconciler.
//
// Two ideas, nothing else:
//   1. Blocks: a template is parsed once. Each instance is a cloneNode(true)
//      plus direct pointers to its dynamic "holes". An update compares the new
//      value with the last one and touches exactly that node. No tree diff.
//   2. Lists: keyed reconciliation that trims the common prefix and suffix,
//      handles a two-element swap in O(window) without a Map, and otherwise
//      keeps a longest increasing subsequence in place and moves only the rest.
//      That is the minimum number of DOM moves.

/** Library version, bumped with every release of the zip package. */
export const VERSION = "0.0.1";

// ---------------------------------------------------------------------------
// Blocks
// ---------------------------------------------------------------------------

const enum Kind {
  Text,
  Class,
  Prop,
  Attr,
}

type Path = readonly number[];
type Locator = (root: Node, out: unknown[]) => void;

const cache = new WeakMap<TemplateStringsArray, Template>();
const ATTR_HOLE = /([^\s"'=<>/]+)\s*=\s*(["']?)$/;
const MARKER = /^bk\d+$/;
// Whitespace-only text containing a line break: formatting between tags.
const FORMATTING = /^\s*\n\s*$/;

/**
 * Tagged template that compiles once per call site.
 * Holes are positional: the Nth `${}` is slot N. The expression value is ignored.
 *
 *   const Row = html`<tr class=${0}><td>${1}</td></tr>`;
 *   const b = Row.mount(["danger", "hello"]);
 *   b.set(1, "world");
 *
 * Attribute rules: `class` sets className, `on*` and `.name` set a property,
 * anything else uses setAttribute (null or false removes it).
 */
export function html(strings: TemplateStringsArray, ..._holes: unknown[]): Template {
  let t = cache.get(strings);
  if (!t) cache.set(strings, (t = new Template(strings)));
  return t;
}

export class Template {
  readonly n: number;
  readonly kinds: Uint8Array;
  readonly names: (string | undefined)[];
  /** Fills out[0, n) with the hole nodes under a clone of this template. */
  readonly locate: Locator;
  private readonly root: Element;
  // The prototype's own hole nodes and the values last written into them.
  private readonly proto: unknown[];

  constructor(strings: readonly string[]) {
    const n = strings.length - 1;
    const parts = strings.slice();
    let src = "";
    let inTag = false;
    for (let i = 0; i <= n; i++) {
      const s = parts[i]!;
      for (let c = 0; c < s.length; c++) {
        if (s[c] === "<") inTag = true;
        else if (s[c] === ">") inTag = false;
      }
      if (i === n) {
        src += s;
        break;
      }
      if (inTag) {
        const m = ATTR_HOLE.exec(s);
        if (!m) throw new Error(`herdsman-dom-diff: hole ${i} is inside a tag but not an attribute value`);
        src += s.slice(0, m.index) + ` bk${i}="${encodeURIComponent(m[1]!)}"`;
        if (m[2]) parts[i + 1] = parts[i + 1]!.replace(m[2], ""); // drop the closing quote
      } else {
        src += s + `<!--bk${i}-->`;
      }
    }

    const tpl = document.createElement("template");
    tpl.innerHTML = src.trim();
    const root = tpl.content.firstChild;
    if (!root || root.nextSibling || root.nodeType !== 1) {
      throw new Error("herdsman-dom-diff: a template must have exactly one root element");
    }

    this.n = n;
    this.kinds = new Uint8Array(n);
    this.names = new Array(n);
    const paths: (Path | undefined)[] = new Array(n);
    this.walk(root, [], paths);
    for (let i = 0; i < n; i++) {
      if (!paths[i]) throw new Error(`herdsman-dom-diff: hole ${i} could not be located`);
    }
    this.root = root as Element;
    this.locate = compileLocator(paths as Path[]);
    this.proto = new Array(2 * n);
    this.locate(root, this.proto);
    // What the parsed prototype holds: empty text and class, absent attributes.
    for (let i = 0; i < n; i++) this.proto[n + i] = this.kinds[i] === Kind.Attr ? null : "";
  }

  /**
   * Make a new instance filled with `vals`.
   *
   * The values are written into the template's own prototype (only where they
   * differ from what it holds) and the prototype is deep-cloned, so the clone
   * is born with its text, classes and attributes. The clone's hole nodes are
   * not looked up until the first update that changes something: a row that
   * is never updated is never walked from JavaScript, which saves the wrapper
   * objects and garbage collection that walking would cost. Properties do not
   * survive cloning, so a non-null property hole is set on the clone directly.
   */
  mount(vals: ArrayLike<unknown>): Block {
    const n = this.n,
      kinds = this.kinds,
      names = this.names,
      proto = this.proto;
    let props = false;
    for (let i = 0; i < n; i++) {
      const v = vals[i];
      if (kinds[i] === Kind.Prop) {
        if (v != null) props = true;
      } else if (proto[n + i] !== v) {
        proto[n + i] = v;
        apply(kinds[i]!, names[i], proto[i] as Node, v);
      }
    }
    const s: unknown[] = new Array(2 * n);
    for (let i = 0; i < n; i++) s[n + i] = vals[i];
    const b = new Block(this, this.root.cloneNode(true) as Element, s);
    if (props) {
      this.locate(b.root, s);
      for (let i = 0; i < n; i++)
        if (kinds[i] === Kind.Prop && s[n + i] != null) apply(Kind.Prop, names[i], s[i] as Node, s[n + i]);
    }
    return b;
  }

  private walk(node: Node, path: Path, paths: (Path | undefined)[]): void {
    if (node.nodeType !== 1) return;
    const el = node as Element;
    const attrs = el.attributes;
    for (let a = attrs.length - 1; a >= 0; a--) {
      const nm = attrs[a]!.name;
      if (!MARKER.test(nm)) continue;
      const i = +nm.slice(2);
      const name = decodeURIComponent(attrs[a]!.value);
      el.removeAttribute(nm);
      paths[i] = path;
      if (name === "class") this.kinds[i] = Kind.Class;
      else if (name[0] === "." || name.startsWith("on")) {
        this.kinds[i] = Kind.Prop;
        this.names[i] = name[0] === "." ? name.slice(1) : name.toLowerCase();
      } else {
        this.kinds[i] = Kind.Attr;
        this.names[i] = name;
      }
    }
    let child = el.firstChild,
      k = 0;
    const keepSpace = el.nodeName === "PRE" || el.nodeName === "TEXTAREA";
    while (child) {
      const next = child.nextSibling;
      if (!keepSpace && child.nodeType === 3 && FORMATTING.test((child as Text).data)) {
        // Indentation from a formatter (bun fmt formats html`` as HTML), not content.
        el.removeChild(child);
        child = next;
        continue;
      }
      if (child.nodeType === 8 && MARKER.test((child as Comment).data)) {
        // Swap the marker comment for a real text node so clones need no fixup.
        el.replaceChild(document.createTextNode(""), child);
        const i = +(child as Comment).data.slice(2);
        paths[i] = path.concat(k);
        this.kinds[i] = Kind.Text;
      } else {
        this.walk(child, path.concat(k), paths);
      }
      child = next;
      k++;
    }
  }
}

// Turn hole paths into one straight-line function of firstChild/nextSibling
// hops that shares common prefixes. Falls back to a loop where eval is blocked (CSP).
// It returns the block's single state array: hole nodes in [0, n), with
// [n, 2n) left for the holes' last values, so a block costs one array.
function compileLocator(paths: Path[]): Locator {
  try {
    let body = "";
    const seen = new Map<string, string>();
    let v = 0;
    const ref = (path: Path): string => {
      if (path.length === 0) return "r";
      const key = path.join(",");
      const hit = seen.get(key);
      if (hit) return hit;
      let expr = ref(path.slice(0, -1)) + ".firstChild";
      for (let j = 0; j < path[path.length - 1]!; j++) expr += ".nextSibling";
      const name = "v" + v++;
      body += "var " + name + "=" + expr + ";";
      seen.set(key, name);
      return name;
    };
    paths.forEach((p, i) => {
      const r = ref(p); // must run before reading `body`: ref() appends declarations
      body += "n[" + i + "]=" + r + ";";
    });
    return new Function("r", "n", body) as Locator;
  } catch {
    return (r, out) => {
      paths.forEach((p, i) => {
        let node = r;
        for (const k of p) {
          node = node.firstChild!;
          for (let j = 0; j < k; j++) node = node.nextSibling!;
        }
        out[i] = node;
      });
    };
  }
}

function apply(kind: number, name: string | undefined, node: Node, v: unknown): void {
  switch (kind) {
    case Kind.Text:
      (node as Text).data = v == null ? "" : "" + v;
      break;
    case Kind.Class:
      (node as Element).className = v == null ? "" : "" + v;
      break;
    case Kind.Prop:
      (node as unknown as Record<string, unknown>)[name!] = v;
      break;
    default:
      if (v == null || v === false) (node as Element).removeAttribute(name!);
      else (node as Element).setAttribute(name!, v === true ? "" : "" + v);
  }
}

export class Block {
  constructor(
    readonly t: Template,
    readonly root: Element,
    /** Hole nodes in [0, n), their last values in [n, 2n). One allocation per block. */
    readonly s: unknown[],
  ) {}

  /** The last value set for hole i. */
  get(i: number): unknown {
    return this.s[this.t.n + i];
  }

  /** Set one hole. A no-op when the value is identical (===) to the last one. */
  set(i: number, v: unknown): void {
    const s = this.s,
      t = this.t,
      j = t.n + i;
    if (s[j] === v) return;
    s[j] = v;
    if (s[0] === undefined) t.locate(this.root, s); // first real update: find the holes now
    apply(t.kinds[i]!, t.names[i], s[i] as Node, v);
  }

  /** Set every hole from an array. */
  patch(vals: ArrayLike<unknown>): void {
    for (let i = 0; i < vals.length; i++) this.set(i, vals[i]);
  }
}

// ---------------------------------------------------------------------------
// Keyed lists
// ---------------------------------------------------------------------------

export type Key = string | number;
export interface Entry {
  readonly root: Node;
}

export interface ListOptions<T, E extends Entry> {
  /** Unique, stable key for an item. */
  key: (item: T) => Key;
  /** Build the DOM for a new item. */
  create: (item: T) => E;
  /** Refresh an existing entry for a (possibly new) item. */
  update: (entry: E, item: T) => void;
  /** Insert before this node; null appends at the end of the parent. */
  anchor?: Node | null;
  /** True when the list is the parent's only content: enables textContent clears. */
  owns?: boolean;
  /**
   * With `owns`: when the list is built from empty (first render or full
   * replacement), take the parent out of the document, fill it, and put it
   * back, so the browser does its style and layout work once instead of per
   * row. Detaching resets the parent's own scroll position and drops focus
   * inside it, so leave this off for scrolling containers.
   */
  detach?: boolean;
}

export class List<T, E extends Entry> {
  keys: Key[] = [];
  entries: E[] = [];
  private readonly anchor: Node | null;
  private readonly owns: boolean;
  private readonly detach: boolean;
  private readonly key: (item: T) => Key;
  private readonly create: (item: T) => E;
  private readonly update: (entry: E, item: T) => void;

  constructor(
    readonly parent: Node,
    o: ListOptions<T, E>,
  ) {
    this.anchor = o.anchor ?? null;
    this.owns = !!o.owns && this.anchor === null;
    this.detach = this.owns && !!o.detach;
    this.key = o.key;
    this.create = o.create;
    this.update = o.update;
  }

  clear(): void {
    const p = this.parent,
      e = this.entries;
    if (this.owns) p.textContent = "";
    else for (let i = 0; i < e.length; i++) p.removeChild(e[i]!.root);
    this.keys = [];
    this.entries = [];
  }

  set(items: readonly T[]): void {
    const p = this.parent,
      a = this.anchor,
      key = this.key,
      upd = this.update,
      mk = this.create;
    const oldK = this.keys,
      oldE = this.entries;
    const n = items.length,
      m = oldK.length;

    if (n === 0) {
      if (m) this.clear();
      return;
    }

    const newK: Key[] = new Array(n),
      newE: E[] = new Array(n);
    for (let i = 0; i < n; i++) newK[i] = key(items[i]!);

    if (m === 0) {
      this.fill(items, newE);
      this.keys = newK;
      this.entries = newE;
      return;
    }

    // 1. Common prefix and suffix: patch in place, never move.
    let s = 0,
      oe = m - 1,
      ne = n - 1;
    while (s <= oe && s <= ne && oldK[s] === newK[s]) {
      upd((newE[s] = oldE[s]!), items[s]!);
      s++;
    }
    while (oe >= s && ne >= s && oldK[oe] === newK[ne]) {
      upd((newE[ne] = oldE[oe]!), items[ne]!);
      oe--;
      ne--;
    }

    if (s > oe) {
      // 2a. Only insertions left.
      const ref = ne + 1 < n ? newE[ne + 1]!.root : a;
      for (let i = s; i <= ne; i++) p.insertBefore((newE[i] = mk(items[i]!)).root, ref);
    } else if (s > ne) {
      // 2b. Only removals left.
      for (let i = s; i <= oe; i++) p.removeChild(oldE[i]!.root);
    } else if (
      oe === ne &&
      oe > s &&
      oldK[s] === newK[ne] &&
      oldK[oe] === newK[s] &&
      sameRange(oldK, newK, s + 1, oe - 1)
    ) {
      // 3. Two ends swapped, middle unchanged: two moves, no Map, no LIS.
      const first = oldE[s]!,
        last = oldE[oe]!;
      const ref = last.root.nextSibling;
      p.insertBefore(last.root, first.root);
      if (oe - s > 1) p.insertBefore(first.root, ref); // adjacent: one move is enough
      newE[s] = last;
      upd(last, items[s]!);
      newE[ne] = first;
      upd(first, items[ne]!);
      for (let i = s + 1; i < oe; i++) upd((newE[i] = oldE[i]!), items[i]!);
    } else {
      this.general(items, newK, newE, s, oe, ne);
    }

    this.keys = newK;
    this.entries = newE;
  }

  // Build every entry into a parent that holds none of them yet.
  private fill(items: readonly T[], newE: E[]): void {
    const p = this.parent,
      a = this.anchor,
      mk = this.create,
      n = items.length;
    const host = this.detach ? p.parentNode : null;
    const next = host ? p.nextSibling : null;
    if (host) host.removeChild(p);
    for (let i = 0; i < n; i++) p.insertBefore((newE[i] = mk(items[i]!)).root, a);
    if (host) host.insertBefore(p, next);
  }

  // 4. General case on the middle window [s..oe] (old) vs [s..ne] (new).
  private general(items: readonly T[], newK: Key[], newE: E[], s: number, oe: number, ne: number): void {
    const p = this.parent,
      a = this.anchor,
      upd = this.update,
      mk = this.create;
    const oldK = this.keys,
      oldE = this.entries;
    const n = newK.length,
      m = oldK.length;
    const len = ne - s + 1;
    const src = new Int32Array(len).fill(-1); // new position -> old index, -1 = new item
    let moved = false,
      last = -1,
      kept = 0;

    // Small windows: a linear scan beats building a Map.
    let idx: Map<Key, number> | null = null;
    if (len > 16) {
      idx = new Map();
      for (let i = s; i <= ne; i++) idx.set(newK[i]!, i);
    }

    for (let i = s; i <= oe; i++) {
      let j: number | undefined;
      if (idx) j = idx.get(oldK[i]!);
      else {
        for (j = s; j <= ne && newK[j] !== oldK[i]; j++);
        if (j > ne) j = undefined;
      }
      if (j === undefined) continue;
      src[j - s] = i;
      newE[j] = oldE[i]!;
      if (j < last) moved = true;
      else last = j;
      kept++;
    }

    if (kept === 0 && this.owns && s === 0 && oe === m - 1 && ne === n - 1) {
      // Full replacement: one native clear beats N removeChild calls.
      p.textContent = "";
      this.fill(items, newE);
      return;
    }

    // Remove old entries that did not survive.
    if (kept < oe - s + 1) {
      for (let i = s; i <= oe; i++) {
        const gone = idx ? !idx.has(oldK[i]!) : !includes(newK, oldK[i]!, s, ne);
        if (gone) p.removeChild(oldE[i]!.root);
      }
    }
    for (let j = 0; j < len; j++) if (src[j] !== -1) upd(newE[j + s]!, items[j + s]!);

    // Walk right to left. Survivors on the LIS stay put; everything else is
    // inserted before its right neighbour, which is already in final position.
    const seq = moved ? lis(src) : null;
    let li = seq ? seq.length - 1 : -1;
    for (let j = len - 1; j >= 0; j--) {
      const pos = j + s;
      const ref = pos + 1 < n ? newE[pos + 1]!.root : a;
      if (src[j] === -1) {
        p.insertBefore((newE[pos] = mk(items[pos]!)).root, ref);
      } else if (moved) {
        if (li >= 0 && seq![li] === j) li--;
        else p.insertBefore(newE[pos]!.root, ref);
      }
    }
  }
}

function sameRange(x: readonly Key[], y: readonly Key[], from: number, to: number): boolean {
  for (let i = from; i <= to; i++) if (x[i] !== y[i]) return false;
  return true;
}

function includes(arr: readonly Key[], v: Key, from: number, to: number): boolean {
  for (let i = from; i <= to; i++) if (arr[i] === v) return true;
  return false;
}

/**
 * Longest strictly increasing subsequence of `a`, ignoring -1 entries.
 * Returns ascending positions into `a`. O(n log n) patience sorting.
 */
export function lis(a: ArrayLike<number>): number[] {
  const n = a.length;
  const prev = new Int32Array(n);
  const tails = new Int32Array(n); // tails[k] = position ending the best run of length k+1
  let len = 0;
  for (let i = 0; i < n; i++) {
    const v = a[i]!;
    if (v === -1) continue;
    if (len > 0 && a[tails[len - 1]!]! < v) {
      prev[i] = tails[len - 1]!;
      tails[len++] = i;
      continue;
    }
    let lo = 0,
      hi = len;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if (a[tails[mid]!]! < v) lo = mid + 1;
      else hi = mid;
    }
    prev[i] = lo > 0 ? tails[lo - 1]! : -1;
    tails[lo] = i;
    if (lo === len) len++;
  }
  const out: number[] = new Array(len);
  for (let k = len - 1, i = len ? tails[len - 1]! : -1; k >= 0; k--, i = prev[i]!) out[k] = i;
  return out;
}

// ---------------------------------------------------------------------------
// Morph: patch live DOM to match freshly built DOM
// ---------------------------------------------------------------------------

export interface MorphOptions {
  /**
   * Called for every live element that is kept and patched, with the fresh
   * element it was matched to. Use it to carry over state the DOM cannot
   * compare, such as event handlers.
   */
  onKeep?: (live: Element, next: Element) => void;
}

/** Attribute that keys a child among its siblings, so it is moved rather than rebuilt. */
export const KEY_ATTR = "data-key";
/** Attribute that tells morph to patch an element's attributes but leave its children alone. */
export const SKIP_ATTR = "data-morph-skip";

interface Slot {
  readonly root: Node;
}

/**
 * Make `target`'s children match `next`, a list of freshly built nodes.
 *
 * Children are matched to live ones by `data-key` (or, without one, by tag
 * and order among unkeyed siblings of the same tag). A matched live node is
 * kept and patched in place: its text, attributes and form state are synced
 * and its children are morphed in turn. Unmatched fresh nodes are inserted as
 * they are; unmatched live nodes are removed. Moves use the keyed list
 * reconciler, so reordering costs the minimum number of DOM moves.
 *
 * Keeping live nodes keeps what a rebuild loses: focus, the caret, text
 * selection, scroll positions, open <details>, and the nodes other code holds.
 * A fresh node that is already a live child (a persistent element reused by
 * the view) is left untouched.
 */
export function morph(target: Node, next: readonly Node[], options: MorphOptions = {}): void {
  morphChildren(target, next, options);
}

function morphChildren(live: Node, next: readonly Node[], o: MorphOptions): void {
  const counts = new Map<string, number>();
  const keyOf = (n: Node): Key => {
    const tag = n.nodeName;
    const k = n.nodeType === 1 ? (n as Element).getAttribute(KEY_ATTR) : null;
    const base = k === null ? "#" + tag : "k:" + tag + ":" + k;
    const seen = counts.get(base) ?? 0;
    counts.set(base, seen + 1);
    // Unkeyed nodes match by order; a repeated key also falls back to order.
    return seen === 0 && k !== null ? base : base + "#" + seen;
  };

  const list = new List<Node, Slot>(live, {
    owns: true,
    key: keyOf,
    create: (n) => ({ root: n }),
    update: (slot, n) => morphNode(slot.root, n, o),
  });
  for (let c = live.firstChild; c; c = c.nextSibling) {
    list.keys.push(keyOf(c));
    list.entries.push({ root: c });
  }
  counts.clear();
  list.set(next);
}

function morphNode(live: Node, next: Node, o: MorphOptions): void {
  if (live === next) return;
  if (live.nodeType !== 1) {
    if (live.nodeValue !== next.nodeValue) live.nodeValue = next.nodeValue;
    return;
  }
  const el = live as Element,
    fresh = next as Element;
  syncAttributes(el, fresh);
  o.onKeep?.(el, fresh);
  if (!el.hasAttribute(SKIP_ATTR)) {
    const kids: Node[] = [];
    for (let c = fresh.firstChild; c; c = c.nextSibling) kids.push(c);
    morphChildren(el, kids, o);
  }
  syncFormState(el, fresh); // after children, so a <select> already has its options
}

function syncAttributes(el: Element, fresh: Element): void {
  const want = fresh.attributes;
  for (let i = 0; i < want.length; i++) {
    const a = want[i]!;
    if (el.getAttribute(a.name) !== a.value) el.setAttribute(a.name, a.value);
  }
  const have = el.attributes;
  for (let i = have.length - 1; i >= 0; i--) {
    const name = have[i]!.name;
    if (!fresh.hasAttribute(name)) el.removeAttribute(name);
  }
}

// Form state lives in properties that attributes do not reflect. A focused
// field keeps the user's in-progress value.
function syncFormState(el: Element, fresh: Element): void {
  const tag = el.nodeName;
  if (tag !== "INPUT" && tag !== "TEXTAREA" && tag !== "SELECT" && tag !== "OPTION") return;
  const a = el as HTMLInputElement,
    b = fresh as HTMLInputElement;
  const focused = el.ownerDocument?.activeElement === el;
  if (tag === "OPTION") {
    const ao = el as HTMLOptionElement,
      bo = fresh as HTMLOptionElement;
    if (ao.selected !== bo.selected) ao.selected = bo.selected;
    return;
  }
  if (!focused && "value" in b && a.value !== b.value) a.value = b.value;
  if (tag === "INPUT" && a.checked !== b.checked) a.checked = b.checked;
}
