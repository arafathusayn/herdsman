import { beforeEach, describe, expect, test } from "bun:test";

import {
  asDom,
  type FakeElement,
  type FakeNode,
  type FakeText,
  installFakeDocument,
  ops,
  resetOps,
} from "./herdsman-dom-diff.testdom.ts";
import { type Entry, html, lis, List, morph } from "./herdsman-dom-diff.ts";

beforeEach(installFakeDocument);

interface Item {
  id: number;
}
interface Row extends Entry {
  item: Item;
}

const fake = (n: Node): FakeElement => n as unknown as FakeElement;
const keysOf = (parent: FakeNode): number[] => parent.childNodes.map((c) => (c as FakeElement)["key"] as number);

function makeList(parent: FakeElement, owns: boolean) {
  let creates = 0;
  const list = new List<Item, Row>(asDom(parent), {
    owns,
    key: (it) => it.id,
    create: (it) => {
      creates++;
      const el = document.createElement("li") as unknown as FakeElement;
      el["key"] = it.id;
      return { root: asDom(el), item: it };
    },
    update: (row, it) => {
      row.item = it;
    },
  });
  return { list, creates: () => creates };
}

// Reference LIS length, O(n^2), ignoring -1.
function lisLen(a: readonly number[]): number {
  const d = a.map(() => 1);
  let best = 0;
  for (let i = 0; i < a.length; i++) {
    if (a[i] === -1) {
      d[i] = 0;
      continue;
    }
    for (let j = 0; j < i; j++) if (a[j] !== -1 && a[j]! < a[i]!) d[i] = Math.max(d[i]!, d[j]! + 1);
    best = Math.max(best, d[i]!);
  }
  return best;
}

