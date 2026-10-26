// Type-level tests. Each @ts-expect-error line must fail to compile, so
// `bun check` fails if one of these mistakes ever starts type-checking. The
// lines live in functions that never run: they test the compiler, not the DOM.
import { beforeEach, describe, expect, test } from "bun:test";

import { asDom, type FakeElement, installFakeDocument } from "./herdsman-dom-diff.testdom.ts";
import {
  attr,
  type Block,
  type BlockOf,
  cls,
  type HoleIndex,
  html,
  List,
  prop,
  type Template,
  text,
  type ValuesOf,
} from "./herdsman-dom-diff.ts";

beforeEach(installFakeDocument);

// Compile-time equality check: resolves to true only when A and B are identical.
type Equal<A, B> = (<X>() => X extends A ? 1 : 2) extends <X>() => X extends B ? 1 : 2 ? true : false;
const assertType = <T extends true>(): T => true as T;

const row = () =>
  html`<tr class=${cls()} data-n=${attr<number>()}>
    <td>${text<number>()}</td>
    <td><a onclick=${prop<() => void>()}>x</a></td>
  </tr>`;

describe("typed holes", () => {
  test("the value tuple, indexes and block type are inferred from the markers", () => {
    const Row = row();
    assertType<Equal<typeof Row, Template<readonly [string, number, number, () => void]>>>();
    assertType<Equal<HoleIndex<readonly [string, number, number, () => void]>, 0 | 1 | 2 | 3>>();
    assertType<Equal<BlockOf<typeof Row>, Block<readonly [string, number, number, () => void]>>>();
    assertType<Equal<ValuesOf<readonly [0, 1]>, readonly [unknown, unknown]>>();

    const b = Row.mount(["", 1, 2, () => {}]);
    b.set(2, 3);
    const n: number = b.get(2);
    const c: string = b.get(0);
    expect([n, c]).toEqual([3, ""]);
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
      new List<{ id: number }, BlockOf<typeof Row>, number>(asDom(document.createElement("tbody") as never), {
        // @ts-expect-error a list key function must return the declared key type
        key: (r) => String(r.id),
        create: () => b,
        update: () => {},
      });
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
    expect(() => html`<p title=${prop()}></p>`).toThrow(/typed prop, but its position makes it attr/);
    expect(() => html`<p onclick=${attr()}></p>`).toThrow(/typed attr, but its position makes it prop/);
  });

  test("markers in the right positions compile", () => {
    expect(() => row()).not.toThrow();
    expect(() => html`<input .value=${prop<string>()} disabled=${attr<boolean>()} />`).not.toThrow();
  });
});
