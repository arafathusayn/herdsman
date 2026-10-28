// Unit tests for herdsman-react on the fake DOM. Behavior against
// React itself is checked in headless Chrome by
// bench/herdsman-cases/react-compat.ts, which runs the same scenarios on
// React 19 and on this module and compares what they render and log.

import { beforeEach, describe, expect, test } from "bun:test";

import { FakeElement, FakeEvent, installFakeDocument, ops, resetOps } from "./herdsman-blocks.testdom.ts";
import {
  __block,
  __delta,
  __template,
  createContext,
  createElement as h,
  createRoot,
  flushSync,
  memo,
  type Root,
  useContext,
  useEffect,
  useLayoutEffect,
  useState,
} from "./herdsman-react.ts";

let container: FakeElement;
let root: Root;

beforeEach(() => {
  installFakeDocument();
  // A container "in the document", so moves can keep state (moveBefore).
  container = new FakeElement("DIV");
  container.isDocument = true;
  root = createRoot(container as unknown as Element);
});

const html = (n: FakeElement | null = container): string => {
  if (n === null) return "";
  let out = "";
  for (let c = n.firstChild; c !== null; c = c.nextSibling) {
    if (c instanceof FakeElement) out += `<${c.tagName.toLowerCase()}>${html(c)}</${c.tagName.toLowerCase()}>`;
    else out += c.textContent;
  }
  return out;
};
const render = (el: unknown): void => flushSync(() => root.render(el));
const items = (keys: string[]) => h("ul", null, ...keys.map((k) => h("li", { key: k }, k)));

describe("keyed children", () => {
  test("a reorder costs the minimum number of moves, and moved nodes keep their state", () => {
    const keys = "abcdefghij".split("");
    render(items(keys));
    const nodes = new Map<string, unknown>();
    for (let c = container.firstChild!.firstChild; c !== null; c = c.nextSibling) nodes.set(c.textContent, c);

    // ops.moves counts every move; ops.stateMoves those that kept state.
    resetOps();
    render(items(["j", ...keys.slice(0, 9)])); // last to first: one move
    expect(ops.moves).toBe(1);
    expect(ops.stateMoves).toBe(1);
    expect(ops.inserts).toBe(0);

    resetOps();
    // From j,a,b,...,i to j,i,h,...,a: j stays first, the other nine reverse,
    // which takes 9 - 1 moves.
    render(items([...keys].reverse()));
    expect(ops.moves).toBe(8);
    expect(ops.stateMoves).toBe(8);

    expect(html()).toBe(
      "<ul>" +
        [...keys]
          .reverse()
          .map((k) => `<li>${k}</li>`)
          .join("") +
        "</ul>",
    );
    for (let c = container.firstChild!.firstChild; c !== null; c = c.nextSibling)
      expect(nodes.get(c.textContent)).toBe(c);
  });

  test("inserts and removals leave the other nodes alone", () => {
    render(items(["a", "b", "c"]));
    resetOps();
    render(items(["a", "x", "b", "c", "y"]));
    expect(ops.inserts).toBe(4); // two items, each with its text
    expect(ops.moves).toBe(0);
    resetOps();
    render(items(["x", "c"]));
    expect(ops.removes).toBe(3);
    expect(ops.moves).toBe(0);
    expect(html()).toBe("<ul><li>x</li><li>c</li></ul>");
  });

  test("a component that changes its root element is placed where it was", () => {
    let flip!: () => void;
    function Toggle() {
      const [on, setOn] = useState(false);
      flip = () => setOn((v) => !v);
      return on ? h("b", null, "on") : h("i", null, "off");
    }
    render(h("p", null, "a", h(Toggle), "z"));
    flushSync(flip);
    expect(html()).toBe("<p>a<b>on</b>z</p>");
    flushSync(flip);
    expect(html()).toBe("<p>a<i>off</i>z</p>");
  });
});

