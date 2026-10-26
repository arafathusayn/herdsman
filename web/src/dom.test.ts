import { beforeEach, describe, expect, test } from "bun:test";

import { icon, icons, logoMark } from "./dom.ts";
import { render, type ViewNode } from "./herdsman-blocks-view.ts";
import { FakeElement, type FakeNode, installFakeDocument, SVG_NS } from "./herdsman-blocks.testdom.ts";

const fake = (n: unknown): FakeElement => n as FakeElement;

beforeEach(installFakeDocument);

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
