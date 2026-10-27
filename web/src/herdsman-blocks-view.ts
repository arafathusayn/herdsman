// herdsman-blocks-view: a declarative layer over herdsman-blocks.
//
// A view is a template you call with its values; the call returns a light
// description. `render` mounts a description the first time and afterwards
// writes only the holes whose values changed (strict equality, as Block.set),
// so a view function can rebuild its descriptions on every render:
//
//   const Card = html`<button class=${cls()} onclick=${on<"click">()}>${text()}${child()}</button>`;
//   const Task = html`<div class="task">${text()}</div>`;
//   render(root, Card(["card", () => open(m.pane), m.title, m.task !== null ? Task([m.task]) : null]));
//
// A `child()` hole takes text, nothing, a DOM node, another view's
// description, or a keyed `list(items, key, render)`; `child<C>()` narrows
// that to C. The same template patches in place; a different template or kind
// replaces the content. `svg` makes views of SVG content.
//
// Event handlers (`on()` holes) are stored indirectly. Bubbling events are
// delegated: each render() root listens once per event type and calls the
// handler the last render passed, as the element. Other events get one shared
// trampoline per element. A new closure per render costs no DOM write.
//
// Every type below is erased: markers are constants evaluated once per
// template, and descriptions carry their value types only at compile time.

import {
  type AnyHole,
  type Hole,
  html as blocks,
  type Key,
  List,
  slot,
  svg as svgBlocks,
  type Template,
  type ValuesOf,
} from "./herdsman-blocks.ts";

export {
  attr,
  cls,
  type EventName,
  type EventOf,
  type Handler,
  type Key,
  on,
  prop,
  type PropName,
  type PropValue,
  text,
} from "./herdsman-blocks.ts";

declare const VIEW_NODE: unique symbol;
declare const LIST_NODE: unique symbol;
declare const RUNS_NODE: unique symbol;
declare const ANY_VALUES: unique symbol;

/** Stands for "any value tuple" in `ViewNode`; never a real value tuple. */
export type AnyValues = typeof ANY_VALUES;

/** The markup a view's elements belong to: HTML, or SVG (made by `svg`). */
export type Namespace = "html" | "svg";

/**
 * A view description: a template's values. Make one by calling a view.
 * `ViewNode<V>` is a description of a view whose value tuple is exactly V
 * (invariant: a narrower or wider tuple is a different type); `ViewNode`
 * alone is any description. NS says whether the view's elements are HTML or SVG.
 */
export type ViewNode<V extends readonly unknown[] | AnyValues = AnyValues, NS extends Namespace = Namespace> = [
  V,
] extends [AnyValues]
  ? AnyViewNode<NS>
  : ExactViewNode<Exclude<V, AnyValues>, NS>;

/** Any description in namespace NS: what `ViewNode` without a value tuple means. */
export interface AnyViewNode<NS extends Namespace = Namespace> {
  readonly [VIEW_NODE]: readonly [unknown, NS];
}

/** A description of a view whose value tuple is exactly V (V appears in and out: invariant). */
export interface ExactViewNode<V extends readonly unknown[], NS extends Namespace = Namespace> {
  readonly [VIEW_NODE]: readonly [(v: V) => V, NS];
}

/** Any description of an SVG view. */
export type SvgNode = ViewNode<AnyValues, "svg">;

/** A keyed list description, made by `list`: its items render to N and are keyed by K. */
export interface ListNode<N extends ViewNode = ViewNode, K extends Key = Key> {
  readonly [LIST_NODE]: readonly [N, K];
}

/** A positional sequence of text and descriptions, made by `runs`: its views are N. */
export interface RunsNode<N extends ViewNode = ViewNode> {
  readonly [RUNS_NODE]: readonly [N];
}

/** What a child hole accepts. null, undefined and false render nothing; a Node is inserted as is. */
export type Child = string | number | null | undefined | false | Node | ViewNode | ListNode | RunsNode;

/** What a child hole inside SVG content should take: no HTML views. */
export type SvgChild =
  string | number | null | undefined | false | SVGElement | SvgNode | ListNode<SvgNode> | RunsNode<SvgNode>;

/** A view: call it with its values tuple to describe one instance. */
export interface View<V extends readonly unknown[], NS extends Namespace = "html"> {
  (vals: V): ExactViewNode<V, NS>;
  /** The compiled template behind the view. */
  readonly template: Template<V>;
}

