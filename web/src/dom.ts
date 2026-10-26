// A small DOM builder. Strings always become text nodes, so nothing a pane
// or a route file holds is ever parsed as HTML.
import { morph } from "./herdsman-dom-diff.ts";

type Child = Node | string | number | false | null | undefined | Child[];

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

function append(el: Node, children: Child[]): void {
  for (const c of children) {
    if (c === null || c === undefined || c === false) continue;
    if (Array.isArray(c)) append(el, c);
    else if (c instanceof Node) el.appendChild(c);
    else el.appendChild(document.createTextNode(String(c)));
  }
}

/** Replaces the children of el. */
export function mount(el: Element, ...children: Child[]): void {
  el.replaceChildren();
  append(el, children);
}

/**
 * Like mount, but keeps the live nodes that still match the new children and
 * patches them in place, so focus, the caret, selection and scroll positions
 * survive a re-render. Give list items a `key` in their dataset (data-key) so
 * they move with their data instead of being rebuilt.
 */
export function patch(el: Element, ...children: Child[]): void {
  morph(el, flatten(children, []), { onKeep: keepHandlers });
}

function flatten(children: Child[], out: Node[]): Node[] {
  for (const c of children) {
    if (c === null || c === undefined || c === false) continue;
    if (Array.isArray(c)) flatten(c, out);
    else out.push(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return out;
}

// Handlers live in a map behind one listener per event type, so patch() can
// give a kept element the fresh closures of the element it was matched to.
// A type the fresh element lacks maps to null and its listener does nothing.
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

const SVG = "http://www.w3.org/2000/svg";

/** Builds an inline SVG icon from path data on a 24 unit grid. */
export function icon(paths: string[], label?: string): SVGSVGElement {
  const svg = document.createElementNS(SVG, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("class", "icon");
  svg.setAttribute("fill", "none");
  svg.setAttribute("stroke", "currentColor");
  svg.setAttribute("stroke-width", "1.8");
  svg.setAttribute("stroke-linecap", "round");
  svg.setAttribute("stroke-linejoin", "round");
  if (label) svg.setAttribute("aria-label", label);
  else svg.setAttribute("aria-hidden", "true");
  for (const d of paths) {
    const p = document.createElementNS(SVG, "path");
    p.setAttribute("d", d);
    svg.appendChild(p);
  }
  return svg;
}

/** The herdsman mark: an h whose leg flicks out like a shepherd's crook, with one of the herd under its arch. */
export function logoMark(): SVGSVGElement {
  const svg = document.createElementNS(SVG, "svg");
  svg.setAttribute("viewBox", "0 0 32 32");
  svg.setAttribute("class", "logo");
  svg.setAttribute("aria-hidden", "true");
  const el = (tag: string, attrs: Record<string, string>): SVGElement => {
    const e = document.createElementNS(SVG, tag);
    for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
    return e;
  };
  const strokes = el("g", {
    fill: "none",
    stroke: "#e9f6ef",
    "stroke-width": "2.6",
    "stroke-linecap": "round",
    "stroke-linejoin": "round",
  });
  strokes.append(el("path", { d: "M10 6.5V26" }), el("path", { d: "M10 17.5a5.5 5.5 0 0 1 11 0v5.5a3 3 0 0 0 3 3" }));
  svg.append(
    el("rect", { width: "32", height: "32", rx: "8", fill: "#1d4d38" }),
    strokes,
    el("circle", { cx: "15.5", cy: "24", r: "2", fill: "#5cc093" }),
  );
  return svg;
}

export const icons = {
  sun: [
    "M12 4V2",
    "M12 22v-2",
    "M4 12H2",
    "M22 12h-2",
    "M5 5 3.6 3.6",
    "M20.4 20.4 19 19",
    "M5 19l-1.4 1.4",
    "M20.4 3.6 19 5",
    "M12 7a5 5 0 1 0 0 10a5 5 0 0 0 0-10z",
  ],
  moon: ["M20 14.5A8 8 0 0 1 9.5 4 8 8 0 1 0 20 14.5z"],
  monitor: ["M3 5h18v11H3z", "M8 20h8", "M12 16v4"],
  send: ["M4 12 20 4l-6 16-3-7z", "M11 13l9-9"],
  close: ["M6 6l12 12", "M18 6 6 18"],
  back: ["M15 6l-6 6 6 6"],
  chevronDown: ["M6 9l6 6 6-6"],
  server: ["M4 4h16v6H4z", "M4 14h16v6H4z", "M8 7h.01", "M8 17h.01"],
  artifact: ["M3 5h18v14H3z", "M3 9h18", "M6 7h.01", "M9 7h.01", "M9.5 12.5 7.5 14.5l2 2", "M14.5 12.5l2 2-2 2"],
  message: ["M4 5h16v11H9l-5 4z", "M8 9h8", "M8 12h5"],
  external: ["M14 4h6v6", "M20 4l-9 9", "M18 14v6H4V6h6"],
  split: ["M4 4h16v16H4z", "M12 4v16"],
  splitDown: ["M4 4h16v16H4z", "M4 12h16"],
  play: ["M7 5v14l11-7z"],
  focus: ["M4 9V4h5", "M20 9V4h-5", "M4 15v5h5", "M20 15v5h-5"],
  tag: ["M3 12V3h9l9 9-9 9z", "M8 8h.01"],
  trash: ["M4 7h16", "M10 11v6", "M14 11v6", "M6 7l1 13h10l1-13", "M9 7V4h6v3"],
  file: ["M6 3h8l4 4v14H6z", "M14 3v4h4"],
  list: ["M8 6h12", "M8 12h12", "M8 18h12", "M4 6h.01", "M4 12h.01", "M4 18h.01"],
  check: ["M5 12l5 5 9-10"],
  activity: ["M3 12h4l3 8 4-16 3 8h4"],
  users: [
    "M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8z",
    "M2 21a7 7 0 0 1 14 0",
    "M16 3.5a4 4 0 0 1 0 7.5",
    "M22 21a7 7 0 0 0-4-6.3",
  ],
  keyboard: ["M3 6h18v12H3z", "M7 10h.01", "M11 10h.01", "M15 10h.01", "M7 14h10"],
  terminal: ["M4 5h16v14H4z", "M7.5 9.5l3 2.5-3 2.5", "M12.5 15h4"],
  sidebar: ["M5 4h14a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2z", "M9 4v16"],
  sidebarOpen: [
    "M5 4h14a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2z",
    "M9 4v16",
    "M5.5 7h1",
    "M5.5 10h1",
    "M5.5 13h1",
  ],
  command: ["M9 6a3 3 0 1 0-3 3h12a3 3 0 1 0-3-3v12a3 3 0 1 0 3-3H6a3 3 0 1 0 3 3z"],
  arrow: ["M5 12h14", "M13 6l6 6-6 6"],
  folder: ["M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"],
  copy: ["M9 9h11v11H9z", "M5 15H4V4h11v1"],
  plus: ["M12 5v14", "M5 12h14"],
  search: ["M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14z", "M20 20l-4-4"],
  clock: ["M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z", "M12 7v5l3 2"],
  diff: ["M6 3h8l4 4v14H6z", "M14 3v4h4", "M12 9v5", "M9.5 11.5h5", "M9.5 17h5"],
  branch: [
    "M6 3v12",
    "M6 21a3 3 0 1 0 0-6 3 3 0 0 0 0 6z",
    "M18 9a3 3 0 1 0 0-6 3 3 0 0 0 0 6z",
    "M18 9a9 9 0 0 1-9 9",
  ],
  refresh: ["M20 11a8 8 0 1 0-2.3 5.7", "M20 4v7h-7"],
  swatch: [
    "M12 3a9 9 0 1 0 0 18c.9 0 1.5-.7 1.5-1.5 0-.4-.1-.7-.4-1-.2-.3-.4-.6-.4-1 0-.8.7-1.5 1.5-1.5H16a5 5 0 0 0 5-5c0-4.4-4-8-9-8z",
    "M7.5 11h.01",
    "M10 7h.01",
    "M14.5 7h.01",
    "M17 11h.01",
  ],
};
