// herdsman-blocks: a zero-dependency DOM diff for the herdsman web app.
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
export const VERSION = "0.0.8";

// ---------------------------------------------------------------------------
// Hole markers: typed at compile time, small negative numbers at runtime
// ---------------------------------------------------------------------------

/** Where a hole sits in a template, which decides how its value is written. */
export type HoleKind = "text" | "class" | "attr" | "prop" | "slot";

/** What a marker declares: a hole kind, or "event" for a property hole whose name starts with `on`. */
export type MarkerKind = HoleKind | "event";

declare const HOLE: unique symbol;

/**
 * A typed hole marker for `html`. At runtime it is a small number; its type
 * records the value type the hole accepts and the position it must sit in.
 */
export type Hole<V, K extends MarkerKind = MarkerKind> = number & { readonly [HOLE]: readonly [V, K] };

/** Any typed hole marker (a plain number is an untyped hole, not a marker). */
export type AnyHole = Hole<unknown, MarkerKind>;

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
const SLOT = 4;
type KindCode = typeof TEXT | typeof CLASS | typeof PROP | typeof ATTR | typeof SLOT;
const KIND_NAMES: readonly HoleKind[] = ["text", "class", "prop", "attr", "slot"];
// Marker-only code: an event marker is a property hole at an `on*` name. Only
// the once-per-template check reads it; holes never carry it at runtime.
const EVENT = 5;
const MARKER_NAMES: readonly MarkerKind[] = ["text", "class", "prop", "attr", "slot", "event"];
const SVG_NS = "http://www.w3.org/2000/svg";

/** A text hole: element content, between tags. */
export function text<V extends TextValue = string>(): Hole<V, "text"> {
  return (-1 - TEXT) as Hole<V, "text">;
}
/**
 * A class hole: the value of a `class` attribute on an HTML element. An SVG
 * element's className is read-only, so `class` there is an attribute hole
 * (`attr()`); a class marker on one fails when the template compiles.
 */
export function cls<V extends ClassValue = string>(): Hole<V, "class"> {
  return (-1 - CLASS) as Hole<V, "class">;
}
/**
 * An attribute hole: the value of any attribute other than `class` (on HTML),
 * `on*` or `.name`. Narrow the type for enumerated attributes: `attr<"true" | "false">()`.
 */
export function attr<V extends AttrValue = string>(): Hole<V, "attr"> {
  return (-1 - ATTR) as Hole<V, "attr">;
}

// True only for a single type: not never, not a union. Markers whose type
// argument was left out get the whole constraint (a union), which this rejects.
type IsUnion<T, U = T> = T extends unknown ? ([U] extends [T] ? false : true) : never;
type One<T> = [T] extends [never] ? false : IsUnion<T> extends false ? true : false;
type IfEquals<X, Y, A, B> = (<T>() => T extends X ? 1 : 2) extends <T>() => T extends Y ? 1 : 2 ? A : B;

/** The writable, non-function, non-handler property names of element type E. */
export type PropName<E extends Element> = {
  [P in keyof E]-?: P extends `on${string}`
    ? never
    : P extends string
      ? E[P] extends (...args: never) => unknown
        ? never
        : IfEquals<Pick<E, P>, { -readonly [Q in P]: E[Q] }, P, never>
      : never;
}[keyof E];

/** The value a property hole for property P of element type E takes; never unless both are single types. */
export type PropValue<E extends Element, P extends PropName<E>> = One<P> extends true ? E[P] : never;

/**
 * A property hole: the value of a `.name` property, typed by element and
 * property: `.value=${prop<HTMLInputElement, "value">()}` takes a string.
 * Only writable, non-function, non-`on*` properties qualify; both type
 * arguments are required (left out, the hole takes nothing). For a property
 * the DOM types do not declare (a custom element's), describe the element:
 * `prop<HTMLElement & { items: Item[] }, "items">()`. Handlers use `on`.
 */
export function prop<E extends Element, P extends PropName<E>>(): Hole<PropValue<E, P>, "prop"> {
  return (-1 - PROP) as Hole<PropValue<E, P>, "prop">;
}

/** The event names element type E has an `on*` handler property for: "click", "input", ... */
export type EventName<E extends Element> = {
  [P in keyof E]-?: P extends `on${infer N}`
    ? NonNullable<E[P]> extends (...args: never) => unknown
      ? N
      : never
    : never;
}[keyof E];

