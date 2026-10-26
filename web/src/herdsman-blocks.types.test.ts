// Type-level tests. Each @ts-expect-error line must fail to compile, so
// `bun check` fails if one of these mistakes ever starts type-checking. The
// lines live in functions that never run: they test the compiler, not the DOM.
import { beforeEach, describe, expect, test } from "bun:test";

import { asDom, type FakeElement, installFakeDocument, SVG_NS } from "./herdsman-blocks.testdom.ts";
import {
  attr,
  type Block,
  type BlockOf,
  cls,
  type EventName,
  type EventOf,
  type Handler,
  type HoleIndex,
  html,
  List,
  on,
  prop,
  type PropName,
  svg,
  type Template,
  text,
  type ValuesOf,
} from "./herdsman-blocks.ts";

beforeEach(installFakeDocument);

// Compile-time equality check: resolves to true only when A and B are identical.
type Equal<A, B> = (<X>() => X extends A ? 1 : 2) extends <X>() => X extends B ? 1 : 2 ? true : false;
const assertType = <T extends true>(): T => true as T;

type Click = ((this: HTMLElement, ev: PointerEvent) => void) | null;
type Vals = readonly [string, number, number, Click];

const row = () =>
  html`<tr class=${cls()} data-n=${attr<number>()}>
    <td>${text<number>()}</td>
    <td><a onclick=${on<"click">()}>x</a></td>
  </tr>`;

