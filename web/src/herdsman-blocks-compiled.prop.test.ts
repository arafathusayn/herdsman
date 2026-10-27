// Property tests for compiled views: random templates (every hole kind, every
// child kind, nested lists, SVG) are rendered through a random sequence of
// values by three implementations side by side: the view layer
// (herdsman-blocks-view.ts), the build-time compiler's output
// (herdsman-blocks-compiler.ts run on the same template, executed by
// herdsman-blocks-compiled.ts), and herdsman-blocks-compiled.ts's own
// run-time compile. After every step the DOM, the DOM work done and the
// handlers reached from every element must be the same.
//
// Run alone: cd web && bun test src/herdsman-blocks-compiled
import { beforeEach, describe, expect, test } from "bun:test";

import * as compiledRt from "./herdsman-blocks-compiled.ts";
import { compileView } from "./herdsman-blocks-compiler.ts";
import * as viewRt from "./herdsman-blocks-view.ts";
import { assertProperty, type Choices, type Gen } from "./herdsman-blocks.prop.ts";
import {
  asDom,
  type FakeElement,
  FakeEvent,
  type FakeNode,
  installFakeDocument,
  ops,
} from "./herdsman-blocks.testdom.ts";
import { attr, cls, on, prop, slot as child, text } from "./herdsman-blocks.ts";

installFakeDocument();
beforeEach(installFakeDocument);

// ---------------------------------------------------------------------------
// Three implementations behind one interface
// ---------------------------------------------------------------------------

type ViewFn = (vals: readonly unknown[]) => unknown;
type HoleKind = "text" | "cls" | "attr" | "child" | "on" | "focus" | "prop";

interface Family {
  readonly name: string;
  view(parts: readonly string[], kinds: readonly HoleKind[], svg: boolean): ViewFn;
  readonly render: (c: Node, d: never) => void;
  readonly renderDirect: (c: Node, d: never) => void;
  readonly mount: (d: never) => Element;
  readonly update: (root: Element, d: never) => void;
  readonly list: (
    items: readonly unknown[],
    key: (i: never) => string | number,
    render: (i: never) => never,
    options?: { readonly pure?: boolean },
  ) => unknown;
  readonly runs: (items: readonly unknown[]) => unknown;
}

const markerOf: Record<HoleKind, () => number> = {
  text,
  cls,
  attr,
  child,
  on: on as () => number,
  focus: on as () => number,
  prop: prop as () => number,
};

const strings = (parts: readonly string[]): TemplateStringsArray =>
  Object.assign(parts.slice(), {
    raw: parts.slice(),
  }) as unknown as TemplateStringsArray;

type Tag = (s: TemplateStringsArray, ...h: number[]) => ViewFn;

function runtimeFamily(name: string, rt: typeof viewRt | typeof compiledRt): Family {
  const cache = new Map<string, ViewFn>();
  return {
    name,
    view(parts, kinds, svg) {
      const id = (svg ? "s" : "h") + parts.join("\u0000") + "|" + kinds.join(",");
      let v = cache.get(id);
      if (v === undefined) {
        const tag = (svg ? rt.svg : rt.html) as unknown as Tag;
        v = tag(strings(parts), ...kinds.map((k) => markerOf[k]()));
        cache.set(id, v);
      }
      return v;
    },
    render: rt.render as never,
    renderDirect: rt.renderDirect as never,
    mount: rt.mount as never,
    update: rt.update as never,
    list: rt.list as never,
    runs: rt.runs as never,
  };
}

function compiledFamily(): Family {
  const cache = new Map<string, ViewFn>();
  const rt = compiledRt;
  return {
    name: "build-time compiled",
    view(parts, kinds, svg) {
      const id = (svg ? "s" : "h") + parts.join("\u0000") + "|" + kinds.join(",");
      let v = cache.get(id);
      if (v === undefined) {
        const r = compileView(
          parts,
          kinds.map((k) => markerOf[k]()),
          svg,
        );
        if (!r.ok) throw new Error(`not compiled: ${r.message}\n${parts.join("${}")}`);
        // eslint-disable-next-line no-new-func -- the compiler's output, as a bundle would hold it
        const make = new Function("$hb", "return " + r.code) as (hb: typeof compiledRt) => { i(): ViewFn };
        v = make(rt).i();
        cache.set(id, v);
      }
      return v;
    },
    render: rt.render as never,
    renderDirect: rt.renderDirect as never,
    mount: rt.mount as never,
    update: rt.update as never,
    list: rt.list as never,
    runs: rt.runs as never,
  };
}

