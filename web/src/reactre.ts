// reactre: declarative UI compiled to herdsman-blocks templates.
//
// You write TSX components over signals:
//
//   const [rows, setRows] = signal<Row[]>([]);
//   const [selected, setSelected] = signal(0);
//   const isSelected = selector(selected);
//   const App = () => (
//     <tbody>
//       <For each={rows()} key={(r) => r.id}>
//         {(row) => <tr class={isSelected(row.id) ? "danger" : ""} onClick={() => setSelected(row.id)}>{row.label}</tr>}
//       </For>
//     </tbody>
//   );
//   render(App, document.getElementById("main")!);
//
// reactre-compiler.ts turns each JSX tree into a template, cloned per
// instance, and the code that keeps its holes up to date. Components run
// once; what changes later is what reads a signal:
//
//   - Outside a list, a hole whose expression calls a function (a signal
//     read, most often) is an effect: it re-runs, and rewrites the hole,
//     when a signal it read changes. Any other hole is written once.
//   - In a `<For>` row, a hole that reads only the row (`row.label`) is
//     written when the row is created and compared when the list renders
//     the row again with a new item of the same key. A hole that calls a
//     function is the row's reactive part: it re-runs when a signal it read
//     changes. A `selector` read costs a row one field, not a subscription:
//     a change of selection re-runs the two rows whose key it was or is.
//   - Event handlers in rows are delegated: one listener per event type on
//     the list's parent, which finds the row and calls its handler.
//
// The list is herdsman-blocks' keyed List (reuse, minimal moves, swaps and
// removals in place). Nothing here depends on React.

import { List, Template } from "./herdsman-blocks.ts";

// ---------------------------------------------------------------------------
// Reactivity
// ---------------------------------------------------------------------------

/** Something an observer reads: a signal, or a selector's key. */
interface Source {
  subs: Observer[];
}

/** What runs again when a source it read changes. */
interface Observer {
  /** Note that this observer read `s`. */
  read(s: Source): void;
  /** A source it read has changed. */
  stale(): void;
}

let current: Observer | null = null;
let batching = 0;
const pending: Observer[] = [];

function notify(s: Source): void {
  const subs = s.subs;
  if (subs.length === 0) return;
  // Re-running drops and re-adds subscriptions: run over a copy.
  const run = subs.slice();
  if (batching) {
    for (let i = 0; i < run.length; i++) if (!pending.includes(run[i]!)) pending.push(run[i]!);
    return;
  }
  for (let i = 0; i < run.length; i++) run[i]!.stale();
}

/** Run `fn` with every effect it causes deferred to its end, each run once. */
export function batch<T>(fn: () => T): T {
  batching++;
  try {
    return fn();
  } finally {
    if (--batching === 0) {
      while (pending.length) pending.shift()!.stale();
    }
  }
}

/** Run `fn` without recording what it reads. */
export function untrack<T>(fn: () => T): T {
  const o = current;
  current = null;
  try {
    return fn();
  } finally {
    current = o;
  }
}

/** A signal's setter: a value, or a function of the current value. */
export type Setter<T> = (v: T | ((prev: T) => T)) => void;

class Signal<T> implements Source {
  subs: Observer[] = [];
  v: T;
  constructor(v: T) {
    this.v = v;
  }
}

/** A value that observers re-run on: [read, write]. Writing an identical (===) value does nothing. */
export function signal<T>(v: T): [() => T, Setter<T>] {
  const s = new Signal(v);
  const read = (): T => {
    if (current !== null) current.read(s);
    return s.v;
  };
  const write: Setter<T> = (next) => {
    const v2 = typeof next === "function" ? (next as (p: T) => T)(s.v) : next;
    if (v2 === s.v) return;
    s.v = v2;
    notify(s);
  };
  return [read, write];
}

// An effect: a function re-run whenever a source it read last time changes.
class Effect implements Observer {
  private readonly fn: () => void;
  private sources: Source[] = [];
  private dead = false;
  constructor(fn: () => void) {
    this.fn = fn;
  }
  read(s: Source): void {
    if (this.sources.includes(s)) return;
    this.sources.push(s);
    s.subs.push(this);
  }
  stale(): void {
    if (!this.dead) this.run();
  }
  run(): void {
    this.drop();
    const o = current;
    current = this;
    try {
      this.fn();
    } finally {
      current = o;
    }
  }
  drop(): void {
    const ss = this.sources;
    for (let i = 0; i < ss.length; i++) {
      const subs = ss[i]!.subs,
        j = subs.indexOf(this);
      if (j >= 0) subs.splice(j, 1);
    }
    this.sources = [];
  }
  dispose(): void {
    this.dead = true;
    this.drop();
  }
}

