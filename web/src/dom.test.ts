import { beforeEach, describe, expect, test } from "bun:test";

import { each, h, icon, icons, logoMark, mount, patch, unkeyed } from "./dom.ts";
import { child, cls, html, list, on, render, text, type ViewNode } from "./herdsman-blocks-view.ts";
import { asDom, FakeElement, type FakeNode, installFakeDocument, SVG_NS } from "./herdsman-blocks.testdom.ts";
import { dispatch, installDom } from "./testdom.ts";

const fake = (n: unknown): FakeElement => n as FakeElement;

beforeEach(installFakeDocument);

describe("patch (dom.ts)", () => {
  test("keeps the live element and runs the newest handler", () => {
    const root = document.createElement("div");
    const calls: string[] = [];
    patch(root, h("button", { class: "a", onclick: () => calls.push("first") }, "Go"));
    const button = root.firstChild as unknown as FakeElement;

    patch(root, h("button", { class: "b", onclick: () => calls.push("second") }, "Go!"));
    expect(root.firstChild).toBe(asDom(button));
    expect(button.className).toBe("b");
    expect(button.textContent).toBe("Go!");
    button.fire("click");
    expect(calls).toEqual(["second"]);

    patch(root, h("button", { onmouseover: () => calls.push("hover") }));
    button.fire("click");
    button.fire("mouseover");
    expect(calls).toEqual(["second", "hover"]);
  });

  test("an identical-looking element still gets the newest handler", () => {
    const root = document.createElement("ul");
    const calls: string[] = [];
    const view = (tag: string) =>
      unkeyed([1, 2, 3].map((i) => h("li", null, h("button", { onclick: () => calls.push(`${tag}${i}`) }, "Go"))));
    patch(root, view("old"));
    const buttons = (root as unknown as FakeElement).childNodes.map((li) => li.firstChild as FakeElement);
    patch(root, view("new")); // same structure and attributes, new closures
    (root as unknown as FakeElement).childNodes.forEach((li, i) => expect(li.firstChild).toBe(buttons[i]!));
    buttons[1]!.fire("click");
    expect(calls).toEqual(["new2"]);
  });

  test("keyed rows from h() keep their nodes across reorders", () => {
    const root = document.createElement("ul");
    const view = (ids: string[]) => unkeyed(ids.map((id) => h("li", { dataset: { key: id } }, id)));
    patch(root, view(["1", "2", "3"]));
    const [one, two, three] = (root as unknown as FakeElement).childNodes;
    patch(root, view(["3", "1", "2", "4"]));
    expect((root as unknown as FakeElement).childNodes.slice(0, 3)).toEqual([three!, one!, two!]);
    expect(root.textContent).toBe("3124");
  });
});

describe("each", () => {
  interface Row {
    id: number;
    label: string;
  }
  const view = (rows: Row[]) =>
    each(
      rows,
      (r) => r.id,
      (r) => h("li", null, r.label),
    );

  test("stamps keys and keeps each row's node through a reorder", () => {
    const root = document.createElement("ul");
    const rows = [1, 2, 3, 4].map((id) => ({ id, label: `row ${id}` }));
    patch(root, view(rows));
    const nodes = (root as unknown as FakeElement).childNodes;
    expect(nodes.map((n) => (n as FakeElement).getAttribute("data-key"))).toEqual(["1", "2", "3", "4"]);
    patch(root, view([rows[3]!, rows[0]!, rows[2]!, rows[1]!]));
    expect((root as unknown as FakeElement).childNodes).toEqual([nodes[3]!, nodes[0]!, nodes[2]!, nodes[1]!]);
    expect(root.textContent).toBe("row 4row 1row 3row 2");
  });

  test("a plain array is not a child; a literal tuple, each() and unkeyed() are", () => {
    const neverRun = (): void => {
      const items = h("li");
      const rows = [1, 2].map(() => h("li"));
      // @ts-expect-error a .map() result has no identity: wrap it in each() or unkeyed()
      h("ul", null, rows);
      h(
        "ul",
        null,
        [h("li"), items],
        unkeyed(rows),
        each([1], String, () => h("li")),
      );
    };
    void neverRun;
  });

  test("an index cannot be a key", () => {
    const neverRun = (): void => {
      const indexKey = (_item: number, i: number): number => i;
      // @ts-expect-error the key function gets only the item, never its position
      each([1, 2], indexKey, () => h("li"));
    };
    void neverRun;
  });

  test("duplicate keys warn only in development", () => {
    const g = globalThis as unknown as { HERDSMAN_DEV?: boolean };
    const warn = console.warn;
    const seen: string[] = [];
    console.warn = (m: string) => void seen.push(m);
    try {
      view([
        { id: 1, label: "a" },
        { id: 1, label: "b" },
      ]);
      expect(seen).toEqual([]);
      g.HERDSMAN_DEV = true;
      view([
        { id: 1, label: "a" },
        { id: 1, label: "b" },
      ]);
      expect(seen.length).toBe(1);
      expect(seen[0]).toContain('duplicate key "1"');
    } finally {
      delete g.HERDSMAN_DEV;
      console.warn = warn;
    }
  });
});

