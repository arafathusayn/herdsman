// herdsman-react: the React 19 client API on herdsman-blocks.
//
// One module stands in for `react`, `react/jsx-runtime`,
// `react/jsx-dev-runtime`, `react/compiler-runtime`, `react-dom` and
// `react-dom/client`: alias those specifiers to it at build time and an
// unchanged React app (components, hooks, libraries) renders on it.
//
//   1. Fibers: one small object per element, rendered depth first and
//      synchronously. An element that is the same object as last time
//      (hoisted JSX, the React Compiler's memo cache, `children` passed
//      through) and has no pending update is skipped with its whole subtree.
//   2. Placement: only host elements (and roots and portals) place DOM nodes.
//      After a render changes which nodes a host holds, the host's node list
//      is compared with its live children: the common prefix and suffix are
//      kept, and the rest is reordered with herdsman-blocks' longest
//      increasing subsequence, so a reorder costs the minimum number of DOM
//      moves. Moves use moveBefore() where the browser has it, so a moved row
//      keeps focus, selection, iframes and running animations; React's
//      insertBefore() loses them.
//   3. Events: one listener per event type on each root (and portal
//      container), added when a prop first uses that type. A dispatch walks
//      the fiber tree from the target, so events bubble through portals as in
//      React. Non-bubbling events (scroll, load, mouseenter, media events)
//      get one listener on the element.
//   4. Effects: insertion effects run when their component completes,
//      layout effects and refs before the render returns, passive effects
//      after paint (or before the next render, whichever comes first).
//      Updates batch automatically and flush in a microtask; updates from an
//      event handler or inside flushSync flush before the handler returns.
//
// What differs from React: rendering is synchronous (no time slicing, so a
// transition renders at once and a suspended update shows its boundary's
// fallback, with the old content kept hidden and its state intact), and
// hydration claims matching nodes in order and patches mismatches in place
// instead of re-rendering on the client. There is no server renderer: use
// React's own on the server.

import { lis } from "./herdsman-blocks.ts";

/** The React version this module implements, as libraries read it. */
export const version: string = "19.2.0";

// ---------------------------------------------------------------------------
// Elements and types
// ---------------------------------------------------------------------------

// biome-ignore lint: props are whatever components declare.
type Any = any;

/** Props of any element. */
export type Props = Record<string, Any>;

/** A React element: what JSX and createElement return. */
export interface ReactElement {
  readonly $$typeof: symbol;
  readonly type: Any;
  readonly key: string | null;
  readonly ref: Any;
  readonly props: Props;
}

/** A ref object. */
export interface RefObject<T> {
  current: T;
}

const ELEMENT = Symbol.for("react.transitional.element");
const LEGACY_ELEMENT = Symbol.for("react.element");
const PORTAL_TYPE = Symbol.for("react.portal");
const CONTEXT_TYPE = Symbol.for("react.context");
const CONSUMER_TYPE = Symbol.for("react.consumer");
const MEMO_TYPE = Symbol.for("react.memo");
const FORWARD_TYPE = Symbol.for("react.forward_ref");
const LAZY_TYPE = Symbol.for("react.lazy");
const MEMO_SENTINEL = Symbol.for("react.memo_cache_sentinel");

/** `<>...</>`: children with no element of their own. */
export const Fragment: symbol = Symbol.for("react.fragment");
/** Renders its children; development checks are not run. */
export const StrictMode: symbol = Symbol.for("react.strict_mode");
/** Renders its children; nothing is measured. */
export const Profiler: symbol = Symbol.for("react.profiler");
/** Shows `fallback` while a child suspends. */
export const Suspense: symbol = Symbol.for("react.suspense");
/** Renders its children in order; reveal order is not coordinated. */
export const SuspenseList: symbol = Symbol.for("react.suspense_list");
/** Hides its children with `mode="hidden"`, keeping their state; their effects are cleaned up while hidden. */
export const Activity: symbol = Symbol.for("react.activity");
/** Renders its children; view transitions are not started. */
export const ViewTransition: symbol = Symbol.for("react.view_transition");

/** Names the transition for view transition types; with no view transitions started, it has no effect. */
export function addTransitionType(_type: string): void {}
export {
  Activity as unstable_Activity,
  SuspenseList as unstable_SuspenseList,
  ViewTransition as unstable_ViewTransition,
};

function element(type: Any, key: string | null, props: Props): ReactElement {
  return { $$typeof: ELEMENT, type, key, ref: props["ref"] ?? null, props };
}

function withDefaults(type: Any, props: Props, copy: boolean): Props {
  const dp = type != null ? type.defaultProps : undefined;
  if (dp == null) return props;
  const out = copy ? { ...props } : props;
  for (const k in dp) if (out[k] === undefined) out[k] = dp[k];
  return out;
}

/** The automatic JSX runtime's element factory. */
export function jsx(type: Any, config: Props, maybeKey?: Any): ReactElement {
  let key = maybeKey === undefined ? null : "" + maybeKey;
  let props = config;
  if ("key" in config) {
    props = {};
    for (const k in config) if (k !== "key") props[k] = config[k];
    if (config["key"] !== undefined) key = "" + config["key"];
  }
  return element(type, key, withDefaults(type, props, props === config));
}
export { jsx as jsxDEV, jsx as jsxs };

/** The classic factory: `createElement(type, props, ...children)`. */
export function createElement(type: Any, config?: Props | null, ...children: Any[]): ReactElement {
  const props: Props = {};
  let key: string | null = null;
  if (config != null) {
    for (const k in config) {
      if (k === "key") {
        if (config[k] !== undefined) key = "" + config[k];
      } else if (k !== "__self" && k !== "__source") props[k] = config[k];
    }
  }
  if (children.length === 1) props["children"] = children[0];
  else if (children.length > 1) props["children"] = children;
  return element(type, key, withDefaults(type, props, false));
}

/** A copy of `el` with `config` merged into its props (key and ref included). */
export function cloneElement(el: ReactElement, config?: Props | null, ...children: Any[]): ReactElement {
  const props: Props = { ...el.props };
  let key = el.key;
  if (config != null) {
    const dp = el.type != null ? el.type.defaultProps : undefined;
    for (const k in config) {
      if (k === "key") {
        if (config[k] !== undefined) key = "" + config[k];
      } else if (k !== "__self" && k !== "__source" && !(k === "ref" && config[k] === undefined)) {
        props[k] = config[k] === undefined && dp != null ? dp[k] : config[k];
      }
    }
  }
  if (children.length === 1) props["children"] = children[0];
  else if (children.length > 1) props["children"] = children;
  return element(el.type, key, props);
}

/** Whether `o` is a React element. */
export function isValidElement(o: unknown): o is ReactElement {
  return (
    typeof o === "object" && o !== null && ((o as Any).$$typeof === ELEMENT || (o as Any).$$typeof === LEGACY_ELEMENT)
  );
}

/** A ref object holding null. */
export function createRef<T = Any>(): RefObject<T | null> {
  return { current: null };
}

/** A component that re-renders only when `compare(prev, next)` (shallow equality by default) says the props changed. */
export function memo<T>(type: T, compare?: (a: Any, b: Any) => boolean): T {
  return { $$typeof: MEMO_TYPE, type, compare: compare ?? null } as Any;
}

/** A component that receives the ref its parent passes as a second argument. */
export function forwardRef<T>(render: (props: Any, ref: Any) => Any): T {
  return { $$typeof: FORWARD_TYPE, render } as Any;
}

/** A component loaded on first render; it suspends until `load` resolves. */
export function lazy<T>(load: () => PromiseLike<{ default: T }>): T {
  const payload: Any = { s: -1, v: load };
  return { $$typeof: LAZY_TYPE, _payload: payload, _init: initLazy } as Any;
}

function initLazy(p: Any): Any {
  if (p.s === -1) {
    const t = p.v();
    p.s = 0;
    p.v = t;
    t.then(
      (m: Any) => {
        if (p.s === 0) ((p.s = 1), (p.v = m.default));
      },
      (e: Any) => {
        if (p.s === 0) ((p.s = 2), (p.v = e));
      },
    );
  }
  if (p.s === 1) return p.v;
  throw p.v;
}

/** A context object; `<Ctx value>` (or `<Ctx.Provider value>`) provides it. */
export interface Context<T> {
  readonly $$typeof: symbol;
  _currentValue: T;
  readonly Provider: Context<T>;
  readonly Consumer: { readonly $$typeof: symbol; readonly _context: Context<T> };
  displayName?: string;
}

/** A context with a default value. */
export function createContext<T>(defaultValue: T): Context<T> {
  const ctx: Any = { $$typeof: CONTEXT_TYPE, _currentValue: defaultValue };
  ctx.Provider = ctx;
  ctx.Consumer = { $$typeof: CONSUMER_TYPE, _context: ctx };
  return ctx;
}

/** A portal: `children` rendered into `container`, with events bubbling through the React tree. */
export function createPortal(children: Any, container: Element | DocumentFragment, key?: string | null): Any {
  return { $$typeof: PORTAL_TYPE, key: key == null ? null : "" + key, children, containerInfo: container };
}

// ---------------------------------------------------------------------------
// Children
// ---------------------------------------------------------------------------

const escapeKey = (k: string): string => "$" + k.replace(/[=:]/g, (c) => (c === "=" ? "=0" : "=2"));
const escapeUserKey = (k: string): string => k.replace(/\/+/g, "$&/");
const isIterable = (o: Any): boolean =>
  typeof o === "object" &&
  o !== null &&
  !Array.isArray(o) &&
  typeof o[Symbol.iterator] === "function" &&
  o.$$typeof === undefined;

function mapInto(
  children: Any,
  out: Any[],
  prefix: string,
  name: string,
  fn: (c: Any, i: number) => Any,
  count: number[],
): void {
  const t = typeof children;
  if (children == null || t === "boolean") children = null;
  if (
    children === null ||
    t === "string" ||
    t === "number" ||
    t === "bigint" ||
    isValidElement(children) ||
    children.$$typeof === PORTAL_TYPE
  ) {
    const child = children;
    let mapped = fn(child, count[0]!++);
    const childKey = name === "" ? "." + (child !== null && child.key != null ? escapeKey("" + child.key) : "0") : name;
    if (Array.isArray(mapped)) {
      mapInto(mapped, out, prefix + escapeUserKey(childKey) + "/", "", (c) => c, [0]);
    } else if (mapped != null) {
      if (isValidElement(mapped)) {
        const mk =
          mapped.key != null && (child === null || child.key !== mapped.key)
            ? escapeUserKey("" + mapped.key) + "/"
            : "";
        mapped = element(mapped.type, prefix + mk + childKey, mapped.props);
      }
      out.push(mapped);
    }
    return;
  }
  const items = Array.isArray(children)
    ? children
    : isIterable(children)
      ? Array.from(children as Iterable<Any>)
      : null;
  if (items === null) throw new Error("Objects are not valid as a React child");
  const base = name === "" ? "." : name + ":";
  for (let i = 0; i < items.length; i++) {
    const c = items[i];
    const k = c != null && typeof c === "object" && c.key != null ? escapeKey("" + c.key) : i.toString(36);
    mapInto(c, out, prefix, base + k, fn, count);
  }
}

/** React's Children utilities. */
export const Children: {
  map<T>(children: Any, fn: (child: Any, index: number) => T, ctx?: unknown): T[];
  forEach(children: Any, fn: (child: Any, index: number) => void, ctx?: unknown): void;
  count(children: Any): number;
  toArray(children: Any): Any[];
  only(children: Any): ReactElement;
} = {
  map(children, fn, ctx) {
    if (children == null) return children;
    const out: Any[] = [];
    mapInto(children, out, "", "", (c, i) => fn.call(ctx, c, i), [0]);
    return out;
  },
  forEach(children, fn, ctx) {
    if (children != null) mapInto(children, [], "", "", (c, i) => void fn.call(ctx, c, i), [0]);
  },
  count(children) {
    const n = [0];
    if (children != null) mapInto(children, [], "", "", () => null, n);
    return n[0]!;
  },
  toArray(children) {
    const out: Any[] = [];
    if (children != null) mapInto(children, out, "", "", (c) => c, [0]);
    return out;
  },
  only(children) {
    if (!isValidElement(children))
      throw new Error("React.Children.only expected to receive a single React element child.");
    return children;
  },
};

// ---------------------------------------------------------------------------
// Fibers
// ---------------------------------------------------------------------------