describe("rendering", () => {
  test("an element identical to the last one skips its subtree", () => {
    let renders = 0;
    function Child() {
      renders++;
      return h("span", null, "child");
    }
    let bump!: () => void;
    function Parent({ children }: { children?: unknown }) {
      const [n, setN] = useState(0);
      bump = () => setN((x) => x + 1);
      return h("div", null, n, children);
    }
    render(h(Parent, null, h(Child)));
    expect(renders).toBe(1);
    flushSync(bump);
    flushSync(bump);
    expect(renders).toBe(1);
    expect(html()).toBe("<div>2<span>child</span></div>");
  });

  test("updates batch and render in a microtask; flushSync renders at once", async () => {
    let renders = 0;
    let set!: (f: (n: number) => number) => void;
    function App() {
      const [n, setN] = useState(0);
      set = setN;
      renders++;
      return h("b", null, n);
    }
    render(h(App));
    set((n) => n + 1);
    set((n) => n + 1);
    expect(html()).toBe("<b>0</b>");
    await Promise.resolve();
    expect(html()).toBe("<b>2</b>");
    expect(renders).toBe(2);
    flushSync(() => set((n) => n + 1));
    expect(html()).toBe("<b>3</b>");
  });

  test("context reaches consumers through a memo boundary", () => {
    const Theme = createContext("light");
    let frozen = 0;
    const Reader = () => h("i", null, useContext(Theme));
    const Frozen = memo(() => {
      frozen++;
      return h(Reader);
    });
    render(h(Theme, { value: "dark" }, h(Frozen)));
    render(h(Theme, { value: "blue" }, h(Frozen)));
    expect(html()).toBe("<i>blue</i>");
    expect(frozen).toBe(1);
  });
});

describe("effects", () => {
  test("children's effects run first, and cleanups run before the next effects", () => {
    const log: string[] = [];
    function Child({ n }: { n: number }) {
      useLayoutEffect(() => {
        log.push("child layout " + n);
        return () => log.push("child layout cleanup " + n);
      });
      useEffect(() => {
        log.push("child effect " + n);
        return () => log.push("child effect cleanup " + n);
      });
      return null;
    }
    function Parent({ n }: { n: number }) {
      useEffect(() => {
        log.push("parent effect " + n);
      });
      return h(Child, { n });
    }
    render(h(Parent, { n: 1 }));
    expect(log.splice(0)).toEqual(["child layout 1", "child effect 1", "parent effect 1"]);
    render(h(Parent, { n: 2 }));
    expect(log.splice(0)).toEqual([
      "child layout cleanup 1",
      "child layout 2",
      "child effect cleanup 1",
      "child effect 2",
      "parent effect 2",
    ]);
    render(null);
    expect(log.splice(0)).toEqual(["child layout cleanup 2", "child effect cleanup 2"]);
  });
});

describe("events", () => {
  test("a click bubbles through handlers, and its updates render after it", async () => {
    const log: string[] = [];
    function App() {
      const [n, setN] = useState(0);
      return h(
        "div",
        { onClick: () => log.push("div " + n) },
        h("button", { onClick: () => (log.push("button " + n), setN(n + 1)) }, n),
      );
    }
    render(h(App));
    const button = container.firstChild!.firstChild as FakeElement;
    button.dispatchEvent(new FakeEvent("click"));
    expect(log).toEqual(["button 0", "div 0"]);
    await Promise.resolve();
    expect(html()).toBe("<div><button>1</button></div>");
  });
});