/** The description type a view returns: `ViewNodeOf<typeof Row>`. */
export type ViewNodeOf<T> = T extends View<infer V, infer NS> ? ExactViewNode<V, NS> : never;

/**
 * A child hole: element content that takes any `Child`, or only C:
 * `child<string>()`, `child<ListNode<ViewNodeOf<typeof Row>>>()`,
 * `child<ViewNodeOf<typeof Task> | null>()`.
 */
export function child<C extends Child = Child>(): Hole<C, "slot"> {
  return slot<C>();
}

/**
 * Tagged template for a view; compiles once per call site, like herdsman-blocks'
 * `html`, and takes the same hole markers plus `child()`. Every hole needs a
 * typed marker: a plain number is not accepted here.
 */
export function html<const H extends readonly AnyHole[]>(
  strings: TemplateStringsArray,
  ...holes: H
): View<ValuesOf<H>, "html"> {
  let v = views.get(strings);
  if (v === undefined) views.set(strings, (v = makeView(blocks(strings, ...holes) as unknown as AnyTemplate)));
  return v as unknown as View<ValuesOf<H>, "html">;
}

/**
 * Like `html`, for SVG content: the markup is parsed inside an `<svg>`, so a
 * root `<g>`, `<path>` or `<circle>` gets the SVG namespace. Its descriptions
 * go into child holes inside SVG content. `class` takes `attr()` here.
 */
export function svg<const H extends readonly AnyHole[]>(
  strings: TemplateStringsArray,
  ...holes: H
): View<ValuesOf<H>, "svg"> {
  let v = views.get(strings);
  if (v === undefined) views.set(strings, (v = makeView(svgBlocks(strings, ...holes) as unknown as AnyTemplate)));
  return v as unknown as View<ValuesOf<H>, "svg">;
}

/**
 * A keyed list for a child hole: `render(item)` describes each item. Keys come
 * from the item (never its index) and must be unique among siblings. Items are
 * reconciled with herdsman-blocks' List: kept items are patched, and moved
 * with the minimum number of DOM moves. T comes from `items` only.
 */
export function list<T, K extends Key, N extends ViewNode>(
  items: readonly T[],
  key: (item: NoInfer<T>) => K,
  render: (item: NoInfer<T>) => N,
): ListNode<N, K> {
  return new ListOf(
    items as readonly unknown[],
    key as (item: unknown) => Key,
    render as (item: unknown) => ViewNode,
  ) as unknown as ListNode<N, K>;
}

/**
 * Unkeyed inline content for a child hole: text runs and descriptions side by
 * side, with no wrapper element, such as highlighted text between `<mark>`
 * elements or inline markdown. Runs are matched by position: text stays the
 * same text node with its data updated, a description patches the view at its
 * position (or replaces it, for another view), and anything else is replaced.
 * Nothing is keyed, so use `list` for items with identity.
 */
export function runs<N extends ViewNode>(items: readonly (string | number | N)[]): RunsNode<N> {
  return new RunsOf(items) as unknown as RunsNode<N>;
}

/**
 * Render a description into `container`. The first render replaces the
 * container's content; later ones patch it (a different view replaces it).
 */
export function render(container: Node, d: ViewNode): void {
  const c = container as Container;
  draw(c, d as unknown as Desc, false);
  // After drawing: a template first compiled while drawing (inside a list
  // callback, say) may have brought a new event type.
  if (c[LISTENING] !== used.length) listen(c);
}

/**
 * Like `render`, but every handler gets a direct trampoline on its element
 * and the container listens for nothing, so the rendered nodes keep their
 * handlers wherever they are moved. For code that moves a view's nodes out
 * of its container (a bridge into a tree another library manages); `render`
 * is faster. Use one of the two per container.
 */
export function renderDirect(container: Node, d: ViewNode): void {
  draw(container as Container, d as unknown as Desc, true);
}

/**
 * Create a view's root element, for a component that places its root itself
 * (no container). The root is its own render root: it listens for delegated
 * events, its own handlers included, wherever it is put. Update it with
 * `update`. Any view works, child holes included.
 */