/** Run `fn` now and again whenever a signal it read changes. */
export function effect(fn: () => void): void {
  new Effect(fn).run();
}

/** A derived value, recomputed when what it reads changes: a read function. */
export function memo<T>(fn: () => T): () => T {
  const [read, write] = signal<T>(undefined as T);
  effect(() => write(fn()));
  return read;
}

/**
 * A keyed view of `source`: `is(k)` is true for the one key the source
 * holds. When it changes, only what asked about the old key and the new one
 * re-runs (a selected row, among a thousand).
 */
export function selector<K>(source: () => K): (k: K) => boolean {
  const sel = new Selector<K>();
  effect(() => {
    const next = source();
    const prev = sel.key;
    sel.key = next;
    if (sel.ready) sel.changed(prev, next);
    sel.ready = true;
  });
  return (k: K): boolean => {
    const o = current;
    if (o !== null) sel.watch(o, k);
    return k === sel.key;
  };
}

class Selector<K> {
  key!: K;
  ready = false;
  // Rows of lists that asked: by list, the rows remember their key themselves.
  readonly lists: ForList<unknown, unknown>[] = [];
  // Anything else that asked, by key.
  readonly byKey = new Map<K, Observer[]>();
  watch(o: Observer, k: K): void {
    if (o === rowContext) {
      // A row asks: it keeps the key itself; the list is told of changes.
      const e = rowContext.entry;
      e.sel = this as Selector<unknown>;
      e.sk = k;
      if (!this.lists.includes(rowContext.list)) this.lists.push(rowContext.list);
      return;
    }
    const list = this.byKey.get(k);
    if (list === undefined) this.byKey.set(k, [o]);
    else if (!list.includes(o)) list.push(o);
  }
  changed(prev: K, next: K): void {
    for (let i = 0; i < this.lists.length; i++) this.lists[i]!.selectorChanged(this as Selector<unknown>, prev, next);
    for (const k of [prev, next]) {
      const obs = this.byKey.get(k);
      if (obs === undefined) continue;
      this.byKey.delete(k);
      for (let i = 0; i < obs.length; i++) obs[i]!.stale();
    }
  }
}

// ---------------------------------------------------------------------------
// Lists
// ---------------------------------------------------------------------------

/** One row of a `<For>`. */
interface Row<T> {
  readonly root: Element;
  item: T;
  /** The row's hole nodes, looked up the first time one is written. */
  nodes: unknown[] | null;
  /** The last values of its reactive holes. */
  dv: unknown[] | null;
  /** The selector it asked, and the key it asked about. */
  sel: Selector<unknown> | null;
  sk: unknown;
  /** Its observer, when its reactive holes read a signal (not a selector). */
  obs: RowObserver | null;
}

/** What the compiler emits for a `<For>`: the row template and how to fill it. */
export interface ForSpec<T, K> {
  /** The rows (read in an effect: the list renders again when it changes). */
  readonly each: () => readonly T[];
  readonly key: (item: T) => K;
  /** The row template. */
  readonly t: Template;
  /** Write the values of the holes that read only the row into `v`, by hole index. */
  readonly fill: (item: T, v: unknown[]) => void;
  /** Indexes of those holes. */
  readonly holes: readonly number[];
  /** Write the values of the reactive holes into `v` (in their order, from 0). */
  readonly dyn?: (item: T, v: unknown[]) => void;
  /** The template hole of each reactive value. */
  readonly dynHoles?: readonly number[];
  /** Delegated handlers: [event type, hole of the element, handler of the row]. */
  readonly events?: readonly (readonly [string, number, (item: T) => (e: Event) => void])[];
}

class RowObserver implements Observer {
  readonly list: ForList<unknown, unknown>;
  readonly entry: Row<unknown>;
  private sources: Source[] = [];
  constructor(list: ForList<unknown, unknown>, entry: Row<unknown>) {
    this.list = list;
    this.entry = entry;
  }
  read(s: Source): void {
    if (this.sources.includes(s)) return;
    this.sources.push(s);
    s.subs.push(this);
  }
  stale(): void {
    this.list.refresh(this.entry);
  }
  drop(): void {
    for (const s of this.sources) {
      const j = s.subs.indexOf(this);
      if (j >= 0) s.subs.splice(j, 1);
    }
    this.sources = [];
  }
}