// A component whose render is one compiled block hosts it in its own fiber
// (runFunc): these cover that fusion, and unfusing when the render changes.
describe("components that render one block", () => {
  const ROW = __template(["tr", { className: { h: 0 } }, ["td", 0, 1], ["td", 0, ["a", { onClick: { h: 2 } }, 3]]]);
  const OTHER = __template(["p", 0, 0]);
  const LIST = __template(["table", 0, ["tbody", 0, 0]]);
  const row = (id: string, label: string, sel: boolean, pick: () => void) =>
    __block(ROW, null, [sel ? "danger" : "", id, pick, label]);
  const Row = memo(function Row({
    id,
    label,
    sel,
    pick,
  }: {
    id: string;
    label: string;
    sel: boolean;
    pick: (id: string) => void;
  }) {
    return row(id, label, sel, () => pick(id));
  });
  const list = (rows: [string, string][], sel: string, pick: (id: string) => void = () => {}) =>
    __block(LIST, null, [rows.map(([id, label]) => h(Row, { key: id, id, label, sel: sel === id, pick }))]);
  const trs = () => [...(container.firstChild!.firstChild as FakeElement).childNodes] as FakeElement[];

  test("rows mount, update in place and keep their nodes", () => {
    render(
      list(
        [
          ["1", "a"],
          ["2", "b"],
        ],
        "",
      ),
    );
    expect(html()).toBe(
      "<table><tbody><tr><td>1</td><td><a>a</a></td></tr><tr><td>2</td><td><a>b</a></td></tr></tbody></table>",
    );
    const [r1, r2] = trs();
    render(
      list(
        [
          ["1", "a"],
          ["2", "B"],
        ],
        "2",
      ),
    );
    expect(html()).toBe(
      "<table><tbody><tr><td>1</td><td><a>a</a></td></tr><tr><td>2</td><td><a>B</a></td></tr></tbody></table>",
    );
    expect(trs()).toEqual([r1!, r2!]);
    expect(r2!.getAttribute("class")).toBe("danger");
    expect(r1!.getAttribute("class")).toBe(null);
  });

  test("keyed moves and removals move the rows' own nodes", () => {
    render(
      list(
        [
          ["1", "a"],
          ["2", "b"],
          ["3", "c"],
        ],
        "",
      ),
    );
    const [r1, r2, r3] = trs();
    render(
      list(
        [
          ["3", "c"],
          ["1", "a"],
        ],
        "",
      ),
    );
    expect(trs()).toEqual([r3!, r1!]);
    render(
      list(
        [
          ["1", "a"],
          ["4", "d"],
          ["3", "c"],
        ],
        "",
      ),
    );
    expect(html()).toBe(
      "<table><tbody><tr><td>1</td><td><a>a</a></td></tr><tr><td>4</td><td><a>d</a></td></tr><tr><td>3</td><td><a>c</a></td></tr></tbody></table>",
    );
    expect(trs()[0]).toBe(r1!);
    expect(trs()[2]).toBe(r3!);
    expect(trs()).not.toContain(r2!);
  });

  test("a click reaches the hole's handler, and the row's state renders in place", async () => {
    const picked: string[] = [];
    render(
      list(
        [
          ["1", "a"],
          ["2", "b"],
        ],
        "",
        (id) => picked.push(id),
      ),
    );
    const a = trs()[1]!.lastChild!.firstChild as FakeElement;
    a.dispatchEvent(new FakeEvent("click"));
    expect(picked).toEqual(["2"]);

    let bump = () => {};
    function Counter() {
      const [n, setN] = useState(0);
      bump = () => setN((x) => x + 1);
      return row("c", "n" + n, n > 0, () => {});
    }
    render(__block(LIST, null, [[h(Counter, { key: "c" })]]));
    const tr = trs()[0]!;
    flushSync(() => bump());
    expect(html()).toBe("<table><tbody><tr><td>c</td><td><a>n1</a></td></tr></tbody></table>");
    expect(trs()[0]).toBe(tr);
    expect(tr.getAttribute("class")).toBe("danger");
  });

  test("a component that stops rendering its block unfuses, and its effects clean up", () => {
    const log: string[] = [];
    let set: (v: number) => void = () => {};
    function Shape() {
      const [v, setV] = useState(0);
      set = setV;
      useEffect(() => () => void log.push("cleanup"), []);
      if (v === 0) return __block(OTHER, null, ["first"]);
      if (v === 1) return __block(OTHER, "k2", ["keyed"]);
      if (v === 2) return "text";
      if (v === 3) return h("b", null, "host");
      return __block(ROW, null, ["", "x", () => {}, "row"]);
    }
    render(h("div", null, h("i", null, "<"), h(Shape), h("i", null, ">")));
    expect(html()).toBe("<div><i><</i><p>first</p><i>></i></div>");
    const p0 = (container.firstChild as FakeElement).childNodes[1];
    flushSync(() => set(1));
    expect(html()).toBe("<div><i><</i><p>keyed</p><i>></i></div>");
    expect((container.firstChild as FakeElement).childNodes[1]).not.toBe(p0);
    flushSync(() => set(2));
    expect(html()).toBe("<div><i><</i>text<i>></i></div>");
    flushSync(() => set(3));
    expect(html()).toBe("<div><i><</i><b>host</b><i>></i></div>");
    flushSync(() => set(4));
    expect(html()).toBe("<div><i><</i><tr><td>x</td><td><a>row</a></td></tr><i>></i></div>");
    render(h("div", null));
    expect(html()).toBe("<div></div>");
    expect(log).toEqual(["cleanup"]);
  });

  test("a component that renders nothing first hosts its block once it renders one", () => {
    let show: (v: boolean) => void = () => {};
    function Later() {
      const [on, setOn] = useState(false);
      show = setOn;
      return on ? __block(OTHER, null, ["late"]) : null;
    }
    render(h("div", null, h("i", null, "<"), h(Later), h("i", null, ">")));
    expect(html()).toBe("<div><i><</i><i>></i></div>");
    flushSync(() => show(true));
    expect(html()).toBe("<div><i><</i><p>late</p><i>></i></div>");
    flushSync(() => show(false));
    expect(html()).toBe("<div><i><</i><i>></i></div>");
  });

  test("a hosted block's list hole reconciles its own children", () => {
    function Table({ rows }: { rows: [string, string][] }) {
      return __block(LIST, null, [
        rows.map(([id, label]) => h(Row, { key: id, id, label, sel: false, pick: () => {} })),
      ]);
    }
    render(h(Table, { rows: [["1", "a"]] }));
    render(
      h(Table, {
        rows: [
          ["2", "b"],
          ["1", "a"],
        ],
      }),
    );
    expect(html()).toBe(
      "<table><tbody><tr><td>2</td><td><a>b</a></td></tr><tr><td>1</td><td><a>a</a></td></tr></tbody></table>",
    );
    render(h(Table, { rows: [] }));
    expect(html()).toBe("<table><tbody></tbody></table>");
  });
});