export function mount(d: ViewNode): Element {
  const outer = direct;
  direct = false;
  try {
    const inst = make(d as unknown as Desc);
    const root = inst.root as Element & Container;
    root[SELF] = inst;
    listen(root);
    return root;
  } finally {
    direct = outer;
  }
}

/**
 * Patch a root made by `mount` toward a description of the same view. A
 * different view would need a different root element, which the caller
 * holds, so it throws: mount the other view instead.
 */
export function update(root: Element, d: ViewNode): void {
  const r = root as Element & Container,
    inst = r[SELF],
    desc = d as unknown as Desc;
  if (inst === undefined) throw new Error("herdsman-blocks-view: update() takes a root made by mount()");
  if (desc.s !== inst.shape)
    throw new Error("herdsman-blocks-view: update() with a different view; mount() it instead");
  const outer = direct;
  direct = false;
  try {
    patch(inst, desc);
  } finally {
    direct = outer;
  }
  // After patching, as in render(): the patch may have compiled a template
  // with a new event type.
  if (r[LISTENING] !== used.length) listen(r);
}

function draw(c: Container, d: Desc, dir: boolean): void {
  const outer = direct;
  direct = dir;
  try {
    const inst = c[ROOT];
    if (inst !== undefined) return patch(inst, d);
    const fresh = make(d);
    c.textContent = "";
    c.appendChild(fresh.root);
    c[ROOT] = fresh;
  } finally {
    direct = outer;
  }
}

// Whether the render running now mounts handlers directly (renderDirect).
let direct = false;

// ---------------------------------------------------------------------------
// Internals
// ---------------------------------------------------------------------------

type AnyTemplate = Template<readonly unknown[]>;
type Values = readonly unknown[];

const ROOT: unique symbol = Symbol("herdsman-blocks-view.root");
// The root element of an instance with handler holes holds the instance here.
const STATE: unique symbol = Symbol("herdsman-blocks-view.state");
type Holder = Node & { [STATE]?: Inst };

// A render root: its instance, and how many of `used` it listens for.
const LISTENING: unique symbol = Symbol("herdsman-blocks-view.listening");
// A root made by mount() holds its own instance here.
const SELF: unique symbol = Symbol("herdsman-blocks-view.self");
type Container = Node & { [ROOT]?: Inst; [LISTENING]?: number; [SELF]?: Inst };

const views = new WeakMap<TemplateStringsArray, View<Values>>();

// How each hole is written. WRITE holes go through Template.write, lazily.
const WRITE = 0;
const HANDLER = 1;
const PROPERTY = 2;
const CHILD = 3;
const DELEGATED = 4;

// Bubbling events whose handlers are delegated to the render root. Others
// (focus, blur, mouseenter, load, scroll, ...) and passive-sensitive ones
// (touch, wheel) get a direct trampoline on the element.
const BUBBLING = new Set([
  "auxclick",
  "beforeinput",
  "change",
  "click",
  "compositionend",
  "compositionstart",
  "compositionupdate",
  "contextmenu",
  "dblclick",
  "focusin",
  "focusout",
  "input",
  "keydown",
  "keyup",
  "mousedown",
  "mousemove",
  "mouseout",
  "mouseover",
  "mouseup",
  "pointerdown",
  "pointermove",
  "pointerout",
  "pointerover",
  "pointerup",
  "submit",
]);
// Every delegated event type any view uses; render roots listen for these.
const used: string[] = [];

/** What a view knows about its template, computed once per template. */
class Shape {
  readonly t: AnyTemplate;
  readonly n: number;
  readonly flags: Uint8Array;
  /** Whether mount must locate holes: any handler, property or child hole. */
  readonly eager: boolean;
  /** Per handler hole, the trampoline a directly mounted element gets. */
  readonly fns: (((this: Holder, e: Event) => unknown) | undefined)[];
  /** Whether any hole is a direct handler: the instance is then kept on its root for the trampolines. */
  readonly handlers: boolean;
  /** Whether any hole is a delegated handler. */
  readonly delegates: boolean;
  /** Per delegated handler hole, its event type. */
  readonly types: (string | undefined)[];
  /** Finds the delegated handler hole nodes, on the first delegated event. */
  readonly findDelegated: (root: Node, out: unknown[]) => void;
  /** Finds the handler, property and child hole nodes, for mount. */
  readonly findEager: (root: Node, out: unknown[]) => void;
  /** Finds the plain write hole nodes, on their first update. */
  readonly findWrite: (root: Node, out: unknown[]) => void;