// Fiber kinds.
const ROOT = 0;
const HOST = 1;
const TEXT = 2;
const FUNC = 3;
const CLASS = 4;
const FRAG = 5;
const PROVIDER = 6;
const CONSUMER = 7;
const PORTAL = 8;
const SUSPENSE = 9;
const LAZY = 10;
const ACTIVITY = 11;
const MEMO = 12; // memo() of anything but a plain function: a wrapper around one child

// Flags.
const DIRTY = 1; // must render even if its element is unchanged
const QUEUED = 2; // in the update queue
const DEAD = 4; // unmounted
const STRUCT = 8; // host: its DOM children changed; place them
const RERENDER = 16; // function: set state during its own render
const FORCE = 32; // class: forceUpdate
const FORWARD = 64; // function: forwardRef render (props, ref)
const SHOWN = 128; // suspense: its primary content has been shown
const HIDDEN = 256; // suspense, activity: content hidden
const CLAIMED = 512; // host: claimed from server HTML while hydrating
const HOIST = 1024; // host: <title>, <meta> or <link> placed in document.head

// Namespaces.
const HTML_NS = 0;
const SVG_NS = 1;
const MATH_NS = 2;
const NS_URI = ["http://www.w3.org/1999/xhtml", "http://www.w3.org/2000/svg", "http://www.w3.org/1998/Math/MathML"];

const ARRAY = Symbol("array"); // type of a fiber made from a nested array
const NO_PROPS: Props = {};
const NO_FIBERS: Fiber[] = [];
const NO_CONTEXT = {};

// DOM node expandos.
const FIBER = Symbol("fiber"); // element -> its host fiber
const OWN = Symbol("own"); // node -> the host fiber that places it
const LISTENS = Symbol("listens"); // element -> direct listener types
const TRACKED = Symbol("tracked"); // input or textarea -> the value (or checked state) last set or seen

type Key = string | number;

class Fiber {
  n: number;
  t: Any;
  k: Key;
  /** Props; for text the string; for arrays the array; for portals the portal. */
  p: Any = NO_PROPS;
  /** The element last rendered, for the identity bailout. */
  e: Any = null;
  r: Fiber | null;
  c: Fiber[] = NO_FIBERS;
  /** Index in the parent's children. */
  i = 0;
  /** DOM node, or container for roots and portals. */
  d: Any = null;
  f = 0;
  /** Depth: parents render before children in the update queue. */
  z: number;
  o: RootImpl;
  h: Hook[] | null = null;
  /** Class instance; function component's render function; provider subscribers; form status; suspense primary. */
  s: Any = null;
  /** Memo compare; class update queue; suspense fallback; form status subscribers. */
  q: Any = null;
  /** Pending effects of a function's render; a value thrown to this boundary. */
  x: Any = null;
  /** Callback ref cleanup. */
  rx: Any = null;
  /** Host: its text content when it holds only text, else null. */
  tx: string | null = null;
  /** Host: its namespace; hydration cursor holder. */
  ns = HTML_NS;
  hc: Any = null;
  constructor(n: number, t: Any, k: Key, r: Fiber | null, o: RootImpl) {
    this.n = n;
    this.t = t;
    this.k = k;
    this.r = r;
    this.z = r === null ? 0 : r.z + 1;
    this.o = o;
  }
}

// What render is doing now.
let cur: Fiber | null = null; // the function or class component rendering
let hi = 0; // its next hook index
let curHost: Fiber = null as Any; // the host that places nodes created now
let curNs = HTML_NS;
let hydrating = false;

const isThenable = (v: Any): boolean => v !== null && typeof v === "object" && typeof v.then === "function";

function kindOf(type: Any): number {
  if (typeof type === "string") return HOST;
  if (typeof type === "function") return type.prototype && type.prototype.isReactComponent ? CLASS : FUNC;
  if (type === Fragment || type === StrictMode || type === Profiler || type === SuspenseList || type === ViewTransition)
    return FRAG;
  if (type === Suspense) return SUSPENSE;
  if (type === Activity) return ACTIVITY;
  if (type != null) {
    switch (type.$$typeof) {
      case CONTEXT_TYPE:
        return PROVIDER;
      case CONSUMER_TYPE:
        return CONSUMER;
      case FORWARD_TYPE:
        return FUNC;
      case LAZY_TYPE:
        return LAZY;
      case MEMO_TYPE: {
        const inner = type.type;
        return (typeof inner === "function" && !(inner.prototype && inner.prototype.isReactComponent)) ||
          (inner != null && inner.$$typeof === FORWARD_TYPE)
          ? FUNC
          : MEMO;
      }
    }
  }
  throw new Error(
    `Element type is invalid: expected a string or a class/function but got: ${type === null ? "null" : typeof type}.`,
  );
}

// A function fiber's render function, forwardRef flag and memo compare.
function setupFunc(f: Fiber, type: Any): void {
  let t = type;
  if (t.$$typeof === MEMO_TYPE) {
    f.q = t.compare ?? shallowEqual;
    t = t.type;
  }
  if (t.$$typeof === FORWARD_TYPE) {
    f.f |= FORWARD;
    t = t.render;
  }
  f.s = t;
}

/** Shallow equality of two objects by own keys and Object.is, as React compares props. */
export function shallowEqual(a: Any, b: Any): boolean {
  if (Object.is(a, b)) return true;
  if (typeof a !== "object" || a === null || typeof b !== "object" || b === null) return false;
  const ka = Object.keys(a);
  if (ka.length !== Object.keys(b).length) return false;
  for (const k of ka) if (!Object.hasOwn(b, k) || !Object.is(a[k], b[k])) return false;
  return true;
}

// ---------------------------------------------------------------------------
// Reconciling children
// ---------------------------------------------------------------------------

// An explicit key is a string and an implicit one (the position) a number, so
// the two never collide and no key string is built per render.
function keyOf(it: Any, i: number): Key {
  return typeof it === "object" && it.key != null ? it.key : i;
}

function sameType(o: Fiber, it: Any): boolean {
  switch (typeof it) {
    case "string":
    case "number":
    case "bigint":
      return o.n === TEXT;
  }
  if (Array.isArray(it) || isIterable(it)) return o.t === ARRAY;
  if (it.$$typeof === PORTAL_TYPE) return o.n === PORTAL && o.d === it.containerInfo;
  return o.t === it.type && o.n !== TEXT;
}

// Unwraps promises (React 19 renders a resolved promise's value) and lazy
// elements' types are resolved when they render.
function unwrap(it: Any): Any {
  while (isThenable(it)) it = readThenable(it);
  return it;
}

function reconcile(p: Fiber, children: Any): void {
  const old = p.c;
  if (children == null || typeof children === "boolean") {
    if (old.length) removeAll(p);
    return;
  }
  const items: Any[] = Array.isArray(children)
    ? children
    : isIterable(children)
      ? Array.from(children as Iterable<Any>)
      : [children];
  const n = items.length,
    m = old.length;
  const nc: Fiber[] = [];
  let oi = 0,
    i = 0,
    moved = false;
  try {
    // Keys and types line up (the common case): update in place.
    for (; i < n; i++) {
      let it = items[i];
      if (it == null || typeof it === "boolean") continue;
      if (typeof it === "object" && isThenable(it)) it = items[i] = unwrap(it);
      const o = old[oi];
      if (o === undefined || o.k !== keyOf(it, i) || !sameType(o, it)) break;
      oi++;
      o.i = nc.length;
      nc.push(o);
      patch(o, it);
    }
    if (i < n || oi < m) {
      let map: Map<Key, Fiber> | null = null;
      if (oi < m) {
        map = new Map();
        for (let j = oi; j < m; j++) map.set(old[j]!.k, old[j]!);
      }
      let last = -1;
      for (; i < n; i++) {
        let it = items[i];
        if (it == null || typeof it === "boolean") continue;
        if (typeof it === "object" && isThenable(it)) it = items[i] = unwrap(it);
        const key = keyOf(it, i);
        let o = map === null ? undefined : map.get(key);
        if (o !== undefined && sameType(o, it)) {
          map!.delete(key);
          if (o.i < last) moved = true;
          else last = o.i;
          o.i = nc.length;
          nc.push(o);
          patch(o, it);
        } else {
          o = create(it, p, key);
          o.i = nc.length;
          nc.push(o);
          moved = true;
        }
      }
      if (map !== null && map.size) {
        if (nc.length === 0 && map.size === m) {
          p.c = NO_FIBERS;
          removeAll(p, old);
          return;
        }
        for (const o of map.values()) unmount(o, true);
      }
    }
  } catch (e) {
    // Keep every live fiber tracked, so the boundary that catches this can
    // retry (suspense) or unmount (errors) the whole subtree.
    p.f |= DIRTY;
    const kept = new Set(nc);
    p.c = nc.concat(old.filter((o) => !kept.has(o) && !(o.f & DEAD)));
    for (let j = 0; j < p.c.length; j++) p.c[j]!.i = j;
    curHost.f |= STRUCT;
    throw e;
  }
  p.c = nc;
  if (moved) curHost.f |= STRUCT;
}

// Remove every child of p. A host whose only content they are is cleared at once.
function removeAll(p: Fiber, old: Fiber[] = p.c): void {
  p.c = NO_FIBERS;
  if (p === curHost) {
    const nodes: Node[] = [];
    for (const o of old) collect(o, nodes);
    if (nodes.length === p.d.childNodes.length) {
      for (const o of old) unmount(o, false);
      p.d.textContent = "";
      return;
    }
  }
  for (const o of old) unmount(o, true);
}

function patch(o: Fiber, it: Any): void {
  switch (o.n) {
    case TEXT: {
      const s = "" + it;
      if (o.p !== s) {
        o.p = s;
        if (!(o.f & HIDDEN)) o.d.data = s;
      }
      return;
    }
    case FRAG:
      if (o.t === ARRAY) {
        reconcile(o, (o.p = it));
        return;
      }
      break;
    case PORTAL:
      o.p = it;
      runPortal(o, false);
      return;
  }
  update(o, it, false);
}

function create(it: Any, p: Fiber, key: Key): Fiber {
  let f: Fiber;
  if (typeof it !== "object") {
    f = new Fiber(TEXT, null, key, p, p.o);
    f.p = "" + it;
    f.d = (hydrating && claimText(f.p)) || document.createTextNode(f.p);
    f.d[OWN] = curHost;
    return f;
  }
  if (Array.isArray(it) || isIterable(it)) {
    f = new Fiber(FRAG, ARRAY, key, p, p.o);
    f.p = it;
  } else if (it.$$typeof === PORTAL_TYPE) {
    f = new Fiber(PORTAL, null, key, p, p.o);
    f.p = it;
    f.d = it.containerInfo;
  } else if (it.$$typeof === ELEMENT || it.$$typeof === LEGACY_ELEMENT) {
    f = new Fiber(kindOf(it.type), it.type, key, p, p.o);
    if (f.n === FUNC) setupFunc(f, it.type);
  } else {
    throw new Error(
      `Objects are not valid as a React child (found: object with keys {${Object.keys(it).join(", ")}}). If you meant to render a collection of children, use an array instead.`,
    );
  }
  try {
    if (f.n === FRAG && f.t === ARRAY) reconcile(f, Array.isArray(it) ? it : (f.p = Array.from(it as Iterable<Any>)));
    else if (f.n === PORTAL) runPortal(f, true);
    else update(f, it, true);
  } catch (e) {
    // A child that fails to mount is dropped: nothing of it is committed.
    unmount(f, true);
    throw e;
  }
  return f;
}

// Render an element into fiber f: mount it, or update it unless nothing changed.
function update(f: Fiber, el: ReactElement, mount: boolean): void {
  let old: Props | null = null;
  if (!mount) {
    if (el === f.e && !(f.f & DIRTY)) return;
    old = f.p;
    if (f.q !== null && (f.n === FUNC || f.n === MEMO) && !(f.f & DIRTY) && f.e !== null) {
      if (f.q(old, el.props) && old!["ref"] === el.props["ref"]) {
        f.e = el;
        return;
      }
    }
  }
  f.e = el;
  f.p = el.props;
  run(f, old, mount);
}

