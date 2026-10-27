// herdsman-blocks-compiled: the runtime of views compiled at build time.
//
// The build plugin (herdsman-blocks-compiler.ts) turns each html`` / svg``
// view literal into straight-line code: a prototype made once from static
// markup, a firstChild/nextSibling walk to the hole nodes, one writer per
// hole specialized by kind, and a description class whose fields are the
// values (no values array). It also points every import of
// herdsman-blocks-view.ts at this module, which offers the same API: render,
// renderDirect, mount, update, list, runs and the markers behave exactly as
// the view layer's. html and svg called at run time (a literal the plugin
// left alone) compile through herdsman-blocks' Template and the same
// protocol, so the two kinds of description mix freely.
//
// Contract: this module imports from herdsman-blocks.ts only its public
// exports `List`, `html` (as the run-time template compiler), `svg`, `slot`,
// `Template` and `Key` (types), and re-exports the markers `attr`, `cls`,
// `on`, `prop`, `text`; from herdsman-blocks-view.ts it imports types only.
// It never reads view-layer internals. Generated code calls only the
// exports below marked "for generated code".

import type { Child, ListNode, RunsNode, View, ViewNode } from "./herdsman-blocks-view.ts";
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
export type {
  AnyValues,
  AnyViewNode,
  Child,
  ExactViewNode,
  ListNode,
  Namespace,
  RunsNode,
  SvgChild,
  SvgNode,
  View,
  ViewNode,
  ViewNodeOf,
} from "./herdsman-blocks-view.ts";

type Values = readonly unknown[];
type Locator = (root: Node, out: unknown[]) => void;
type Fn = (this: Holder, e: Event) => unknown;

// ---------------------------------------------------------------------------
// Public API (the same as herdsman-blocks-view.ts)
// ---------------------------------------------------------------------------

/** A child hole: element content that takes any Child, or only C. */
export function child<C extends Child = Child>(): Hole<C, "slot"> {
  return slot<C>();
}

/** Tagged template for a view, compiled at run time (the plugin compiles literals at build time). */
export function html<const H extends readonly AnyHole[]>(
  strings: TemplateStringsArray,
  ...holes: H
): View<ValuesOf<H>, "html"> {
  let v = views.get(strings);
  if (v === undefined) views.set(strings, (v = runtimeView(blocks(strings, ...holes) as unknown as AnyTemplate)));
  return v as unknown as View<ValuesOf<H>, "html">;
}

/** Like `html`, for SVG content. */
export function svg<const H extends readonly AnyHole[]>(
  strings: TemplateStringsArray,
  ...holes: H
): View<ValuesOf<H>, "svg"> {
  let v = views.get(strings);
  if (v === undefined) views.set(strings, (v = runtimeView(svgBlocks(strings, ...holes) as unknown as AnyTemplate)));
  return v as unknown as View<ValuesOf<H>, "svg">;
}

/** Options of list(). */
export interface ListOptions {
  /**
   * Rows are pure functions of their items: a row whose item is the same
   * object as last time (===), rendered by the same function, is not
   * rendered again. Without it every surviving row is updated, so items
   * mutated in place still show their new state.
   */
  readonly pure?: boolean;
}

/** A keyed list for a child hole: `render(item)` describes each item. */
export function list<T, K extends Key, N extends ViewNode>(
  items: readonly T[],
  key: (item: NoInfer<T>) => K,
  render: (item: NoInfer<T>) => N,
  options?: ListOptions,
): ListNode<N, K> {
  return new ListOf(
    items as readonly unknown[],
    key as (item: unknown) => Key,
    render as (item: unknown) => ViewNode,
    options?.pure === true,
  ) as unknown as ListNode<N, K>;
}

/** Unkeyed inline content for a child hole: text runs and descriptions side by side. */
export function runs<N extends ViewNode>(items: readonly (string | number | N)[]): RunsNode<N> {
  return new RunsOf(items) as unknown as RunsNode<N>;
}

/** Render a description into `container`: mount the first time, patch afterwards. */
export function render(container: Node, d: ViewNode): void {
  const c = container as Container;
  if (c[LISTENING] !== used.length) listen(c);
  draw(c, d as unknown as Desc, false);
}

/** Like `render`, with every handler on its own element (no delegation). */
export function renderDirect(container: Node, d: ViewNode): void {
  draw(container as Container, d as unknown as Desc, true);
}