  constructor(t: AnyTemplate) {
    const n = t.n;
    this.t = t;
    this.n = n;
    this.flags = new Uint8Array(n);
    this.fns = new Array(n);
    this.types = new Array(n);
    let handlers = false;
    const eager: number[] = [],
      write: number[] = [],
      delegated: number[] = [];
    for (let i = 0; i < n; i++) {
      const k = t.kind(i),
        name = t.names[i];
      if (k === "slot") this.flags[i] = CHILD;
      else if (k === "prop" && name!.startsWith("on")) {
        const type = name!.slice(2);
        if (BUBBLING.has(type)) {
          this.flags[i] = DELEGATED;
          this.types[i] = type;
          if (!used.includes(type)) used.push(type);
          this.fns[i] = trampoline(i); // used when rendered with renderDirect
          delegated.push(i);
          continue;
        }
        this.flags[i] = HANDLER;
        this.fns[i] = trampoline(i);
        handlers = true;
      } else if (k === "prop") this.flags[i] = PROPERTY;
      else {
        write.push(i);
        continue;
      }
      eager.push(i);
    }
    this.eager = eager.length > 0;
    this.handlers = handlers;
    this.delegates = delegated.length > 0;
    // Hole paths hold only in the pristine clone: once a child hole puts
    // content before its anchor, a path through that parent's children points
    // at the wrong node. A view with child holes therefore locates every hole
    // at mount, before any child content goes in; without child holes the
    // clone's structure never changes, and the rest can wait.
    this.findEager = eager.length === n || this.flags.includes(CHILD) ? t.locate : t.locator(eager);
    this.findWrite = write.length === n ? t.locate : t.locator(write);
    this.findDelegated = delegated.length === n ? t.locate : t.locator(delegated);
  }
}

// Set a direct trampoline: through the element's on* property when it has
// one, otherwise (compositionstart, ...) as a listener. Either way it is set
// once per element and never changes.
function attach(node: Node, name: string, fn: (this: Holder, e: Event) => unknown): void {
  if (name in node) (node as unknown as Record<string, unknown>)[name] = fn;
  else node.addEventListener(name.slice(2), fn as EventListener);
}

// Calls the handler hole i of the instance the element belongs to, as the
// element. The instance is kept on its root only: the walk up from the
// element passes only the template's own nodes before reaching it.
function trampoline(i: number): (this: Holder, e: Event) => unknown {
  return function (this: Holder, e: Event): unknown {
    let h: Holder = this;
    while (h[STATE] === undefined) h = h.parentNode!;
    const f = h[STATE].v[i];
    return typeof f === "function" ? (f as (this: Holder, e: Event) => unknown).call(this, e) : undefined;
  };
}

// Listen on a render root for every delegated event type not yet listened for.
function listen(c: Container): void {
  for (let k = c[LISTENING] ?? 0; k < used.length; k++) c.addEventListener(used[k]!, delegate);
  c[LISTENING] = used.length;
}

// Scratch for one delegated dispatch: handler elements, instances and holes,
// innermost first. Dispatches can nest (a handler can dispatch an event), so
// each takes its own arrays only when it finds something.
function delegate(this: Container, e: Event): void {
  const self = this[SELF],
    content = this[ROOT];
  if (self === undefined && content === undefined) return;
  // The event's path from its target up to the render root (included when it
  // is itself an instance root, made by mount()).
  const path: Node[] = [];
  for (let node = e.target as Node | null; node !== null && node !== this; node = node.parentNode) path.push(node);
  if (self !== undefined) path.push(this);
  // The render root's own instance, then what render() put inside it.
  const chain: Inst[] = [],
    tops: number[] = [];
  let k = path.length - 1;
  if (self !== undefined) {
    chain.push(self);
    tops.push(k--);
  }
  if (content !== undefined) {
    if (k < 0 || path[k] !== content.root) return dispatch(e, path, chain, tops);
    chain.push(content);
    tops.push(k);
  } else k++;
  if (chain.length === 0) return;
  // Descend along the path: a node that is the root of a child instance (a
  // view, list entry or run in one of the current instance's child holes)
  // enters it. Rows need no expando to be found.
  let inst = chain[chain.length - 1]!;
  while (--k >= 0) {
    const sub = childAt(inst, path[k]!);
    if (sub !== null) {
      chain.push((inst = sub));
      tops.push(k);
    }
  }
  dispatch(e, path, chain, tops);
}