function run(f: Fiber, old: Props | null, mount: boolean): void {
  f.f &= ~DIRTY;
  switch (f.n) {
    case HOST:
      return runHost(f, old, mount);
    case FUNC:
      return runFunc(f);
    case CLASS:
      if (classes === null) throw noClasses(f.t);
      return classes.run(f, old, mount);
    case FRAG:
      return reconcile(f, f.t === ARRAY ? f.p : f.p.children);
    case PROVIDER: {
      if (old !== null && !Object.is(old["value"], f.p.value) && f.s !== null) {
        for (const s of f.s as Set<Fiber>) {
          if (s.f & DEAD) f.s.delete(s);
          else schedule(s);
        }
      }
      return reconcile(f, f.p.children);
    }
    case CONSUMER: {
      const render = f.p.children;
      return reconcile(f, typeof render === "function" ? render(readContext(f, f.t._context)) : null);
    }
    case PORTAL:
      return runPortal(f, mount);
    case SUSPENSE:
      return runSuspense(f);
    case ACTIVITY:
      return runActivity(f);
    case LAZY: {
      const type = f.t._init(f.t._payload);
      return reconcile(f, element(type, null, f.p));
    }
    case MEMO:
      if (f.q === null) f.q = f.t.compare ?? shallowEqual;
      return reconcile(f, element(f.t.type, null, f.p));
    case ROOT:
      return runRoot(f);
  }
}

// ---------------------------------------------------------------------------
// Placing DOM nodes
// ---------------------------------------------------------------------------

// The top-level DOM nodes of f's children, in order (portals place their own).
function collect(f: Fiber, out: Node[]): void {
  if (f.n === HOST || f.n === TEXT) {
    if (!(f.f & HOIST)) out.push(f.d);
  } else if (f.n !== PORTAL) for (const c of f.c) collect(c, out);
}

// The host-level fibers (elements and text) at the top of f's subtree.
function tops(f: Fiber, out: Fiber[]): void {
  if (f.n === HOST || f.n === TEXT) {
    if (!(f.f & HOIST)) out.push(f);
  } else if (f.n !== PORTAL) for (const c of f.c) tops(c, out);
}

function next(x: Any, h: Fiber): Any {
  while (x !== null && x[OWN] !== h) x = x.nextSibling;
  return x;
}
function prev(x: Any, h: Fiber): Any {
  while (x !== null && x[OWN] !== h) x = x.previousSibling;
  return x;
}

type Movable = Node & { moveBefore?: (node: Node, child: Node | null) => void };

// Move a node already in parent: keep its state where the browser can.
function move(parent: Node, node: Node, ref: Node | null): void {
  const m = parent as Movable;
  if (m.moveBefore !== undefined && parent.isConnected) m.moveBefore(node, ref);
  else parent.insertBefore(node, ref);
}

// Make host h's DOM children match its fibers' top-level nodes with the
// fewest moves: keep the common prefix and suffix, keep the longest run of
// the rest that is already in order, and move or insert everything else.
// Nodes h does not own (another root's, a portal's, a library's) are left alone.
function place(h: Fiber): void {
  h.f &= ~STRUCT;
  const parent = h.d as Node;
  const want: Node[] = [];
  for (const c of h.c) collect(c, want);
  const n = want.length;
  let node = next(parent.firstChild, h),
    i = 0;
  while (i < n && node === want[i]) {
    i++;
    node = next(node.nextSibling, h);
  }
  if (i === n) {
    while (node !== null) {
      const nx = next(node.nextSibling, h);
      parent.removeChild(node);
      node = nx;
    }
    return;
  }
  let tail = prev(parent.lastChild, h),
    j = n - 1;
  while (j >= i && tail === want[j]) {
    j--;
    tail = prev(tail.previousSibling, h);
  }
  const end = j + 1 < n ? want[j + 1]! : null;
  const old: Node[] = [];
  for (let x = node; x !== null && x !== end; x = next(x.nextSibling, h)) old.push(x);
  const len = j - i + 1;
  if (old.length === 0) {
    for (let k = i; k <= j; k++) parent.insertBefore(want[k]!, end);
    return;
  }
  const pos = new Map<Node, number>();
  for (let k = 0; k < old.length; k++) pos.set(old[k]!, k);
  const src = new Int32Array(len);
  for (let k = 0; k < len; k++) {
    const at = pos.get(want[i + k]!);
    if (at === undefined) src[k] = -1;
    else {
      src[k] = at;
      pos.delete(want[i + k]!);
    }
  }
  for (const stale of pos.keys()) parent.removeChild(stale);
  const keep = lis(src);
  let li = keep.length - 1,
    ref = end;
  for (let k = len - 1; k >= 0; k--) {
    const x = want[i + k]!;
    if (src[k] === -1) parent.insertBefore(x, ref);
    else if (li >= 0 && keep[li] === k) li--;
    else move(parent, x, ref);
    ref = x;
  }
}

// ---------------------------------------------------------------------------
// Host elements
// ---------------------------------------------------------------------------

function nsOfContainer(d: Any): number {
  const uri = d.namespaceURI;
  return uri === NS_URI[SVG_NS] && d.localName !== "foreignObject"
    ? SVG_NS
    : uri === NS_URI[MATH_NS]
      ? MATH_NS
      : HTML_NS;
}

function hostNs(h: Fiber): number {
  return h.n === HOST ? (h.t === "foreignObject" ? HTML_NS : h.ns) : nsOfContainer(h.d);
}

function runHost(f: Fiber, old: Props | null, mount: boolean): void {
  const p = f.p;
  let dom = f.d;
  if (mount) {
    const type: string = f.t;
    f.ns = type === "svg" ? SVG_NS : type === "math" ? MATH_NS : curNs;
    dom = hydrating ? claimElement(f, type) : null;
    if (dom === null) {
      dom =
        f.ns === HTML_NS
          ? document.createElement(type, p.is ? { is: p.is } : undefined)
          : document.createElementNS(NS_URI[f.ns]!, type);
      if (type === "select") {
        if (p.multiple) dom.multiple = true;
        else if (p.size) dom.size = p.size;
      }
    } else f.f |= CLAIMED;
    f.d = dom;
    dom[FIBER] = f;
    if (hoistable(f, p)) {
      // React 19 document metadata: rendered anywhere, placed in <head>.
      f.f |= HOIST;
      dom[OWN] = null;
      document.head.appendChild(dom);
    } else dom[OWN] = curHost;
  }
  setProps(f, dom, old ?? NO_PROPS, p, mount);
  const ph = curHost,
    pn = curNs;
  curHost = f;
  curNs = hostNs(f);
  try {
    hostChildren(f, dom, p, old);
  } finally {
    curHost = ph;
    curNs = pn;
  }
  if (f.f & CLAIMED) {
    // Hydration: nodes the server rendered that nothing claimed go.
    f.f &= ~CLAIMED;
    if (f.tx === null && p.dangerouslySetInnerHTML == null) removeUnclaimed(dom, f);
  }
  if (f.f & STRUCT) place(f);
  if (f.t === "select") setSelect(dom, p, mount);
  const ref = p.ref,
    oref = old === null ? undefined : old["ref"];
  if (ref !== oref) {
    if (oref != null) detachRef(f, oref);
    if (ref != null) later(f, () => attachRef(f, ref, dom));
  }
  if (mount && p.autoFocus && typeof dom.focus === "function") later(f, () => dom.focus());
}

function hoistable(f: Fiber, p: Any): boolean {
  const t = f.t;
  if (f.ns !== HTML_NS || p.itemProp != null || (t !== "title" && t !== "meta" && t !== "link")) return false;
  if (t === "link") return p.onLoad == null && p.onError == null && (p.rel !== "stylesheet" || p.precedence != null);
  return true;
}

function hostChildren(f: Fiber, dom: Any, p: Any, old: Props | null): void {
  const html = p["dangerouslySetInnerHTML"];
  if (html != null) {
    if (f.c.length) removeAll(f);
    const s = html.__html ?? "";
    if (old === null || old["dangerouslySetInnerHTML"]?.__html !== s || f.tx !== null) {
      if (!(hydrating && dom.innerHTML === s)) dom.innerHTML = s;
    }
    f.tx = null;
    return;
  }
  const ch = f.t === "textarea" ? null : p.children;
  const t = typeof ch;
  if (t === "string" || t === "number" || t === "bigint") {
    const s = "" + ch;
    if (f.c.length) {
      removeAll(f);
      dom.textContent = s;
    } else if (f.tx !== s || (old !== null && old["dangerouslySetInnerHTML"] != null)) {
      const fc = dom.firstChild;
      if (fc !== null && fc.nodeType === 3 && fc.nextSibling === null) {
        if (fc.data !== s) fc.data = s;
      } else dom.textContent = s;
    }
    f.tx = s;
    return;
  }
  if (f.tx !== null || (old !== null && old["dangerouslySetInnerHTML"] != null)) {
    if (!hydrating) dom.textContent = "";
    f.tx = null;
  }
  reconcile(f, ch);
}

const BOOL_ATTRS = new Set(
  (
    "allowfullscreen async autofocus autoplay controls default defer disabled disablepictureinpicture " +
    "disableremoteplayback formnovalidate hidden inert loop nomodule novalidate open playsinline readonly " +
    "required reversed scoped seamless itemscope capture download multiple muted selected"
  ).split(" "),
);
const BOOLEANISH = new Set([
  "contentEditable",
  "draggable",
  "spellCheck",
  "value",
  "autoReverse",
  "externalResourcesRequired",
  "focusable",
  "preserveAlpha",
]);
// Props whose attribute has another name, in every namespace (React's table):
// the HTML ones below, and the presentation attributes SVG spells in
// kebab-case. Every other prop is set under its own name, so camelCase SVG
// attributes (viewBox, gradientUnits, ...) keep their case.
const ALIASES: Record<string, string> = {
  className: "class",
  htmlFor: "for",
  httpEquiv: "http-equiv",
  acceptCharset: "accept-charset",
  tabIndex: "tabindex",
  crossOrigin: "crossorigin",
  xmlnsXlink: "xmlns:xlink",
};
for (const name of (
  "accentHeight alignmentBaseline arabicForm baselineShift capHeight clipPath clipRule colorInterpolation " +
  "colorInterpolationFilters colorProfile colorRendering dominantBaseline enableBackground fillOpacity fillRule " +
  "floodColor floodOpacity fontFamily fontSize fontSizeAdjust fontStretch fontStyle fontVariant fontWeight glyphName " +
  "glyphOrientationHorizontal glyphOrientationVertical horizAdvX horizOriginX imageRendering letterSpacing " +
  "lightingColor markerEnd markerMid markerStart maskType overlinePosition overlineThickness paintOrder " +
  "pointerEvents renderingIntent shapeRendering stopColor stopOpacity strikethroughPosition strikethroughThickness " +
  "strokeDasharray strokeDashoffset strokeLinecap strokeLinejoin strokeMiterlimit strokeOpacity strokeWidth " +
  "textAnchor textDecoration textRendering transformOrigin underlinePosition underlineThickness unicodeBidi " +
  "unicodeRange unitsPerEm vAlphabetic vHanging vIdeographic vMathematical vectorEffect vertAdvY vertOriginX " +
  "vertOriginY wordSpacing writingMode xHeight"
).split(" "))
  ALIASES[name] = name.replace(/[A-Z]/g, (c) => "-" + c.toLowerCase());
const RESERVED = new Set([
  "children",
  "key",
  "ref",
  "dangerouslySetInnerHTML",
  "suppressHydrationWarning",
  "suppressContentEditableWarning",
  "innerHTML",
  "autoFocus",
  "defaultValue",
  "defaultChecked",
]);
const ACTION_URL =
  "javascript:throw new Error('A React form was unexpectedly submitted. If you called form.submit() manually, consider using form.requestSubmit() instead. If you\\'re trying to use event.stopPropagation() in a submit event handler, consider also calling event.preventDefault().')";
const XLINK = "http://www.w3.org/1999/xlink";
const XML = "http://www.w3.org/XML/1998/namespace";
// Style properties that take plain numbers (Preact's test, which covers React's unitless list).
const UNITLESS = /acit|ex(?:s|g|n|p|$)|rph|grid|ows|mnc|ntw|ine[ch]|zoo|^ord|itera/i;