/** Create a view's root element, its own render root. Update it with `update`. */
export function mount(d: ViewNode): Element {
  const outer = dir;
  dir = false;
  try {
    const inst = (d as unknown as Desc).m();
    const root = inst.r as Element & Container;
    root[SELF] = inst;
    listen(root);
    return root;
  } finally {
    dir = outer;
  }
}

/** Patch a root made by `mount` toward a description of the same view. */
export function update(root: Element, d: ViewNode): void {
  const r = root as Element & Container,
    inst = r[SELF],
    desc = d as unknown as Desc;
  if (inst === undefined) throw new Error("herdsman-blocks-view: update() takes a root made by mount()");
  if (desc.t !== inst.t) throw new Error("herdsman-blocks-view: update() with a different view; mount() it instead");
  if (r[LISTENING] !== used.length) listen(r);
  const outer = dir;
  dir = false;
  try {
    patch(inst, desc);
  } finally {
    dir = outer;
  }
}

function draw(c: Container, d: Desc, direct: boolean): void {
  const outer = dir;
  dir = direct;
  try {
    const inst = c[ROOT];
    if (inst !== undefined) return patch(inst, d);
    const fresh = d.m();
    c.textContent = "";
    c.appendChild(fresh.r);
    c[ROOT] = fresh;
  } finally {
    dir = outer;
  }
}

// ---------------------------------------------------------------------------
// The protocol between generated code and this module
// ---------------------------------------------------------------------------

/** For generated code: whether the render running now mounts handlers directly (renderDirect). */
export let dir: boolean = false;

/** For generated code: the key under which a view root with handlers holds its instance. */
export const STATE: unique symbol = Symbol("herdsman-blocks-compiled.state");
type Holder = Node & { [STATE]?: Inst };

/** What every instance of one view shares. */
export interface Def {
  /** Hole count. */
  readonly n: number;
  /** Per hole, the delegated event type, or null when the view delegates none. */
  readonly ty: readonly (string | undefined)[] | null;
  /** Locates the delegated handler holes. */
  readonly fd: Locator;
  /** Whether any hole is a handler: the instance is then kept on its root. */
  readonly h: boolean;
}

/**
 * For generated code: the base of every description. A compiled view's
 * subclass keeps its values in fields $0, $1, ...; `t` is its view's Def (on
 * the prototype), `m` mounts it, `p` patches an instance of the same view
 * toward it, and `g(i)` reads hole i's value.
 */
export abstract class Desc {
  declare readonly t: Def;
  abstract m(): Inst;
  abstract p(inst: Inst): void;
  g(i: number): unknown {
    return (this as unknown as Record<string, unknown>)["$" + i];
  }
}

/**
 * For generated code: a mounted view. `d` is the description last rendered;
 * `s` holds the hole nodes (a Slot for child holes), or null until a hole is
 * first located. Every field changes when another view replaces this one in
 * place, so lists and parents holding the instance need no update.
 */
export class Inst {
  t: Def;
  r: Element;
  d: Desc;
  s: unknown[] | null;
  /** Mounted by renderDirect: its handlers are on its elements, never delegated. */
  x: boolean;
  /** As a list row: the item it was last rendered from (for pure lists). */
  it: unknown = undefined;
  constructor(t: Def, r: Element, d: Desc, s: unknown[] | null, x: boolean) {
    this.t = t;
    this.r = r;
    this.d = d;
    this.s = s;
    this.x = x;
  }
}

/** A compiled description's constructor: values in, fields $0, $1, ... set. */
type DescCtor = { prototype: Desc } & (new (...v: unknown[]) => Desc);
/** Writes the description's text, class and attribute values into the prototype's hole nodes q, where they differ from the values p last written there. */
type Bake = (d: Desc, q: Node[], p: unknown[]) => void;
/** Mounting, for views with property, slot or handler holes: s holds every hole node; x is renderDirect; F the trampolines. */
type Extras = (s: unknown[], d: Desc, x: boolean, F: readonly Fn[]) => void;
/** Writes every non-handler hole whose value differs between d and o, the instance's last description. */
type Patch = (i: Inst, d: Desc, o: Desc) => void;

/** A compiled view: the view function, its initializer (called where the literal stood) and its description class. */
export interface CompiledView {
  (vals: Values): Desc;
  i(): CompiledView;
  readonly D: DescCtor;
}

/**
 * For generated code: make a compiled view. `kinds` has one letter per hole:
 * t text, c class, a attribute, p property, d delegated handler, h direct
 * handler, s slot. `ty` lists the delegated event types by hole (0: none).
 * The prototype is parsed when the literal is first evaluated (`i()`), as
 * the run-time compiler parses it.
 */
