// Property-based tests for the view layer: random templates that mix child
// holes (views, lists, runs, nodes, text) with text, class, attribute and
// event holes, rendered through a random sequence of values, must end up
// exactly like a fresh render of the last values: the same DOM and the same
// handlers reached from every element. See herdsman-blocks.prop.ts.
import { beforeEach, describe, expect, test } from "bun:test";

import {
  attr,
  child,
  cls,
  html,
  list,
  on,
  render,
  runs,
  text,
  type View,
  type ViewNode,
} from "./herdsman-blocks-view.ts";
import { assertProperty, type Choices, type Gen } from "./herdsman-blocks.prop.ts";
import { asDom, type FakeElement, FakeEvent, type FakeNode, installFakeDocument } from "./herdsman-blocks.testdom.ts";
import { List } from "./herdsman-blocks.ts";

installFakeDocument();
beforeEach(installFakeDocument);

// ---------------------------------------------------------------------------
// Random templates
// ---------------------------------------------------------------------------

type HoleKind = "text" | "cls" | "attr" | "child" | "on";

/** A template's markup as string parts and the kinds of its holes, in order. */
interface Shape {
  readonly parts: string[];
  readonly kinds: HoleKind[];
}

// Appends markup for a sequence of 1 to 4 items to `sh`.
function items(c: Choices, sh: Shape, depth: number): void {
  const n = 1 + c.int(4);
  for (let k = 0; k < n; k++) {
    const hole = (kind: HoleKind, after: string): void => {
      sh.kinds.push(kind);
      sh.parts.push(after);
    };
    const add = (s: string): void => {
      sh.parts[sh.parts.length - 1] += s;
    };
    switch (c.int(depth < 2 ? 8 : 7)) {
      case 0:
        add(`<span class="s">x</span>`);
        break;
      case 1:
        hole("text", ""); // bare text hole between siblings
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
        add(`<u title=`);
        hole("attr", `>t</u>`);
        break;
      case 5:
        hole("child", "");
        break;
      case 6:
        add(`<em onclick=`);
        hole("on", `>e</em>`);
        break;
      case 7:
        add(`<div>`);
        items(c, sh, depth + 1);
        add(`</div>`);
        break;
    }
  }
}

function shapeGen(c: Choices): Shape {
  const sh: Shape = { parts: ["<section>"], kinds: [] };
  items(c, sh, 0);
  sh.parts[sh.parts.length - 1] += "</section>";
  return sh;
}

const markers = { text, cls, attr, child, on } as const;

// One view per distinct markup, as a tagged template literal at one call site would be.
const viewCache = new Map<string, View<readonly unknown[]>>();
function viewOf(sh: Shape): View<readonly unknown[]> {
  const id = sh.parts.join("\u0000") + "|" + sh.kinds.join(",");
  let v = viewCache.get(id);
  if (v === undefined) {
    const strings = Object.assign(sh.parts.slice(), { raw: sh.parts.slice() }) as unknown as TemplateStringsArray;
    const holes = sh.kinds.map((k) => (markers[k] as () => number)());
    v = (html as unknown as (s: TemplateStringsArray, ...h: number[]) => View<readonly unknown[]>)(strings, ...holes);
    viewCache.set(id, v);
  }
  return v;
}

// ---------------------------------------------------------------------------
// Random values, as recipes realized once per tree (a DOM node can only be in one)
// ---------------------------------------------------------------------------

type ChildRecipe =
  | { readonly t: "none" }
  | { readonly t: "text"; readonly v: string | number }
  | { readonly t: "em"; readonly v: string }
  | { readonly t: "strong"; readonly v: string }
  | { readonly t: "node"; readonly v: string }
  | { readonly t: "list"; readonly ids: readonly number[]; readonly v: string }
  | { readonly t: "runs"; readonly runs: readonly (string | { readonly em: string })[] };

type ValueRecipe = string | number | boolean | null | ChildRecipe | { readonly handler: number };

const childGen = (c: Choices): ChildRecipe => {
  switch (c.int(7)) {
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
    case 5: {
      const pool = [1, 2, 3, 4, 5];
      const ids: number[] = [];
      const n = c.int(5);
      for (let i = 0; i < n && pool.length; i++) ids.push(pool.splice(c.int(pool.length), 1)[0]!);
      return { t: "list", ids, v: c.pick(["", "v"]) };
    }
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
      return c.pick(["", "a", "b c", 3, null]);
    case "cls":
      return c.pick(["", "a", "b c", null]);
    case "attr":
      return c.pick([null, "", "x", false, true, 3]);
    case "child":
      return childGen(c);
    case "on":
      return { handler: c.bool() ? version : -1 }; // -1: no handler
  }
};