function setProps(f: Fiber, dom: Any, o: Props, p: Any, mount: boolean): void {
  const tag: string = f.t;
  // Inputs take type, value, checked and name after their other attributes, in
  // that order, as React sets them; textareas and selects take their value.
  const skip = tag === "input" ? INPUT_LAST : tag === "textarea" || tag === "select" ? VALUE_LAST : null;
  for (const k in o) {
    if (!(k in p) && !RESERVED.has(k) && (skip === null || !skip.has(k))) setAttr(f, dom, k, null, o[k]);
  }
  for (const k in p) {
    const v = p[k];
    if (v === o[k] || RESERVED.has(k) || (skip !== null && skip.has(k))) continue;
    setAttr(f, dom, k, v, o[k]);
  }
  if (tag === "input") setInput(dom, p, o, mount);
  if (mount && (tag === "input" || tag === "textarea")) track(dom);
  if (tag === "textarea") {
    const v = p.value;
    if (v != null) {
      const s = toStr(v);
      if (mount) dom.defaultValue = s;
      else {
        if (dom.value !== s) dom.value = s;
        if (p.defaultValue == null && dom.defaultValue !== s) dom.defaultValue = s;
      }
    } else if (mount) {
      const d = p.defaultValue ?? p.children;
      if (d != null) dom.defaultValue = toStr(Array.isArray(d) ? d[0] : d);
    }
  }
}

const INPUT_LAST = new Set(["type", "value", "checked", "name"]);
const VALUE_LAST = new Set(["value"]);
// Strings for type and name: functions, symbols and booleans are left out.
const plain = (v: Any): boolean =>
  v != null && typeof v !== "function" && typeof v !== "symbol" && typeof v !== "boolean";

function setInput(dom: Any, p: Any, o: Props, mount: boolean): void {
  const type = p.type,
    v = p.value,
    dv = p.defaultValue,
    button = type === "submit" || type === "reset";
  if (mount) {
    if (plain(type)) dom.type = type;
    if ((v != null || dv != null) && !(button && v == null)) {
      const d = dv != null ? toStr(dv) : "";
      const s = v != null ? toStr(v) : d;
      if (dom.value !== s) dom.value = s;
      dom.defaultValue = s;
    }
    const c = p.checked ?? p.defaultChecked;
    dom.checked = dom.defaultChecked = !!c && typeof c !== "function" && typeof c !== "symbol";
    if (plain(p.name)) dom.name = p.name;
    return;
  }
  if (type !== o["type"]) {
    if (plain(type)) dom.type = type;
    else dom.removeAttribute("type");
  }
  if (v != null) {
    const s = toStr(v);
    if (type === "number" ? (v === 0 && dom.value === "") || dom.value != v : dom.value !== s) dom.value = s;
    const d = type === "number" && dom.value == v ? dom.value : s;
    if (dom.defaultValue !== d) dom.defaultValue = d;
  } else {
    if (button) dom.removeAttribute("value");
    if (dv != null) {
      if (dom.defaultValue !== toStr(dv)) dom.defaultValue = toStr(dv);
    } else if (o["defaultValue"] != null) dom.removeAttribute("value");
  }
  if (p.checked == null && p.defaultChecked != null) dom.defaultChecked = !!p.defaultChecked;
  if (p.checked != null) dom.checked = !!p.checked && typeof p.checked !== "function" && typeof p.checked !== "symbol";
  if (p.name !== o["name"]) {
    if (plain(p.name)) dom.name = "" + p.name;
    else dom.removeAttribute("name");
  }
}

const toStr = (v: Any): string =>
  typeof v === "function" || typeof v === "symbol" || typeof v === "boolean" ? "" : "" + v;

function setSelect(dom: Any, p: Any, mount: boolean): void {
  const v = p.value != null ? p.value : mount ? p.defaultValue : undefined;
  if (v == null) return;
  const opts = dom.options;
  if (p.multiple) {
    const want = new Set((v as Any[]).map(toStr));
    for (let i = 0; i < opts.length; i++) {
      const sel = want.has(opts[i].value);
      if (opts[i].selected !== sel) opts[i].selected = sel;
    }
    return;
  }
  const s = toStr(v);
  for (let i = 0; i < opts.length; i++) {
    if (opts[i].value === s) {
      if (!opts[i].selected) opts[i].selected = true;
      return;
    }
  }
  if (opts.length) opts[0].selected = true;
}

function setAttr(f: Fiber, dom: Any, name: string, v: Any, old: Any): void {
  if (name.length > 2 && name.charCodeAt(0) === 111 && name.charCodeAt(1) === 110 && name.charCodeAt(2) < 91) {
    // onX: events read the fiber's props when they fire; only listening is set up here.
    if (v != null) listenFor(dom, name);
    return;
  }
  if (name === "style") return setStyle(dom, v, old);
  if ((name === "action" || name === "formAction") && typeof v === "function") {
    // Submitting runs the function (see submitAction); the URL React puts here
    // stops a submission that bypasses the submit event.
    ensure("submit", false);
    dom.setAttribute(name.toLowerCase(), ACTION_URL);
    return;
  }
  if (f.ns === HTML_NS && f.t.includes("-") && name in dom && typeof v !== "string" && v != null) {
    dom[name] = v; // custom element property
    return;
  }
  let attr = ALIASES[name] ?? name;
  let ns: string | null = null;
  if (f.ns === SVG_NS) {
    if (name.startsWith("xlink") && name.length > 5) ((ns = XLINK), (attr = "xlink:" + name.slice(5).toLowerCase()));
    else if (name.startsWith("xml") && name.length > 3 && name.charCodeAt(3) < 91 && name !== "xmlnsXlink")
      ((ns = XML), (attr = "xml:" + name.slice(3).toLowerCase()));
  }
  if (v == null || typeof v === "function" || typeof v === "symbol") return removeAttr(dom, ns, attr);
  if (typeof v === "boolean") {
    if (name.startsWith("data-") || name.startsWith("aria-") || BOOLEANISH.has(name)) v = "" + v;
    else if (BOOL_ATTRS.has(attr.toLowerCase())) {
      if (!v) return removeAttr(dom, ns, attr);
      v = "";
    } else return removeAttr(dom, ns, attr);
  }
  if (attr === "muted" && f.t === "video") dom.muted = !!v;
  if (ns !== null) dom.setAttributeNS(ns, attr, "" + v);
  else dom.setAttribute(attr, "" + v);
}

function removeAttr(dom: Any, ns: string | null, attr: string): void {
  if (ns !== null) dom.removeAttributeNS(ns, attr.slice(attr.indexOf(":") + 1));
  else dom.removeAttribute(attr);
}

function setStyle(dom: Any, v: Any, old: Any): void {
  const s = dom.style;
  if (typeof v === "string") {
    s.cssText = v;
    return;
  }
  if (typeof old === "string") {
    s.cssText = "";
    old = null;
  }
  if (old != null) for (const k in old) if (v == null || !(k in v)) styleProp(s, k, null);
  if (v != null) for (const k in v) if (old == null || v[k] !== old[k]) styleProp(s, k, v[k]);
}

function styleProp(s: Any, k: string, v: Any): void {
  const val =
    v == null || typeof v === "boolean" || v === ""
      ? ""
      : typeof v === "number" && v !== 0 && k.charCodeAt(0) !== 45 && !UNITLESS.test(k)
        ? v + "px"
        : "" + v;
  if (k.charCodeAt(0) === 45) s.setProperty(k, val);
  else s[k === "float" ? "cssFloat" : k] = val;
}

// ---------------------------------------------------------------------------
// Hydration
// ---------------------------------------------------------------------------

// The next server node in the host being hydrated, skipping comments.
function cursor(h: Fiber): Any {
  let x = h.hc;
  while (x !== null && (x.nodeType === 8 || x.nodeType === 10)) x = x.nextSibling;
  return x;
}

function claimElement(f: Fiber, type: string): Any {
  const h = curHost;
  if (h.hc === null) return null;
  const x = cursor(h);
  if (x === null || x.nodeType !== 1 || x.localName !== (f.ns === HTML_NS ? type.toLowerCase() : type)) {
    h.hc = x;
    return null;
  }
  h.hc = x.nextSibling;
  f.hc = x.firstChild;
  return x;
}

function claimText(s: string): Any {
  const h = curHost;
  if (h.hc === null) return null;
  const x = cursor(h);
  if (x === null || x.nodeType !== 3) {
    h.hc = x;
    return null;
  }
  if (x.data !== s) {
    if (s !== "" && x.data.startsWith(s)) x.splitText(s.length);
    else x.data = s;
  }
  h.hc = x.nextSibling;
  return x;
}

function removeUnclaimed(dom: Any, h: Fiber): void {
  for (let x = dom.firstChild; x !== null;) {
    const nx = x.nextSibling;
    if (x[OWN] !== h && x.nodeType !== 8) dom.removeChild(x);
    x = nx;
  }
  h.hc = null;
}

// ---------------------------------------------------------------------------
// Roots and portals
// ---------------------------------------------------------------------------

/** What createRoot and hydrateRoot return. */
export interface Root {
  render(children: Any): void;
  unmount(): void;
}

/** Options for createRoot and hydrateRoot. */
export interface RootOptions {
  identifierPrefix?: string;
  onUncaughtError?: (error: unknown, info: { componentStack?: string }) => void;
  onCaughtError?: (error: unknown, info: { componentStack?: string }) => void;
  onRecoverableError?: (error: unknown, info: { componentStack?: string }) => void;
}

class RootImpl implements Root {
  readonly f: Fiber;
  readonly prefix: string;
  readonly opts: RootOptions;
  ids = 0;
  hydrate = false;
  mounted = false;
  readonly listeners = new Map<Node, Listener>();
  constructor(container: Any, opts: RootOptions) {
    this.opts = opts;
    this.prefix = opts.identifierPrefix ?? "";
    this.f = new Fiber(ROOT, null, 0, null, this);
    this.f.d = container;
    this.f.p = null;
    listenOn(container, this);
  }
  render(children: Any): void {
    if (this.f.f & DEAD) throw new Error("Cannot update an unmounted root.");
    this.f.p = children;
    schedule(this.f);
  }
  unmount(): void {
    if (this.f.f & DEAD) return;
    const f = this.f;
    flushSync(() => {
      const ph = curHost;
      curHost = f;
      try {
        removeAll(f);
      } finally {
        curHost = ph;
      }
    });
    f.f |= DEAD;
    for (const l of this.listeners.values()) l.remove();
    this.listeners.clear();
    roots.delete(this);
  }
}

const roots = new Set<RootImpl>();

/** A root that renders into `container`, replacing its content on the first render. */
export function createRoot(container: Element | DocumentFragment, options: RootOptions = {}): Root {
  const r = new RootImpl(container, options);
  roots.add(r);
  return r;
}

/**
 * A root that adopts the server-rendered HTML in `container`: the first
 * render claims matching nodes in order, patches text and attributes that
 * differ, and replaces nodes that do not match.
 */
export function hydrateRoot(container: Element | Document, children: Any, options: RootOptions = {}): Root {
  const r = new RootImpl(container, options);
  roots.add(r);
  r.hydrate = true;
  r.render(children);
  flushWork(true);
  return r;
}

function runRoot(f: Fiber): void {
  const r = f.o;
  const ph = curHost,
    pn = curNs,
    ph2 = hydrating;
  curHost = f;
  curNs = nsOfContainer(f.d);
  let thrown = f.x;
  f.x = null;
  if (!r.mounted) {
    r.mounted = true;
    if (r.hydrate) {
      hydrating = true;
      f.hc = f.d.firstChild;
    } else f.d.textContent = "";
  }
  try {
    if (thrown === null) {
      try {
        reconcile(f, f.p);
      } catch (e) {
        thrown = e;
      }
    }
    if (hydrating) {
      hydrating = ph2;
      if (f.d.nodeType !== 9) removeUnclaimed(f.d, f);
    }
    if (thrown !== null) {
      if (isThenable(thrown)) thrown.then(retry(f), retry(f));
      else {
        removeAll(f);
        const report = r.opts.onUncaughtError;
        if (report) report(thrown, { componentStack: "" });
        else reportError(thrown);
      }
    }
  } finally {
    curHost = ph;
    curNs = pn;
    hydrating = ph2;
  }
  if (f.f & STRUCT) place(f);
}

function runPortal(f: Fiber, mount: boolean): void {
  if (mount) listenOn(f.d, f.o);
  const ph = curHost,
    pn = curNs;
  curHost = f;
  curNs = nsOfContainer(f.d);
  try {
    reconcile(f, f.p.children);
  } finally {
    curHost = ph;
    curNs = pn;
  }
  if (f.f & STRUCT) place(f);
}