const FAMILIES: readonly Family[] = [
  runtimeFamily("view layer", viewRt),
  compiledFamily(),
  runtimeFamily("compiled runtime, run-time compile", compiledRt),
];

// ---------------------------------------------------------------------------
// Random templates
// ---------------------------------------------------------------------------

interface Shape {
  readonly parts: string[];
  readonly kinds: HoleKind[];
  readonly svg: boolean;
}

function htmlItems(c: Choices, sh: Shape, depth: number): void {
  const n = 1 + c.int(4);
  const hole = (kind: HoleKind, after: string): void => {
    sh.kinds.push(kind);
    sh.parts.push(after);
  };
  const add = (s: string): void => {
    sh.parts[sh.parts.length - 1] += s;
  };
  for (let k = 0; k < n; k++) {
    switch (c.int(depth < 2 ? 13 : 11)) {
      case 0:
        add(`<span class="s">x</span>`);
        break;
      case 1:
        hole("text", "");
        break;
      case 2:
        add(`<b>`);
        hole("text", `</b>`);
        break;
      case 3:
        add(`<i class=`);
        hole("cls", `>k</i>`);
        break;
      case 4:
        add(`<u title="`);
        hole("attr", `" data-x=1>t</u>`);
        break;
      case 5:
        hole("child", "");
        break;
      case 6:
        add(`<em onclick=`);
        hole("on", `>e</em>`);
        break;
      case 7:
        add(`<input onfocus=`);
        hole("focus", ` .value=`);
        hole("prop", ` />`);
        break;
      case 8:
        add(`\n  <p>a `);
        hole("text", ` b</p>\n`);
        break;
      case 9:
        add(`<svg viewBox="0 0 9 9">`);
        hole("child", `</svg>`);
        break;
      case 10:
        add(`<a onclick=`);
        hole("on", `>`);
        hole("child", `</a>`);
        break;
      default: {
        const div = c.bool();
        add(div ? `<div>` : `<section class="z">\n`);
        htmlItems(c, sh, depth + 1);
        add(div ? `</div>` : `\n</section>`);
        break;
      }
    }
  }
}

function svgItems(c: Choices, sh: Shape, depth: number): void {
  const n = 1 + c.int(3);
  const hole = (kind: HoleKind, after: string): void => {
    sh.kinds.push(kind);
    sh.parts.push(after);
  };
  const add = (s: string): void => {
    sh.parts[sh.parts.length - 1] += s;
  };
  for (let k = 0; k < n; k++) {
    switch (c.int(depth < 2 ? 7 : 6)) {
      case 0:
        add(`<circle r="1"/>`);
        break;
      case 1:
        add(`<circle class=`);
        hole("attr", ` r=`);
        hole("attr", `></circle>`);
        break;
      case 2:
        add(`<text>`);
        hole("text", `</text>`);
        break;
      case 3:
        hole("child", "");
        break;
      case 4:
        add(`<rect onclick=`);
        hole("on", ` />`);
        break;
      case 5:
        // (foreignObject content is HTML in a browser but not in the fake DOM: checked in Chrome.)
        add(`<path d="M0 0" stroke=`);
        hole("attr", ` />`);
        break;
      default:
        add(`<g>`);
        svgItems(c, sh, depth + 1);
        add(`</g>`);
    }
  }
}

function shapeGen(c: Choices): Shape {
  if (c.int(4) === 0) {
    const sh: Shape = { parts: ["<g>"], kinds: [], svg: true };
    svgItems(c, sh, 0);
    sh.parts[sh.parts.length - 1] += "</g>";
    return sh;
  }
  const sh: Shape = {
    parts: [c.bool() ? "<section>" : '<div class="root">\n'],
    kinds: [],
    svg: false,
  };
  htmlItems(c, sh, 0);
  sh.parts[sh.parts.length - 1] += sh.parts[0]!.startsWith("<section") ? "</section>" : "</div>";
  return sh;
}

// ---------------------------------------------------------------------------
// Random values, as recipes realized once per family and step
// ---------------------------------------------------------------------------

type ChildRecipe =
  | { readonly t: "none" }
  | { readonly t: "text"; readonly v: string | number }
  | { readonly t: "em"; readonly v: string }
  | { readonly t: "strong"; readonly v: string }
  | { readonly t: "node"; readonly v: string }
  | { readonly t: "list"; readonly ids: readonly number[]; readonly v: string }
  | {
      readonly t: "lanes";
      readonly lanes: readonly {
        readonly id: string;
        readonly ids: readonly number[];
      }[];
    }
  | { readonly t: "dots"; readonly ids: readonly number[] }
  // Items kept per run and relabeled in place (same objects), by one stable render function.
  | {
      readonly t: "objs";
      readonly ids: readonly number[];
      readonly label: string;
      readonly pure: boolean;
    }
  | {
      readonly t: "runs";
      readonly runs: readonly (string | { readonly em: string })[];
    };