// A compiled list loop reports which positions got a new element (__delta);
// the rows between are left alone. These make the reports by hand, as the
// loop would, and check the result is what a full reconcile gives.
describe("list updates from a loop's report", () => {
  const ROW = __template(["tr", { className: { h: 0 } }, ["td", 0, 1], ["td", 0, 2]]);
  const LIST = __template(["table", 0, ["tbody", 0, 0]]);
  type Item = { id: string; label: string };
  let prev: unknown[] = [];
  const Row = memo(function Row({ row, sel }: { row: Item; sel: boolean }) {
    return __block(ROW, null, [sel ? "danger" : "", row.id, row.label]);
  });
  // Last list's element where its row and selection are the same, as the loop does.
  const list = (rows: Item[], sel: string, report = true) => {
    const out: unknown[] = [],
      idx: number[] = [];
    rows.forEach((row, i) => {
      const old = prev[i] as { props: { row: Item; sel: boolean } } | undefined;
      const el =
        old !== undefined && old.props.row === row && old.props.sel === (sel === row.id)
          ? old
          : h(Row, { key: row.id, row, sel: sel === row.id });
      out.push(el);
      if (el !== prev[i]) idx.push(i);
    });
    if (report && out.length === prev.length) __delta(out, prev, idx);
    prev = out;
    return __block(LIST, null, [out]);
  };
  const rows = (n: number) => Array.from({ length: n }, (_, i) => ({ id: "" + (i + 1), label: "r" + (i + 1) }));
  const tbody = () => container.firstChild!.firstChild as FakeElement;
  const text = () => [...tbody().childNodes].map((tr) => (tr as FakeElement).textContent).join(",");
  beforeEach(() => {
    prev = [];
  });

  test("a changed row is patched in place and the others keep their nodes", () => {
    const rs = rows(5);
    render(list(rs, ""));
    const before = [...tbody().childNodes];
    render(list(rs, "3"));
    expect((tbody().childNodes[2] as FakeElement).className).toBe("danger");
    render(list(rs, "4"));
    expect([...tbody().childNodes].map((tr) => (tr as FakeElement).className)).toEqual(["", "", "", "danger", ""]);
    expect([...tbody().childNodes]).toEqual(before);
    const next = rs.slice();
    next[1] = { id: "2", label: "changed" };
    render(list(next, "4"));
    expect(text()).toBe("1r1,2changed,3r3,4r4,5r5");
  });

  test("two rows that traded places move, near or far, and keep their nodes", () => {
    const rs = rows(6);
    render(list(rs, ""));
    const nodes = [...tbody().childNodes];
    // Each row's node by its original position.
    const order = () => [...tbody().childNodes].map((n) => nodes.indexOf(n));
    const far = rs.slice();
    [far[1], far[4]] = [far[4]!, far[1]!];
    render(list(far, ""));
    expect(text()).toBe("1r1,5r5,3r3,4r4,2r2,6r6");
    expect(order()).toEqual([0, 4, 2, 3, 1, 5]);
    const near = far.slice();
    [near[2], near[3]] = [near[3]!, near[2]!];
    render(list(near, "4"));
    expect(text()).toBe("1r1,5r5,4r4,3r3,2r2,6r6");
    expect(order()).toEqual([0, 4, 3, 2, 1, 5]);
    expect((nodes[3] as FakeElement).className).toBe("danger");
    // And back, with a full reconcile: the rows are where the report left them.
    render(list(rows(6), "", false));
    expect(text()).toBe("1r1,2r2,3r3,4r4,5r5,6r6");
  });

  test("a report that does not fit (a row replaced by another key) falls back to the whole list", () => {
    const rs = rows(4);
    render(list(rs, ""));
    const next = rs.slice();
    next[2] = { id: "9", label: "new" };
    render(list(next, ""));
    expect(text()).toBe("1r1,2r2,9new,4r4");
    // Three swapped around: three changed positions, not a two-row swap.
    const rot = [next[0]!, next[2]!, next[3]!, next[1]!];
    render(list(rot, "2"));
    expect(text()).toBe("1r1,9new,4r4,2r2");
    expect((tbody().childNodes[3] as FakeElement).className).toBe("danger");
  });

  test("a stale report, or one for another list, is ignored", () => {
    const rs = rows(3);
    render(list(rs, ""));
    // A report for arrays this slot never rendered.
    __delta([], [], []);
    render(list(rs, "2", false));
    expect((tbody().childNodes[1] as FakeElement).className).toBe("danger");
    const other = rows(3).map((r) => ({ ...r, label: r.label + "!" }));
    const out = other.map((row) => h(Row, { key: row.id, row, sel: false }));
    // Against a list this slot did not render (another instance of the loop's call site).
    __delta(out, out.slice(), []);
    prev = out;
    render(__block(LIST, null, [out]));
    expect(text()).toBe("1r1!,2r2!,3r3!");
  });

  test("a row's own update and a context change under an unchanged row still render", () => {
    const Ctx = createContext("a");
    let bump: () => void = () => {};
    const Cell = () => h("i", null, useContext(Ctx));
    const Counted = memo(function Counted({ row }: { row: Item }) {
      const [n, set] = useState(0);
      if (row.id === "2") bump = () => set((x) => x + 1);
      return h("tr", null, row.id + ":" + n, h(Cell, null));
    });
    const rs = rows(3);
    let last: unknown[] = [];
    const table = (v: string, sel: number) => {
      const out = rs.map((row, i) => (last[i] !== undefined && i !== sel ? last[i] : h(Counted, { key: row.id, row })));
      const idx = out.flatMap((el, i) => (el !== last[i] ? [i] : []));
      if (last.length) __delta(out, last, idx);
      last = out;
      return h(Ctx.Provider, { value: v }, __block(LIST, null, [out]));
    };
    render(table("a", -1));
    expect(text()).toBe("1:0a,2:0a,3:0a");
    flushSync(() => {
      bump();
      root.render(table("a", 0));
    });
    expect(text()).toBe("1:0a,2:1a,3:0a");
    render(table("b", 2));
    expect(text()).toBe("1:0b,2:1b,3:0b");
  });
});