/** The event type E's `on${K}` handler receives: MouseEvent for "click" on an HTMLElement, ... */
export type EventOf<E extends Element, K extends EventName<E>> =
  NonNullable<E[`on${K}` & keyof E]> extends (this: never, ev: infer V) => unknown ? V : never;

/** The value of an event hole: a handler called with the element as `this`, or null for none. */
export type Handler<E extends Element, K extends EventName<E>> =
  One<K> extends true ? ((this: E, ev: EventOf<E, K>) => void) | null : never;

/**
 * An event hole: the value of an `on*` attribute (`onclick=${on<"click">()}`),
 * the only marker an `on*` name accepts. Its value is a handler for event K
 * of element type E (HTMLElement by default), called with the element as
 * `this`, or null. K is required (left out, the hole takes nothing). The
 * event name in the markup is not checked against K: TypeScript sees a
 * template's markup only as plain strings.
 */
export function on<K extends EventName<E>, E extends Element = HTMLElement>(): Hole<Handler<E, K>, "event"> {
  return (-1 - EVENT) as Hole<Handler<E, K>, "event">;
}

/**
 * A slot hole: element content the template never writes. It sits where a
 * text hole would and holds an empty text node, which a layer above (see
 * herdsman-blocks-view.ts) uses as an anchor to place content before.
 * Mounting leaves it empty; Block.set records its value and writes nothing.
 */
export function slot<V = unknown>(): Hole<V, "slot"> {
  return (-1 - SLOT) as Hole<V, "slot">;
}