type ValueRecipe = string | number | boolean | null | ChildRecipe | { readonly handler: number };

const ids = (c: Choices): number[] => {
  const pool = [1, 2, 3, 4, 5];
  const out: number[] = [];
  const n = c.int(6);
  for (let i = 0; i < n && pool.length; i++) out.push(pool.splice(c.int(pool.length), 1)[0]!);
  return out;
};

const childGen = (c: Choices): ChildRecipe => {
  switch (c.int(9)) {
    case 0:
      return { t: "none" };
    case 1:
      return { t: "text", v: c.pick(["", "a", "bb", 7]) };
    case 2:
      return { t: "em", v: c.pick(["p", "q"]) };
    case 3:
      return { t: "strong", v: c.pick(["", "x y"]) };
    case 4:
      return { t: "node", v: c.pick(["n1", "n2"]) };
    case 5:
      return { t: "list", ids: ids(c), v: c.pick(["", "v"]) };
    case 6: {
      const lanes: { id: string; ids: number[] }[] = [];
      for (const id of c.pick([["a", "b"], ["b", "a"], ["a"], ["c", "a", "b"]])) lanes.push({ id, ids: ids(c) });
      return { t: "lanes", lanes };
    }
    case 7:
      return c.bool()
        ? { t: "dots", ids: ids(c) }
        : {
            t: "objs",
            ids: ids(c),
            label: c.pick(["k", "l"]),
            pure: PURE_LISTS && c.bool(),
          };
    default: {
      const n = c.int(4);
      const rs: (string | { em: string })[] = [];
      for (let i = 0; i < n; i++) rs.push(c.bool() ? c.pick(["r", "", "s t"]) : { em: c.pick(["m", "n"]) });
      return { t: "runs", runs: rs };
    }
  }
};

const valueGen = (c: Choices, kind: HoleKind, version: number): ValueRecipe => {
  switch (kind) {
    case "text":
      return c.pick(["", "a", "b c", 3, null, "<i>"]);
    case "cls":
      return c.pick(["", "a", "b c", null]);
    case "attr":
      return c.pick([null, "", "x", false, true, 3]);
    case "prop":
      return c.pick(["", "typed", "x", null]);
    case "child":
      return childGen(c);
    case "on":
    case "focus":
      return { handler: c.bool() ? version : -1 };
  }
};

// inmount: render() into a root made by mount() (a container that is also a view's root, like a dialog).
type Mode = "render" | "direct" | "mount" | "inmount";

interface Case {
  readonly shape: Shape;
  readonly mode: Mode;
  readonly versions: readonly (readonly ValueRecipe[])[];
}

const caseGen: Gen<Case> = (c) => {
  const shape = shapeGen(c);
  const mode = c.pick(["render", "render", "direct", "mount", "inmount"] as const);
  const n = 2 + c.int(4);
  const versions: ValueRecipe[][] = [];
  for (let v = 0; v < n; v++) {
    // Sometimes keep the previous values exactly (the === skip), sometimes change some.
    const prev = versions[v - 1];
    versions.push(shape.kinds.map((k, i) => (prev && c.int(3) === 0 ? prev[i]! : valueGen(c, k, v))));
  }
  return { shape, mode, versions };
};

// Fixed child views, one set per family.
interface Kids {
  readonly Em: ViewFn;
  readonly Strong: ViewFn;
  readonly Li: ViewFn;
  readonly Tag: ViewFn;
  readonly Lane: ViewFn;
  readonly Card: ViewFn;
  readonly Dot: ViewFn;
}
const kidsOf = new Map<Family, Kids>();
function kids(f: Family): Kids {
  let k = kidsOf.get(f);
  if (k === undefined) {
    k = {
      Em: f.view(["<em>", "</em>"], ["text"], false),
      Strong: f.view(["<strong class=", ">z</strong>"], ["cls"], false),
      Tag: f.view(["<b onclick=", ">", "</b>"], ["on", "text"], false),
      Li: f.view(["<li onclick=", ">", "</li>"], ["on", "text"], false),
      Lane: f.view(["<section>\n  <h2 onclick=", ">", "</h2>\n  ", "\n</section>"], ["on", "text", "child"], false),
      Card: f.view(["<button class=", ">", "</button>"], ["cls", "text"], false),
      Dot: f.view(["<circle cx=", ' r="1" class=', "></circle>"], ["attr", "attr"], true),
    };
    kidsOf.set(f, k);
  }
  return k;
}

