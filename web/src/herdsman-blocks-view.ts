// herdsman-blocks-view: a declarative layer over herdsman-blocks.
//
// A view is a template you call with its values; the call returns a light
// description. `render` mounts a description the first time and afterwards
// writes only the holes whose values changed (strict equality, as Block.set),
// so a view function can rebuild its descriptions on every render:
//
//   const Card = html`<button class=${cls()} onclick=${prop<() => void>()}>${text()}${child()}</button>`;
//   const Task = html`<div class="task">${text()}</div>`;
//   render(root, Card(["card", () => open(m.pane), m.title, m.task !== null ? Task([m.task]) : null]));
//
// A `child()` hole takes text, nothing, a DOM node, another view's
// description, or a keyed `list(items, key, render)`. The same template
// patches in place; a different template or kind replaces the content.
//
// Event handlers (`on*` property holes) are stored indirectly: each such
// element gets one shared trampoline at mount that calls the handler the last
// render passed. A new closure per render then costs no DOM write.

import { type Hole, html as blocks, type Key, List, slot, type Template, type ValuesOf } from "./herdsman-blocks.ts";

export { attr, cls, type Key, prop, text } from "./herdsman-blocks.ts";

declare const VIEW_NODE: unique symbol;
declare const LIST_NODE: unique symbol;

/** A view description: a template and its values. Make one by calling a view. */
export interface ViewNode {
  readonly [VIEW_NODE]: true;
}
/** A keyed list description, made by `list`. */
export interface ListNode {
  readonly [LIST_NODE]: true;
}

/** What a child hole accepts. null, undefined and false render nothing; a Node is inserted as is. */
export type Child = string | number | null | undefined | false | Node | ViewNode | ListNode;

/** A view: call it with its values tuple to describe one instance. */
export interface View<V extends readonly unknown[]> {
  (vals: V): ViewNode;
  /** The compiled template behind the view. */
  readonly template: Template<V>;
}

/** A child hole: element content that takes any `Child`. */
export function child(): Hole<Child, "slot"> {
  return slot<Child>();
}

/**
 * Tagged template for a view; compiles once per call site, like herdsman-blocks'
 * `html`, and takes the same hole markers plus `child()`.
 */
export function html<const H extends readonly number[]>(strings: TemplateStringsArray, ...holes: H): View<ValuesOf<H>> {
  let v = views.get(strings);
  if (v === undefined) views.set(strings, (v = makeView(blocks(strings, ...holes) as unknown as AnyTemplate)));
  return v as unknown as View<ValuesOf<H>>;
}

/**
 * A keyed list for a child hole: `render(item)` describes each item. Keys come
 * from the item (never its index) and must be unique among siblings. Items are
 * reconciled with herdsman-blocks' List: kept items are patched, and moved
 * with the minimum number of DOM moves.
 */
export function list<T>(items: readonly T[], key: (item: T) => Key, render: (item: T) => ViewNode): ListNode {
  return new ListOf(
    items as readonly unknown[],
    key as (item: unknown) => Key,
    render as (item: unknown) => ViewNode,
  ) as unknown as ListNode;
}

/**
 * Render a description into `container`. The first render replaces the
 * container's content; later ones patch it (a different view replaces it).
 */
export function render(container: Node, d: ViewNode): void {
  const c = container as Node & { [ROOT]?: Inst };
  const inst = c[ROOT];
  if (inst !== undefined) return patch(inst, d as unknown as Desc);
  const fresh = mount(d as unknown as Desc);
  container.textContent = "";
  container.appendChild(fresh.root);
  c[ROOT] = fresh;
}

// ---------------------------------------------------------------------------
// Internals
// ---------------------------------------------------------------------------

type AnyTemplate = Template<readonly unknown[]>;
type Values = readonly unknown[];

const ROOT: unique symbol = Symbol("herdsman-blocks-view.root");
// An element with handler holes holds its instance's state array here.
const STATE: unique symbol = Symbol("herdsman-blocks-view.state");
type Holder = Node & { [STATE]?: unknown[] };

const views = new WeakMap<TemplateStringsArray, View<Values>>();

// How each hole is written. WRITE holes go through Template.write, lazily.
const WRITE = 0;
const HANDLER = 1;
const PROPERTY = 2;
const CHILD = 3;

/** What a view knows about its template, computed once per template. */
class Shape {
  readonly t: AnyTemplate;
  readonly n: number;
  readonly flags: Uint8Array;
  /** Whether mount must locate holes: any handler, property or child hole. */
  readonly eager: boolean;
  /** Per handler hole, the trampoline every instance's element gets. */
  readonly fns: (((this: Holder, e: Event) => unknown) | undefined)[];

