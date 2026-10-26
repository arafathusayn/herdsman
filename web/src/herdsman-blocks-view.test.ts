import { beforeEach, describe, expect, test } from "bun:test";

import { child, cls, html, list, prop, render, text, type ViewNode } from "./herdsman-blocks-view.ts";
import {
  asDom,
  type FakeElement,
  type FakeNode,
  installFakeDocument,
  ops,
  resetOps,
} from "./herdsman-blocks.testdom.ts";

beforeEach(installFakeDocument);

const fake = (n: unknown): FakeElement => n as FakeElement;
const box = (): FakeElement => fake(document.createElement("main"));
const first = (root: FakeElement): FakeElement => root.firstChild as FakeElement;
// Elements and non-empty text, so a slot's empty anchor does not count.
const shape = (n: FakeNode): string =>
  n.nodeType === 3
    ? (n.textContent ?? "")
    : n.nodeType === 1
      ? `<${tag(n)}>${n.childNodes.map(shape).join("")}</${tag(n)}>`
      : "";
const tag = (n: FakeNode): string => (n as FakeElement).tagName.toLowerCase();

describe("child holes", () => {
  const Wrap = html`<div><b>x</b>${child()}<i>y</i></div>`;
  const Tag = html`<em>${text()}</em>`;
  const Other = html`<u>${text()}</u>`;

  test("each kind renders in place and switching replaces the content", () => {
    const root = box();
    const put = (c: Parameters<typeof Wrap>[0][0]) => render(asDom(root), Wrap([c]));
    put("a");
    expect(shape(root)).toBe("<main><div><b>x</b>a<i>y</i></div></main>");
    const div = first(root);
    put(7);
    expect(shape(root)).toBe("<main><div><b>x</b>7<i>y</i></div></main>");
    put(null);
    expect(shape(root)).toBe("<main><div><b>x</b><i>y</i></div></main>");
    const node = document.createElement("span");
    put(node);
    expect(div.childNodes[1]).toBe(fake(node));
    put(Tag(["t"]));
    expect(shape(root)).toBe("<main><div><b>x</b><em>t</em><i>y</i></div></main>");
    put(
      list(
        [1, 2],
        (n) => n,
        (n) => Tag([String(n)]),
      ),
    );
    expect(shape(root)).toBe("<main><div><b>x</b><em>1</em><em>2</em><i>y</i></div></main>");
    put(false);
    expect(shape(root)).toBe("<main><div><b>x</b><i>y</i></div></main>");
    put("back");
    expect(shape(root)).toBe("<main><div><b>x</b>back<i>y</i></div></main>");
    expect(first(root)).toBe(div); // the outer view was patched every time
    expect(div.childNodes.length).toBe(3); // b, the anchor holding "back", i
  });

  test("the same view patches, another view replaces", () => {
    const root = box();
    render(asDom(root), Wrap([Tag(["a"])]));
    const em = first(root).childNodes[1];
    render(asDom(root), Wrap([Tag(["b"])]));
    expect(first(root).childNodes[1]).toBe(em!);
    expect(shape(root)).toBe("<main><div><b>x</b><em>b</em><i>y</i></div></main>");
    render(asDom(root), Wrap([Other(["b"])]));
    expect(first(root).childNodes[1]).not.toBe(em!);
    expect(shape(root)).toBe("<main><div><b>x</b><u>b</u><i>y</i></div></main>");
  });

  test("a root render switches views in place", () => {
    const root = box();
    render(asDom(root), Tag(["a"]));
    const em = first(root);
    render(asDom(root), Tag(["b"]));
    expect(first(root)).toBe(em);
    render(asDom(root), Other(["c"]));
    expect(shape(root)).toBe("<main><u>c</u></main>");
    expect(root.childNodes.length).toBe(1);
  });

  test("unchanged values write nothing", () => {
    const Row = html`<p class=${cls()}>${text()}${child()}</p>`;
    const root = box();
    render(asDom(root), Row(["c", "t", Tag(["x"])]));
    resetOps();
    render(asDom(root), Row(["c", "t", Tag(["x"])]));
    expect(ops).toEqual({
      inserts: 0,
      moves: 0,
      stateMoves: 0,
      removes: 0,
      clones: 0,
      classWrites: 0,
      attrWrites: 0,
      textWrites: 0,
    });
  });
});

