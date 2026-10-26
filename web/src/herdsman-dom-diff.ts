// herdsman-dom-diff: a zero-dependency DOM diff for the herdsman web app.
//
//   1. Blocks: a template is parsed once. Each instance is a cloneNode(true)
//      of a prototype that already holds the instance's values; its hole nodes
//      are looked up on the first update that changes something. No tree diff.
//   2. Lists: keyed reconciliation that trims the common prefix and suffix,
//      handles a two-element swap without a Map, and otherwise keeps a longest
//      increasing subsequence in place and moves only the rest. That is the
//      minimum number of DOM moves.
//   3. Morph: patches a live subtree to match a freshly built one.
//
// Types are strict and erase completely: nothing below adds work on the hot
// paths (mount, set, List.set) for the sake of types.

/** Library version, bumped with every release of the zip package. */
export const VERSION = "0.0.3";

// ---------------------------------------------------------------------------
// Hole markers: typed at compile time, small negative numbers at runtime
// ---------------------------------------------------------------------------

/** Where a hole sits in a template, which decides how its value is written. */
export type HoleKind = "text" | "class" | "attr" | "prop";

declare const HOLE: unique symbol;

/**
 * A typed hole marker for `html`. At runtime it is a small number; its type
 * records the value type the hole accepts and the position it must sit in.
 */
export type Hole<V, K extends HoleKind = HoleKind> = number & { readonly [HOLE]: readonly [V, K] };

/** Values a text hole accepts. null and undefined write empty text. */
export type TextValue = string | number | bigint | boolean | null | undefined;
/** Values a class hole accepts. null and undefined clear the class. */
export type ClassValue = string | null | undefined;
/** Values an attribute hole accepts. null, undefined and false remove it; true sets it empty. */
export type AttrValue = string | number | bigint | boolean | null | undefined;

// Runtime kind codes. Plain numbers (erasable syntax), inlined by minifiers.
const TEXT = 0;
const CLASS = 1;
const PROP = 2;
const ATTR = 3;
type KindCode = typeof TEXT | typeof CLASS | typeof PROP | typeof ATTR;
const KIND_NAMES: readonly HoleKind[] = ["text", "class", "prop", "attr"];

/** A text hole: element content, between tags. */
export function text<V extends TextValue = string>(): Hole<V, "text"> {
  return (-1 - TEXT) as Hole<V, "text">;
}
/** A class hole: the value of a `class` attribute. */
export function cls<V extends ClassValue = string>(): Hole<V, "class"> {
  return (-1 - CLASS) as Hole<V, "class">;
}
/** An attribute hole: the value of any attribute other than `class`, `on*` or `.name`. */
export function attr<V extends AttrValue = string>(): Hole<V, "attr"> {
  return (-1 - ATTR) as Hole<V, "attr">;
}
/** A property hole: the value of an `on*` attribute or a `.name` property. */
export function prop<V = unknown>(): Hole<V, "prop"> {
  return (-1 - PROP) as Hole<V, "prop">;
}

/** The value tuple of a template whose holes are `H`; an untyped hole (a plain number) accepts unknown. */
export type ValuesOf<H extends readonly number[]> = {
  readonly [I in keyof H]: H[I] extends Hole<infer V, HoleKind> ? V : unknown;
};

type NumericKey<S> = S extends `${infer N extends number}` ? N : never;

/** The valid hole indexes of a value tuple: 0 | 1 | ... | n - 1 (number for a non-tuple array). */
export type HoleIndex<V extends readonly unknown[]> = (number extends V["length"]
  ? number
  : NumericKey<Extract<keyof V, `${number}`>>) &
  number;

/** The value type of hole `I` in value tuple `V`. */
export type HoleValue<V extends readonly unknown[], I extends number> = I extends keyof V ? V[I] : never;

/** The block type a template mounts. */
export type BlockOf<T> = T extends Template<infer V> ? Block<V> : never;

// ---------------------------------------------------------------------------
// Blocks
// ---------------------------------------------------------------------------

type Path = readonly number[];

// Marks a hole whose current value is unknown, so its next set always writes.
const UNSET: unique symbol = Symbol("unset");
// Caches a lazily created block on its element.
const BLOCK: unique symbol = Symbol("herdsman-dom-diff.block");