describe("memo bailouts in lists", () => {
  test("rows re-render exactly when a prop changes by Object.is, keys added or removed included", () => {
    const renders: string[] = [];
    const Item = memo(function Item(p: Record<string, unknown>) {
      renders.push(String(p["id"]));
      return h("li", null, String(p["id"]));
    });
    const list = (props: Record<string, unknown>[]) =>
      h("ul", null, ...props.map((p) => h(Item, { key: String(p["id"]), ...p })));
    render(list([{ id: 1, v: NaN }, { id: 2, v: 0 }, { id: 3, v: 1 }, { id: 4 }, { id: 5, v: undefined }]));
    renders.length = 0;
    render(list([{ id: 1, v: NaN }, { id: 2, v: 0 }, { id: 3, v: 1 }, { id: 4 }, { id: 5, v: undefined }]));
    expect(renders).toEqual([]);
    render(list([{ id: 1, v: NaN }, { id: 2, v: -0 }, { id: 3, v: 1, w: 2 }, { id: 4, v: undefined }, { id: 5 }]));
    expect(renders).toEqual(["2", "3", "4", "5"]);
    renders.length = 0;
    render(list([{ id: 1, v: NaN }, { id: 2, v: -0 }, { id: 3, v: 1 }, { id: 4, v: undefined }, { id: 5 }]));
    expect(renders).toEqual(["3"]);
  });
});