/** The value tuple of a template whose holes are `H`; an untyped hole (a plain number) accepts unknown. */
export type ValuesOf<H extends readonly number[]> = {
  readonly [I in keyof H]: H[I] extends Hole<infer V, MarkerKind> ? V : unknown;
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
const BLOCK: unique symbol = Symbol("herdsman-blocks.block");

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
 * Holes are positional: the Nth `${}` is hole N. Use `text`, `cls`, `attr`,
 * `prop` and `on` markers to type each hole; a marker in the wrong position
 * throws when the template is first compiled. A plain number (`${0}`) is an
 * untyped hole.
 *
 * Attribute rules: `class` sets className (on an SVG element it is an
 * attribute), `on*` and `.name` set a property, anything else uses
 * setAttribute (null, undefined or false removes it).
 */
export function html<const H extends readonly number[]>(
  strings: TemplateStringsArray,
  ...holes: H
): Template<ValuesOf<H>> {
  let t = cache.get(strings);
  if (!t) cache.set(strings, (t = new Template(strings, holes)));
  return t as Template<ValuesOf<H>>;
}

/**
 * Like `html`, for SVG content: the markup is parsed inside an `<svg>`
 * element, so a template whose root is `<g>`, `<path>` or `<circle>` gets the
 * SVG namespace. `class` is an attribute hole here (`attr()`).
 */
export function svg<const H extends readonly number[]>(
  strings: TemplateStringsArray,
  ...holes: H
): Template<ValuesOf<H>> {
  let t = cache.get(strings);
  if (!t) cache.set(strings, (t = new Template(strings, holes, true)));
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
  private readonly paths: readonly Path[];

  constructor(strings: readonly string[], holes: readonly number[] = [], svg = false) {
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
        if (!m) throw new Error(`herdsman-blocks: hole ${i} is inside a tag but not an attribute value`);
        src += s.slice(0, m.index) + ` bk${i}="${encodeURIComponent(m[1]!)}"`;
        if (m[2]) parts[i + 1] = parts[i + 1]!.replace(m[2], ""); // drop the closing quote
      } else {
        src += s + `<!--bk${i}-->`;
      }
    }

    const tpl = document.createElement("template");
    tpl.innerHTML = svg ? "<svg>" + src.trim() + "</svg>" : src.trim();
    const root = svg ? tpl.content.firstChild!.firstChild : tpl.content.firstChild;
    if (!root || root.nextSibling || root.nodeType !== 1) {
      throw new Error("herdsman-blocks: a template must have exactly one root element");
    }

    this.n = n;
    this.kinds = new Uint8Array(n);
    const names: (string | undefined)[] = new Array(n);
    const paths: (Path | undefined)[] = new Array(n);
    walk(root, [], paths, this.kinds, names);
    for (let i = 0; i < n; i++) {
      if (!paths[i]) throw new Error(`herdsman-blocks: hole ${i} could not be located`);
      // A typed marker must sit where its kind says. Checked once per template.
      const h = holes[i];
      if (h === undefined || h >= 0) continue;
      const k = this.kinds[i]!,
        at = k === PROP && names[i]!.startsWith("on") ? EVENT : k;
      if (h === -1 - SLOT && at === TEXT) this.kinds[i] = SLOT;
      else if (-1 - h !== at) {
        const want = MARKER_NAMES[-1 - h] ?? "unknown",
          got = MARKER_NAMES[at] ?? "unknown",
          hint =
            at === EVENT
              ? ": an on* name takes on()"
              : at === ATTR && names[i] === "class"
                ? ": class on an SVG element is an attribute, use attr()"
                : "";
        throw new Error(`herdsman-blocks: hole ${i} is typed ${want}, but its position makes it ${got}${hint}`);
      }
    }
    this.names = names;
    this.root = root as Element;
    this.paths = paths as Path[];
    this.locate = compileLocator(this.paths);
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
    const root = this.clone(vals);
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
    const root = this.clone(vals);
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

  /**
   * Bake `vals` into the prototype and deep-clone it. The clone holds the
   * text, class and attribute values; property and slot holes are left for
   * the caller (`mount` sets properties). For layers that keep their own
   * per-instance state; most code wants `mount` or `mountRoot`.
   */
  clone(vals: V): Element {
    const n = this.n,
      kinds = this.kinds,
      names = this.names,
      proto = this.proto;
    for (let i = 0; i < n; i++) {
      const v = vals[i];
      if (kinds[i] !== PROP && kinds[i] !== SLOT && proto[n + i] !== v) {
        proto[n + i] = v;
        apply(kinds[i] as KindCode, names[i], proto[i] as Node, v);
      }
    }
    return this.root.cloneNode(true) as Element;
  }

  /**
   * A locator like `locate` that finds only the given holes: it writes
   * out[i] for each i in `holes` and leaves the other entries alone, so a
   * layer can look up the nodes it needs at mount and the rest later.
   */
  locator(holes: readonly number[]): (root: Node, out: unknown[]) => void {
    const sub: (Path | undefined)[] = new Array(this.n);
    for (const i of holes) sub[i] = this.paths[i];
    return compileLocator(sub);
  }

  /** The kind of hole i, from its position (and its marker, for a slot). */
  kind(i: number): HoleKind {
    return KIND_NAMES[this.kinds[i]!]!;
  }

  /** Write value `v` into `node`, hole i's node under a clone (see `locate`), by the hole's kind. */
  write(i: number, node: Node, v: unknown): void {
    apply(this.kinds[i] as KindCode, this.names[i], node, v);
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
    // className is read-only on SVG elements: there `class` is an attribute.
    if (name === "class" && el.namespaceURI !== SVG_NS) kinds[i] = CLASS;
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
function compileLocator(paths: readonly (Path | undefined)[]): Locator {
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
      if (p === undefined) return;
      const r = ref(p); // must run before reading `body`: ref() appends declarations
      body += "n[" + i + "]=" + r + ";";
    });
    return new Function("r", "n", body) as Locator;
  } catch {
    return (r, out) => {
      paths.forEach((p, i) => {
        if (p === undefined) return;
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
    // SLOT: never written.
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
    const len = ne - s + 1;
    // Scratch buffers for this nesting depth: callbacks below (update, create)
    // may run a nested reconcile, which must not overwrite ours.
    const sc = scratchAt(depth++, len);
    try {
      this.window(items, newK, newE, s, oe, ne, len, sc);
    } finally {
      depth--;
    }
  }

  private window(
    items: readonly T[],
    newK: readonly K[],
    newE: E[],
    s: number,
    oe: number,
    ne: number,
    len: number,
    sc: Scratch,
  ): void {
    const p = this.parent,
      a = this.anchor,
      upd = this.update,
      mk = this.create,
      r = this.r;
    const oldK = this.keys,
      oldE = this.entries;
    const n = newK.length,
      m = oldK.length;
    const src = sc.src; // new position -> old index, -1 = new item
    src.fill(-1, 0, len);
    let moved = false,
      last = -1,
      kept = 0;

    // Key -> new position. Small windows: a linear scan. Dense integer keys:
    // a direct-address table (no hashing, no clearing). Anything else: a Map.
    let mode = SCAN,
      base = 0,
      idx: Map<K, number> | null = null;
    if (len > 16) {
      base = intRange(newK, s, ne, len);
      if (base >= 0) {
        mode = TABLE;
        const g = ++lutGen;
        for (let i = s; i <= ne; i++) {
          const o = (newK[i] as number) - base;
          lutPos[o] = i;
          lutStamp[o] = g;
        }
      } else {
        mode = MAP;
        idx = new Map();
        for (let i = s; i <= ne; i++) idx.set(newK[i]!, i);
      }
    }
    const g = lutGen;
    const find = (k: K): number => {
      if (mode === TABLE) {
        const o = (k as number) - base;
        return typeof k === "number" && o >= 0 && o < lutPos.length && lutStamp[o] === g ? lutPos[o]! : -1;
      }
      if (mode === MAP) return idx!.get(k) ?? -1;
      for (let j = s; j <= ne; j++) if (newK[j] === k) return j;
      return -1;
    };

    for (let i = s; i <= oe; i++) {
      const j = find(oldK[i]!);
      if (j < 0) continue;
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
      for (let i = s; i <= oe; i++) if (find(oldK[i]!) < 0) p.removeChild(r(oldE[i]!));
    }
    for (let j = 0; j < len; j++) if (src[j] !== -1) upd(newE[j + s]!, items[j + s]!);

    // Walk right to left. Survivors on the LIS stay put; everything else is
    // inserted before its right neighbour, which is already in final position.
    const seq = moved ? lisInto(src, len, sc) : null;
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

// Key lookup modes for the general case.
const SCAN = 0;
const TABLE = 1;
const MAP = 2;

// Direct-address table shared by every list: key - base -> new position, valid
// only where its stamp equals the current generation, so it is never cleared.
// Sized up to TABLE_MAX entries (two 4-byte arrays: 32 MB at the cap).
const TABLE_MAX = 1 << 22;
let lutPos = new Int32Array(0);
let lutStamp = new Uint32Array(0);
let lutGen = 0;

// If keys[from..to] are all non-negative integers in a range no wider than
// 8 times the window (and within TABLE_MAX), size the table and return the
// smallest key; otherwise return -1.
function intRange(keys: readonly Key[], from: number, to: number, len: number): number {
  let lo = Infinity,
    hi = -1;
  for (let i = from; i <= to; i++) {
    const k = keys[i];
    if (typeof k !== "number" || k < 0 || (k | 0) !== k) return -1;
    if (k < lo) lo = k;
    if (k > hi) hi = k;
  }
  const span = hi - lo + 1;
  if (span > 8 * len + 64 || span > TABLE_MAX) return -1;
  if (lutPos.length < span) {
    const size = Math.min(TABLE_MAX, Math.max(span, 2 * lutPos.length));
    lutPos = new Int32Array(size);
    lutStamp = new Uint32Array(size);
    lutGen = 0;
  }
  if (lutGen === 0xffffffff) {
    lutStamp.fill(0);
    lutGen = 0;
  }
  return lo;
}

// Per-nesting-depth scratch: the source array and the LIS work arrays, grown
// on demand and reused, so a reconcile allocates no typed arrays.
interface Scratch {
  src: Int32Array;
  prev: Int32Array;
  tails: Int32Array;
}
const scratch: Scratch[] = [];
let depth = 0;

function scratchAt(d: number, len: number): Scratch {
  let sc = scratch[d];
  if (sc === undefined) scratch[d] = sc = { src: new Int32Array(0), prev: new Int32Array(0), tails: new Int32Array(0) };
  if (sc.src.length < len) {
    const size = Math.max(len, 2 * sc.src.length, 64);
    sc.src = new Int32Array(size);
    sc.prev = new Int32Array(size);
    sc.tails = new Int32Array(size);
  }
  return sc;
}

/**
 * Longest strictly increasing subsequence of `a`, ignoring -1 entries.
 * Returns ascending positions into `a`. O(n log n) patience sorting.
 */
export function lis(a: ArrayLike<number>): number[] {
  const n = a.length;
  return lisWith(a, n, new Int32Array(n), new Int32Array(n));
}

function lisInto(a: Int32Array, n: number, sc: Scratch): number[] {
  return lisWith(a, n, sc.prev, sc.tails);
}

function lisWith(a: ArrayLike<number>, n: number, prev: Int32Array, tails: Int32Array): number[] {
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
  const old: Node[] = [];
  for (let c = live.firstChild; c; c = c.nextSibling) old.push(c);
  const m = old.length,
    n = next.length;

  // Children already identical at either end (one native deep compare each)
  // are kept as they are: no diff below them, only handlers and form state
  // carried over. An unkeyed prepend then costs one insert, not a rewrite of
  // every sibling that would otherwise shift its order-based match.
  let s = 0;
  while (s < m && s < n && same(old[s]!, next[s]!)) (keep(old[s]!, next[s]!, o), s++);
  let oe = m - 1,
    ne = n - 1;
  while (oe >= s && ne >= s && same(old[oe]!, next[ne]!)) (keep(old[oe]!, next[ne]!, o), oe--, ne--);
  if (s > oe && s > ne) return;

  const oldWin = s === 0 && oe === m - 1 ? old : old.slice(s, oe + 1);
  const newWin = s === 0 && ne === n - 1 ? next : next.slice(s, ne + 1);
  const [oldKeys, newKeys] = windowKeys(oldWin, newWin);
  const keyOf = new Map<Node, string>();
  for (let i = 0; i < newWin.length; i++) keyOf.set(newWin[i]!, newKeys[i]!);

  // The differing middle, reconciled as a keyed list.
  const whole = s === 0 && oe === m - 1;
  const list = new List<Node, Node, string>(live, {
    owns: whole,
    anchor: whole ? null : (old[oe + 1] ?? null),
    key: (fresh) => keyOf.get(fresh)!,
    root: nodeRoot,
    create: nodeRoot,
    update: (liveNode, fresh) => morphNode(liveNode, fresh, o),
  });
  list.keys = oldKeys;
  list.entries = oldWin.slice();
  list.set(newWin);
}

// Keys for the old and new window, so the keyed reconciler can match them:
//  - an element with data-key: tag and key (a repeated key falls back to order);
//  - an unkeyed node whose content (tag and text) appears on both sides: that
//    content, so a moved row keeps its node and costs one move;
//  - any other unkeyed node: tag and order among those leftovers, so a row
//    whose text changed is patched in place rather than replaced.
function windowKeys(a: readonly Node[], b: readonly Node[]): [string[], string[]] {
  const content = (n: Node): string | null =>
    n.nodeType === 1 && (n as Element).hasAttribute(KEY_ATTR)
      ? null
      : n.nodeName + "\u0001" + (n.nodeType === 1 ? n.textContent : n.nodeValue);
  const ca = a.map(content),
    cb = b.map(content);
  const countA = new Map<string, number>(),
    countB = new Map<string, number>();
  for (const c of ca) if (c !== null) countA.set(c, (countA.get(c) ?? 0) + 1);
  for (const c of cb) if (c !== null) countB.set(c, (countB.get(c) ?? 0) + 1);
  const keys = (side: readonly Node[], cs: readonly (string | null)[]): string[] => {
    const seen = new Map<string, number>();
    const take = (base: string): number => {
      const j = seen.get(base) ?? 0;
      seen.set(base, j + 1);
      return j;
    };
    return side.map((node, i) => {
      const c = cs[i];
      if (c === null || c === undefined) {
        const base = "k:" + node.nodeName + ":" + (node as Element).getAttribute(KEY_ATTR);
        const j = take(base);
        return j === 0 ? base : base + "#" + j;
      }
      const shared = Math.min(countA.get(c) ?? 0, countB.get(c) ?? 0);
      const j = take("c:" + c);
      if (j < shared) return "c:" + c + "#" + j;
      return "#" + node.nodeName + "#" + take("#" + node.nodeName);
    });
  };
  return [keys(a, ca), keys(b, cb)];
}

const same = (a: Node, b: Node): boolean => a === b || a.isEqualNode(b);

// A kept identical subtree: give each element the fresh partner's handlers
// (onKeep) and form state, which a structural compare cannot see.
function keep(a: Node, b: Node, o: MorphOptions): void {
  if (a === b || a.nodeType !== 1) return;
  const el = a as Element,
    fresh = b as Element;
  o.onKeep?.(el, fresh);
  if (!el.hasAttribute(SKIP_ATTR)) {
    for (let x = el.firstChild, y = fresh.firstChild; x !== null && y !== null; x = x.nextSibling, y = y.nextSibling)
      keep(x, y, o);
  }
  syncFormState(el, fresh);
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
