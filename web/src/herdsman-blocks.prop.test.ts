// Property-based tests: the library's behavior stated as properties and
// checked on thousands of generated inputs, with shrinking to the smallest
// counterexample on failure. See herdsman-blocks.prop.ts.
import { beforeEach, describe, expect, test } from "bun:test";

import { assertProperty, type Choices, type Gen, keyOf, type KeyStyle, listCase } from "./herdsman-blocks.prop.ts";
import {
  asDom,
  type FakeElement,
  type FakeNode,
  installFakeDocument,
  ops,
  resetOps,
} from "./herdsman-blocks.testdom.ts";
import { attr, cls, html, lis, List, morph, text } from "./herdsman-blocks.ts";

beforeEach(installFakeDocument);

const el = (tag: string): FakeElement => document.createElement(tag) as unknown as FakeElement;
const tx = (t: string): FakeNode => document.createTextNode(t) as unknown as FakeNode;
const keysIn = (p: FakeNode): unknown[] => p.childNodes.map((c) => (c as FakeElement)["key"]);

// O(n^2) reference LIS length, ignoring -1.
function lisLen(a: readonly number[]): number {
  const d = a.map(() => 1);
  let best = 0;
  for (let i = 0; i < a.length; i++) {
    for (let j = 0; j < i; j++) if (a[j]! < a[i]!) d[i] = Math.max(d[i]!, d[j]! + 1);
    best = Math.max(best, d[i]!);
  }
  return best;
}

interface Setup {
  readonly c: ListCase2;
  readonly connected: boolean;
  readonly owns: boolean;
  readonly anchored: boolean;
}
type ListCase2 = ReturnType<ReturnType<typeof listCase>>;

const setupGen: Gen<Setup> = (c: Choices) => {
  const lc = listCase(40)(c);
  const anchored = c.int(4) === 0;
  return { c: lc, connected: c.bool(), owns: !anchored && c.bool(), anchored };
};

function build(s: Setup) {
  const doc = el("html");
  doc.isDocument = s.connected;
  const parent = el("ul");
  doc.appendChild(parent);
  const head = el("h1"),
    foot = el("footer");
  if (s.anchored) {
    parent.appendChild(head);
    parent.appendChild(foot);
  }
  let creates = 0;
  const style: KeyStyle = s.c.style;
  const list = new List<number, FakeElement, string | number>(asDom(parent), {
    key: (id) => keyOf(style, id),
    root: (x) => asDom(x),
    owns: s.owns,
    anchor: s.anchored ? asDom(foot) : null,
    create: (id) => {
      creates++;
      const li = el("li");
      li["key"] = id;
      return li;
    },
    update: () => {},
  });
  return { parent, head, foot, list, creates: () => creates };
}

describe("keyed list properties", () => {
  test("the DOM equals the new list, survivors keep their nodes, and moves are minimal", () => {
    assertProperty(
      "reconcile",
      setupGen,
      (s) => {
        const { parent, head, foot, list, creates } = build(s);
        list.set(s.c.before);
        const nodeOf = new Map(s.c.before.map((id, i) => [id, parent.childNodes[i + (s.anchored ? 1 : 0)]]));
        const made = creates();
        resetOps();
        list.set(s.c.after);

        const kids = keysIn(parent);
        const listKids = s.anchored ? kids.slice(1, -1) : kids;
        expect(listKids).toEqual([...s.c.after]);
        if (s.anchored) {
          expect(parent.firstChild).toBe(head);
          expect(parent.lastChild).toBe(foot);
        }
        const survivors = s.c.after.filter((id) => nodeOf.has(id));
        for (const id of survivors) expect(list.entries[s.c.after.indexOf(id)]).toBe(nodeOf.get(id) as FakeElement);
        expect(creates() - made).toBe(s.c.after.length - survivors.length);
        const oldIndex = new Map(s.c.before.map((id, i) => [id, i]));
        const fullClear = survivors.length === 0 && s.owns;
        if (!fullClear) {
          expect(ops.moves).toBe(survivors.length - lisLen(survivors.map((id) => oldIndex.get(id)!)));
          expect(ops.removes).toBe(s.c.before.length - survivors.length);
          expect(ops.stateMoves).toBe(s.connected ? ops.moves : 0);
        }
      },
      3000,
    );
  });

  test("setting the same items again does no DOM work", () => {
    assertProperty(
      "idempotent",
      setupGen,
      (s) => {
        const { list } = build(s);
        list.set(s.c.before);
        list.set(s.c.after);
        resetOps();
        list.set(s.c.after.slice());
        expect([ops.inserts, ops.moves, ops.removes]).toEqual([0, 0, 0]);
      },
      1500,
    );
  });

  test("lis matches the O(n^2) reference on arbitrary inputs", () => {
    const gen: Gen<number[]> = (c) => Array.from({ length: c.int(60) }, () => (c.int(5) === 0 ? -1 : c.int(40)));
    assertProperty(
      "lis",
      gen,
      (a) => {
        const seq = lis(Int32Array.from(a));
        expect(seq.length).toBe(lisLen(a.filter((v) => v !== -1)));
        for (let k = 1; k < seq.length; k++) expect(a[seq[k]!]!).toBeGreaterThan(a[seq[k - 1]!]!);
      },
      3000,
    );
  });
});