// ---------------------------------------------------------------------------
// Components
// ---------------------------------------------------------------------------

function runFunc(f: Fiber): void {
  const fn = f.s,
    props = f.p,
    prevCur = cur,
    prevHi = hi;
  let out: Any,
    tries = 0;
  cur = f;
  try {
    let args: Any = props,
      ref: Any;
    if (f.f & FORWARD) {
      if ("ref" in props) {
        ref = props.ref;
        args = { ...props };
        delete args.ref;
      } else ref = null;
    }
    do {
      if (++tries > 25)
        throw new Error("Too many re-renders. React limits the number of renders to prevent an infinite loop.");
      f.f &= ~(RERENDER | DIRTY);
      hi = 0;
      f.x = null;
      out = ref === undefined ? fn(args) : fn(args, ref);
    } while (f.f & RERENDER);
  } catch (e) {
    f.f |= DIRTY;
    throw e;
  } finally {
    cur = prevCur;
    hi = prevHi;
  }
  reconcile(f, out);
  if (f.x !== null) commitEffects(f);
}

// Class components

/** A class component instance. */
export interface Component<P = Props, S = Any> {
  props: P;
  context: Any;
  state: S;
  refs: Record<string, Any>;
  /** Merge `partial` (or what it returns, given state and props) into state, then re-render. */
  setState(partial: Any, callback?: () => void): void;
  /** Re-render, skipping shouldComponentUpdate. */
  forceUpdate(callback?: () => void): void;
  render?(): Any;
  componentDidMount?(): void;
  shouldComponentUpdate?(nextProps: P, nextState: S, nextContext: Any): boolean;
  getSnapshotBeforeUpdate?(prevProps: P, prevState: S): Any;
  componentDidUpdate?(prevProps: P, prevState: S, snapshot?: Any): void;
  componentWillUnmount?(): void;
  componentDidCatch?(error: Any, info: { componentStack?: string }): void;
}

/** The type of Component and PureComponent: classes extend them. */
export interface ComponentClass {
  new <P = Props, S = Any>(props: P, context?: Any): Component<P, S>;
  readonly prototype: Component;
}

// Constructor functions, as React's are: libraries compiled to ES5 subclass
// them with `Component.call(this, props, context)`, which an ES class forbids.
function ComponentFn(this: Any, props: Any, context?: Any): void {
  this.props = props;
  this.context = context;
  this.refs = {};
}
ComponentFn.prototype.isReactComponent = {};
ComponentFn.prototype.setState = function (this: Any, partial: Any, callback?: () => void): void {
  if (classes !== null) classes.enqueue(this, partial, callback, false);
};
ComponentFn.prototype.forceUpdate = function (this: Any, callback?: () => void): void {
  if (classes !== null) classes.enqueue(this, null, callback, true);
};

function PureComponentFn(this: Any, props: Any, context?: Any): void {
  ComponentFn.call(this, props, context);
}
PureComponentFn.prototype = Object.create(ComponentFn.prototype);
PureComponentFn.prototype.constructor = PureComponentFn;
PureComponentFn.prototype.isPureReactComponent = true;

/** Base class for class components. */
export const Component: ComponentClass = ComponentFn as Any;
/** A class component that re-renders only when its props or state change (shallowly). */
export const PureComponent: ComponentClass = PureComponentFn as Any;

// Class support is a separate module (herdsman-react-classes.ts), so
// apps without class components do not ship it. Its entry installs it here.

/** What class support uses of the runtime. Fibers are opaque to it. */
export interface ClassCore {
  readonly DIRTY: number;
  readonly FORCE: number;
  readonly RERENDER: number;
  readonly DEAD: number;
  readonly NO_CONTEXT: object;
  reconcile(f: Any, children: Any): void;
  removeAll(f: Any): void;
  readContext(f: Any, ctx: Any): Any;
  later(f: Any, fn: () => void): void;
  attachRef(f: Any, ref: Any, v: Any): void;
  detachRef(f: Any, ref: Any): void;
  schedule(f: Any): void;
  /** Run fn with f as the component rendering now. */
  rendering<T>(f: Any, fn: () => T): T;
  /** Whether f is the component rendering now. */
  isRendering(f: Any): boolean;
}

/** What class support provides. */
export interface ClassSupport {
  run(f: Any, old: Props | null, mount: boolean): void;
  enqueue(inst: Any, partial: Any, cb: (() => void) | undefined, force: boolean): void;
  boundary(f: Any): boolean;
  unmount(f: Any): void;
}

let classes: ClassSupport | null = null;

/** Installs class support; herdsman-react-classes.ts calls it when it loads. */
export function __installClasses(make: (core: ClassCore) => ClassSupport): void {
  classes ??= make({
    DIRTY,
    FORCE,
    RERENDER,
    DEAD,
    NO_CONTEXT,
    reconcile,
    removeAll: (f) => removeAll(f),
    readContext,
    later,
    attachRef,
    detachRef,
    schedule,
    rendering(f, fn) {
      const prev = cur;
      cur = f;
      try {
        return fn();
      } finally {
        cur = prev;
      }
    },
    isRendering: (f) => f === cur,
  });
}

function noClasses(type: Any): Error {
  return new Error(
    `${type.displayName ?? type.name ?? "A class component"} is a class component, and this build leaves class support out. Alias react to herdsman-react-classes.ts (reactAlias("herdsman-react", { classes: true })) to include it.`,
  );
}

// Refs

function attachRef(f: Fiber, ref: Any, v: Any): void {
  if (typeof ref === "function") {
    const r = ref(v);
    if (typeof r === "function" && f.n !== CLASS) f.rx = r;
  } else if (ref !== null && typeof ref === "object") ref.current = v;
}

function detachRef(f: Fiber, ref: Any): void {
  if (f.n !== CLASS && typeof f.rx === "function") {
    const r = f.rx;
    f.rx = null;
    r();
  } else if (typeof ref === "function") ref(null);
  else if (ref !== null && typeof ref === "object") ref.current = null;
}

// Context

function readContext(f: Fiber, ctx: Any): Any {
  for (let p = f.r; p !== null; p = p.r) {
    if (p.n === PROVIDER && p.t === ctx) {
      (p.s ??= new Set<Fiber>()).add(f);
      return p.p.value;
    }
  }
  return ctx._currentValue;
}

// Suspense and Activity

// Hide or show the top-level nodes of f's subtree, as React does: elements
// get display: none !important; text is emptied.
function setHidden(f: Fiber, hide: boolean): void {
  const list: Fiber[] = [];
  tops(f, list);
  for (const h of list) {
    if (hide === ((h.f & HIDDEN) !== 0)) continue;
    h.f ^= HIDDEN;
    if (h.n === TEXT) h.d.data = hide ? "" : h.p;
    else if (hide) h.d.style.setProperty("display", "none", "important");
    else {
      h.d.style.removeProperty("display");
      const d = h.p.style != null && typeof h.p.style === "object" ? h.p.style.display : undefined;
      if (d != null && d !== "") h.d.style.display = d;
    }
  }
}

function frag(p: Fiber, k: Key): Fiber {
  return new Fiber(FRAG, Fragment, k, p, p.o);
}

function runSuspense(f: Fiber): void {
  let thrown = f.x;
  f.x = null;
  let primary: Fiber | null = f.s;
  if (thrown === null) {
    try {
      if (primary === null) {
        primary = frag(f, 0);
        try {
          reconcile(primary, f.p.children);
        } catch (e) {
          unmount(primary, true);
          throw e;
        }
        f.s = primary;
      } else reconcile(primary, f.p.children);
      f.f |= SHOWN;
      if (f.q !== null) {
        setHidden(primary, false);
        unmount(f.q, true);
        f.q = null;
        curHost.f |= STRUCT;
      }
      if (f.c.length !== 1 || f.c[0] !== primary) {
        f.c = [primary];
        curHost.f |= STRUCT;
      }
      return;
    } catch (e) {
      if (!isThenable(e)) throw e;
      thrown = e;
    }
  }
  // Suspended: keep shown content hidden with its state; drop content never shown.
  f.f &= ~DIRTY;
  primary = f.s;
  if (primary !== null && !(f.f & SHOWN)) {
    unmount(primary, true);
    primary = f.s = null;
  }
  if (primary !== null) {
    primary.f |= DIRTY;
    setHidden(primary, true);
  }
  let fb: Fiber | null = f.q;
  if (fb === null) {
    fb = f.q = frag(f, 1);
    try {
      reconcile(fb, f.p.fallback);
    } catch (e) {
      f.q = null;
      unmount(fb, true);
      throw e;
    }
  } else reconcile(fb, f.p.fallback);
  f.c = primary !== null ? [primary, fb] : [fb];
  f.c.forEach((c, i) => (c.i = i));
  curHost.f |= STRUCT;
  const again = retry(f);
  thrown.then(again, again);
}

function retry(f: Fiber): () => void {
  return () => schedule(f);
}

function runActivity(f: Fiber): void {
  const hide = f.p.mode === "hidden";
  let inner: Fiber | null = f.s;
  if (inner === null) {
    inner = f.s = frag(f, 0);
    f.c = [inner];
    curHost.f |= STRUCT;
  }
  const was = (f.f & HIDDEN) !== 0;
  reconcile(inner, f.p.children);
  if (hide !== was) {
    f.f ^= HIDDEN;
    setHidden(inner, hide);
    if (hide) disconnect(inner);
    else reconnect(inner);
  }
}

// Clean up and later re-run the effects of a hidden Activity's subtree.
function disconnect(f: Fiber): void {
  if (f.h !== null) {
    for (const h of f.h) {
      if (h.k !== 0 && h.x !== undefined) {
        const x = h.x;
        h.x = undefined;
        safely(f, x);
      }
    }
  }
  for (const c of f.c) disconnect(c);
}

function reconnect(f: Fiber): void {
  for (const c of f.c) reconnect(c);
  if (f.h !== null) for (const h of f.h) if (h.k === LAYOUT) layoutQ.push(h);
  if (f.h !== null) for (const h of f.h) if (h.k === PASSIVE) passiveQ.push(h);
}

// ---------------------------------------------------------------------------
// Unmounting
// ---------------------------------------------------------------------------

function unmount(f: Fiber, rm: boolean): void {
  f.f |= DEAD;
  switch (f.n) {
    case HOST:
      if ((rm || f.f & HOIST) && f.d.parentNode !== null) f.d.parentNode.removeChild(f.d);
      if (f.p.ref != null) safely(f, () => detachRef(f, f.p.ref));
      for (const c of f.c) unmount(c, false);
      return;
    case TEXT:
      if (rm && f.d.parentNode !== null) f.d.parentNode.removeChild(f.d);
      return;
    case FUNC:
      if (f.h !== null) {
        for (const h of f.h) {
          if (h.x === undefined) continue;
          if (h.k !== PASSIVE) safely(f, h.x);
          else if (h.k === PASSIVE) passiveUnmountQ.push(h.x);
          h.x = undefined;
        }
      }
      break;
    case CLASS:
      if (classes !== null) safely(f, () => classes!.unmount(f));
      break;
    case PORTAL:
      for (const c of f.c) unmount(c, true);
      return;
  }
  for (const c of f.c) unmount(c, rm);
}

// ---------------------------------------------------------------------------
// Scheduling
// ---------------------------------------------------------------------------

let queue: Fiber[] = [];
let flushing = false;
let batch = 0;
let micro = false;
let passiveTimer = false;

function schedule(f: Fiber): void {
  if (f.f & DEAD) return;
  f.f |= DIRTY;
  if (!(f.f & QUEUED)) {
    f.f |= QUEUED;
    queue.push(f);
  }
  if (!batch && !flushing) flushSoon(false);
}

// Flush in a microtask. `sync`: updates from an event, whose passive
// effects also run before the microtask ends (React's sync lane).
let microSync = false;
function flushSoon(sync: boolean): void {
  if (sync) microSync = true;
  if (micro) return;
  micro = true;
  queueMicrotask(() => {
    micro = false;
    const s = microSync;
    microSync = false;
    flushWork(s);
  });
}

const byDepth = (a: Fiber, b: Fiber): number => a.z - b.z;