/** A block obtained from `Template.block` for an element made by `mountRoot`: it can write holes but does not know their values. */
export type RootBlock<V extends readonly unknown[]> = Pick<Block<V>, "t" | "root" | "set" | "patch">;
type Locator = (root: Node, out: unknown[]) => void;

const cache = new WeakMap<TemplateStringsArray, Template<readonly unknown[]>>();
const ATTR_HOLE = /([^\s"'=<>/]+)\s*=\s*(["']?)$/;
const MARKER = /^bk\d+$/;
// Whitespace-only text containing a line break: formatting between tags.
const FORMATTING = /^\s*\n\s*$/;

/**
 * Tagged template that compiles once per call site.
 *
 *   const Row = html`<tr class=${cls()}><td>${text<number>()}</td></tr>`;
 *   const b = Row.mount(["danger", 7]);   // typed: readonly [string, number]
 *   b.set(1, 8);
 *
 * Holes are positional: the Nth `${}` is hole N. Use `text`, `cls`, `attr` and
 * `prop` markers to type each hole; a marker in the wrong position throws when
 * the template is first compiled. A plain number (`${0}`) is an untyped hole.
 *
 * Attribute rules: `class` sets className, `on*` and `.name` set a property,
 * anything else uses setAttribute (null, undefined or false removes it).
 */
export function html<const H extends readonly number[]>(
  strings: TemplateStringsArray,
  ...holes: H
): Template<ValuesOf<H>> {
  let t = cache.get(strings);
  if (!t) cache.set(strings, (t = new Template(strings, holes)));
  return t as Template<ValuesOf<H>>;
}

/** A compiled template. Make instances with `mount`. */
export class Template<V extends readonly unknown[] = readonly unknown[]> {
  /** Number of holes. */
  readonly n: number;
  /** Runtime kind code of each hole. */
  readonly kinds: Uint8Array;
  /** Attribute or property name of each attribute and property hole. */
  readonly names: readonly (string | undefined)[];
  /** Fills out[0, n) with the hole nodes under a clone of this template. */
  readonly locate: Locator;
  private readonly root: Element;
  // The prototype's own hole nodes and the values last written into them.
  private readonly proto: unknown[];

  constructor(strings: readonly string[], holes: readonly number[] = []) {
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
    const names: (string | undefined)[] = new Array(n);
    const paths: (Path | undefined)[] = new Array(n);
    walk(root, [], paths, this.kinds, names);
    for (let i = 0; i < n; i++) {
      if (!paths[i]) throw new Error(`herdsman-dom-diff: hole ${i} could not be located`);
      // A typed marker must sit where its kind says. Checked once per template.
      const h = holes[i];
      if (h !== undefined && h < 0 && -1 - h !== this.kinds[i]) {
        const want = KIND_NAMES[-1 - h] ?? "unknown",
          got = KIND_NAMES[this.kinds[i]!] ?? "unknown";
        throw new Error(`herdsman-dom-diff: hole ${i} is typed ${want}, but its position makes it ${got}`);
      }
    }
    this.names = names;
    this.root = root as Element;
    this.locate = compileLocator(paths as Path[]);
    this.proto = new Array(2 * n);
    this.locate(root, this.proto);
    // What the parsed prototype holds: empty text and class, absent attributes.
    for (let i = 0; i < n; i++) this.proto[n + i] = this.kinds[i] === ATTR ? null : "";
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
  mount(vals: V): Block<V> {
    const root = this.cloneWith(vals);
    const s: unknown[] = new Array(2 * this.n);
    for (let i = 0; i < this.n; i++) s[this.n + i] = vals[i];
    if (this.props(vals)) this.setProps(root, s, vals);
    return new Block<V>(this, root, s);
  }

  /**
   * Like `mount`, but returns only the new element: no JavaScript object is
   * kept per instance. Ask `block(root)` for a block when the element first
   * needs an update. This is the cheapest way to create rows that mostly
   * never change.
   */
  mountRoot(vals: V): Element {
    const root = this.cloneWith(vals);
    if (this.props(vals)) this.setProps(root, new Array(2 * this.n), vals);
    return root;
  }

  /**
   * The block for an element made by `mountRoot`, created on first request
   * and cached on the element. It does not know the values the element was
   * created with, so it offers `set` and `patch` (each hole's first set
   * always writes) but not `get`.
   */
  block(root: Element): RootBlock<V> {
    const holder = root as Element & { [BLOCK]?: Block<V> };
    let b = holder[BLOCK];
    if (b === undefined) {
      const n = this.n,
        s: unknown[] = new Array(2 * n);
      for (let i = 0; i < n; i++) s[n + i] = UNSET;
      holder[BLOCK] = b = new Block<V>(this, root, s);
    }
    return b;
  }

  // Bake the non-property values into the prototype and deep-clone it.
  private cloneWith(vals: V): Element {
    const n = this.n,
      kinds = this.kinds,
      names = this.names,
      proto = this.proto;
    for (let i = 0; i < n; i++) {
      const v = vals[i];
      if (kinds[i] !== PROP && proto[n + i] !== v) {
        proto[n + i] = v;
        apply(kinds[i] as KindCode, names[i], proto[i] as Node, v);
      }
    }
    return this.root.cloneNode(true) as Element;
  }

  // Whether any property hole has a value. Properties do not survive cloning.
  private props(vals: V): boolean {
    for (let i = 0; i < this.n; i++) if (this.kinds[i] === PROP && vals[i] != null) return true;
    return false;
  }

  // Locate the clone's holes into `s` and set its non-null properties.
  private setProps(root: Element, s: unknown[], vals: V): void {
    this.locate(root, s);
    for (let i = 0; i < this.n; i++) {
      if (this.kinds[i] === PROP && vals[i] != null) apply(PROP, this.names[i], s[i] as Node, vals[i]);
    }
  }
}

function walk(
  node: Node,
  path: Path,
  paths: (Path | undefined)[],
  kinds: Uint8Array,
  names: (string | undefined)[],
): void {
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
    if (name === "class") kinds[i] = CLASS;
    else if (name[0] === "." || name.startsWith("on")) {
      kinds[i] = PROP;
      names[i] = name[0] === "." ? name.slice(1) : name.toLowerCase();
    } else {
      kinds[i] = ATTR;
      names[i] = name;
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
      kinds[i] = TEXT;
    } else {
      walk(child, path.concat(k), paths, kinds, names);
    }
    child = next;
    k++;
  }
}

// Turn hole paths into one straight-line function of firstChild/nextSibling
// hops that shares common prefixes. Falls back to a loop where eval is blocked (CSP).
// It fills the first half of a block's single state array: hole nodes in
// [0, n), with [n, 2n) left for the holes' last values.
function compileLocator(paths: readonly Path[]): Locator {
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

function apply(kind: KindCode, name: string | undefined, node: Node, v: unknown): void {
  switch (kind) {
    case TEXT:
      (node as Text).data = v == null ? "" : "" + (v as TextValue);
      break;
    case CLASS:
      (node as Element).className = v == null ? "" : "" + (v as ClassValue);
      break;
    case PROP:
      (node as unknown as Record<string, unknown>)[name!] = v;
      break;
    case ATTR:
      if (v == null || v === false) (node as Element).removeAttribute(name!);
      else (node as Element).setAttribute(name!, v === true ? "" : "" + (v as AttrValue));
      break;
  }
}

/** One instance of a template: its root element and the last value of each hole. */
export class Block<V extends readonly unknown[] = readonly unknown[]> {
  /** The template this block was mounted from. */
  readonly t: Template<V>;
  /** The instance's root element. */
  readonly root: Element;
  // Hole nodes in [0, n) (empty until the first real update), their last
  // values in [n, 2n). One allocation per block.
  private readonly s: unknown[];

  constructor(t: Template<V>, root: Element, s: unknown[]) {
    this.t = t;
    this.root = root;
    this.s = s;
  }

  /** The last value set for hole i. */
  get<I extends HoleIndex<V>>(i: I): HoleValue<V, I> {
    return this.s[this.t.n + i] as HoleValue<V, I>;
  }

  /** Set one hole. A no-op when the value is identical (===) to the last one. */
  set<I extends HoleIndex<V>>(i: I, v: HoleValue<V, I>): void {
    const s = this.s,
      t = this.t,
      j = t.n + i;
    if (s[j] === v) return;
    s[j] = v;
    if (s[0] === undefined) t.locate(this.root, s); // first real update: find the holes now
    apply(t.kinds[i] as KindCode, t.names[i], s[i] as Node, v);
  }

  /** Set every hole. */
  patch(vals: V): void {
    for (let i = 0; i < vals.length; i++) this.set(i as HoleIndex<V>, vals[i] as HoleValue<V, HoleIndex<V>>);
  }
}

// ---------------------------------------------------------------------------
// Keyed lists
// ---------------------------------------------------------------------------

/** A list item's key: unique among its siblings and stable across renders. */
export type Key = string | number;

/** What a list keeps per item: at least the item's root node. */
export interface Entry {
  readonly root: Node;
}

/** How a list finds an entry's root node: optional for entries with a `root`, required otherwise. */
export type RootOption<E> = E extends Entry
  ? { readonly root?: (entry: E) => Node }
  : { readonly root: (entry: E) => Node };

/** How a list maps items to DOM. */
export type ListOptions<T, E, K extends Key = Key> = ListBase<T, E, K> & RootOption<E>;

/** The options every list takes. */
export interface ListBase<T, E, K extends Key = Key> {
  /** Unique, stable key for an item. */
  readonly key: (item: T) => K;
  /** Build the DOM for a new item. */
  readonly create: (item: T) => E;
  /** Refresh an existing entry for a (possibly new) item. */
  readonly update: (entry: E, item: T) => void;
  /** Insert before this node; null appends at the end of the parent. */
  readonly anchor?: Node | null;
  /** True when the list is the parent's only content: enables textContent clears. */
  readonly owns?: boolean;
  /**
   * With `owns`: when the list is built from empty (first render or full
   * replacement), take the parent out of the document, fill it, and put it
   * back, so the browser does its style and layout work once instead of per
   * row. Detaching resets the parent's own scroll position and drops focus
   * inside it, so leave this off for scrolling containers.
   */
  readonly detach?: boolean;
}

/** A keyed list of items rendered into a parent node. Call `set` with each new array. */
export class List<T, E = Entry, K extends Key = Key> {
  /** The parent the list renders into. */
  readonly parent: Node;
  /** Keys of the last render, in order. */
  keys: K[] = [];
  /** Entries of the last render, in order. */
  entries: E[] = [];
  private readonly anchor: Node | null;
  private readonly owns: boolean;
  private readonly detach: boolean;
  private readonly key: (item: T) => K;
  private readonly create: (item: T) => E;
  private readonly update: (entry: E, item: T) => void;
  private readonly r: (entry: E) => Node;

  constructor(parent: Node, o: ListOptions<T, E, K>) {
    this.parent = parent;
    this.anchor = o.anchor ?? null;
    this.owns = o.owns === true && this.anchor === null;
    this.detach = this.owns && o.detach === true;
    this.key = o.key;
    this.create = o.create;
    this.update = o.update;
    this.r = (o as { readonly root?: (entry: E) => Node }).root ?? (entryRoot as (entry: E) => Node);
  }

  /** Remove every entry. */
  clear(): void {
    const p = this.parent,
      e = this.entries,
      r = this.r;
    if (this.owns) p.textContent = "";
    else for (let i = 0; i < e.length; i++) p.removeChild(r(e[i]!));
    this.keys = [];
    this.entries = [];
  }

  /** Render `items`, reusing the entries of items whose keys were already there. */
  set(items: readonly T[]): void {
    const p = this.parent,
      a = this.anchor,
      key = this.key,
      upd = this.update,
      mk = this.create,
      r = this.r;
    const oldK = this.keys,
      oldE = this.entries;
    const n = items.length,
      m = oldK.length;

    if (n === 0) {
      if (m) this.clear();
      return;
    }

    const newK: K[] = new Array(n),
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
      const ref = ne + 1 < n ? r(newE[ne + 1]!) : a;
      for (let i = s; i <= ne; i++) place(p, r((newE[i] = mk(items[i]!))), ref);
    } else if (s > ne) {
      // 2b. Only removals left.
      for (let i = s; i <= oe; i++) p.removeChild(r(oldE[i]!));
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
      const lastRoot = r(last),
        firstRoot = r(first);
      const ref = lastRoot.nextSibling;
      move(p, lastRoot, firstRoot);
      if (oe - s > 1) move(p, firstRoot, ref); // adjacent: one move is enough
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
      r = this.r,
      n = items.length;
    const host = this.detach ? p.parentNode : null;
    const next = host ? p.nextSibling : null;
    if (host) host.removeChild(p);
    for (let i = 0; i < n; i++) p.insertBefore(r((newE[i] = mk(items[i]!))), a);
    if (host) host.insertBefore(p, next);
  }

  // 4. General case on the middle window [s..oe] (old) vs [s..ne] (new).
  private general(items: readonly T[], newK: readonly K[], newE: E[], s: number, oe: number, ne: number): void {
    const p = this.parent,
      a = this.anchor,
      upd = this.update,
      mk = this.create,
      r = this.r;
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
    let idx: Map<K, number> | null = null;
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
        if (gone) p.removeChild(r(oldE[i]!));
      }
    }
    for (let j = 0; j < len; j++) if (src[j] !== -1) upd(newE[j + s]!, items[j + s]!);

    // Walk right to left. Survivors on the LIS stay put; everything else is
    // inserted before its right neighbour, which is already in final position.
    const seq = moved ? lis(src) : null;
    let li = seq ? seq.length - 1 : -1;
    for (let j = len - 1; j >= 0; j--) {
      const pos = j + s;
      const ref = pos + 1 < n ? r(newE[pos + 1]!) : a;
      if (src[j] === -1) {
        place(p, r((newE[pos] = mk(items[pos]!))), ref);
      } else if (moved) {
        if (li >= 0 && seq![li] === j) li--;
        else move(p, r(newE[pos]!), ref);
      }
    }
  }
}

const entryRoot = (e: Entry): Node => e.root;

type Movable = Node & { moveBefore?: (node: Node, child: Node | null) => void };

// Move a child that is already in `p`. moveBefore (Chrome 133+) keeps focus,
// iframe and animation state that insertBefore resets, at the same speed; it
// throws off-document, so a detached parent uses insertBefore.
function move(p: Node, node: Node, ref: Node | null): void {
  const m = p as Movable;
  if (m.moveBefore !== undefined && p.isConnected) m.moveBefore(node, ref);
  else p.insertBefore(node, ref);
}

// Insert a new entry's node. A node that is already in the document elsewhere
// (a persistent element a view reuses) is moved with its state kept.
function place(p: Node, node: Node, ref: Node | null): void {
  if (node.parentNode !== null && node.isConnected) move(p, node, ref);
  else p.insertBefore(node, ref);
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

/** Options for `morph`. */
export interface MorphOptions {
  /**
   * Called for every live element that is kept and patched, with the fresh
   * element it was matched to. Use it to carry over state the DOM cannot
   * compare, such as event handlers.
   */
  readonly onKeep?: (live: Element, next: Element) => void;
}

/** Attribute that keys a child among its siblings, so it is moved rather than rebuilt. */
export const KEY_ATTR = "data-key";
/** Attribute that tells morph to patch an element's attributes but leave its children alone. */
export const SKIP_ATTR = "data-morph-skip";

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
  const keyOf = (n: Node): string => {
    const tag = n.nodeName;
    const k = n.nodeType === 1 ? (n as Element).getAttribute(KEY_ATTR) : null;
    const base = k === null ? "#" + tag : "k:" + tag + ":" + k;
    const seen = counts.get(base) ?? 0;
    counts.set(base, seen + 1);
    // Unkeyed nodes match by order; a repeated key also falls back to order.
    return seen === 0 && k !== null ? base : base + "#" + seen;
  };

  const list = new List<Node, Node, string>(live, {
    owns: true,
    key: keyOf,
    root: nodeRoot,
    create: nodeRoot,
    update: (liveNode, n) => morphNode(liveNode, n, o),
  });
  for (let c = live.firstChild; c; c = c.nextSibling) {
    list.keys.push(keyOf(c));
    list.entries.push(c);
  }
  counts.clear();
  list.set(next);
}

const nodeRoot = (n: Node): Node => n;

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
  if (tag === "OPTION") {
    const ao = el as HTMLOptionElement,
      bo = fresh as HTMLOptionElement;
    if (ao.selected !== bo.selected) ao.selected = bo.selected;
    return;
  }
  const a = el as HTMLInputElement,
    b = fresh as HTMLInputElement;
  const focused = el.ownerDocument?.activeElement === el;
  if (!focused && "value" in b && a.value !== b.value) a.value = b.value;
  if (tag === "INPUT" && a.checked !== b.checked) a.checked = b.checked;
}