// Run the delegated handlers for `e` of the instances in `chain` (outermost
// first; tops[c] is the path index of chain[c]'s root), innermost first.
function dispatch(e: Event, path: readonly Node[], chain: readonly Inst[], tops: readonly number[]): void {
  const type = e.type;
  let els: Node[] | null = null,
    insts: Inst[] | null = null,
    holes: number[] | null = null,
    pos: number[] | null = null;
  for (let c = 0; c < chain.length; c++) {
    const it = chain[c]!,
      sh = it.shape,
      types = sh.types;
    if (it.d) continue; // mounted by renderDirect: its own trampolines run
    for (let i = 0; i < sh.n; i++) {
      if (types[i] !== type) continue;
      let s = it.s;
      if (s === null) s = it.s = new Array(sh.n);
      if (s[i] === undefined) sh.findDelegated(it.root, s);
      const at = path.lastIndexOf(s[i] as Node, tops[c]);
      if (at < 0) continue; // the handler element is not on the event's path
      ((els ??= []), (insts ??= []), (holes ??= []), (pos ??= []));
      // Keep innermost first: insert by path position.
      let j = pos.length;
      while (j > 0 && pos[j - 1]! > at) j--;
      els.splice(j, 0, s[i] as Node);
      insts.splice(j, 0, it);
      holes.splice(j, 0, i);
      pos.splice(j, 0, at);
    }
  }
  if (els === null) return;
  for (let q = 0; q < els.length; q++) {
    if (q > 0 && els[q] !== els[q - 1] && e.cancelBubble) break; // stopPropagation: no outer handlers
    const f = insts![q]!.v[holes![q]!];
    if (typeof f !== "function") continue;
    const el = els[q]!;
    Object.defineProperty(e, "currentTarget", { configurable: true, get: () => el });
    try {
      (f as (this: Node, e: Event) => unknown).call(el, e);
    } finally {
      delete (e as { currentTarget?: unknown }).currentTarget;
    }
  }
}

// The child instance of `inst` whose root is `node`, if any: a view, list
// entry or run in one of its child holes whose parent is node's parent.
function childAt(inst: Inst, node: Node): Inst | null {
  const s = inst.s,
    sh = inst.shape;
  if (s === null) return null;
  const parent = node.parentNode;
  for (let i = 0; i < sh.n; i++) {
    if (sh.flags[i] !== CHILD) continue;
    const sl = s[i] as Slot;
    if (sl.parent !== parent) continue;
    switch (sl.kind) {
      case VIEW:
        if (sl.inst!.root === node) return sl.inst;
        break;
      case LIST: {
        const es = sl.list!.entries;
        for (let j = 0; j < es.length; j++) if (es[j]!.root === node) return es[j]!;
        break;
      }
      case RUNS:
        for (const r of sl.runs!) if (r instanceof Inst && r.root === node) return r;
        break;
    }
  }
  return null;
}

function makeView(t: AnyTemplate): View<Values> {
  const shape = new Shape(t);
  const v = ((vals: Values) => new Desc(shape, vals)) as unknown as {
    (vals: Values): ExactViewNode<Values, "html">;
    template: AnyTemplate;
  };
  v.template = t;
  return v;
}

class Desc {
  readonly s: Shape;
  readonly v: Values;
  constructor(s: Shape, v: Values) {
    this.s = s;
    this.v = v;
  }
}

class RunsOf {
  readonly items: readonly unknown[];
  constructor(items: readonly unknown[]) {
    this.items = items;
  }
}

class ListOf {
  readonly items: readonly unknown[];
  readonly key: (item: unknown) => Key;
  readonly render: (item: unknown) => ViewNode;
  constructor(items: readonly unknown[], key: (item: unknown) => Key, render: (item: unknown) => ViewNode) {
    this.items = items;
    this.key = key;
    this.render = render;
  }
}

/**
 * A mounted view. `v` is the values array of the description last rendered
 * (descriptions are never mutated, so it is kept, not copied); `s` holds the
 * hole nodes (a Slot for child holes), or null until a hole is first written.
 * All fields change when a different view replaces this one in place, so
 * lists and parents that hold the instance need no update.
 */