// The temporary bridge (stage 3 of plans/views-migration.md removes it, and these tests with it).
describe("views through h(), mount(), patch() and each()", () => {
  type Click = (this: HTMLElement, e: PointerEvent) => void;
  const Button = html`<button class=${cls()} onclick=${on<"click">()}>${text()}</button>`;
  const Item = html`<li>${text()}</li>`;
  const kids = (n: FakeNode): FakeElement[] => n.childNodes.filter((c): c is FakeElement => c.nodeType === 1);
  const press = (el: FakeElement): void => void dispatch(el, "click");
  const button = (label: string, fn: Click) => Button(["btn", fn, label]);

  test("a view child is rendered in place with its classes, text and handler", () => {
    const calls: string[] = [];
    const root = fake(
      h(
        "div",
        null,
        "a",
        button("Go", () => calls.push("go")),
        "b",
      ),
    );
    expect(root.textContent).toBe("aGob");
    const [b] = kids(root);
    expect(b!.tagName).toBe("BUTTON");
    expect(b!.className).toBe("btn");
    expect(b!.getAttribute("data-key")).toMatch(/^view:\d+$/);
    press(b!);
    expect(calls).toEqual(["go"]);
  });

  test("patch() replaces a materialized view instead of morphing it, so the newest handler runs", () => {
    const root = document.createElement("div");
    const calls: string[] = [];
    patch(
      root,
      h(
        "p",
        null,
        button("Go", () => calls.push("first")),
      ),
    );
    const p = fake(root).firstChild as FakeElement;
    const first = kids(p)[0]!;
    patch(
      root,
      h(
        "p",
        null,
        button("Go", () => calls.push("second")),
      ),
    );
    expect(fake(root).firstChild).toBe(p); // the h() parent is kept
    const second = kids(p)[0]!;
    expect(second).not.toBe(first);
    expect(kids(p).length).toBe(1);
    press(second);
    expect(calls).toEqual(["second"]);
  });

  test("each() rows that are views fold the item key into a fresh stamp", () => {
    const root = document.createElement("ul");
    const calls: string[] = [];
    const rows = (ids: number[], tag: string) =>
      each(
        ids,
        (id) => id,
        (id) => button(String(id), () => calls.push(`${tag}${id}`)),
      );
    patch(root, rows([1, 2], "old"));
    const before = kids(fake(root));
    expect(before.map((e) => e.getAttribute("data-key"))).toEqual([
      expect.stringMatching(/^1\|view:\d+$/),
      expect.stringMatching(/^2\|view:\d+$/),
    ]);
    patch(root, rows([2, 1, 3], "new"));
    const after = kids(fake(root));
    expect(after.map((e) => e.textContent)).toEqual(["2", "1", "3"]);
    for (const e of after) expect(before.includes(e)).toBe(false);
    press(after[0]!);
    expect(calls).toEqual(["new2"]);
  });

  test("handlers survive the move: an event bubbling from inside a moved node runs the handler once", () => {
    const Labelled = html`<button onclick=${on<"click">()}><b>${text()}</b></button>`;
    const calls: string[] = [];
    // A render() root that delegates events, holding an h() tree, holding a bridged view.
    const Shell = html`<main onclick=${on<"click">()}>${child<HTMLElement>()}</main>`;
    const root = document.createElement("div");
    const draw = (tag: string) =>
      render(root, Shell([() => calls.push("shell"), h("section", null, Labelled([() => calls.push(tag), "Go"]))]));
    draw("first");
    draw("second");
    const b = fake(root).firstChild!.firstChild!.firstChild!.firstChild as FakeElement;
    expect(b.tagName).toBe("B");
    dispatch(b, "click");
    expect(calls).toEqual(["second", "shell"]); // each once, innermost first
  });

  test("a view list child and mount() work too", () => {
    installDom(); // the app test DOM has replaceChildren
    const root = document.createElement("ul");
    mount(
      root,
      list(
        ["a", "b"],
        (s) => s,
        (s) => Item([s]),
      ),
    );
    expect(kids(fake(root)).map((e) => e.textContent)).toEqual(["a", "b"]);
    expect(fake(root).childNodes.length).toBe(2); // no slot anchor comes along
  });
});