interface Case {
  readonly shape: Shape;
  readonly versions: readonly (readonly ValueRecipe[])[];
}

const caseGen: Gen<Case> = (c) => {
  const shape = shapeGen(c);
  const n = 2 + c.int(3);
  const versions: ValueRecipe[][] = [];
  for (let v = 0; v < n; v++) versions.push(shape.kinds.map((k) => valueGen(c, k, v)));
  return { shape, versions };
};

const Em = html`<dfn onclick=${on<"click">()}>${text()}</dfn>`;
const Strong = html`<strong class=${cls()}>z</strong>`;
const Li = html`<li onclick=${on<"click">()}>${text()}</li>`;

function realizeChild(r: ChildRecipe, log: string[]): unknown {
  const h = (tag: string) => () => log.push(tag);
  switch (r.t) {
    case "none":
      return null;
    case "text":
      return r.v;
    case "em":
      return Em([h(`em ${r.v}`), r.v]);
    case "strong":
      return Strong([r.v]);
    case "node": {
      const q = document.createElement("q");
      q.textContent = r.v;
      return q;
    }
    case "list":
      return list(
        r.ids,
        (id) => id,
        (id) => Li([h(`li ${id}${r.v}`), `${id}${r.v}`]),
      );
    case "runs":
      return runs(r.runs.map((x) => (typeof x === "string" ? x : Em([h(`run ${x.em}`), x.em]))));
  }
}

function realize(kinds: readonly HoleKind[], vals: readonly ValueRecipe[], log: string[]): unknown[] {
  return vals.map((v, i) => {
    const k = kinds[i]!;
    if (k === "child") return realizeChild(v as ChildRecipe, log);
    if (k === "on") {
      const h = (v as { handler: number }).handler;
      return h < 0 ? null : () => log.push(`hole ${i} v${h}`);
    }
    return v;
  });
}

// ---------------------------------------------------------------------------
// Comparing trees
// ---------------------------------------------------------------------------

// Elements with their class and attributes, and text with adjacent runs merged.
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
      out += `<${e.tagName.toLowerCase()}${attrs ? " " + attrs : ""}>${canon(c)}</>`;
    }
  }
  flush();
  return out;
}

// Click every element in document order; record which handlers run.
function clicks(root: FakeElement, log: string[]): string[] {
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
  for (const e of all) {
    log.length = 0;
    e.dispatchEvent(new FakeEvent("click"));
    const got = log.join(",");
    // Every list item and nested view row has a handler: delegation must reach it.
    const tagName = e.tagName.toLowerCase();
    if (tagName === "li" && !got.startsWith("li ")) throw new Error(`li handler not reached: ${got}`);
    if (tagName === "dfn" && !/^(em|run) /.test(got)) throw new Error(`dfn handler not reached: ${got}`);
    out.push(got);
  }
  return out;
}