describe("typed holes", () => {
  test("the value tuple, indexes and block type are inferred from the markers", () => {
    const Row = row();
    assertType<Equal<typeof Row, Template<Vals>>>();
    assertType<Equal<HoleIndex<Vals>, 0 | 1 | 2 | 3>>();
    assertType<Equal<BlockOf<typeof Row>, Block<Vals>>>();
    assertType<Equal<ValuesOf<readonly [0, 1]>, readonly [unknown, unknown]>>();

    const b = Row.mount(["", 1, 2, () => {}]);
    b.set(2, 3);
    const n: number = b.get(2);
    const c: string = b.get(0);
    expect([n, c]).toEqual([3, ""]);
  });

  test("event and property holes are typed by element, event and property", () => {
    assertType<
      Equal<
        ValuesOf<readonly [ReturnType<typeof on<"input", HTMLInputElement>>]>,
        readonly [((this: HTMLInputElement, ev: InputEvent) => void) | null]
      >
    >();
    assertType<Equal<EventOf<HTMLElement, "keydown">, KeyboardEvent>>();
    assertType<
      Equal<Handler<SVGCircleElement, "pointerdown">, ((this: SVGCircleElement, ev: PointerEvent) => void) | null>
    >();
    assertType<Equal<ValuesOf<readonly [ReturnType<typeof prop<HTMLInputElement, "checked">>]>, readonly [boolean]>>();
    assertType<
      Equal<ValuesOf<readonly [ReturnType<typeof prop<HTMLSelectElement, "selectedIndex">>]>, readonly [number]>
    >();
    assertType<Equal<"click" extends EventName<HTMLButtonElement> ? true : false, true>>();
    assertType<Equal<"value" extends PropName<HTMLInputElement> ? true : false, true>>();
    // Read-only, method and handler names are not property names.
    assertType<Equal<"form" extends PropName<HTMLInputElement> ? true : false, false>>();
    assertType<Equal<"focus" extends PropName<HTMLInputElement> ? true : false, false>>();
    assertType<Equal<"oninput" extends PropName<HTMLInputElement> ? true : false, false>>();
    assertType<Equal<"className" extends PropName<SVGElement> ? true : false, false>>();

    const Field = html`<input
      .value=${prop<HTMLInputElement, "value">()}
      oninput=${on<"input", HTMLInputElement>()}
    />`;
    const seen: string[] = [];
    const b = Field.mount([
      "a",
      function (ev) {
        seen.push(this.value, ev.type);
      },
    ]);
    const input = b.root as unknown as FakeElement;
    expect(input["value"]).toBe("a");
    (input["oninput"] as (this: FakeElement, e: unknown) => void).call(input, { type: "input" });
    expect(seen).toEqual(["a", "input"]);
  });

  test("mistakes do not compile", () => {
    const neverRun = (): void => {
      const Row = row();
      const b = Row.mount(["", 1, 2, () => {}]);
      // @ts-expect-error a text hole typed number does not take a string
      b.set(2, "three");
      // @ts-expect-error there is no hole 4
      b.set(4, 1);
      // @ts-expect-error hole 0 is a class, a string
      const wrong: number = b.get(0);
      // @ts-expect-error one value short
      Row.mount(["", 1, 2]);
      // @ts-expect-error one value too many
      Row.mount(["", 1, 2, () => {}, 5]);
      // @ts-expect-error a handler hole takes a function
      Row.mount(["", 1, 2, "click"]);
      // @ts-expect-error class holes take strings, not numbers
      cls<number>();
      // @ts-expect-error text holes take printable values, not objects
      text<{ a: 1 }>();
      // @ts-expect-error attribute holes take printable values, not objects
      attr<{ a: 1 }>();
      const Aria = html`<button aria-pressed=${attr<"true" | "false">()}>x</button>`;
      Aria.mount(["true"]);
      // @ts-expect-error a narrowed attribute hole takes only its values
      Aria.mount(["yes"]);

      // Event holes.
      // @ts-expect-error a click handler receives a PointerEvent, not a KeyboardEvent
      Row.mount(["", 1, 2, (ev: KeyboardEvent) => void ev.key]);
      // @ts-expect-error the handler's this is the element type the marker names (HTMLElement here)
      Row.mount(["", 1, 2, function (this: HTMLInputElement) {}]);
      // @ts-expect-error no such event
      on<"nosuchevent">();
      // @ts-expect-error input has no "enterpictureinpicture" handler; video has
      on<"enterpictureinpicture", HTMLInputElement>();
      on<"enterpictureinpicture", HTMLVideoElement>();
      // A form's index signature does not hide its handler names.
      const Form = html`<form onsubmit=${on<"submit", HTMLFormElement>()}></form>`;
      Form.mount([
        function (ev) {
          void (this satisfies HTMLFormElement).action;
          void (ev satisfies SubmitEvent).submitter;
        },
      ]);
      // @ts-expect-error a submit handler receives a SubmitEvent, not a KeyboardEvent
      Form.mount([(ev: KeyboardEvent) => void ev.key]);
      // @ts-expect-error a misspelled event on a form is still rejected
      on<"sumbit", HTMLFormElement>();
      // Events with no on* property come from the element's event map, typed from it.
      const Ime = html`<input oncompositionstart=${on<"compositionstart", HTMLInputElement>()} />`;
      Ime.mount([(ev) => void (ev satisfies CompositionEvent).data]);
      // @ts-expect-error a compositionstart handler receives a CompositionEvent, not a KeyboardEvent
      Ime.mount([(ev: KeyboardEvent) => void ev.key]);
      const NoEvent = html`<a onclick=${on()}>x</a>`;
      // @ts-expect-error an event marker without its event takes nothing
      NoEvent.mount([() => {}]);
      const TwoEvents = html`<a onclick=${on<"click" | "keydown">()}>x</a>`;
      // @ts-expect-error a hole is one event: a union takes nothing
      TwoEvents.mount([() => {}]);

      // Property holes.
      const Field = html`<input
        .value=${prop<HTMLInputElement, "value">()}
        .checked=${prop<HTMLInputElement, "checked">()}
      />`;
      Field.mount(["v", true]);
      // @ts-expect-error value is a string
      Field.mount([1, true]);
      // @ts-expect-error checked is a boolean
      Field.mount(["v", "yes"]);
      // @ts-expect-error null is not a string: the property's own type, nothing wider
      Field.mount([null, true]);
      // @ts-expect-error form is read-only
      prop<HTMLInputElement, "form">();
      // @ts-expect-error focus is a method
      prop<HTMLInputElement, "focus">();
      // @ts-expect-error a handler property takes on(), not prop()
      prop<HTMLInputElement, "oninput">();
      // @ts-expect-error no such property
      prop<HTMLInputElement, "nosuch">();
      // @ts-expect-error both the element and the property are required
      prop<HTMLInputElement>();
      const NoProp = html`<input .value=${prop()} />`;
      // @ts-expect-error a property marker without its element and property takes nothing
      NoProp.mount(["v"]);
      const Custom = html`<x-list .items=${prop<HTMLElement & { items: readonly number[] }, "items">()}></x-list>`;
      Custom.mount([[1, 2]]);
      // @ts-expect-error a described custom property keeps its type
      Custom.mount([["1"]]);

      new List<{ id: number }, BlockOf<typeof Row>, number>(asDom(document.createElement("tbody") as never), {
        // @ts-expect-error a list key function must return the declared key type
        key: (r) => String(r.id),
        create: () => b,
        update: () => {},
      });
      // @ts-expect-error entries without a root property need a root function
      new List<{ id: number }, Element, number>(asDom(document.createElement("ul") as never), {
        key: (r) => r.id,
        create: () => Row.mountRoot(["", 1, 2, () => {}]),
        update: () => {},
      });
      // @ts-expect-error a block from block() does not know values, so it has no get
      Row.block(Row.mountRoot(["", 1, 2, () => {}])).get(0);
      Row.block(Row.mountRoot(["", 1, 2, () => {}])).set(1, 5);
      void wrong;
    };
    void neverRun;
  });

  test("untyped holes still work and accept anything", () => {
    const T = html`<p title=${0}>${1}</p>`;
    assertType<Equal<typeof T, Template<readonly [unknown, unknown]>>>();
    const b = T.mount([{ toString: () => "t" }, 42]);
    b.set(1, "x");
    expect((b.root as unknown as FakeElement).textContent).toBe("x");
  });
});

