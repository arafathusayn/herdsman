// Unit tests for herdsman-react on the fake DOM. Behavior against
// React itself is checked in headless Chrome by
// bench/herdsman-cases/react-compat.ts, which runs the same scenarios on
// React 19 and on this module and compares what they render and log.

import { beforeEach, describe, expect, test } from "bun:test";

import { FakeElement, FakeEvent, installFakeDocument, ops, resetOps } from "./herdsman-blocks.testdom.ts";
import {
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