class Inst {
  shape: Shape;
  root: Element;
  v: Values;
  s: unknown[] | null;
  /** Mounted by renderDirect: its handlers are on its elements, never delegated. */
  d: boolean;
  constructor(shape: Shape, root: Element, v: Values, s: unknown[] | null, d: boolean) {
    this.shape = shape;
    this.root = root;
    this.v = v;
    this.s = s;
    this.d = d;
  }
}

function make(d: Desc): Inst {
  const sh = d.s,
    vals = d.v,
    t = sh.t;
  const root = t.clone(vals);
  const dir = direct && sh.delegates;
  if (!sh.eager && !dir) {
    const inst = new Inst(sh, root, vals, null, direct);
    if (sh.handlers) (root as Holder)[STATE] = inst; // only direct handlers need it: delegation finds instances by descent
    return inst;
  }
  const n = sh.n,
    s: unknown[] = new Array(n);
  if (sh.eager) sh.findEager(root, s);
  if (dir) sh.findDelegated(root, s);
  const inst = new Inst(sh, root, vals, s, direct);
  if (sh.handlers || dir) (root as Holder)[STATE] = inst;
  const flags = sh.flags;
  for (let i = 0; i < n; i++) {
    const f = flags[i];
    if (f === WRITE || (f === DELEGATED && !dir)) continue;
    const node = s[i] as Node,
      v = vals[i];
    if (f === HANDLER || f === DELEGATED) {
      attach(node, t.names[i]!, sh.fns[i]!);
    } else if (f === PROPERTY) {
      if (v != null) t.write(i, node, v);
    } else {
      const sl = new Slot(node);
      s[i] = sl;
      setChild(sl, v);
    }
  }
  return inst;
}

// Bring a mounted instance to `d`: patch it, or replace it with a fresh mount
// when `d` is another view.
function patch(inst: Inst, d: Desc): void {
  const sh = d.s,
    vals = d.v;
  if (sh !== inst.shape) {
    const fresh = make(d);
    inst.root.parentNode!.replaceChild(fresh.root, inst.root);
    inst.shape = sh;
    inst.root = fresh.root;
    inst.v = vals;
    inst.s = fresh.s;
    inst.d = fresh.d;
    if (sh.handlers || (fresh.d && sh.delegates)) (fresh.root as Holder)[STATE] = inst;
    return;
  }
  const old = inst.v;
  if (old === vals) return; // the same description again: nothing can have changed
  inst.v = vals;
  const n = sh.n,
    flags = sh.flags;
  let s = inst.s;
  for (let i = 0; i < n; i++) {
    const v = vals[i];
    if (old[i] === v) continue;
    const f = flags[i];
    if (f === HANDLER || f === DELEGATED) continue; // handlers read inst.v when they run
    if (f === CHILD) setChild(s![i] as Slot, v);
    else {
      if (s === null)
        sh.findWrite(inst.root, (s = inst.s = new Array(n))); // first real update: find the holes now
      else if (s[i] === undefined) sh.findWrite(inst.root, s);
      sh.t.write(i, s[i] as Node, v);
    }
  }
}

// ---------------------------------------------------------------------------
// Child holes
// ---------------------------------------------------------------------------

// What a child hole holds.
const EMPTY = 0;
const TEXT = 1;
const NODE = 2;
const VIEW = 3;
const LIST = 4;
const RUNS = 5;

const kindOf = (v: unknown): number =>
  v == null || v === false
    ? EMPTY
    : typeof v !== "object"
      ? TEXT
      : v instanceof Desc
        ? VIEW
        : v instanceof ListOf
          ? LIST
          : v instanceof RunsOf
            ? RUNS
            : NODE;

/**
 * A child hole's state. Its content sits just before `anchor`, the slot's empty
 * text node, which never moves; text is written into the anchor itself. A list
 * that is its parent's only content owns the parent (one native clear) and may
 * drop the anchor while it is shown; the anchor is put back when it is not.
 */