describe("keyed lists", () => {
  const Ul = html`<ul>
    ${child()}
  </ul>`;
  const Li = html`<li>${text()}</li>`;
  const items = (ids: number[]) =>
    list(
      ids,
      (n) => n,
      (n) => Li([`#${n}`]),
    );

  test("a reorder keeps every node and moves the minimum", () => {
    const root = box();
    render(asDom(root), Ul([items([1, 2, 3, 4, 5])]));
    const ul = first(root);
    const before = new Map(ul.childNodes.map((n) => [n.textContent, n]));
    resetOps();
    render(asDom(root), Ul([items([5, 1, 2, 3, 4])]));
    expect(ul.childNodes.map((n) => n.textContent).filter(Boolean)).toEqual(["#5", "#1", "#2", "#3", "#4"]);
    for (const n of ul.childNodes) if (n.textContent) expect(before.get(n.textContent)).toBe(n);
    expect(ops.moves + ops.stateMoves).toBe(1);
    expect(ops.inserts + ops.removes + ops.clones).toBe(0);
  });

  test("a list between siblings stays between them", () => {
    const Mid = html`<div>
      <h2>head</h2>
      ${child()}
      <p>foot</p>
    </div>`;
    const root = box();
    render(asDom(root), Mid([items([1, 2])]));
    render(asDom(root), Mid([items([3, 2, 1])]));
    expect(shape(root)).toBe("<main><div><h2>head</h2><li>#3</li><li>#2</li><li>#1</li><p>foot</p></div></main>");
    render(asDom(root), Mid([items([])]));
    expect(shape(root)).toBe("<main><div><h2>head</h2><p>foot</p></div></main>");
  });

  test("a list that owns its parent can clear it and give the slot back", () => {
    const root = box();
    render(asDom(root), Ul([items([1, 2])]));
    render(asDom(root), Ul([items([])]));
    expect(shape(root)).toBe("<main><ul></ul></main>");
    render(asDom(root), Ul([items([4])]));
    render(asDom(root), Ul(["text"]));
    expect(shape(root)).toBe("<main><ul>text</ul></main>");
    render(asDom(root), Ul([items([5, 6])]));
    expect(shape(root)).toBe("<main><ul><li>#5</li><li>#6</li></ul></main>");
  });

  test("an item whose view changes is replaced where it stands", () => {
    const B = html`<li><b>${text()}</b></li>`;
    const root = box();
    const pick = (bold: number) =>
      list(
        [1, 2, 3],
        (n) => n,
        (n): ViewNode => (n === bold ? B([`#${n}`]) : Li([`#${n}`])),
      );
    render(asDom(root), Ul([pick(0)]));
    render(asDom(root), Ul([pick(2)]));
    expect(shape(root)).toBe("<main><ul><li>#1</li><li><b>#2</b></li><li>#3</li></ul></main>");
  });

  test("nested lists: cards move between lanes and reorder within one", () => {
    interface Lane {
      id: string;
      cards: number[];
    }
    const Board = html`<div>${child()}</div>`;
    const LaneV = html`<section>
      <h2>${text()}</h2>
      ${child()}
    </section>`;
    const Card = html`<button>${text<number>()}</button>`;
    const board = (lanes: Lane[]) =>
      Board([
        list(
          lanes,
          (l) => l.id,
          (l) =>
            LaneV([
              l.id,
              list(
                l.cards,
                (c) => c,
                (c) => Card([c]),
              ),
            ]),
        ),
      ]);
    const root = box();
    render(
      asDom(root),
      board([
        { id: "a", cards: [1, 2, 3] },
        { id: "b", cards: [4] },
      ]),
    );
    const div = first(root);
    const [laneA, laneB] = div.childNodes.filter((n) => n.nodeType === 1);
    const card2 = laneA!.childNodes[2];
    render(
      asDom(root),
      board([
        { id: "a", cards: [3, 2] },
        { id: "b", cards: [4, 1] },
      ]),
    );
    expect(shape(root)).toBe(
      "<main><div><section><h2>a</h2><button>3</button><button>2</button></section><section><h2>b</h2><button>4</button><button>1</button></section></div></main>",
    );
    const lanes = div.childNodes.filter((n) => n.nodeType === 1);
    expect(lanes).toEqual([laneA!, laneB!]);
    expect(laneA!.childNodes.includes(card2!)).toBe(true);
    render(
      asDom(root),
      board([
        { id: "b", cards: [1] },
        { id: "a", cards: [] },
      ]),
    );
    expect(shape(root)).toBe(
      "<main><div><section><h2>b</h2><button>1</button></section><section><h2>a</h2></section></div></main>",
    );
  });
});