// ---------------------------------------------------------------------------
// Blocks: any sequence of updates ends in the same DOM as a fresh mount
// ---------------------------------------------------------------------------

type V = readonly [string | null, string | boolean | null, number | string | null];
const T = () =>
  html`<p class=${cls<string | null>()} title=${attr<string | boolean | null>()}>${text<number | string | null>()}</p>`;
const pool = {
  cls: ["", "a", "b c", null] as const,
  txt: ["", "x", 0, 42, "<b>", null] as const,
  att: ["", "t", true, false, null] as const,
};
const values: Gen<V> = (c) => [c.pick(pool.cls), c.pick(pool.att), c.pick(pool.txt)] as const;
const seri = (n: FakeNode): string =>
  n.nodeType === 3
    ? JSON.stringify((n as unknown as { data: string }).data)
    : `<${(n as FakeElement).tagName}${(n as FakeElement).attributes
        .map((a) => ` ${a.name}=${JSON.stringify(a.value)}`)
        .sort()
        .join("")}>${n.childNodes.map(seri).join("")}</>`;

describe("block properties", () => {
  test("eager and lazy blocks end in the same DOM as a fresh mount of the final values", () => {
    interface Run {
      first: V;
      updates: [0 | 1 | 2, V][];
    }
    const gen: Gen<Run> = (c) => ({
      first: values(c),
      updates: Array.from({ length: c.int(8) }, () => [c.int(3) as 0 | 1 | 2, values(c)]),
    });
    assertProperty(
      "blocks",
      gen,
      (r) => {
        const tpl = T();
        const eager = tpl.mount(r.first);
        const lazyRoot = tpl.mountRoot(r.first);
        const final: (string | number | boolean | null)[] = [...r.first];
        for (const [i, v] of r.updates) {
          final[i] = v[i];
          if (i === 0) {
            eager.set(0, v[0]);
            tpl.block(lazyRoot).set(0, v[0]);
          } else if (i === 1) {
            eager.set(1, v[1]);
            tpl.block(lazyRoot).set(1, v[1]);
          } else {
            eager.set(2, v[2]);
            tpl.block(lazyRoot).set(2, v[2]);
          }
        }
        const fresh = tpl.mount(final as unknown as V);
        const want = seri(fresh.root as unknown as FakeNode);
        expect(seri(eager.root as unknown as FakeNode)).toBe(want);
        expect(seri(lazyRoot as unknown as FakeNode)).toBe(want);
      },
      3000,
    );
  });
});

// ---------------------------------------------------------------------------
// Morph: the live tree becomes the fresh tree, and morphing again is a no-op
// ---------------------------------------------------------------------------

interface Tree {
  readonly tag?: string;
  readonly key?: string;
  readonly attrs?: Readonly<Record<string, string>>;
  readonly text?: string;
  readonly kids?: readonly Tree[];
}

const treeGen =
  (depth: number): Gen<Tree> =>
  (c) => {
    if (depth === 0 || c.int(4) === 0) return { text: c.pick(["", "a", "b", "hello"]) };
    const attrs: Record<string, string> = {};
    for (const name of ["id", "title", "data-x"]) if (c.int(3) === 0) attrs[name] = c.pick(["1", "2", "z"]);
    return {
      tag: c.pick(["div", "p", "span", "li"]),
      ...(c.int(3) === 0 ? { key: c.pick(["k1", "k2", "k3", "k4"]) } : {}),
      attrs,
      kids: Array.from({ length: c.int(5) }, () => treeGen(depth - 1)(c)),
    };
  };