// The observer a row's reactive holes run under. A selector read records
// the key on the row; a signal read makes the row an observer of its own.
class RowContext implements Observer {
  list!: ForList<unknown, unknown>;
  entry!: Row<unknown>;
  read(s: Source): void {
    let o = this.entry.obs;
    if (o === null) o = this.entry.obs = new RowObserver(this.list, this.entry);
    o.read(s);
  }
  stale(): void {}
}
const rowContext = new RowContext();

class ForList<T, K> {
  readonly spec: ForSpec<T, K>;
  readonly list: List<T, Row<T>, K & (string | number)>;
  readonly parent: Element;
  private readonly v: unknown[];
  private readonly dv: unknown[] = [];
  private readonly nd: number;

  constructor(parent: Element, anchor: Node | null, spec: ForSpec<T, K>) {
    this.spec = spec;
    this.parent = parent;
    this.v = new Array(spec.t.n).fill("");
    this.nd = spec.dynHoles?.length ?? 0;
    const owns = anchor === null;
    this.list = new List<T, Row<T>, K & (string | number)>(parent, {
      key: spec.key as (item: T) => K & (string | number),
      create: (item) => this.create(item),
      update: (e, item) => this.update(e, item),
      anchor,
      owns,
      detach: owns,
      skipUnchanged: true,
      remove: (e) => this.dispose(e),
    });
    if (spec.events) for (const [type] of spec.events) this.listen(type);
  }

  private create(item: T): Row<T> {
    const spec = this.spec,
      v = this.v;
    spec.fill(item, v);
    const e: Row<T> = {
      root: null as unknown as Element,
      item,
      nodes: null,
      dv: null,
      sel: null,
      sk: undefined,
      obs: null,
    };
    if (this.nd) {
      const dv = this.dv;
      this.runDyn(e, item, dv);
      const holes = spec.dynHoles!;
      for (let i = 0; i < holes.length; i++) v[holes[i]!] = dv[i];
      e.dv = dv.slice(0, this.nd);
    }
    (e as { root: Element }).root = spec.t.clone(v as never);
    return e;
  }

  // Run the reactive holes of row e for item, recording what they read.
  private runDyn(e: Row<T>, item: T, out: unknown[]): void {
    const o = current,
      pl = rowContext.list,
      pe = rowContext.entry;
    if (e.obs !== null) e.obs.drop();
    rowContext.list = this as ForList<unknown, unknown>;
    rowContext.entry = e as Row<unknown>;
    current = rowContext;
    try {
      this.spec.dyn!(item, out);
    } finally {
      current = o;
      rowContext.list = pl;
      rowContext.entry = pe;
    }
  }

  private update(e: Row<T>, item: T): void {
    const spec = this.spec,
      old = e.item,
      v = this.v,
      w = this.dv;
    e.item = item;
    // The row's own holes: written where the new item's value differs.
    spec.fill(old, w);
    spec.fill(item, v);
    const holes = spec.holes;
    for (let i = 0; i < holes.length; i++) {
      const h = holes[i]!;
      if (v[h] !== w[h]) this.write(e, h, v[h]);
    }
    if (this.nd) this.refresh(e);
  }

  /** Run row e's reactive holes again and write those that changed. */
  refresh(e: Row<T>): void {
    const dv = this.dv,
      prev = e.dv!,
      holes = this.spec.dynHoles!;
    this.runDyn(e, e.item, dv);
    for (let i = 0; i < holes.length; i++) {
      if (dv[i] !== prev[i]) {
        prev[i] = dv[i];
        this.write(e, holes[i]!, dv[i]);
      }
    }
  }

  private write(e: Row<T>, hole: number, value: unknown): void {
    this.spec.t.write(hole, this.node(e, hole), value);
  }

  // Row e's node for one hole, looked up (that hole only) the first time.
  private node(e: Row<T>, hole: number): Node {
    let nodes = e.nodes;
    if (nodes === null) nodes = e.nodes = [];
    let n = nodes[hole] as Node | undefined;
    if (n === undefined) {
      (this.finders[hole] ??= this.spec.t.locator([hole]))(e.root, nodes);
      n = nodes[hole] as Node;
    }
    return n;
  }
  private readonly finders: ((root: Node, out: unknown[]) => void)[] = [];