describe("marker positions", () => {
  test("a marker in the wrong position throws when the template compiles", () => {
    expect(() => html`<p class=${text()}></p>`).toThrow(/typed text, but its position makes it class/);
    expect(() => html`<p>${cls()}</p>`).toThrow(/typed class, but its position makes it text/);
    expect(() => html`<p title=${prop<HTMLElement, "title">()}></p>`).toThrow(
      /typed prop, but its position makes it attr/,
    );
    expect(() => html`<p onclick=${attr()}></p>`).toThrow(
      /typed attr, but its position makes it event: an on\* name takes on\(\)/,
    );
  });

  test("an on* name takes only the event marker, and the event marker only an on* name", () => {
    expect(() => html`<p onclick=${prop<HTMLElement, "title">()}></p>`).toThrow(
      /typed prop, but its position makes it event: an on\* name takes on\(\)/,
    );
    expect(() => html`<p .onclick=${prop<HTMLElement, "title">()}></p>`).toThrow(/makes it event/);
    expect(() => html`<p title=${on<"click">()}></p>`).toThrow(/typed event, but its position makes it attr/);
    expect(() => html`<p .title=${on<"click">()}></p>`).toThrow(/typed event, but its position makes it prop/);
    expect(() => html`<p>${on<"click">()}</p>`).toThrow(/typed event, but its position makes it text/);
    expect(() => html`<p onclick=${0}></p>`).not.toThrow(); // untyped holes are not checked
  });

  test("markers in the right positions compile", () => {
    expect(() => row()).not.toThrow();
    expect(
      () =>
        html`<input
          .value=${prop<HTMLInputElement, "value">()}
          disabled=${attr<boolean>()}
          oninput=${on<"input">()}
        />`,
    ).not.toThrow();
  });
});

describe("svg templates", () => {
  test("svg parses inside an <svg>, so a bare <circle> root is SVG", () => {
    const Dot = svg`<circle r=${attr<number>()} class=${attr()}></circle>`;
    const el = Dot.mountRoot([4, "dot"]) as unknown as FakeElement;
    expect(el.namespaceURI).toBe(SVG_NS);
    expect(el.tagName).toBe("circle");
    expect([el.getAttribute("r"), el.getAttribute("class")]).toEqual(["4", "dot"]);
    const html_ = html`<circle></circle>`.mountRoot([]) as unknown as FakeElement;
    expect(html_.namespaceURI).not.toBe(SVG_NS);
  });

  test("a class marker on an SVG element throws once, when the template compiles", () => {
    let compiled = 0;
    const make = () => {
      compiled++;
      return svg`<g class=${cls()}></g>`;
    };
    expect(make).toThrow(
      /typed class, but its position makes it attr: class on an SVG element is an attribute, use attr\(\)/,
    );
    expect(() => html`<div><svg class=${cls()}></svg></div>`).toThrow(/class on an SVG element is an attribute/);
    expect(() => html`<div class=${cls()}><svg class=${attr()}></svg></div>`).not.toThrow();
    expect(compiled).toBe(1);
  });
});