class Slot {
  readonly anchor: Text;
  readonly parent: Node;
  kind = EMPTY;
  node: Node | null = null;
  inst: Inst | null = null;
  list: List<unknown, Inst, Key> | null = null;
  key: (item: unknown) => Key = noKey;
  render: (item: unknown) => ViewNode = noRender;
  /** For runs: a text node or an instance per run, in order. */
  runs: (Text | Inst)[] | null = null;
  constructor(anchor: Node) {
    this.anchor = anchor as Text;
    this.parent = anchor.parentNode!;
  }
}

const noKey = (): Key => 0;
const noRender = (): ViewNode => {
  throw new Error("herdsman-blocks-view: no list");
};

function setChild(sl: Slot, v: unknown): void {
  const k = kindOf(v);
  if (k === sl.kind) {
    switch (k) {
      case TEXT:
        sl.anchor.data = "" + (v as string | number);
        return;
      case NODE:
        if (v === sl.node) return;
        if (sl.node!.parentNode === sl.parent) sl.parent.replaceChild(v as Node, sl.node!);
        else sl.parent.insertBefore(v as Node, sl.anchor); // the old node was moved elsewhere
        sl.node = v as Node;
        return;
      case VIEW:
        return patch(sl.inst!, v as Desc);
      case LIST:
        return setList(sl, v as ListOf);
      case RUNS:
        return setRuns(sl, (v as RunsOf).items);
    }
    return;
  }
  clearChild(sl);
  sl.kind = k;
  switch (k) {
    case TEXT:
      sl.anchor.data = "" + (v as string | number);
      return;
    case NODE:
      sl.parent.insertBefore((sl.node = v as Node), sl.anchor);
      return;
    case VIEW:
      sl.inst = make(v as Desc);
      sl.parent.insertBefore(sl.inst.root, sl.anchor);
      return;
    case LIST: {
      const alone = sl.anchor.previousSibling === null && sl.anchor.nextSibling === null;
      sl.list = new List<unknown, Inst, Key>(sl.parent, {
        owns: alone,
        anchor: alone ? null : sl.anchor,
        key: (item) => sl.key(item),
        create: (item) => make(sl.render(item) as unknown as Desc),
        update: (inst, item) => patch(inst, sl.render(item) as unknown as Desc),
      });
      return setList(sl, v as ListOf);
    }
    case RUNS:
      sl.runs = [];
      return setRuns(sl, (v as RunsOf).items);
  }
}

// Bring a slot's runs to `items` by position.
function setRuns(sl: Slot, items: readonly unknown[]): void {
  const old = sl.runs!,
    p = sl.parent,
    n = items.length;
  for (let i = 0; i < n; i++) {
    const v = items[i],
      cur = old[i];
    if (v instanceof Desc) {
      if (cur instanceof Inst) {
        patch(cur, v);
        continue;
      }
      const inst = make(v);
      if (cur === undefined) p.insertBefore(inst.root, sl.anchor);
      else p.replaceChild(inst.root, cur);
      old[i] = inst;
    } else {
      const t = "" + (v as string | number);
      if (cur instanceof Inst || cur === undefined) {
        const node = document.createTextNode(t);
        if (cur === undefined) p.insertBefore(node, sl.anchor);
        else p.replaceChild(node, cur.root);
        old[i] = node;
      } else if (cur.data !== t) cur.data = t;
    }
  }
  for (let i = n; i < old.length; i++) p.removeChild(runRoot(old[i]!));
  old.length = n;
}

const runRoot = (r: Text | Inst): Node => (r instanceof Inst ? r.root : r);

function setList(sl: Slot, l: ListOf): void {
  sl.key = l.key;
  sl.render = l.render;
  sl.list!.set(l.items);
}

function clearChild(sl: Slot): void {
  switch (sl.kind) {
    case TEXT:
      sl.anchor.data = "";
      break;
    case NODE:
      if (sl.node!.parentNode === sl.parent) sl.parent.removeChild(sl.node!);
      sl.node = null;
      break;
    case VIEW:
      sl.parent.removeChild(sl.inst!.root);
      sl.inst = null;
      break;
    case LIST:
      sl.list!.clear();
      sl.list = null;
      sl.key = noKey;
      sl.render = noRender;
      if (sl.anchor.parentNode === null) sl.parent.appendChild(sl.anchor); // an owning list's clear drops it
      break;
    case RUNS:
      for (const r of sl.runs!) sl.parent.removeChild(runRoot(r));
      sl.runs = null;
      break;
  }
  sl.kind = EMPTY;
}