  constructor(t: AnyTemplate) {
    const n = t.n;
    this.t = t;
    this.n = n;
    this.flags = new Uint8Array(n);
    this.fns = new Array(n);
    let eager = false;
    for (let i = 0; i < n; i++) {
      const k = t.kind(i);
      if (k === "slot") this.flags[i] = CHILD;
      else if (k === "prop" && t.names[i]!.startsWith("on")) {
        this.flags[i] = HANDLER;
        this.fns[i] = trampoline(n + i);
      } else if (k === "prop") this.flags[i] = PROPERTY;
      else continue;
      eager = true;
    }
    this.eager = eager;
  }
}

// Calls the handler hole j of the instance the element belongs to, as the element.
function trampoline(j: number): (this: Holder, e: Event) => unknown {
  return function (this: Holder, e: Event): unknown {
    const f = this[STATE]![j];
    return typeof f === "function" ? (f as (this: Holder, e: Event) => unknown).call(this, e) : undefined;
  };
}

function makeView(t: AnyTemplate): View<Values> {
  const shape = new Shape(t);
  const v = ((vals: Values) => new Desc(shape, vals)) as unknown as { (vals: Values): ViewNode; template: AnyTemplate };
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
 * A mounted view. `s` holds hole nodes in [0, n) (a Slot for child holes) and
 * the last values in [n, 2n), as a Block's state does. All fields change when
 * a different view replaces this one in place, so lists and parents that hold
 * the instance need no update.
 */
class Inst {
  shape: Shape;
  root: Element;
  s: unknown[];
  constructor(shape: Shape, root: Element, s: unknown[]) {
    this.shape = shape;
    this.root = root;
    this.s = s;
  }
}

function mount(d: Desc): Inst {
  const sh = d.s,
    vals = d.v,
    n = sh.n,
    t = sh.t;
  const root = t.clone(vals);
  const s: unknown[] = new Array(2 * n);
  for (let i = 0; i < n; i++) s[n + i] = vals[i];
  if (sh.eager) {
    t.locate(root, s);
    const flags = sh.flags;
    for (let i = 0; i < n; i++) {
      const f = flags[i],
        v = vals[i];
      if (f === WRITE) continue;
      const node = s[i] as Node;
      if (f === HANDLER) {
        (node as Holder)[STATE] = s;
        t.write(i, node, sh.fns[i]);
      } else if (f === PROPERTY) {
        if (v != null) t.write(i, node, v);
      } else {
        const sl = new Slot(node);
        s[i] = sl;
        setChild(sl, v);
      }
    }
  }
  return new Inst(sh, root, s);
}

// Bring a mounted instance to `d`: patch it, or replace it with a fresh mount
// when `d` is another view.
function patch(inst: Inst, d: Desc): void {
  const sh = d.s;
  if (sh !== inst.shape) {
    const fresh = mount(d);
    inst.root.parentNode!.replaceChild(fresh.root, inst.root);
    inst.shape = sh;
    inst.root = fresh.root;
    inst.s = fresh.s;
    return;
  }
  const vals = d.v,
    s = inst.s,
    n = sh.n,
    flags = sh.flags;
  for (let i = 0; i < n; i++) {
    const v = vals[i],
      j = n + i;
    if (s[j] === v) continue;
    s[j] = v;
    const f = flags[i];
    if (f === HANDLER) continue; // the trampoline reads s[j]
    if (f === CHILD) setChild(s[i] as Slot, v);
    else {
      if (s[0] === undefined) sh.t.locate(inst.root, s); // first real update: find the holes now
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

const kindOf = (v: unknown): number =>
  v == null || v === false
    ? EMPTY
    : typeof v !== "object"
      ? TEXT
      : v instanceof Desc
        ? VIEW
        : v instanceof ListOf
          ? LIST
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
      sl.inst = mount(v as Desc);
      sl.parent.insertBefore(sl.inst.root, sl.anchor);
      return;
    case LIST: {
      const alone = sl.anchor.previousSibling === null && sl.anchor.nextSibling === null;
      sl.list = new List<unknown, Inst, Key>(sl.parent, {
        owns: alone,
        anchor: alone ? null : sl.anchor,
        key: (item) => sl.key(item),
        create: (item) => mount(sl.render(item) as unknown as Desc),
        update: (inst, item) => patch(inst, sl.render(item) as unknown as Desc),
      });
      return setList(sl, v as ListOf);
    }
  }
}

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
  }
  sl.kind = EMPTY;
}
