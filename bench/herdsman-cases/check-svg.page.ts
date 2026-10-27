// In-browser checks for herdsman-blocks-view's SVG support and typed event and
// property holes, run by check-svg.ts in headless Chrome. The fake DOM the unit
// tests use only imitates namespaces; this runs the real parser and SVG DOM.

import {
  attr,
  child,
  cls,
  html,
  list,
  on,
  prop,
  render,
  type SvgChild,
  svg,
  text,
} from "../../web/src/herdsman-blocks-view.ts";

const SVG_NS = "http://www.w3.org/2000/svg";
const results: { name: string; ok: boolean; detail: string }[] = [];
const check = (name: string, ok: boolean, detail = ""): void => {
  results.push({ name, ok, detail });
};

const host = document.createElement("main");
document.body.appendChild(host);

// 1. A template whose root is <circle> is SVG only when made with svg``.
const Dot = svg`<circle cx=${attr<number>()} cy="5" r="2" class=${attr()} onclick=${on<"click", SVGCircleElement>()}></circle>`;
const NotSvg = html`<circle r="2"></circle>`;
{
  const el = Dot.template.mountRoot([1, "a", null]);
  check(
    "svg root is in the SVG namespace",
    el.namespaceURI === SVG_NS && el instanceof SVGCircleElement,
    el.namespaceURI ?? "",
  );
  const plain = NotSvg.template.mountRoot([]);
  check(
    "the same markup through html is not SVG",
    plain.namespaceURI !== SVG_NS,
    plain.constructor.name,
  );
}

// 2. SVG views in child holes inside SVG, as a keyed list; class is an attribute.
const Chart = html`<figure>
  <svg width="100" height="20" viewBox="0 0 100 20">${child<SvgChild>()}</svg
  >${text()}
</figure>`;
const Group = svg`<g class=${attr()}>${child<SvgChild>()}</g>`;
const clicks: string[] = [];
const draw = (xs: number[], tag: string): void =>
  render(
    host,
    Chart([
      Group([
        "dots",
        list(
          xs,
          (x) => x,
          (x) =>
            Dot([
              x * 10,
              x % 2 ? "odd" : "even",
              function (ev) {
                clicks.push(
                  `${tag}:${this.getAttribute("cx")}:${ev instanceof MouseEvent}:${this instanceof SVGCircleElement}`,
                );
              },
            ]),
        ),
      ]),
      `${xs.length} dots`,
    ]),
  );
draw([1, 2, 3], "first");
const g = host.querySelector("g")!;
const circles = [...host.querySelectorAll("circle")];
check(
  "list items are SVG elements with class attributes",
  g.namespaceURI === SVG_NS &&
    g.getAttribute("class") === "dots" &&
    circles.length === 3 &&
    circles.every((c) => c.namespaceURI === SVG_NS) &&
    circles.map((c) => c.getAttribute("class")).join() === "odd,even,odd",
  circles.map((c) => `${c.namespaceURI} ${c.getAttribute("class")}`).join("; "),
);
const box = circles[0]!.getBoundingClientRect();
check(
  "SVG content is laid out (non-zero box)",
  box.width > 0 && box.height > 0,
  `${box.width}x${box.height}`,
);

draw([3, 1, 2], "second");
const after = [...host.querySelectorAll("circle")];
check(
  "a reorder keeps the SVG nodes",
  after[0] === circles[2] && after[1] === circles[0] && after[2] === circles[1],
  after.map((c) => c.getAttribute("cx")).join(),
);
after[0]!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
check(
  "the latest handler runs with the circle as this",
  clicks.join() === "second:30:true:true",
  clicks.join(),
);

// 3. Why class on SVG must be an attribute: className is read-only there.
let threw = "";
try {
  (after[0] as unknown as { className: string }).className = "x";
} catch (e) {
  threw = String(e);
}
check(
  "assigning className on an SVG element throws in strict code",
  threw.includes("TypeError"),
  threw,
);
let compile = "";
try {
  svg`<path class=${cls()}></path>`;
} catch (e) {
  compile = String(e);
}
check(
  "a class marker on an SVG element fails when the template compiles",
  compile.includes("class on an SVG element is an attribute, use attr()"),
  compile,
);

// 4. Typed property and event holes on a real input.
const seen: string[] = [];
const Field = html`<input
  .value=${prop<HTMLInputElement, "value">()}
  oninput=${on<"input", HTMLInputElement>()}
/>`;
const field = document.createElement("div");
document.body.appendChild(field);
render(
  field,
  Field([
    "start",
    function (ev) {
      seen.push(`${this.value}:${ev.constructor.name}`);
    },
  ]),
);
const input = field.querySelector("input")!;
input.value = "typed";
input.dispatchEvent(new InputEvent("input", { bubbles: true }));
check(
  "property and event holes on an input",
  input.value === "typed" && seen.join() === "typed:InputEvent",
  seen.join(),
);

(window as unknown as { __results: typeof results }).__results = results;