function flushWork(sync: boolean): void {
  if (flushing) return;
  flushing = true;
  try {
    while (queue.length || layoutQ.length) {
      while (queue.length) {
        // Effects of the last commit run before the next render, as in React.
        flushPassive();
        const q = queue;
        queue = [];
        if (q.length > 1) q.sort(byDepth);
        for (const f of q) {
          f.f &= ~QUEUED;
          if ((f.f & (DIRTY | DEAD)) === DIRTY) rerender(f);
        }
      }
      flushLayout();
    }
    if (sync) flushPassive();
  } finally {
    flushing = false;
  }
  if (queue.length) flushSoon(false);
  if ((passiveQ.length || passiveUnmountQ.length) && !passiveTimer) {
    passiveTimer = true;
    afterPaint(() => {
      passiveTimer = false;
      if (!flushing) {
        flushing = true;
        try {
          flushPassive();
        } finally {
          flushing = false;
        }
        if (queue.length) flushWork(false);
      }
    });
  }
}

const afterPaint: (fn: () => void) => void =
  typeof MessageChannel === "function"
    ? (fn) => {
        const ch = new MessageChannel();
        ch.port1.onmessage = () => fn();
        ch.port2.postMessage(null);
      }
    : (fn) => void setTimeout(fn, 0);

// Re-render a fiber from the update queue, in its place in the tree.
function rerender(f: Fiber): void {
  let h = f.r;
  while (h !== null && h.n !== HOST && h.n !== ROOT && h.n !== PORTAL) h = h.r;
  const ph = curHost,
    pn = curNs;
  curHost = h ?? f;
  curNs = h === null ? HTML_NS : hostNs(h);
  try {
    run(f, f.p, false);
  } catch (e) {
    throwTo(f, e);
  } finally {
    const host = curHost;
    curHost = ph;
    curNs = pn;
    if (host.f & STRUCT) place(host);
  }
}

// Route a value thrown while rendering (or from an effect) of f to the
// nearest boundary above it, marking the path so a retry renders it again.
function throwTo(f: Fiber, e: Any): void {
  const promise = isThenable(e);
  f.f |= DIRTY;
  let p = f.r;
  for (; p !== null; p = p.r) {
    if (p.n === ROOT || (promise ? p.n === SUSPENSE : p.n === CLASS && classes !== null && classes.boundary(p))) break;
    p.f |= DIRTY;
  }
  if (p === null) {
    if (f.n === ROOT) p = f;
    else throw e;
  }
  p.x = e;
  schedule(p);
}

function safely(f: Fiber, fn: () => void): void {
  try {
    fn();
  } catch (e) {
    throwTo(f, e);
  }
}

/** Run `fn` and render every update it makes before returning. */
export function flushSync<T>(fn?: () => T): T {
  batch++;
  let r: Any;
  try {
    if (fn) r = fn();
  } finally {
    batch--;
  }
  flushWork(true);
  return r;
}

/** Run `fn` with its updates batched (they are batched anyway). */
export function unstable_batchedUpdates<A, R>(fn: (a: A) => R, a: A): R {
  batch++;
  try {
    return fn(a);
  } finally {
    if (--batch === 0) flushWork(true);
  }
}

/** Run `callback` and flush every update and effect it causes, as tests expect. */
export function act(callback: () => Any): Promise<void> {
  batch++;
  let r: Any;
  try {
    r = callback();
  } finally {
    batch--;
  }
  const settle = (): void => {
    for (let i = 0; i < 100 && (queue.length || passiveQ.length || passiveUnmountQ.length || layoutQ.length); i++)
      flushWork(true);
  };
  if (isThenable(r)) return Promise.resolve(r).then(settle);
  settle();
  return Promise.resolve();
}

// ---------------------------------------------------------------------------
// Effects
// ---------------------------------------------------------------------------

// Effect kinds of a hook.
const LAYOUT = 1;
const PASSIVE = 2;
const INSERTION = 3;

const NO_DEPS: unknown[] = [];

class Hook {
  readonly f: Fiber;
  /** Effect kind, 0 for other hooks. */
  k = 0;
  v: Any = undefined;
  /** Committed deps. */
  d: Any = NO_DEPS;
  /** Effect create, or a callback queued by `later`. */
  c: Any = null;
  /** Effect cleanup. */
  x: Any = undefined;
  /** Setter or other per-hook function; null until first render. */
  s: Any = null;
  /** Reducer. */
  r: Any = null;
  constructor(f: Fiber) {
    this.f = f;
  }
}

let layoutQ: Hook[] = [];
let passiveQ: Hook[] = [];
let passiveUnmountQ: (() => void)[] = [];

// Queue fn to run with layout effects, in order.
function later(f: Fiber, fn: () => void): void {
  const h = new Hook(f);
  h.c = fn;
  layoutQ.push(h);
}

function flushLayout(): void {
  const q = layoutQ;
  if (!q.length) return;
  layoutQ = [];
  runEffects(q);
}

function flushPassive(): void {
  const un = passiveUnmountQ;
  if (un.length) {
    passiveUnmountQ = [];
    for (const x of un) {
      try {
        x();
      } catch (e) {
        reportError(e);
      }
    }
  }
  const q = passiveQ;
  if (!q.length) return;
  passiveQ = [];
  runEffects(q);
}

function runEffects(q: Hook[]): void {
  for (const h of q) {
    if (h.k !== 0 && h.x !== undefined && !(h.f.f & DEAD)) {
      const x = h.x;
      h.x = undefined;
      safely(h.f, x);
    }
  }
  for (const h of q) {
    if (h.f.f & DEAD) continue;
    const c = h.c;
    if (h.k === 0) safely(h.f, c);
    else
      safely(h.f, () => {
        const r = c();
        h.x = typeof r === "function" ? r : undefined;
      });
  }
}

// After a function component rendered: commit its effects whose deps changed.
function commitEffects(f: Fiber): void {
  const fx: Any[] = f.x;
  f.x = null;
  for (let i = 0; i < fx.length; i += 3) {
    const h: Hook = fx[i],
      deps = fx[i + 2];
    h.c = fx[i + 1];
    h.d = deps;
    if (h.k === INSERTION) {
      if (h.x !== undefined) safely(f, h.x);
      safely(f, () => {
        const r = h.c();
        h.x = typeof r === "function" ? r : undefined;
      });
    } else (h.k === LAYOUT ? layoutQ : passiveQ).push(h);
  }
}

// ---------------------------------------------------------------------------
// Hooks
// ---------------------------------------------------------------------------

function hook(): Hook {
  const f = cur;
  if (f === null)
    throw new Error("Invalid hook call. Hooks can only be called inside of the body of a function component.");
  const hs = (f.h ??= []);
  let h = hs[hi];
  if (h === undefined) hs[hi] = h = new Hook(f);
  hi++;
  return h;
}

function depsChanged(a: Any, b: Any): boolean {
  if (a === NO_DEPS || b == null || a == null || a.length !== b.length) return true;
  for (let i = 0; i < a.length; i++) if (!Object.is(a[i], b[i])) return true;
  return false;
}

function effect(kind: number, create: () => Any, deps: Any): void {
  const f = cur!,
    h = hook();
  h.k = kind;
  if (deps == null || depsChanged(h.d, deps)) (f.x ??= []).push(h, create, deps ?? null);
}

/** Run `effect` after the browser paints, again whenever `deps` change; its returned function cleans up. */
export function useEffect(create: () => Any, deps?: readonly unknown[]): void {
  effect(PASSIVE, create, deps);
}
/** Like useEffect, but before the browser paints. */
export function useLayoutEffect(create: () => Any, deps?: readonly unknown[]): void {
  effect(LAYOUT, create, deps);
}
/** Like useEffect, but as soon as the component's render completes (for injecting styles). */
export function useInsertionEffect(create: () => Any, deps?: readonly unknown[]): void {
  effect(INSERTION, create, deps);
}

const basicReducer = (s: Any, a: Any): Any => (typeof a === "function" ? a(s) : a);

/** A state value and its setter. */
export function useState<S>(init: S | (() => S)): [S, (action: S | ((prev: S) => S)) => void] {
  return useReducer(basicReducer, init, typeof init === "function" ? (init as () => S) : undefined) as Any;
}

/** State updated through a reducer. */
export function useReducer<S, A>(
  reducer: (s: S, a: A) => S,
  initArg: Any,
  init?: (arg: Any) => S,
): [S, (a: A) => void] {
  const f = cur!,
    h = hook();
  if (h.s === null) {
    h.v = init !== undefined ? init(initArg) : initArg;
    h.s = (a: A) => {
      if (f.f & DEAD) return;
      const next = h.r(h.v, a);
      if (Object.is(next, h.v)) return;
      h.v = next;
      if (f === cur) f.f |= RERENDER;
      else schedule(f);
    };
  }
  h.r = reducer;
  return [h.v, h.s];
}

/** A mutable object that lives as long as the component. */
export function useRef<T>(init?: T): RefObject<T> {
  const h = hook();
  if (h.s === null) {
    h.s = 1;
    h.v = { current: init };
  }
  return h.v;
}

/** `compute()`, recomputed only when `deps` change. */
export function useMemo<T>(compute: () => T, deps?: readonly unknown[]): T {
  const h = hook();
  if (h.s === null || deps == null || depsChanged(h.d, deps)) {
    h.s = 1;
    h.v = compute();
    h.d = deps ?? null;
  }
  return h.v;
}

/** `fn`, replaced only when `deps` change. */
export function useCallback<T>(fn: T, deps?: readonly unknown[]): T {
  const h = hook();
  if (h.s === null || deps == null || depsChanged(h.d, deps)) {
    h.s = 1;
    h.v = fn;
    h.d = deps ?? null;
  }
  return h.v;
}

/** The value of the nearest provider of `ctx` above, or its default. */
export function useContext<T>(ctx: Context<T>): T {
  if (cur === null) throw new Error("Invalid hook call.");
  return readContext(cur, ctx);
}

/** Set `ref` to what `create` returns, with layout effects. */
export function useImperativeHandle(ref: Any, create: () => Any, deps?: readonly unknown[]): void {
  effect(
    LAYOUT,
    () => {
      if (typeof ref === "function") {
        const r = ref(create());
        return () => (typeof r === "function" ? r() : ref(null));
      }
      if (ref != null) {
        ref.current = create();
        return () => {
          ref.current = null;
        };
      }
      return undefined;
    },
    deps != null ? [...deps, ref] : undefined,
  );
}

/** Does nothing: there are no development tools to label. */
export function useDebugValue(_value: unknown, _format?: (v: Any) => Any): void {}

/** A unique id, stable for the component's life. */
export function useId(): string {
  const h = hook();
  if (h.s === null) {
    h.s = 1;
    const r = h.f.o;
    h.v = "_" + r.prefix + "r_" + (r.ids++).toString(32) + "_";
  }
  return h.v;
}

/** A value read from an external store, re-rendering when it changes. */
export function useSyncExternalStore<T>(
  subscribe: (cb: () => void) => () => void,
  getSnapshot: () => T,
  _server?: () => T,
): T {
  const f = cur!;
  const v = getSnapshot();
  const h = hook();
  if (h.s === null) h.s = { v, g: getSnapshot };
  const box = h.s;
  box.v = v;
  box.g = getSnapshot;
  effect(PASSIVE, () => {
    const check = (): void => {
      let changed = true;
      try {
        changed = !Object.is(box.g(), box.v);
      } catch {}
      if (changed) schedule(f);
    };
    check();
    return subscribe(check);
  }, [subscribe]);
  return v;
}

/** The store hook with a selector, as use-sync-external-store/with-selector exports it. */
export function useSyncExternalStoreWithSelector<S, T>(
  subscribe: (cb: () => void) => () => void,
  getSnapshot: () => S,
  getServerSnapshot: (() => S) | undefined | null,
  selector: (s: S) => T,
  isEqual?: (a: T, b: T) => boolean,
): T {
  const memo = useRef<{ s: S; t: T } | null>(null);
  const select = useCallback(() => {
    const s = getSnapshot();
    const m = memo.current;
    if (m !== null && Object.is(m.s, s)) return m.t;
    const t = selector(s);
    if (m !== null && isEqual !== undefined && isEqual(m.t, t)) {
      memo.current = { s, t: m.t };
      return m.t;
    }
    memo.current = { s, t };
    return t;
  }, [getSnapshot, selector, isEqual]);
  void getServerSnapshot;
  return useSyncExternalStore(subscribe, select);
}

/** A stable function that always calls the latest `fn` (for effects that must not re-run when it changes). */
export function useEffectEvent<F extends (...args: Any[]) => Any>(fn: F): F {
  const h = hook();
  if (h.s === null) {
    h.v = fn;
    h.s = (...args: Any[]) => h.v(...args);
  }
  h.v = fn;
  return h.s;
}