// Whether the view layer takes list(..., { pure }) yet; until then only impure lists are compared.
const PURE_LISTS = false;

// Per run (its log): the items of "objs" lists by id, and their one render function.
const stores = new WeakMap<
  string[],
  {
    items: Map<number, { id: number; label: string }>;
    render: (o: never) => never;
  }
>();

function realizeChild(f: Family, r: ChildRecipe, log: string[]): unknown {
  const K = kids(f);
  switch (r.t) {
    case "none":
      return null;
    case "text":
      return r.v;
    case "em":
      return K.Em([r.v]);
    case "strong":
      return K.Strong([r.v]);
    case "node": {
      const q = document.createElement("q");
      q.textContent = r.v;
      return q;
    }
    case "list":
      return f.list(
        r.ids,
        ((id: number) => id) as never,
        ((id: number) => K.Li([() => log.push(`li ${id}`), `${id}${r.v}`])) as never,
      );
    case "lanes":
      return f.list(
        r.lanes,
        ((l: { id: string }) => l.id) as never,
        ((l: { id: string; ids: readonly number[] }) =>
          K.Lane([
            () => log.push(`lane ${l.id}`),
            l.id,
            f.list(l.ids, ((id: number) => id) as never, ((id: number) => K.Card([`c${id % 2}`, id])) as never),
          ])) as never,
      );
    case "dots":
      return f.list(
        r.ids,
        ((id: number) => id) as never,
        ((id: number) => K.Dot([id, id % 2 ? "odd" : null])) as never,
      );
    case "objs": {
      let st = stores.get(log);
      if (st === undefined) {
        const render = ((o: { id: number; label: string }) => K.Card([`c${o.id % 2}`, o.label])) as never;
        stores.set(log, (st = { items: new Map(), render }));
      }
      const items = r.ids.map((id) => {
        let o = st.items.get(id);
        if (o === undefined) st.items.set(id, (o = { id, label: "" }));
        o.label = `${r.label}${id}`; // in place: the same object, a new label
        return o;
      });
      return f.list(items, ((o: { id: number }) => o.id) as never, st.render, {
        pure: r.pure,
      });
    }
    case "runs":
      return f.runs(
        r.runs.map((x) =>
          typeof x === "string" ? x : x.em === "n" ? K.Tag([() => log.push(`run ${x.em}`), x.em]) : K.Em([x.em]),
        ),
      );
  }
}

function realize(f: Family, kinds: readonly HoleKind[], vals: readonly ValueRecipe[], log: string[]): unknown[] {
  return vals.map((v, i) => {
    const k = kinds[i]!;
    if (k === "child") return realizeChild(f, v as ChildRecipe, log);
    if (k === "on" || k === "focus") {
      const h = (v as { handler: number }).handler;
      return h < 0
        ? null
        : function (this: FakeElement, e: FakeEvent) {
            log.push(`hole ${i} v${h} ${e.type} on ${this.tagName}`);
          };
    }
    return v;
  });
}

// ---------------------------------------------------------------------------
// Comparing trees
// ---------------------------------------------------------------------------

function canon(n: FakeNode): string {
  let out = "",
    textRun = "";
  const flush = (): void => {
    if (textRun !== "") out += JSON.stringify(textRun);
    textRun = "";
  };
  for (const c of n.childNodes) {
    if (c.nodeType === 3) textRun += c.textContent ?? "";
    else if (c.nodeType === 1) {
      flush();
      const e = c as FakeElement;
      const attrs = e.attributes
        .map((a) => `${a.name}=${JSON.stringify(a.value)}`)
        .sort()
        .join(" ");
      const ns = e.namespaceURI.endsWith("svg") ? "svg:" : "";
      const value = e.tagName === "INPUT" ? ` .value=${JSON.stringify(e["value"])}` : "";
      out += `<${ns}${e.tagName.toLowerCase()}${attrs ? " " + attrs : ""}${value}>${canon(c)}</>`;
    } else if (c.nodeType === 8) {
      flush();
      out += "<!---->";
    }
  }
  flush();
  return out;
}

// Exact node structure (text nodes kept apart, empty ones included).
function structure(n: FakeNode): string {
  return n.childNodes
    .map((c) => (c.nodeType === 1 ? `<${(c as FakeElement).tagName}>${structure(c)}</>` : c.nodeType === 3 ? "#" : "!"))
    .join("");
}