function make(t: Tree): FakeNode {
  if (t.text !== undefined) return tx(t.text);
  const e = el(t.tag!);
  if (t.key !== undefined) e.setAttribute("data-key", t.key);
  for (const [k, v] of Object.entries(t.attrs ?? {})) e.setAttribute(k, v);
  for (const k of t.kids ?? []) e.appendChild(make(k));
  return e;
}

describe("morph properties", () => {
  test("morph turns any live tree into any fresh tree, and a second morph does nothing", () => {
    const gen: Gen<{ a: readonly Tree[]; b: readonly Tree[] }> = (c) => ({
      a: Array.from({ length: c.int(6) }, () => treeGen(3)(c)),
      b: Array.from({ length: c.int(6) }, () => treeGen(3)(c)),
    });
    assertProperty(
      "morph",
      gen,
      (s) => {
        const live = el("main");
        for (const t of s.a) live.appendChild(make(t));
        const want = el("main");
        for (const t of s.b) want.appendChild(make(t));
        morph(
          asDom(live),
          s.b.map((t) => asDom(make(t))),
        );
        expect(seri(live)).toBe(seri(want));
        const again = s.b.map((t) => asDom(make(t))); // built before counting: only morph's own work counts
        resetOps();
        morph(asDom(live), again);
        expect([ops.inserts, ops.moves, ops.removes, ops.attrWrites, ops.textWrites]).toEqual([0, 0, 0, 0, 0]);
      },
      2000,
    );
  });
});

describe("pure list properties", () => {
  test("a pure list, or one with same, ends like a plain one, with refresh for a mark and invalidate for a suffix", () => {
    interface Item {
      readonly id: number;
      readonly label: string;
    }
    // Each step: an order of ids, per position whether the item is a new
    // object, the marked id (update reads it besides the item, so the pure
    // list refreshes the old and the new marked entry), and a suffix every
    // entry reads (a change invalidates the pure list).
    interface Step {
      readonly ids: number[];
      readonly fresh: boolean[];
      readonly label: string;
      readonly mark: number;
      readonly suffix: string;
    }
    const gen: Gen<Step[]> = (c) =>
      Array.from({ length: 2 + c.int(6) }, () => {
        const pool = [1, 2, 3, 4, 5, 6, 7, 8];
        const ids: number[] = [];
        const n = c.int(9);
        for (let i = 0; i < n && pool.length; i++) ids.push(pool.splice(c.int(pool.length), 1)[0]!);
        return {
          ids,
          fresh: ids.map(() => c.int(3) === 0),
          label: c.pick(["a", "b", "c"]),
          mark: c.int(10),
          suffix: c.pick(["", "", "", "!"]),
        };
      });
    let mark = 0,
      suffix = "";
    const make = (pure: boolean, same?: (a: Item, b: Item) => boolean) => {
      const p = el("ul");
      const write = (li: FakeElement, it: Item): void => {
        li.textContent = `${it.id}${it.label}${it.id === mark ? "*" : ""}${suffix}`;
      };
      const l = new List<Item, FakeElement, number>(asDom(p), {
        key: (it) => it.id,
        create: (it) => {
          const li = el("li");
          write(li, it);
          return li;
        },
        update: write,
        root: (li) => asDom(li),
        owns: true,
        pure,
        ...(same ? { same } : {}),
      });
      return { p, l };
    };
    assertProperty(
      "pure list",
      gen,
      (steps) => {
        const a = make(true),
          b = make(false),
          c = make(false, (x, y) => x.label === y.label); // fresh objects with the same label are the same
        const last = new Map<number, Item>();
        mark = 0;
        suffix = "";
        for (const st of steps) {
          const items = st.ids.map((id, i) => {
            const prev = last.get(id);
            const it = prev === undefined || st.fresh[i] ? { id, label: st.label } : prev;
            last.set(id, it);
            return it;
          });
          const was = mark;
          mark = st.mark;
          if (st.suffix !== suffix) {
            suffix = st.suffix;
            a.l.invalidate();
            c.l.invalidate();
          }
          for (const x of [a, c]) {
            x.l.set(items);
            x.l.refresh(was);
            x.l.refresh(mark);
          }
          b.l.set(items);
          expect(a.p.childNodes.map((n) => n.textContent)).toEqual(b.p.childNodes.map((n) => n.textContent));
          expect(c.p.childNodes.map((n) => n.textContent)).toEqual(b.p.childNodes.map((n) => n.textContent));
        }
      },
      3000,
    );
  });
});