  private dispose(e: Row<T>): void {
    if (e.obs !== null) e.obs.drop();
  }

  /** A selector this list's rows asked has moved from prev to next. */
  selectorChanged(sel: Selector<unknown>, prev: unknown, next: unknown): void {
    const es = this.list.entries;
    for (let i = 0; i < es.length; i++) {
      const e = es[i]!;
      if (e.sel === sel && (e.sk === prev || e.sk === next)) this.refresh(e);
    }
  }

  render(items: readonly T[]): void {
    this.list.set(items);
  }

  // One listener per event type on the list's parent: the row is the
  // parent's child the event came through, its handler the one of the
  // innermost element with a handler for the type.
  private listen(type: string): void {
    const spec = this.spec,
      parent = this.parent;
    parent.addEventListener(type, (ev) => {
      let node = ev.target as Node | null;
      const path: Node[] = [];
      while (node !== null && node.parentNode !== parent) {
        path.push(node);
        node = node.parentNode;
      }
      if (node === null) return;
      const es = this.list.entries;
      let e: Row<T> | undefined;
      for (let i = 0; i < es.length; i++) {
        if (es[i]!.root === node) {
          e = es[i];
          break;
        }
      }
      if (e === undefined) return;
      path.push(node);
      for (let p = 0; p < path.length; p++) {
        for (const [t, hole, h] of spec.events!) {
          if (t === type && this.node(e, hole) === path[p]) {
            batch(() => h(e!.item)(ev));
            return;
          }
        }
      }
    });
  }
}

// ---------------------------------------------------------------------------
// What compiled code calls
// ---------------------------------------------------------------------------

/** Internal: a template from markup strings and hole markers (reactre-compiler). */
export function __template(strings: readonly string[], holes: readonly number[]): Template {
  return new Template(strings, holes);
}

/** Internal: a `<For>` in `parent`, before `anchor` (null: the list is the parent's only content). */
export function __for<T, K>(parent: Element, anchor: Node | null, spec: ForSpec<T, K>): void {
  const l = new ForList<T, K>(parent, anchor, spec);
  // Rendering the rows reads the rows' items, not what the rows read: only
  // `each` is this effect's dependency.
  effect(() => {
    const items = spec.each();
    untrack(() => l.render(items));
  });
}

/** Internal: put `value` (a node, an array of nodes, text, or nothing) before `anchor`, replacing what it put there before. */
export function __insert(anchor: Node, value: unknown, prev: Node[] | null): Node[] | null {
  const parent = anchor.parentNode!;
  if (prev !== null) for (const n of prev) parent.removeChild(n);
  if (value == null || value === false || value === true) return null;
  const out: Node[] = [];
  const add = (x: unknown): void => {
    if (x == null || x === false || x === true) return;
    if (Array.isArray(x)) {
      for (const y of x) add(y);
      return;
    }
    const n = typeof x === "object" && "nodeType" in (x as object) ? (x as Node) : document.createTextNode("" + x);
    parent.insertBefore(n, anchor);
    out.push(n);
  };
  add(value);
  return out;
}

/** Internal: a hole whose value may change: written now and whenever what it reads changes. */
export function __bind(t: Template, hole: number, node: Node, value: () => unknown): void {
  let last: unknown = NONE;
  effect(() => {
    const v = value();
    if (v !== last) {
      last = v;
      t.write(hole, node, v);
    }
  });
}

/** Internal: a slot whose content may change (a component's or a function's result). */
export function __slot(anchor: Node, value: () => unknown): void {
  let prev: Node[] | null = null;
  effect(() => {
    const v = value();
    prev = untrack(() => __insert(anchor, v, prev));
  });
}

const NONE = Symbol("none");

/** Render the component `view` into `parent`, after what it holds. Components run once. */
export function render(view: () => unknown, parent: Element): void {
  const anchor = document.createTextNode("");
  parent.appendChild(anchor);
  __insert(anchor, untrack(view), null);
}

/** `<For each key>`: a keyed list; compiled away, so only its type is used. */
export function For<T>(_props: {
  each: readonly T[];
  key: (item: T) => string | number;
  children: (item: T) => unknown;
}): unknown {
  throw new Error("reactre: <For> must be compiled by reactre-compiler");
}