/** The React Compiler's per-component cache (`react/compiler-runtime`). */
export function c(size: number): Any[] {
  const h = hook();
  if (h.s === null) {
    h.s = 1;
    h.v = new Array(size).fill(MEMO_SENTINEL);
  }
  return h.v;
}
export { c as useMemoCache };

/** `{ c }`, which `react/compiler-runtime` re-exports. */
export const __COMPILER_RUNTIME: { c: typeof c } = { c };

// Thenables, as React 19 tracks them on the promise itself.
function readThenable(t: Any): Any {
  switch (t.status) {
    case "fulfilled":
      return t.value;
    case "rejected":
      throw t.reason;
  }
  if (t.status !== "pending") {
    t.status = "pending";
    t.then(
      (v: Any) => {
        if (t.status === "pending") ((t.status = "fulfilled"), (t.value = v));
      },
      (e: Any) => {
        if (t.status === "pending") ((t.status = "rejected"), (t.reason = e));
      },
    );
    // A thenable that resolves synchronously is read at once.
    if (t.status === "fulfilled") return t.value;
    if (t.status === "rejected") throw t.reason;
  }
  throw t;
}

/** Read a context, or a promise's value (suspending until it settles). */
export function use<T>(usable: PromiseLike<T> | Context<T>): T {
  if ((usable as Any).$$typeof === CONTEXT_TYPE) {
    if (cur === null) throw new Error("Invalid hook call.");
    return readContext(cur, usable);
  }
  return readThenable(usable);
}

// Transitions

interface Transition {
  readonly done: (() => void)[];
  /** Async work started inside the transition (queued actions), which it waits for. */
  readonly wait?: PromiseLike<unknown>[];
}
let transition: Transition | null = null;

function inTransition(t: Transition, fn: () => Any): Any {
  const prevT = transition;
  transition = t;
  try {
    return fn();
  } finally {
    transition = prevT;
  }
}

function settle(t: Transition, r: Any, onError?: (e: Any) => void): void {
  const fin = (): void => {
    for (const d of t.done) d();
  };
  if (t.wait !== undefined && t.wait.length) r = Promise.all([r, ...t.wait]);
  if (isThenable(r))
    r.then(fin, (e: Any) => {
      fin();
      if (onError) onError(e);
      else reportError(e);
    });
  else fin();
}

/** Run `fn` as a transition. Rendering is synchronous, so its updates render at once. */
export function startTransition(fn: () => Any): void {
  const t: Transition = { done: [], wait: [] };
  settle(t, inTransition(t, fn));
}

/** `[isPending, startTransition]`; pending lasts while an async transition runs. */
export function useTransition(): [boolean, (fn: () => Any) => void] {
  const [pending, setPending] = useState(false);
  const f = cur!;
  const h = hook();
  if (h.s === null) {
    h.s = (fn: () => Any) => {
      setPending(true);
      const t: Transition = { done: [() => setPending(false)], wait: [] };
      let r: Any;
      try {
        r = inTransition(t, fn);
      } catch (e) {
        setPending(false);
        throw e;
      }
      settle(t, r, (e) => throwTo(f, e));
    };
  }
  return [pending, h.s];
}

/** The value; `initial` on the first render, then the value right after. */
export function useDeferredValue<T>(value: T, initial?: T): T {
  const h = hook();
  if (h.s === null) {
    h.s = 1;
    if (initial !== undefined) {
      h.v = true;
      const f = h.f;
      later(f, () => {
        h.v = false;
        schedule(f);
      });
      return initial;
    }
  }
  return h.v === true ? (initial as T) : value;
}

/** State shown optimistically while an action or transition runs, then dropped. */
export function useOptimistic<S, A>(passthrough: S, reducer?: (s: S, a: A) => S): [S, (a: A) => void] {
  const h = hook();
  if (h.s === null) {
    const f = h.f;
    h.v = [];
    h.s = (a: A) => {
      const entry = { a };
      h.v = [...h.v, entry];
      schedule(f);
      const clear = (): void => {
        h.v = h.v.filter((x: Any) => x !== entry);
        schedule(f);
      };
      if (transition !== null) transition.done.push(clear);
      else queueMicrotask(clear);
    };
  }
  let s = passthrough;
  for (const x of h.v) s = reducer ? reducer(s, x.a) : x.a;
  return [s, h.s];
}

/** `[state, dispatch, isPending]`: dispatch runs `action(state, payload)`, one at a time, and keeps its result. */
export function useActionState<S, P>(
  action: (state: S, payload: P) => S | Promise<S>,
  initialState: S,
  _permalink?: string,
): [S, (payload: P) => void, boolean] {
  const [state, setState] = useState(initialState);
  const [pending, setPending] = useState(false);
  const f = cur!;
  const h = hook();
  if (h.s === null) {
    const box = (h.v = { a: action, s: initialState, n: 0, p: Promise.resolve() as Promise<unknown> });
    h.s = (payload: P) => {
      box.n++;
      setPending(true);
      const t: Transition = { done: [] };
      const outer = transition;
      box.p = box.p
        .then(() => inTransition(t, () => box.a(box.s, payload)))
        .then(
          (next) => {
            box.s = next as S;
            setState(() => next as S);
          },
          (e) => throwTo(f, e),
        )
        .finally(() => {
          for (const d of t.done) d();
          if (--box.n === 0) setPending(false);
        });
      // A form action or transition that queued this waits for it.
      if (outer !== null && outer.wait !== undefined) outer.wait.push(box.p);
    };
  }
  h.v.a = action;
  return [state, h.s, pending];
}
export { useActionState as useFormState };

/** Status of the nearest `<form>` above while its action runs. */
export interface FormStatus {
  readonly pending: boolean;
  readonly data: FormData | null;
  readonly method: string | null;
  readonly action: Any;
}
const IDLE: FormStatus = { pending: false, data: null, method: null, action: null };

/** The status of the form this component renders inside. */
export function useFormStatus(): FormStatus {
  const f = cur!;
  for (let p = f.r; p !== null; p = p.r) {
    if (p.n === HOST && p.t === "form") {
      (p.q ??= new Set<Fiber>()).add(f);
      return p.s ?? IDLE;
    }
  }
  return IDLE;
}

function setFormStatus(form: Fiber, st: FormStatus): void {
  form.s = st;
  if (form.q !== null) for (const s of form.q as Set<Fiber>) schedule(s);
}

function runFormAction(form: Fiber, action: Any, data: FormData): void {
  const el = form.d as HTMLFormElement;
  setFormStatus(form, { pending: true, data, method: el.method, action });
  const t: Transition = {
    done: [
      () => {
        setFormStatus(form, IDLE);
        if (!(form.f & DEAD)) el.reset();
      },
    ],
    wait: [],
  };
  settle(
    t,
    inTransition(t, () => action(data)),
    (e) => throwTo(form, e),
  );
}

/** Reset a form when its running action finishes (forms with actions reset anyway). */
export function requestFormReset(form: HTMLFormElement): void {
  const f = (form as Any)[FIBER] as Fiber | undefined;
  if (f === undefined || f.s === null || !f.s.pending) form.reset();
}

/** The client has no cache: the function itself. */
export function cache<F>(fn: F): F {
  return fn;
}
/** Always null on the client. */
export function cacheSignal(): null {
  return null;
}
/** Always null: there are no owner stacks outside development builds. */
export function captureOwnerStack(): null {
  return null;
}
/** A function that does nothing: there is no cache to refresh. */
export function unstable_useCacheRefresh(): () => void {
  return noop;
}
const noop = (): void => {};

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

// Events that do not bubble: one listener on the element itself.
const DIRECT = new Set(
  (
    "scroll scrollend load error abort toggle beforetoggle " +
    "invalid cancel close canplay canplaythrough durationchange emptied encrypted ended loadeddata " +
    "loadedmetadata loadstart pause play playing progress ratechange resize seeked seeking stalled suspend " +
    "timeupdate volumechange waiting"
  ).split(" "),
);
// Native type -> React prop name, recorded as props use them.
const PROP_OF = new Map<string, string>([
  ["dblclick", "onDoubleClick"],
  ["focusin", "onFocus"],
  ["focusout", "onBlur"],
]);
const SYN_TYPE: Record<string, string> = { onChange: "change", onFocus: "focus", onBlur: "blur" };
const bubbleTypes: string[] = [];
const captureTypes: string[] = [];
const listeners = new Set<Listener>();

function eventType(base: string): string {
  switch (base) {
    case "onDoubleClick":
      return "dblclick";
    case "onFocus":
      return "focusin";
    case "onBlur":
      return "focusout";
  }
  return base.slice(2).toLowerCase();
}

// Make sure events for prop `name` (onClick, onKeyDownCapture, ...) reach dom.
function listenFor(dom: Any, name: string): void {
  const capture = name.endsWith("Capture") && name !== "onGotPointerCapture" && name !== "onLostPointerCapture";
  const base = capture ? name.slice(0, -7) : name;
  if (base === "onChange") {
    ensure("input", capture);
    ensure("change", capture);
    ensure("click", capture);
    return;
  }
  if (ENTER_LEAVE.has(base)) {
    // Derived from over and out events, as React does: they fire per element
    // entered or left, outermost first for enter, innermost first for leave.
    const kind = base.startsWith("onMouse") ? "mouse" : "pointer";
    ensure(kind + "over", false);
    ensure(kind + "out", false);
    return;
  }
  const type = eventType(base);
  if (!PROP_OF.has(type)) PROP_OF.set(type, base);
  if (DIRECT.has(type)) {
    const set: Set<string> = (dom[LISTENS] ??= new Set<string>());
    if (!set.has(type)) {
      set.add(type);
      dom.addEventListener(type, direct);
    }
    return;
  }
  ensure(type, capture);
}

function ensure(type: string, capture: boolean): void {
  const list = capture ? captureTypes : bubbleTypes;
  if (list.includes(type)) return;
  list.push(type);
  for (const l of listeners) l.sync();
}

class Listener {
  readonly el: Node;
  readonly root: RootImpl;
  nb = 0;
  nc = 0;
  readonly b: (e: Event) => void;
  readonly cp: (e: Event) => void;
  constructor(el: Node, root: RootImpl) {
    this.el = el;
    this.root = root;
    this.b = (e) => dispatch(e, root, false);
    this.cp = (e) => dispatch(e, root, true);
    this.sync();
  }
  sync(): void {
    for (; this.nb < bubbleTypes.length; this.nb++) this.el.addEventListener(bubbleTypes[this.nb]!, this.b);
    for (; this.nc < captureTypes.length; this.nc++) this.el.addEventListener(captureTypes[this.nc]!, this.cp, true);
  }
  remove(): void {
    for (let i = 0; i < this.nb; i++) this.el.removeEventListener(bubbleTypes[i]!, this.b);
    for (let i = 0; i < this.nc; i++) this.el.removeEventListener(captureTypes[i]!, this.cp, true);
    listeners.delete(this);
  }
}

function listenOn(el: Node, root: RootImpl): void {
  if (root.listeners.has(el)) return;
  const l = new Listener(el, root);
  root.listeners.set(el, l);
  listeners.add(l);
}

const SEEN_B = Symbol("seen");
const SEEN_C = Symbol("seen capture");

// Inputs whose onChange follows `input` events; checkboxes and radios follow
// `click` (when their checked state changed), selects and files `change`.
function textLike(el: Any): boolean {
  const tag = el.nodeName;
  if (tag === "TEXTAREA") return true;
  if (tag !== "INPUT") return false;
  const t = el.type;
  return t !== "checkbox" && t !== "radio" && t !== "file";
}

// React's value tracker: an input or textarea records the value last set
// through its own `value` property (`checked` for checkboxes and radios), by
// code or by render. onChange fires only when the field's value differs from
// it, so a script that sets `el.value` and dispatches an input event causes
// no onChange, as in React.
function track(dom: Any): void {
  const key = checkable(dom) ? "checked" : "value";
  const proto = Object.getPrototypeOf(dom);
  const desc = Object.getOwnPropertyDescriptor(proto, key);
  if (desc === undefined || desc.set === undefined || Object.hasOwn(dom, key)) return;
  dom[TRACKED] = "" + dom[key];
  Object.defineProperty(dom, key, {
    configurable: true,
    enumerable: desc.enumerable ?? false,
    get(this: Any): Any {
      return desc.get!.call(this);
    },
    set(this: Any, v: Any): void {
      this[TRACKED] = "" + v;
      desc.set!.call(this, v);
    },
  });
}