// Fire click and focus on every element in document order; record which handlers run.
function fire(root: FakeElement, log: string[]): string[] {
  const all: FakeElement[] = [];
  const walk = (n: FakeNode): void => {
    for (const c of n.childNodes)
      if (c.nodeType === 1) {
        all.push(c as FakeElement);
        walk(c);
      }
  };
  walk(root);
  const out: string[] = [];
  for (const e of all)
    for (const type of ["click", "focus"]) {
      log.length = 0;
      e.dispatchEvent(new FakeEvent(type));
      out.push(log.join(","));
    }
  return out;
}

const opsNow = (): string => JSON.stringify(ops);
function opsDelta(before: string): string {
  const b = JSON.parse(before) as Record<string, number>;
  return Object.entries(ops)
    .map(([k, v]) => `${k}:${v - b[k]!}`)
    .join(" ");
}

interface Run {
  page: FakeElement;
  log: string[];
  root: Element | null;
}

function step(f: Family, run: Run, shape: Shape, mode: Mode, vals: readonly ValueRecipe[]): string {
  const V = f.view(shape.parts, shape.kinds, shape.svg);
  const values = realize(f, shape.kinds, vals, run.log);
  const d = V(values) as never;
  const host = shape.svg ? (run.page.firstChild as FakeElement) : run.page;
  if (mode === "inmount" && run.root === null) {
    // Made before the step's DOM work is counted: only the render into it counts.
    const Host = f.view(['<div class="host" onclick=', "></div>"], ["on"], false);
    run.root = f.mount(Host([() => run.log.push("host")]) as never);
    host.appendChild(run.root as unknown as FakeNode);
  }
  const before = opsNow();
  if (mode === "inmount") f.render(run.root!, d);
  else if (mode === "mount") {
    if (run.root === null) {
      run.root = f.mount(d);
      host.appendChild(run.root as unknown as FakeNode);
    } else f.update(run.root, d);
  } else (mode === "direct" ? f.renderDirect : f.render)(asDom(host), d);
  return opsDelta(before);
}

function newRun(shape: Shape): Run {
  const page = document.createElement("main") as unknown as FakeElement;
  if (shape.svg) {
    // SVG views render inside an <svg> element.
    const tpl = document.createElement("template") as unknown as FakeElement;
    tpl.innerHTML = "<svg></svg>";
    page.appendChild(tpl.firstChild!);
  }
  return { page, log: [], root: null };
}

describe("compiled views equal the view layer", () => {
  test("random templates through random values: same DOM, same DOM work, same handlers, after every step", () => {
    assertProperty(
      "compiled = runtime",
      caseGen,
      ({ shape, mode, versions }) => {
        const runs = FAMILIES.map((f) => ({ f, run: newRun(shape) }));
        for (let s = 0; s < versions.length; s++) {
          const results = runs.map(({ f, run }) => {
            const work = step(f, run, shape, mode, versions[s]!);
            return {
              dom: canon(run.page),
              nodes: structure(run.page),
              work,
              events: fire(run.page, run.log),
            };
          });
          for (let k = 1; k < results.length; k++) {
            const name = `${runs[k]!.f.name} vs ${runs[0]!.f.name}, step ${s}, ${mode}`;
            expect({ name, ...results[k]! }).toEqual({ name, ...results[0]! });
          }
        }
      },
      2500,
    );
  });
});

describe("pure lists", () => {
  test("a pure list skips rows whose item and render function are unchanged; an impure one updates every survivor", () => {
    for (const f of FAMILIES.slice(1)) {
      const Row = f.view(["<li>", "</li>"], ["text"], false);
      const Box = f.view(["<ul>", "</ul>"], ["child"], false);
      for (const pure of [true, false]) {
        const page = document.createElement("main") as unknown as FakeElement;
        const items = [
          { id: 1, label: "a" },
          { id: 2, label: "b" },
        ];
        const render = ((o: { label: string }) => Row([o.label])) as never;
        const show = (xs: readonly { id: number; label: string }[], r: never = render): void =>
          f.render(asDom(page), Box([f.list(xs, ((o: { id: number }) => o.id) as never, r, { pure })]) as never);
        show(items);
        items[0]!.label = "A"; // in place
        show(items.slice());
        expect({ f: f.name, pure, dom: canon(page) }).toEqual({
          f: f.name,
          pure,
          dom: `<ul><li>"${pure ? "a" : "A"}"</><li>"b"</></>`,
        });
        // A new object, or a new render function, renders again.
        show([{ id: 1, label: "A" }, items[1]!]);
        expect(canon(page)).toBe(`<ul><li>"A"</><li>"b"</></>`);
        items[1]!.label = "B";
        show([{ id: 1, label: "A" }, items[1]!], ((o: { label: string }) => Row([o.label])) as never);
        expect(canon(page)).toBe(`<ul><li>"A"</><li>"B"</></>`);
      }
    }
  });
});