describe("a memoized list's swap", () => {
  test("two rows trade places; rows between them are not rendered again", () => {
    let renders = 0;
    const Item = memo(function Item({ id }: { id: string }) {
      renders++;
      return h("li", null, id);
    });
    const els = ["a", "b", "c", "d", "e"].map((id) => h(Item, { key: id, id }));
    render(h("ul", null, els));
    const lis = [...(container.firstChild as FakeElement).childNodes];
    renders = 0;
    const swapped = [els[0], els[3], els[2], els[1], els[4]];
    render(h("ul", null, swapped));
    expect(html()).toBe("<ul><li>a</li><li>d</li><li>c</li><li>b</li><li>e</li></ul>");
    expect(renders).toBe(0);
    expect([...(container.firstChild as FakeElement).childNodes]).toEqual([
      lis[0]!,
      lis[3]!,
      lis[2]!,
      lis[1]!,
      lis[4]!,
    ]);
    // And back, then a change after the swap renders that row only.
    render(h("ul", null, els));
    expect(html()).toBe("<ul><li>a</li><li>b</li><li>c</li><li>d</li><li>e</li></ul>");
    const changed = [els[0], els[3], els[2], els[1], h(Item, { key: "e", id: "E" })];
    render(h("ul", null, changed));
    expect(html()).toBe("<ul><li>a</li><li>d</li><li>c</li><li>b</li><li>E</li></ul>");
    expect(renders).toBe(1);
  });
});

describe("a memoized list's removal", () => {
  test("rows deleted from the middle go; the rest keep their nodes and are not rendered again", () => {
    let renders = 0;
    const Item = memo(function Item({ id }: { id: string }) {
      renders++;
      return h("li", null, id);
    });
    const els = ["a", "b", "c", "d", "e", "f"].map((id) => h(Item, { key: id, id }));
    render(h("ul", null, els));
    const lis = [...(container.firstChild as FakeElement).childNodes];
    renders = 0;
    render(h("ul", null, [els[0], els[1], els[3], els[4], els[5]]));
    expect(html()).toBe("<ul><li>a</li><li>b</li><li>d</li><li>e</li><li>f</li></ul>");
    expect([...(container.firstChild as FakeElement).childNodes]).toEqual([
      lis[0]!,
      lis[1]!,
      lis[3]!,
      lis[4]!,
      lis[5]!,
    ]);
    render(h("ul", null, [els[0], els[5]]));
    expect(html()).toBe("<ul><li>a</li><li>f</li></ul>");
    // A hole in the rest: the general path, same result.
    render(h("ul", null, [els[0], null, els[5]]));
    render(h("ul", null, [null, els[5]]));
    expect(html()).toBe("<ul><li>f</li></ul>");
    expect(renders).toBe(0);
    // Rows after a removal still reorder correctly.
    render(h("ul", null, [els[5], els[0], els[2]]));
    expect(html()).toBe("<ul><li>f</li><li>a</li><li>c</li></ul>");
  });
});

describe("holes next to text", () => {
  test("a hole where a text child was removes the text", () => {
    render(h("p", null, ["a", "b", "c"]));
    expect(html()).toBe("<p>abc</p>");
    render(h("p", null, [null, "b", "c"]));
    expect(html()).toBe("<p>bc</p>");
    render(h("p", null, ["a", null, null]));
    expect(html()).toBe("<p>a</p>");
    render(h("p", null, [null, null, "c"]));
    expect(html()).toBe("<p>c</p>");
  });
});