// Whether the field's value moved since the tracker last saw it; records it
// unless `peek` (the capture phase looks without taking the change).
function changed(el: Any, peek: boolean): boolean {
  if (el[TRACKED] === undefined) return true;
  const v = checkable(el) ? (el.checked ? "true" : "false") : "" + el.value;
  if (v === el[TRACKED]) return false;
  if (!peek) el[TRACKED] = v;
  return true;
}

function checkable(el: Any): boolean {
  return el.nodeName === "INPUT" && (el.type === "checkbox" || el.type === "radio");
}

const ON_INPUT = ["onInput"],
  ON_INPUT_CHANGE = ["onInput", "onChange"],
  ON_CHANGE = ["onChange"];

function propsFor(type: string, target: Any, capture: boolean): string[] | null {
  if (type === "input") return textLike(target) && changed(target, capture) ? ON_INPUT_CHANGE : ON_INPUT;
  if (type === "change") return textLike(target) || checkable(target) ? null : ON_CHANGE;
  const name = PROP_OF.get(type);
  if (type === "click" && checkable(target) && changed(target, capture)) {
    return name === undefined ? ON_CHANGE : [name, "onChange"];
  }
  return name === undefined ? null : [name];
}

function dispatch(e: Any, root: RootImpl, capture: boolean): void {
  const mark = capture ? SEEN_C : SEEN_B;
  const seen: RootImpl[] | undefined = e[mark];
  if (seen !== undefined) {
    if (seen.includes(root)) return;
    seen.push(root);
  } else e[mark] = [root];
  let node = e.target;
  while (node != null && !(node[FIBER] !== undefined && node[FIBER].o === root)) node = node.parentNode;
  if (node == null) return;
  const target: Fiber = node[FIBER];
  const type: string = e.type;
  const names = propsFor(type, node, capture);
  const over = !capture && OVER_OUT.has(type);
  if (names === null && !over && type !== "submit") return;
  // Controlled fields are restored once, after the bubble phase.
  const changed = !capture && names !== null && names.includes("onChange");
  batch++;
  try {
    if (over) enterLeave(e, root, type, target);
    if (names !== null)
      for (const name of names) {
        const prop = capture ? name + "Capture" : name;
        const path: Fiber[] = [];
        for (let f: Fiber | null = target; f !== null; f = f.r) {
          if (f.n === HOST && typeof f.p[prop] === "function" && !(f.f & DEAD)) path.push(f);
        }
        if (path.length === 0) continue;
        const syn = synthetic(e, name);
        if (capture) path.reverse();
        for (const f of path) {
          if (syn._stopped) break;
          syn.currentTarget = f.d;
          f.p[prop](syn);
        }
        syn.currentTarget = null;
      }
    if (!capture && e.type === "submit" && !e.defaultPrevented) submitAction(e, target);
  } finally {
    if (--batch === 0) afterEvent(changed ? target : undefined);
  }
}

const ENTER_LEAVE = new Set(["onMouseEnter", "onMouseLeave", "onPointerEnter", "onPointerLeave"]);
const OVER_OUT = new Set(["mouseover", "mouseout", "pointerover", "pointerout"]);

function fiberAt(node: Any, root: RootImpl): Fiber | null {
  while (node != null && !(node[FIBER] !== undefined && node[FIBER].o === root)) node = node.parentNode;
  return node == null ? null : node[FIBER];
}

function hostPath(f: Fiber | null): Fiber[] {
  const out: Fiber[] = [];
  for (; f !== null; f = f.r) if (f.n === HOST) out.push(f);
  return out;
}

// onMouseLeave on the elements left and onMouseEnter on those entered.
// `out` events carry both ends; an `over` event counts only when the pointer
// comes from outside this root (React's rule).
function enterLeave(e: Any, root: RootImpl, type: string, target: Fiber): void {
  const mouse = type.startsWith("mouse");
  let from: Fiber | null, to: Fiber | null;
  const related = e.relatedTarget;
  if (type.endsWith("over")) {
    if (related != null && fiberAt(related, root) !== null) return;
    from = null;
    to = target;
  } else {
    from = target;
    to = related == null ? null : fiberAt(related, root);
  }
  if (from === to) return;
  const a = hostPath(from),
    b = hostPath(to);
  const common = new Set(b);
  const leave = mouse ? "onMouseLeave" : "onPointerLeave",
    enter = mouse ? "onMouseEnter" : "onPointerEnter";
  const left = a.filter((f) => !common.has(f));
  const shared = new Set(a);
  const entered = b.filter((f) => !shared.has(f)).reverse();
  const fire = (list: Fiber[], name: string, tgt: Fiber | null, rel: Fiber | null): void => {
    let syn: Any = null;
    for (const f of list) {
      const fn = f.p[name];
      if (typeof fn !== "function") continue;
      if (syn === null) {
        syn = synthetic(e, name);
        syn.type = mouse
          ? name === leave
            ? "mouseleave"
            : "mouseenter"
          : name === leave
            ? "pointerleave"
            : "pointerenter";
        syn.target = tgt === null ? null : tgt.d;
        syn.relatedTarget = rel === null ? null : rel.d;
      }
      syn.currentTarget = f.d;
      fn(syn);
    }
  };
  fire(left, leave, from, to);
  fire(entered, enter, to, from);
}

// After an event's handlers: React renders their updates in a microtask,
// except that a controlled field is rendered at once and shown its props again.
function afterEvent(target: Fiber | undefined): void {
  if (target !== undefined && isControlled(target)) {
    flushWork(true);
    restoreControlled(target);
  } else if (queue.length) flushSoon(true);
}

function isControlled(f: Fiber): boolean {
  const t = f.t;
  return (
    f.n === HOST && (t === "input" || t === "textarea" || t === "select") && (f.p.value != null || f.p.checked != null)
  );
}

function direct(this: Any, e: Any): void {
  const f: Fiber | undefined = this[FIBER];
  const name = PROP_OF.get(e.type);
  if (f === undefined || name === undefined || f.f & DEAD) return;
  const fn = f.p[name];
  if (typeof fn !== "function") return;
  batch++;
  try {
    const syn = synthetic(e, name);
    syn.currentTarget = this;
    fn(syn);
  } finally {
    if (--batch === 0) afterEvent(undefined);
  }
}

// A synthetic event: the native event's fields, React's methods, and
// nativeEvent. Methods of the native event are bound to it.
function synthetic(e: Any, name: string): Any {
  const s: Any = {
    nativeEvent: e,
    currentTarget: null,
    _stopped: false,
    type: SYN_TYPE[name] ?? e.type,
    preventDefault(): void {
      e.preventDefault();
      s.defaultPrevented = true;
    },
    stopPropagation(): void {
      e.stopPropagation();
      s._stopped = true;
    },
    isDefaultPrevented: (): boolean => e.defaultPrevented,
    isPropagationStopped: (): boolean => s._stopped,
    persist: noop,
    isPersistent: (): boolean => true,
  };
  for (const k in e) {
    if (k in s) continue;
    const v = e[k];
    s[k] = typeof v === "function" ? v.bind(e) : v;
  }
  return s;
}

// After an event: a controlled field shows its props again if the handler did not take the change.
function restoreControlled(f: Fiber): void {
  if (f.n !== HOST || f.f & DEAD) return;
  const t = f.t;
  if (t !== "input" && t !== "textarea" && t !== "select") return;
  const dom = f.d,
    p = f.p;
  if (t === "select") return setSelect(dom, p, false);
  if (p.value != null && dom.value !== toStr(p.value)) dom.value = toStr(p.value);
  if (p.checked != null) {
    dom.checked = !!p.checked;
    if (dom.type === "radio" && dom.name) {
      const group = (dom.form ?? dom.ownerDocument).querySelectorAll(
        `input[type=radio][name="${CSS.escape(dom.name)}"]`,
      );
      for (const r of group) {
        const rf: Fiber | undefined = r[FIBER];
        if (rf !== undefined && rf !== f && rf.p.checked != null) r.checked = !!rf.p.checked;
      }
    }
  }
}

function submitAction(e: Any, target: Fiber): void {
  const form: Fiber | undefined = e.target[FIBER];
  if (form === undefined || form.t !== "form") return;
  let action = form.p.action;
  const sub = e.submitter;
  const sf: Fiber | undefined = sub != null ? sub[FIBER] : undefined;
  if (sf !== undefined && typeof sf.p.formAction === "function") action = sf.p.formAction;
  if (typeof action !== "function" || target.o !== form.o) return;
  e.preventDefault();
  runFormAction(form, action, new FormData(e.target, sub ?? undefined));
}

// ---------------------------------------------------------------------------
// Resource hints (react-dom)
// ---------------------------------------------------------------------------

function hint(rel: string, href: string, opts: Any = {}): void {
  if (typeof document === "undefined" || !href) return;
  const sel = `link[rel="${rel}"][href="${CSS.escape(href)}"]`;
  if (document.head.querySelector(sel)) return;
  const l = document.createElement("link");
  l.rel = rel;
  l.href = href;
  if (opts.as) l.setAttribute("as", opts.as);
  if (opts.crossOrigin != null) l.crossOrigin = opts.crossOrigin === "use-credentials" ? "use-credentials" : "";
  if (opts.type) l.type = opts.type;
  if (opts.fetchPriority) l.setAttribute("fetchpriority", opts.fetchPriority);
  document.head.appendChild(l);
}

/** Adds `<link rel="preconnect">`. */
export function preconnect(href: string, opts?: Any): void {
  hint("preconnect", href, opts);
}
/** Adds `<link rel="dns-prefetch">`. */
export function prefetchDNS(href: string): void {
  hint("dns-prefetch", href);
}
/** Adds `<link rel="preload">`. */
export function preload(href: string, opts?: Any): void {
  hint("preload", href, opts);
}
/** Adds `<link rel="modulepreload">`. */
export function preloadModule(href: string, opts?: Any): void {
  hint("modulepreload", href, opts);
}
/** Loads a stylesheet or script now. */
export function preinit(href: string, opts: Any = {}): void {
  if (opts.as === "script") {
    if (document.querySelector(`script[src="${CSS.escape(href)}"]`)) return;
    const s = document.createElement("script");
    s.src = href;
    s.async = true;
    document.head.appendChild(s);
  } else hint("stylesheet", href, opts);
}
/** Loads a module script now. */
export function preinitModule(href: string): void {
  if (document.querySelector(`script[src="${CSS.escape(href)}"]`)) return;
  const s = document.createElement("script");
  s.type = "module";
  s.src = href;
  document.head.appendChild(s);
}

// ---------------------------------------------------------------------------
// Default export: `import React from "react"` and `import ReactDOM from "react-dom"`.
// ---------------------------------------------------------------------------

/** Internals some libraries probe for; only the dispatcher's hooks are real. */
export const __CLIENT_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE: Any = {
  H: { useMemoCache: c },
  A: null,
  T: null,
  S: null,
};

const React: Record<string, unknown> = {
  version,
  Fragment,
  StrictMode,
  Profiler,
  Suspense,
  SuspenseList,
  Activity,
  ViewTransition,
  addTransitionType,
  Children,
  Component,
  PureComponent,
  createElement,
  cloneElement,
  isValidElement,
  createRef,
  createContext,
  memo,
  forwardRef,
  lazy,
  use,
  useState,
  useReducer,
  useEffect,
  useLayoutEffect,
  useInsertionEffect,
  useRef,
  useMemo,
  useCallback,
  useContext,
  useImperativeHandle,
  useDebugValue,
  useId,
  useSyncExternalStore,
  useTransition,
  useDeferredValue,
  useOptimistic,
  useActionState,
  useEffectEvent,
  startTransition,
  cache,
  cacheSignal,
  captureOwnerStack,
  unstable_useCacheRefresh,
  act,
  __COMPILER_RUNTIME,
  __CLIENT_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE,
  // react-dom and react-dom/client
  createRoot,
  hydrateRoot,
  createPortal,
  flushSync,
  unstable_batchedUpdates,
  useFormStatus,
  useFormState: useActionState,
  requestFormReset,
  preconnect,
  prefetchDNS,
  preload,
  preloadModule,
  preinit,
  preinitModule,
};
export default React;