describe("icons as views", () => {
  // The builders icon() and logoMark() replaced, kept to prove the views
  // render the same DOM: tags, namespace, attributes and children.
  const el = (tag: string, attrs: Record<string, string>, ...kids: FakeElement[]): FakeElement => {
    const e = new FakeElement(tag, SVG_NS);
    for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
    for (const k of kids) e.appendChild(k);
    return e;
  };
  const oldIcon = (paths: readonly string[], label?: string): FakeElement =>
    el(
      "svg",
      {
        viewBox: "0 0 24 24",
        class: "icon",
        fill: "none",
        stroke: "currentColor",
        "stroke-width": "1.8",
        "stroke-linecap": "round",
        "stroke-linejoin": "round",
        ...(label ? { "aria-label": label } : { "aria-hidden": "true" }),
      },
      ...paths.map((d) => el("path", { d })),
    );
  const oldLogo = (): FakeElement =>
    el(
      "svg",
      { viewBox: "0 0 32 32", class: "logo", "aria-hidden": "true" },
      el("rect", { width: "32", height: "32", rx: "8", fill: "#1d4d38" }),
      el(
        "g",
        {
          fill: "none",
          stroke: "#e9f6ef",
          "stroke-width": "2.6",
          "stroke-linecap": "round",
          "stroke-linejoin": "round",
        },
        el("path", { d: "M10 6.5V26" }),
        el("path", { d: "M10 17.5a5.5 5.5 0 0 1 11 0v5.5a3 3 0 0 0 3 3" }),
      ),
      el("circle", { cx: "15.5", cy: "24", r: "2", fill: "#5cc093" }),
    );
  // Elements and text, attributes sorted; a slot's empty anchor text is not content.
  const dump = (n: FakeNode): string => {
    if (n.nodeType === 3) return n.textContent;
    if (n.nodeType !== 1) return "";
    const e = n as FakeElement;
    const attrs = e.attributes
      .map((a) => `${a.name}="${a.value}"`)
      .sort()
      .join(" ");
    return `<${e.tagName} ${e.namespaceURI} ${attrs}>${e.childNodes.map(dump).join("")}</${e.tagName}>`;
  };
  const rendered = (v: ViewNode): FakeElement => {
    const box = document.createElement("div");
    render(box, v);
    return fake(box).firstChild as FakeElement;
  };

  test("every icon renders the same SVG as the old builder, labelled or not", () => {
    for (const paths of Object.values(icons)) {
      expect(new Set(paths).size).toBe(paths.length); // path data keys the list
      expect(dump(rendered(icon(paths)))).toBe(dump(oldIcon(paths)));
      expect(dump(rendered(icon(paths, "Label")))).toBe(dump(oldIcon(paths, "Label")));
    }
  });

  test("the logo renders the same SVG as the old builder", () => {
    expect(dump(rendered(logoMark()))).toBe(dump(oldLogo()));
  });

  test("an icon swapped in place reuses the svg and its kept paths", () => {
    const box = document.createElement("div");
    render(box, icon(icons.sidebar));
    const svgEl = fake(box).firstChild as FakeElement;
    const frame = svgEl.firstChild;
    render(box, icon(icons.sidebarOpen));
    expect(fake(box).firstChild).toBe(svgEl);
    expect(svgEl.firstChild).toBe(frame); // the shared frame path is kept
    expect(dump(svgEl)).toBe(dump(oldIcon(icons.sidebarOpen)));
  });
});