describe("events and form state", () => {
  type Handler = (this: HTMLElement, e: Event) => void;
  const Field = html`<label
    >${text()}<input class=${cls()} .value=${prop<string>()} oninput=${prop<Handler | null>()}
  /></label>`;

  test("a new handler each render costs no DOM write and the latest one runs", () => {
    const root = box();
    const calls: string[] = [];
    const draw = (tag: string) => render(asDom(root), Field(["Name", "f", "v", () => calls.push(tag)]));
    draw("one");
    const input = first(root).lastChild as FakeElement;
    const trampoline = input["oninput"];
    let writes = 0;
    const real = trampoline;
    Object.defineProperty(input, "oninput", {
      get: () => real,
      set: () => writes++,
    });
    draw("two");
    draw("three");
    expect(writes).toBe(0);
    (input["oninput"] as (this: FakeElement, e: unknown) => void).call(input, {});
    expect(calls).toEqual(["three"]);
    render(asDom(root), Field(["Name", "f", "v", null]));
    (input["oninput"] as (this: FakeElement, e: unknown) => void).call(input, {});
    expect(calls).toEqual(["three"]);
  });

  test("a focused input keeps its node and its typed value across renders", () => {
    const root = box();
    render(asDom(root), Field(["Name", "f", "start", () => {}]));
    const input = first(root).lastChild as FakeElement;
    (input as unknown as { ownerDocument: unknown }).ownerDocument = { activeElement: input };
    input["value"] = "typed by the user";
    resetOps();
    render(asDom(root), Field(["Name", "f focus", "start", () => {}]));
    expect(first(root).lastChild).toBe(input);
    expect(input["value"]).toBe("typed by the user"); // unchanged hole value: not written
    expect(input.className).toBe("f focus");
    expect(ops.inserts + ops.moves + ops.removes + ops.clones).toBe(0);
  });
});

describe("types", () => {
  test("values are typed from the hole markers", () => {
    const neverRun = (): void => {
      const Row = html`<tr class=${cls()}>
        <td>${text<number>()}</td>
        ${child()}
      </tr>`;
      Row(["a", 1, null]);
      Row(["a", 1, Row(["b", 2, "x"])]);
      // @ts-expect-error missing value
      Row(["a", 1]);
      // @ts-expect-error extra value
      Row(["a", 1, null, 2]);
      // @ts-expect-error a number hole given a string
      Row(["a", "1", null]);
      // @ts-expect-error true is not a child
      Row(["a", 1, true]);
      const byIndex = (_n: number, i: number) => i;
      const view = (n: number) => Row(["a", n, null]);
      // @ts-expect-error a key from the index
      list([1], byIndex, view);
      // @ts-expect-error render must describe a view
      list([1], String, String);
    };
    expect(typeof neverRun).toBe("function");
  });
});