describe("view properties", () => {
  test("rendering a sequence of values ends like a fresh render of the last ones", () => {
    assertProperty(
      "patch equals fresh mount",
      caseGen,
      ({ shape, versions }) => {
        const V = viewOf(shape) as unknown as (vals: readonly unknown[]) => ViewNode;
        const patchedLog: string[] = [],
          freshLog: string[] = [];
        const patched = document.createElement("main") as unknown as FakeElement;
        for (const vals of versions) render(asDom(patched), V(realize(shape.kinds, vals, patchedLog)));
        const fresh = document.createElement("main") as unknown as FakeElement;
        render(asDom(fresh), V(realize(shape.kinds, versions[versions.length - 1]!, freshLog)));
        expect(canon(patched)).toBe(canon(fresh));
        expect(clicks(patched, patchedLog)).toEqual(clicks(fresh, freshLog));
      },
      3000,
    );
  });

  test("a low-level list in a node held by a child hole and view re-renders never clobber each other", () => {
    // Each step either re-renders the view (new title, same node in the
    // child hole, or the node dropped and put back) or sets the low-level
    // list inside that node.
    type Step = { readonly view: string; readonly keep: boolean } | { readonly ids: readonly number[] };
    const gen: Gen<Step[]> = (c) =>
      Array.from({ length: 2 + c.int(8) }, (): Step => {
        if (c.bool()) return { view: c.pick(["a", "b", "c"]), keep: c.int(4) > 0 };
        const pool = [1, 2, 3, 4, 5, 6];
        const ids: number[] = [];
        const n = c.int(7);
        for (let i = 0; i < n && pool.length; i++) ids.push(pool.splice(c.int(pool.length), 1)[0]!);
        return { ids };
      });
    const Panel = html`<section>
      <h1>${text()}</h1>
      ${child<Node | null>()}
      <p>${text()}</p>
    </section>`;
    assertProperty(
      "hot node",
      gen,
      (steps) => {
        const root = document.createElement("main") as unknown as FakeElement;
        const host = document.createElement("ul");
        const rows = new List<number, FakeElement, number>(host, {
          key: (n) => n,
          create: (n) => {
            const li = document.createElement("li") as unknown as FakeElement;
            li.textContent = `${n}`;
            return li;
          },
          update: () => {},
          root: (li) => asDom(li),
          owns: true,
        });
        let title = "",
          ids: readonly number[] = [];
        render(asDom(root), Panel([title, host, title]));
        for (const st of steps) {
          if ("view" in st) {
            title = st.view;
            render(asDom(root), Panel([title, st.keep ? host : null, title]));
            if (!st.keep) render(asDom(root), Panel([title, host, title])); // and back
          } else {
            ids = st.ids;
            rows.set(ids);
          }
          const sec = root.firstChild as FakeElement;
          const els = sec.childNodes.filter((n) => n.nodeType === 1) as FakeElement[];
          expect(els.map((e) => e.tagName.toLowerCase())).toEqual(["h1", "ul", "p"]);
          expect(els[0]!.textContent).toBe(title);
          expect(els[2]!.textContent).toBe(title);
          expect(els[1]).toBe(host as unknown as FakeElement);
          expect((host as unknown as FakeElement).childNodes.map((n) => n.textContent)).toEqual(ids.map(String));
        }
      },
      2000,
    );
  });

  test("a pure list, or one with same, with a selection renders like a plain one", () => {
    interface Item {
      readonly id: number;
      readonly label: string;
    }
    interface Step {
      readonly ids: readonly number[];
      readonly fresh: readonly boolean[]; // per position: a new object for the item, or the last one
      readonly labels: readonly string[];
      readonly mode: number; // what the render function closes over
      readonly sel: number | undefined; // the selected key, maybe absent from the list
    }
    const stepGen = (c: Choices): Step => {
      const pool = [1, 2, 3, 4, 5, 6, 7];
      const ids: number[] = [];
      const n = c.int(8);
      for (let i = 0; i < n && pool.length; i++) ids.push(pool.splice(c.int(pool.length), 1)[0]!);
      return {
        ids,
        fresh: ids.map(() => c.int(3) === 0),
        labels: ids.map(() => c.pick(["a", "b"])),
        mode: c.int(2),
        sel: c.int(3) === 0 ? undefined : c.int(9),
      };
    };
    const gen: Gen<Step[]> = (c) => Array.from({ length: 2 + c.int(5) }, () => stepGen(c));
    const Row = html`<li class=${cls()}>${text()}</li>`;
    const Box = html`<ul>
      ${child()}
    </ul>`;
    // One render function per mode, so the same mode gives the same function.
    const renders = [0, 1].map(
      (m) => (it: Item, selected: boolean) => Row([`m${m}${selected ? " sel" : ""}`, `${it.id}${it.label}`]),
    );
    assertProperty(
      "pure list equals plain list",
      gen,
      (steps) => {
        const pureBox = document.createElement("main") as unknown as FakeElement;
        const plainBox = document.createElement("main") as unknown as FakeElement;
        const sameBox = document.createElement("main") as unknown as FakeElement;
        const last = new Map<number, Item>();
        for (const st of steps) {
          const items = st.ids.map((id, i) => {
            const prev = last.get(id);
            const it = prev === undefined || st.fresh[i] ? { id, label: st.labels[i]! } : prev;
            last.set(id, it);
            return it;
          });
          const r = renders[st.mode]!;
          render(asDom(pureBox), Box([list(items, (it) => it.id, r, { pure: true, selected: st.sel })]));
          render(
            asDom(plainBox),
            Box([
              list(
                items,
                (it) => it.id,
                (it) => r(it, it.id === st.sel),
              ),
            ]),
          );
          const same = (x: Item, y: Item): boolean => x.label === y.label;
          render(asDom(sameBox), Box([list(items, (it) => it.id, r, { same, selected: st.sel })]));
          expect(canon(pureBox)).toBe(canon(plainBox));
          expect(canon(sameBox)).toBe(canon(plainBox));
        }
      },
      3000,
    );
  });
});