export function V(
  markup: string,
  isSvg: number,
  kinds: string,
  ty: readonly (string | undefined)[] | 0,
  D: DescCtor,
  wa: Locator,
  bake: Bake | 0,
  extras: Extras | 0,
  patch: Patch | 0,
): CompiledView {
  const n = kinds.length,
    eager = /[sph]/.test(kinds),
    handlers = /[dh]/.test(kinds),
    directs = kinds.includes("h"),
    delegates = ty !== 0;
  const F: Fn[] = [];
  for (let i = 0; i < n; i++) if (kinds[i] === "d" || kinds[i] === "h") F[i] = tramp(i);
  const q: Node[] = new Array(n),
    p: unknown[] = new Array(n);
  let T: Element | null = null,
    t: Def;
  const P = (D.prototype = Object.create(Desc.prototype) as Desc & { t: Def });
  P.m = function (this: Desc): Inst {
    if (bake !== 0) bake(this, q, p);
    const r = T!.cloneNode(true) as Element,
      x = dir;
    let inst: Inst;
    if (!eager && !(x && delegates)) inst = new Inst(t, r, this, null, x);
    else {
      const s: unknown[] = new Array(n);
      wa(r, s);
      inst = new Inst(t, r, this, s, x);
      // Only trampolines look an instance up from its root; delegation finds it from the render root.
      if (directs || (x && delegates)) (r as Holder)[STATE] = inst;
      (extras as Extras)(s, this, x, F);
    }
    return inst;
  };
  P.p = function (this: Desc, i: Inst): void {
    const o = i.d;
    if (o === this) return;
    i.d = this;
    if (patch !== 0) patch(i, this, o);
  };
  const view = ((vals: Values) => new D(...vals)) as CompiledView & {
    i: () => CompiledView;
    D: DescCtor;
  };
  view.i = (): CompiledView => {
    if (T === null) {
      const tpl = document.createElement("template");
      tpl.innerHTML = isSvg ? "<svg>" + markup + "</svg>" : markup;
      const root = (isSvg ? tpl.content.firstChild!.firstChild : tpl.content.firstChild) as Element;
      wa(root, q);
      for (let i = 0; i < n; i++) {
        const k = kinds[i];
        if (k === "t" || k === "s") {
          // The placeholder comment becomes the empty text node the run-time compiler makes.
          const c = q[i]!,
            e = document.createTextNode("");
          c.parentNode!.replaceChild(e, c);
          q[i] = e;
        }
        p[i] = k === "a" ? null : "";
      }
      if (ty !== 0) for (const type of ty) if (type !== undefined && !used.includes(type)) used.push(type);
      t = P.t = { n, ty: ty === 0 ? null : ty, fd: wa, h: handlers };
      T = root;
    }
    return view;
  };
  (view as { D: DescCtor }).D = D;
  return view;
}

/** For generated code: locate every hole of an instance on its first update. */
export function L(i: Inst): unknown[] {
  i.t.fd(i.r, (i.s = new Array(i.t.n)));
  return i.s;
}

/** For generated code: write a text hole. */
export function wt(node: Node, v: unknown): void {
  (node as Text).data = v == null ? "" : "" + (v as string);
}

/** For generated code: write a class hole. */
export function wc(node: Node, v: unknown): void {
  (node as Element).className = v == null ? "" : "" + (v as string);
}

/** For generated code: write an attribute hole. */
export function wa(node: Node, name: string, v: unknown): void {
  if (v == null || v === false) (node as Element).removeAttribute(name);
  else (node as Element).setAttribute(name, v === true ? "" : "" + (v as string));
}

/**
 * For generated code: set a direct trampoline, through the element's on*
 * property when it has one, otherwise as a listener; once per element.
 */
export function attach(node: Node, name: string, fn: Fn): void {
  if (name in node) (node as unknown as Record<string, unknown>)[name] = fn;
  else node.addEventListener(name.slice(2), fn as EventListener);
}

/** For generated code: the trampoline of handler hole i, shared by every instance of a view. */
export function tramp(i: number): Fn {
  return function (this: Holder, e: Event): unknown {
    let h: Holder = this;
    while (h[STATE] === undefined) h = h.parentNode!;
    const f = h[STATE].d.g(i);
    return typeof f === "function" ? (f as Fn).call(this, e) : undefined;
  };
}

