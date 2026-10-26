// The DOM builder herdsman web used before views: h() builds a fresh tree,
// patch() morphs the live tree toward it (herdsman-blocks' Layer 3), and
// each() keys a list's elements. The app no longer has it (its views
// migration removed it on 2026-10-27); this benchmark app measures the
// approach, so it keeps its own copy, without the views bridge.
import { KEY_ATTR, morph } from "../../../../web/src/herdsman-blocks.ts";

declare const KEYED: unique symbol;
declare const UNKEYED: unique symbol;

/** A rendered list whose elements carry keys. Only `each` makes one. */
export type Keyed = readonly Element[] & { readonly [KEYED]: true };

/** A list of nodes matched by order and content, not by key. Only `unkeyed` makes one. */
export type Unkeyed = readonly Child[] & { readonly [UNKEYED]: true };

type Child = Node | string | number | false | null | undefined | Keyed | Unkeyed | readonly [Child, ...Child[]];

declare const HERDSMAN_DEV: boolean | undefined;

/** Renders a list whose elements carry the item's key (data-key), so patch() moves them. */
export function each<T>(items: readonly T[], key: (item: T) => string | number, render: (item: T) => Element): Keyed {
  const out: Element[] = new Array(items.length);
  const keys: string[] = new Array(items.length);
  for (let i = 0; i < items.length; i++) {
    const item = items[i]!;
    const k = (keys[i] = String(key(item)));
    const r = render(item);
    r.setAttribute(KEY_ATTR, k);
    out[i] = r;
  }
  if (typeof HERDSMAN_DEV !== "undefined" && HERDSMAN_DEV) {
    const seen = new Set<string>();
    for (const k of keys) {
      if (seen.has(k)) console.warn(`each(): duplicate key "${k}"; duplicates fall back to order and may rewrite rows`);
      seen.add(k);
    }
  }
  return out as unknown as Keyed;
}

type Props = {
  class?: string | undefined;
  style?: string | undefined;
  dataset?: Record<string, string> | undefined;
  [attr: string]: unknown;
};

export function h<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  props: Props | null = null,
  ...children: Child[]
): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  if (props) {
    for (const [k, v] of Object.entries(props)) {
      if (v === undefined || v === null || v === false) continue;
      if (k === "class") el.className = String(v);
      else if (k === "dataset") Object.assign(el.dataset, v);
      else if (k.startsWith("on") && typeof v === "function") on(el, k.slice(2).toLowerCase(), v as EventListener);
      else if (k in el && k !== "style" && k !== "list" && k !== "form") {
        (el as unknown as Record<string, unknown>)[k] = v;
      } else el.setAttribute(k, v === true ? "" : String(v));
    }
  }
  append(el, children);
  return el;
}

function append(el: Node, children: readonly Child[]): void {
  for (const c of children) {
    if (c === null || c === undefined || c === false) continue;
    if (isList(c)) append(el, c);
    else if (c instanceof Node) el.appendChild(c);
    else el.appendChild(document.createTextNode(String(c)));
  }
}

const isList = (c: Child): c is Keyed | Unkeyed | readonly [Child, ...Child[]] => Array.isArray(c);

/** Marks a list as deliberately unkeyed: patch() matches its items by content and order. */
export function unkeyed(nodes: readonly Child[]): Unkeyed {
  return nodes as Unkeyed;
}

/** Replaces the children of el. */
export function mount(el: Element, ...children: readonly Child[]): void {
  el.replaceChildren();
  append(el, children);
}

/** Like mount, but keeps the live nodes that still match the new children and patches them in place. */
export function patch(el: Element, ...children: Child[]): void {
  morph(el, flatten(children, []), { onKeep: keepHandlers });
}

function flatten(children: readonly Child[], out: Node[]): Node[] {
  for (const c of children) {
    if (c === null || c === undefined || c === false) continue;
    if (isList(c)) flatten(c, out);
    else if (c instanceof Node) out.push(c);
    else out.push(document.createTextNode(String(c)));
  }
  return out;
}

// Handlers live in a map behind one listener per event type, so patch() can
// give a kept element the fresh closures of the element it was matched to.
const handlers = new WeakMap<Element, Record<string, EventListener | null>>();

function on(el: Element, type: string, fn: EventListener): void {
  let map = handlers.get(el);
  if (!map) handlers.set(el, (map = {}));
  if (!(type in map)) {
    el.addEventListener(type, function (this: Element, e: Event) {
      handlers.get(el)?.[type]?.call(this, e);
    });
  }
  map[type] = fn;
}

function keepHandlers(live: Element, fresh: Element): void {
  const want = handlers.get(fresh);
  const have = handlers.get(live);
  if (have) for (const type in have) have[type] = want?.[type] ?? null;
  if (want) for (const type in want) if (!have || !(type in have)) on(live, type, want[type]!);
}