let seed = 42;
const rnd = (): number => (seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff;
function shuffle<T>(a: T[]): T[] {
  for (let i = a.length - 1; i > 0; i--) {
    const j = (rnd() * (i + 1)) | 0;
    [a[i], a[j]] = [a[j]!, a[i]!];
  }
  return a;
}

describe("lis", () => {
  test("returns a valid longest increasing subsequence", () => {
    for (let t = 0; t < 2000; t++) {
      const a = Array.from({ length: (rnd() * 40) | 0 }, () => (rnd() < 0.2 ? -1 : (rnd() * 30) | 0));
      const seq = lis(Int32Array.from(a));
      expect(seq.length).toBe(lisLen(a));
      for (let k = 1; k < seq.length; k++) {
        expect(seq[k]!).toBeGreaterThan(seq[k - 1]!);
        expect(a[seq[k]!]!).toBeGreaterThan(a[seq[k - 1]!]!);
      }
      for (const i of seq) expect(a[i]).not.toBe(-1);
    }
  });
});

describe("List", () => {
  test("random updates: correct order, node reuse, minimal moves", () => {
    let nextId = 0;
    for (let t = 0; t < 3000; t++) {
      const parent = document.createElement("ul") as unknown as FakeElement;
      const { list, creates } = makeList(parent, rnd() < 0.5);
      const oldItems = Array.from({ length: (rnd() * 60) | 0 }, () => ({ id: nextId++ }));
      list.set(oldItems);
      const before = new Map(list.entries.map((e) => [e.item.id, e]));

      let newItems = oldItems.filter(() => rnd() > 0.2);
      const mode = rnd();
      if (mode < 0.33) shuffle(newItems);
      else if (mode < 0.66 && newItems.length > 2) {
        const i = (rnd() * newItems.length) | 0;
        const j = (rnd() * newItems.length) | 0;
        [newItems[i], newItems[j]] = [newItems[j]!, newItems[i]!];
      }
      const adds = (rnd() * 10) | 0;
      for (let k = 0; k < adds; k++) newItems.splice((rnd() * (newItems.length + 1)) | 0, 0, { id: nextId++ });

      const createdBefore = creates();
      resetOps();
      list.set(newItems);

      expect(keysOf(parent)).toEqual(newItems.map((x) => x.id));
      const survivors = newItems.filter((x) => before.has(x.id));
      newItems.forEach((x, i) => {
        if (before.has(x.id)) expect(list.entries[i]!).toBe(before.get(x.id)!);
      });
      expect(creates() - createdBefore).toBe(newItems.length - survivors.length);
      if (survivors.length > 0) {
        const oldIndex = new Map(oldItems.map((x, i) => [x.id, i]));
        const order = survivors.map((x) => oldIndex.get(x.id)!);
        expect(ops.moves).toBe(survivors.length - lisLen(order));
      }
    }
  });

  test("every pairwise swap uses the minimum moves", () => {
    const parent = document.createElement("ul") as unknown as FakeElement;
    const { list } = makeList(parent, true);
    const base = Array.from({ length: 12 }, (_, i) => ({ id: i }));
    for (let i = 0; i < base.length; i++) {
      for (let j = i + 1; j < base.length; j++) {
        list.set(base);
        const next = base.slice();
        [next[i], next[j]] = [next[j]!, next[i]!];
        resetOps();
        list.set(next);
        expect(keysOf(parent)).toEqual(next.map((x) => x.id));
        expect(ops.moves).toBe(j - i === 1 ? 1 : 2);
      }
    }
  });

  test("edge cases: empty, reverse, swap, append, prepend, remove, clear", () => {
    const ids = (n: number, o = 0): Item[] => Array.from({ length: n }, (_, i) => ({ id: i + o }));
    const parent = document.createElement("ul") as unknown as FakeElement;
    const { list, creates } = makeList(parent, true);
    const check = (items: Item[]): void => expect(keysOf(parent)).toEqual(items.map((x) => x.id));

    list.set([]);
    check([]);
    const a = ids(1000);
    list.set(a);
    check(a);

    resetOps();
    const b = a.slice().reverse();
    list.set(b);
    check(b);
    expect(ops.moves).toBe(999);

    resetOps();
    const c = b.slice();
    [c[1], c[998]] = [c[998]!, c[1]!];
    let made = creates();
    list.set(c);
    check(c);
    expect(ops.moves).toBe(2);
    expect(creates()).toBe(made);

    resetOps();
    const adjacent = c.slice();
    [adjacent[10], adjacent[11]] = [adjacent[11]!, adjacent[10]!];
    list.set(adjacent);
    check(adjacent);
    expect(ops.moves).toBe(1);

    resetOps();
    made = creates();
    const d = adjacent.concat(ids(1000, 5000));
    list.set(d);
    check(d);
    expect(ops.moves).toBe(0);
    expect(creates() - made).toBe(1000);

    resetOps();
    const e = ids(3, 9000).concat(d);
    list.set(e);
    check(e);
    expect(ops.moves).toBe(0);

    resetOps();
    const f = e.filter((_, i) => i !== 500);
    list.set(f);
    check(f);
    expect(ops.removes).toBe(1);
    expect(ops.moves).toBe(0);

    list.set([]);
    check([]);
  });

  test("detach builds off-document and puts the parent back in place", () => {
    const host = document.createElement("table") as unknown as FakeElement;
    const before = document.createElement("thead") as unknown as FakeElement;
    const parent = document.createElement("tbody") as unknown as FakeElement;
    const after = document.createElement("tfoot") as unknown as FakeElement;
    host.appendChild(before);
    host.appendChild(parent);
    host.appendChild(after);
    let attachedDuringCreate = 0;
    const list = new List<Item, Row>(asDom(parent), {
      owns: true,
      detach: true,
      key: (it) => it.id,
      create: (it) => {
        if (parent.parentNode) attachedDuringCreate++;
        const el = document.createElement("tr") as unknown as FakeElement;
        el["key"] = it.id;
        return { root: asDom(el), item: it };
      },
      update: () => {},
    });
    const ids = (n: number, o: number): Item[] => Array.from({ length: n }, (_, i) => ({ id: i + o }));
    list.set(ids(50, 0)); // first render
    list.set(ids(50, 100)); // full replacement
    expect(attachedDuringCreate).toBe(0);
    expect(host.childNodes).toEqual([before, parent, after]);
    expect(keysOf(parent)).toEqual(ids(50, 100).map((x) => x.id));
    list.set(ids(51, 100)); // append to a live list: no detach
    expect(attachedDuringCreate).toBe(1);
  });

  test("respects an anchor and leaves other children alone", () => {
    const parent = document.createElement("div") as unknown as FakeElement;
    const head = document.createElement("h1") as unknown as FakeElement;
    const foot = document.createElement("footer") as unknown as FakeElement;
    head["key"] = -1;
    foot["key"] = -2;
    parent.appendChild(head);
    parent.appendChild(foot);
    const list = new List<Item, Row>(asDom(parent), {
      anchor: asDom(foot),
      owns: true, // ignored because an anchor is set
      key: (it) => it.id,
      create: (it) => {
        const el = document.createElement("p") as unknown as FakeElement;
        el["key"] = it.id;
        return { root: asDom(el), item: it };
      },
      update: () => {},
    });
    list.set([{ id: 1 }, { id: 2 }, { id: 3 }]);
    expect(keysOf(parent)).toEqual([-1, 1, 2, 3, -2]);
    list.set([{ id: 3 }, { id: 1 }]);
    expect(keysOf(parent)).toEqual([-1, 3, 1, -2]);
    list.set([]);
    expect(keysOf(parent)).toEqual([-1, -2]);
  });
});

describe("html templates", () => {
  test("mount fills text, class, attribute and property holes", () => {
    const T = html`<tr class="${0}">
      <td title=${1}>${2}</td>
      <td><a .href=${3} onclick=${4}>x ${5} y</a></td>
    </tr>`;
    const handler = (): void => {};
    const b = T.mount(["danger", "tip", 42, "/go", handler, "mid"]);
    const tr = fake(b.root);
    expect(tr.tagName).toBe("TR");
    expect(tr.className).toBe("danger");
    const td1 = tr.firstChild as FakeElement;
    expect(td1.getAttribute("title")).toBe("tip");
    expect((td1.firstChild as FakeText).data).toBe("42");
    const a = td1.nextSibling!.firstChild as FakeElement;
    expect(a["href"]).toBe("/go");
    expect(a["onclick"]).toBe(handler);
    expect(a.textContent).toBe("x mid y");
    expect(tr.attributes.some((x) => x.name.startsWith("bk"))).toBe(false);
  });

  test("set writes only on change and handles null, false and true", () => {
    const T = html`<input class=${0} disabled=${1} placeholder=${2} />`;
    const b = T.mount(["a", true, null]);
    const el = fake(b.root);
    expect(el.getAttribute("disabled")).toBe("");
    expect(el.getAttribute("placeholder")).toBeNull();
    b.set(1, false);
    expect(el.getAttribute("disabled")).toBeNull();
    el.className = "changed-outside";
    b.set(0, "a"); // same value as last set: no write
    expect(el.className).toBe("changed-outside");
    b.patch(["b", false, "hi"]);
    expect(el.className).toBe("b");
    expect(el.getAttribute("placeholder")).toBe("hi");
  });

  test("mount skips writes that match the clone's initial state", () => {
    const T = html`<tr class=${0} data-x=${1}>
      <td>${2}</td>
    </tr>`;
    resetOps();
    const b = T.mount(["", false, ""]);
    expect(ops.classWrites).toBe(0);
    expect(fake(b.root).getAttribute("data-x")).toBeNull();
    b.set(0, "danger");
    expect(ops.classWrites).toBe(1);
    b.set(0, "");
    expect(fake(b.root).className).toBe("");
    const withEmptyAttr = T.mount(["", "", ""]);
    expect(fake(withEmptyAttr.root).getAttribute("data-x")).toBe(""); // "" on an attribute still means present
  });

  test("clones are independent and the template compiles once per call site", () => {
    const make = (label: string) => html`<li>${0}</li>`.mount([label]);
    const one = make("one");
    const two = make("two");
    expect(one.t).toBe(two.t);
    one.set(0, "uno");
    expect(fake(one.root).textContent).toBe("uno");
    expect(fake(two.root).textContent).toBe("two");
  });

  test("holes are never parsed as HTML", () => {
    const b = html`<p>${0}</p>`.mount(["<b>bold</b>"]);
    const p = fake(b.root);
    expect(p.childNodes.length).toBe(1);
    expect(p.firstChild!.nodeType).toBe(3);
    expect(p.textContent).toBe("<b>bold</b>");
  });

  test("mount bakes values into the prototype without leaking between instances", () => {
    const T = html`<p class=${0} data-n=${1}>${2}</p>`;
    const a = T.mount(["x", "1", "first"]);
    const b = T.mount(["", null, "second"]);
    const c = T.mount(["x", "1", "first"]);
    expect([fake(a.root).className, fake(a.root).getAttribute("data-n"), fake(a.root).textContent]).toEqual([
      "x",
      "1",
      "first",
    ]);
    expect([fake(b.root).className, fake(b.root).getAttribute("data-n"), fake(b.root).textContent]).toEqual([
      "",
      null,
      "second",
    ]);
    expect(fake(c.root).textContent).toBe("first");
    b.set(2, "changed"); // first update locates b's own holes
    expect(fake(b.root).textContent).toBe("changed");
    expect(fake(a.root).textContent).toBe("first");
    expect(b.get(2)).toBe("changed");
  });

  test("property holes go on the clone, never the shared prototype", () => {
    const T = html`<button onclick=${0}>${1}</button>`;
    const fn = (): void => {};
    const withHandler = T.mount([fn, "a"]);
    const without = T.mount([null, "b"]);
    expect(fake(withHandler.root)["onclick"]).toBe(fn);
    expect(fake(without.root)["onclick"]).toBeUndefined();
  });

  test("ignores formatter indentation but keeps spaces inside lines and in <pre>", () => {
    const T = html`<ul>
      <li>a ${0} b</li>
      <li>
        <pre>
  keep
</pre>
      </li>
    </ul>`;
    const b = T.mount(["mid"]);
    const ul = fake(b.root);
    expect(ul.childNodes.map((c) => c.nodeName)).toEqual(["LI", "LI"]);
    expect(ul.childNodes[0]!.textContent).toBe("a mid b");
    expect(ul.childNodes[1]!.firstChild!.textContent).toBe("\n  keep\n");
  });

  test("rejects templates without exactly one root element", () => {
    expect(
      () =>
        html`<td>${0}</td>
          <td></td>`,
    ).toThrow(/one root element/);
    expect(() => html`text ${0}`).toThrow(/one root element/);
  });
});

describe("morph", () => {
  const el = (tag: string): FakeElement => document.createElement(tag) as unknown as FakeElement;
  const text = (t: string): FakeText => document.createTextNode(t) as unknown as FakeText;
  const kids = (...n: FakeNode[]): Node[] => n.map((x) => asDom(x));
  const item = (key: string, label: string): FakeElement => {
    const li = el("li");
    li.setAttribute("data-key", key);
    li.appendChild(text(label));
    return li;
  };

  test("keeps matching nodes and patches text and attributes in place", () => {
    const root = el("div");
    const p = el("p");
    p.setAttribute("title", "old");
    p.setAttribute("data-gone", "x");
    p.appendChild(text("hello"));
    root.appendChild(p);
    const liveText = p.firstChild;

    const freshP = el("p");
    freshP.setAttribute("title", "new");
    freshP.className = "on";
    freshP.appendChild(text("world"));
    morph(asDom(root), kids(freshP));

    expect(root.firstChild).toBe(p);
    expect(p.firstChild).toBe(liveText);
    expect(p.textContent).toBe("world");
    expect(p.getAttribute("title")).toBe("new");
    expect(p.className).toBe("on");
    expect(p.hasAttribute("data-gone")).toBe(false);
  });

  test("moves keyed children with the fewest moves and keeps their nodes", () => {
    const root = el("ul");
    const live = ["a", "b", "c", "d", "e"].map((k) => item(k, k));
    for (const li of live) root.appendChild(li);
    resetOps();
    morph(asDom(root), kids(item("e", "E"), item("b", "b"), item("c", "c"), item("x", "x"), item("a", "a")));
    expect(root.childNodes.map((c) => (c as FakeElement).getAttribute("data-key"))).toEqual(["e", "b", "c", "x", "a"]);
    expect(root.childNodes[0]).toBe(live[4]!);
    expect(root.childNodes[1]).toBe(live[1]!);
    expect(root.childNodes[4]).toBe(live[0]!);
    expect(root.textContent).toBe("Ebcxa");
    expect(ops.moves).toBe(2); // b, c stay; e and a move; x is new; d is removed
    expect(ops.removes).toBe(1);
  });

  test("unkeyed children match by tag and order; a changed tag is replaced", () => {
    const root = el("div");
    const h2 = el("h2");
    const span = el("span");
    root.appendChild(h2);
    root.appendChild(span);
    const freshH2 = el("h2");
    const freshEm = el("em");
    morph(asDom(root), kids(freshH2, freshEm));
    expect(root.childNodes[0]).toBe(h2);
    expect(root.childNodes[1]).toBe(freshEm);
  });

  test("a persistent node reused by the view is left alone", () => {
    const root = el("main");
    const shell = el("section");
    shell.appendChild(text("owned elsewhere"));
    root.appendChild(shell);
    morph(asDom(root), kids(shell, el("footer")));
    expect(root.childNodes[0]).toBe(shell);
    expect(shell.textContent).toBe("owned elsewhere");
    expect(root.childNodes.length).toBe(2);
  });

  test("data-morph-skip patches attributes but keeps children", () => {
    const root = el("div");
    const term = el("div");
    term.setAttribute("data-morph-skip", "");
    term.appendChild(el("canvas"));
    root.appendChild(term);
    const fresh = el("div");
    fresh.setAttribute("data-morph-skip", "");
    fresh.setAttribute("aria-label", "terminal");
    morph(asDom(root), kids(fresh));
    expect(root.firstChild).toBe(term);
    expect(term.getAttribute("aria-label")).toBe("terminal");
    expect(term.childNodes.length).toBe(1);
  });

  test("form values sync unless the field has focus", () => {
    const root = el("form");
    const input = el("input");
    input["value"] = "typed by user";
    root.appendChild(input);
    const fresh = el("input");
    fresh["value"] = "from store";
    Object.defineProperty(input, "ownerDocument", { value: { activeElement: input } });
    morph(asDom(root), kids(fresh));
    expect(input["value"]).toBe("typed by user");

    Object.defineProperty(input, "ownerDocument", { value: { activeElement: null } });
    const again = el("input");
    again["value"] = "from store";
    morph(asDom(root), kids(again));
    expect(input["value"]).toBe("from store");
  });
});