/** For generated code: wrap a child hole's anchor in a slot and set its first value. */
export function slotOf(anchor: Node, v: unknown): Slot {
  const sl = new Slot(anchor);
  setChild(sl, v);
  return sl;
}

/** Bring a mounted instance to `d`: patch it, or replace it with a fresh mount when `d` is another view. */
function patch(inst: Inst, d: Desc): void {
  const t = d.t;
  if (t === inst.t) return d.p(inst);
  const fresh = d.m();
  inst.r.parentNode!.replaceChild(fresh.r, inst.r);
  inst.t = t;
  inst.r = fresh.r;
  inst.d = d;
  inst.s = fresh.s;
  inst.x = fresh.x;
  if ((fresh.r as Holder)[STATE] !== undefined) (fresh.r as Holder)[STATE] = inst;
}

// ---------------------------------------------------------------------------
// Views compiled at run time (literals the plugin did not compile)
// ---------------------------------------------------------------------------

type AnyTemplate = Template<Values>;

const views = new WeakMap<TemplateStringsArray, View<Values>>();

// How each hole is written. WRITE holes go through Template.write, lazily.
const WRITE = 0;
const HANDLER = 1;
const PROPERTY = 2;
const CHILD = 3;
const DELEGATED = 4;

/** Events delegated to the render root; the plugin's list is the same. */
export const BUBBLING: ReadonlySet<string> = new Set([
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

/** A run-time view's Def: the template and what each hole needs. */
class RDef implements Def {
  readonly n: number;
  readonly ty: (string | undefined)[] | null;
  readonly fd: Locator;
  readonly h: boolean;
  readonly tpl: AnyTemplate;
  readonly flags: Uint8Array;
  readonly eager: boolean;
  readonly fns: (Fn | undefined)[];
  readonly delegates: boolean;
  readonly findEager: Locator;
  readonly findWrite: Locator;

  constructor(t: AnyTemplate) {
    const n = t.n;
    this.tpl = t;
    this.n = n;
    this.flags = new Uint8Array(n);
    this.fns = new Array(n);
    const ty: (string | undefined)[] = new Array(n);
    let handlers = false;
    const eager: number[] = [],
      write: number[] = [],
      delegated: number[] = [];
    for (let i = 0; i < n; i++) {
      const k = t.kind(i),
        name = t.names[i];
      if (k === "slot") this.flags[i] = CHILD;
      else if (k === "prop" && name!.startsWith("on")) {
        handlers = true;
        const type = name!.slice(2);
        this.fns[i] = tramp(i);
        if (BUBBLING.has(type)) {
          this.flags[i] = DELEGATED;
          ty[i] = type;
          if (!used.includes(type)) used.push(type);
          delegated.push(i);
          continue;
        }
        this.flags[i] = HANDLER;
      } else if (k === "prop") this.flags[i] = PROPERTY;
      else {
        write.push(i);
        continue;
      }
      eager.push(i);
    }
    this.h = handlers;
    this.ty = delegated.length > 0 ? ty : null;
    this.delegates = delegated.length > 0;
    this.eager = eager.length > 0;
    // Hole paths hold only in the pristine clone (see herdsman-blocks-view.ts).
    this.findEager = eager.length === n || this.flags.includes(CHILD) ? t.locate : t.locator(eager);
    this.findWrite = write.length === n ? t.locate : t.locator(write);
    this.fd = delegated.length === n ? t.locate : t.locator(delegated);
  }
}

function runtimeView(t: AnyTemplate): View<Values> {
  const rd = new RDef(t);
  const v = ((vals: Values) => new RDesc(rd, vals)) as unknown as {
    (vals: Values): unknown;
    template: AnyTemplate;
  };
  v.template = t;
  return v as unknown as View<Values>;
}

class RDesc extends Desc {
  declare readonly t: RDef;
  readonly v: Values;
  constructor(t: RDef, v: Values) {
    super();
    (this as { t: RDef }).t = t;
    this.v = v;
  }

  override g(i: number): unknown {
    return this.v[i];
  }

  m(): Inst {
    const sh = this.t,
      vals = this.v,
      t = sh.tpl;
    const root = t.clone(vals);
    const direct = dir && sh.delegates;
    if (!sh.eager && !direct) {
      return new Inst(sh, root, this, null, dir);
    }
    const n = sh.n,
      s: unknown[] = new Array(n);
    if (sh.eager) sh.findEager(root, s);
    if (direct) sh.fd(root, s);
    const inst = new Inst(sh, root, this, s, dir);
    if (direct || sh.flags.includes(HANDLER)) (root as Holder)[STATE] = inst;
    const flags = sh.flags;
    for (let i = 0; i < n; i++) {
      const f = flags[i];
      if (f === WRITE || (f === DELEGATED && !direct)) continue;
      const node = s[i] as Node,
        v = vals[i];
      if (f === HANDLER || f === DELEGATED) attach(node, t.names[i]!, sh.fns[i]!);
      else if (f === PROPERTY) {
        if (v != null) t.write(i, node, v);
      } else s[i] = slotOf(node, v);
    }
    return inst;
  }

  p(inst: Inst): void {
    const sh = this.t,
      vals = this.v;
    const old = inst.d as RDesc;
    if (old === this) return;
    const ov = old.v;
    inst.d = this;
    if (ov === vals) return; // the same values again: nothing can have changed
    const n = sh.n,
      flags = sh.flags;
    let s = inst.s;
    for (let i = 0; i < n; i++) {
      const v = vals[i];
      if (ov[i] === v) continue;
      const f = flags[i];
      if (f === HANDLER || f === DELEGATED) continue;
      if (f === CHILD) setChild(s![i] as Slot, v);
      else {
        if (s === null) sh.findWrite(inst.r, (s = inst.s = new Array(n)));
        else if (s[i] === undefined) sh.findWrite(inst.r, s);
        sh.tpl.write(i, s[i] as Node, v);
      }
    }
  }
}

// ---------------------------------------------------------------------------
// Render roots and delegated events
// ---------------------------------------------------------------------------

const ROOT: unique symbol = Symbol("herdsman-blocks-compiled.root");
const LISTENING: unique symbol = Symbol("herdsman-blocks-compiled.listening");
const SELF: unique symbol = Symbol("herdsman-blocks-compiled.self");
type Container = Node & { [ROOT]?: Inst; [LISTENING]?: number; [SELF]?: Inst };

function listen(c: Container): void {
  for (let k = c[LISTENING] ?? 0; k < used.length; k++) c.addEventListener(used[k]!, delegate);
  c[LISTENING] = used.length;
}

function delegate(this: Container, e: Event): void {
  const type = e.type;
  // The path from the target up to the render root (the root itself only when
  // it is a mount() root, whose own handlers count).
  const path: Node[] = [];
  const self = this[SELF];
  for (let node = e.target as Node | null; node !== null && node !== this; node = node.parentNode) path.push(node);
  if (self !== undefined) path.push(this);
  // A mount() root can also be a render() container: both instances count.
  const content = this[ROOT];
  if (self === undefined && content === undefined) return;
  // The instances on the path, found from the root's instance down through its
  // child holes (a view, a list entry or a run whose root is on the path), so
  // no instance needs a reference on its root element. Content that is not an
  // instance (a node, another render root) is not entered: its own listener
  // handles what is inside it.
  const found: {
    at: number;
    depth: number;
    i: number;
    inst: Inst;
    el: Node;
  }[] = [];
  const visit = (inst: Inst, depth: number): void => {
    const t = inst.t,
      types = t.ty;
    if (types !== null && !inst.x) {
      for (let i = 0; i < t.n; i++) {
        if (types[i] !== type) continue;
        let s = inst.s;
        if (s === null) s = inst.s = new Array(t.n);
        if (s[i] === undefined) t.fd(inst.r, s);
        const at = path.lastIndexOf(s[i] as Node);
        if (at >= 0) found.push({ at, depth, i, inst, el: s[i] as Node });
      }
    }
    const s = inst.s;
    if (s === null) return;
    for (const sl of s) {
      if (!(sl instanceof Slot)) continue;
      const at = path.indexOf(sl.parent);
      if (at <= 0) continue;
      const c = path[at - 1]!;
      let next: Inst | undefined;
      if (sl.kind === VIEW) next = sl.inst!.r === c ? sl.inst! : undefined;
      else if (sl.kind === LIST) next = sl.list!.entries.find((x) => x.r === c);
      else if (sl.kind === RUNS) next = sl.runs!.find((x): x is Inst => x instanceof Inst && x.r === c);
      if (next !== undefined) visit(next, depth + 1);
    }
  };
  if (self !== undefined) visit(self, 0);
  if (content !== undefined && path.includes(content.r)) visit(content, 1);
  if (found.length === 0) return;
  // Innermost element first; on one element, the inner instance first, then hole order.
  found.sort((a, b) => a.at - b.at || b.depth - a.depth || a.i - b.i);
  const els = found.map((f) => f.el),
    insts = found.map((f) => f.inst),
    holes = found.map((f) => f.i);
  for (let k = 0; k < els.length; k++) {
    if (k > 0 && els[k] !== els[k - 1] && e.cancelBubble) break;
    const f = insts[k]!.d.g(holes[k]!);
    if (typeof f !== "function") continue;
    const el = els[k]!;
    Object.defineProperty(e, "currentTarget", {
      configurable: true,
      get: () => el,
    });
    try {
      (f as (this: Node, e: Event) => unknown).call(el, e);
    } finally {
      delete (e as { currentTarget?: unknown }).currentTarget;
    }
  }
}

// ---------------------------------------------------------------------------
// Child holes
// ---------------------------------------------------------------------------

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
  readonly pure: boolean;
  constructor(
    items: readonly unknown[],
    key: (item: unknown) => Key,
    render: (item: unknown) => ViewNode,
    pure: boolean,
  ) {
    this.items = items;
    this.key = key;
    this.render = render;
    this.pure = pure;
  }
}

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

/** A child hole's state; its content sits just before `anchor`, which never moves. */
export class Slot {
  readonly anchor: Text;
  readonly parent: Node;
  kind: number = EMPTY;
  node: Node | null = null;
  inst: Inst | null = null;
  list: List<unknown, Inst, Key> | null = null;
  key: (item: unknown) => Key = noKey;
  render: (item: unknown) => ViewNode = noRender;
  /** This pass may skip rows whose item is unchanged: the list is pure and its render function the same as last time. */
  skip = false;
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

/** For generated code: set a child hole's content. */
export function setChild(sl: Slot, v: unknown): void {
  const k = kindOf(v);
  if (k === sl.kind) {
    switch (k) {
      case TEXT:
        sl.anchor.data = "" + (v as string | number);
        return;
      case NODE:
        if (v === sl.node) return;
        if (sl.node!.parentNode === sl.parent) sl.parent.replaceChild(v as Node, sl.node!);
        else sl.parent.insertBefore(v as Node, sl.anchor);
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
      sl.inst = (v as Desc).m();
      sl.parent.insertBefore(sl.inst.r, sl.anchor);
      return;
    case LIST: {
      const alone = sl.anchor.previousSibling === null && sl.anchor.nextSibling === null;
      sl.list = new List<unknown, Inst, Key>(sl.parent, {
        owns: alone,
        anchor: alone ? null : sl.anchor,
        key: (item) => sl.key(item),
        root: instRoot,
        create: (item) => {
          const inst = (sl.render(item) as unknown as Desc).m();
          inst.it = item;
          return inst;
        },
        update: (inst, item) => {
          if (sl.skip && inst.it === item) return;
          inst.it = item;
          patch(inst, sl.render(item) as unknown as Desc);
        },
      });
      return setList(sl, v as ListOf);
    }
    case RUNS:
      sl.runs = [];
      return setRuns(sl, (v as RunsOf).items);
  }
}

const instRoot = (i: Inst): Node => i.r;

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
      const inst = v.m();
      if (cur === undefined) p.insertBefore(inst.r, sl.anchor);
      else p.replaceChild(inst.r, cur);
      old[i] = inst;
    } else {
      const t = "" + (v as string | number);
      if (cur instanceof Inst || cur === undefined) {
        const node = document.createTextNode(t);
        if (cur === undefined) p.insertBefore(node, sl.anchor);
        else p.replaceChild(node, cur.r);
        old[i] = node;
      } else if (cur.data !== t) cur.data = t;
    }
  }
  for (let i = n; i < old.length; i++) p.removeChild(runRoot(old[i]!));
  old.length = n;
}

const runRoot = (r: Text | Inst): Node => (r instanceof Inst ? r.r : r);

function setList(sl: Slot, l: ListOf): void {
  sl.key = l.key;
  sl.skip = l.pure && sl.render === l.render;
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
      sl.parent.removeChild(sl.inst!.r);
      sl.inst = null;
      break;
    case LIST:
      sl.list!.clear();
      sl.list = null;
      sl.key = noKey;
      sl.render = noRender;
      sl.skip = false;
      if (sl.anchor.parentNode === null) sl.parent.appendChild(sl.anchor);
      break;
    case RUNS:
      for (const r of sl.runs!) sl.parent.removeChild(runRoot(r));
      sl.runs = null;
      break;
  }
  sl.kind = EMPTY;
}
