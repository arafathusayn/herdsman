import { beforeEach, expect, test } from "bun:test";

import { child, cls, html, on, text } from "../herdsman-blocks-view.ts";
import { type FakeElement, installFakeDocument, ops, resetOps } from "../herdsman-blocks.testdom.ts";
import { dispatch } from "../testdom.ts";
import { ElementView } from "./element-view.ts";

beforeEach(installFakeDocument);

const fake = (n: unknown): FakeElement => n as FakeElement;

test("an element view is placed by its caller and patched where it stands", () => {
  const Button = html`<button class=${cls()} onclick=${on<"click">()}>
    ${child<string | null>()}<span>${text()}</span>
  </button>`;
  const clicks: string[] = [];
  const v = new ElementView("button", Button, ["copy", () => clicks.push("a"), null, "Copy"]);
  const pre = fake(document.createElement("pre"));
  pre.appendChild(fake(document.createElement("code")));
  pre.appendChild(fake(v.el));
  const span = fake(v.el).lastChild;

  v.update(["copy done", () => clicks.push("b"), "!", "Copied"]);
  expect(fake(pre.lastChild)).toBe(fake(v.el)); // still the same element, in its place
  expect(fake(v.el).className).toBe("copy done");
  expect(fake(v.el).lastChild).toBe(span);
  expect(v.el.textContent).toBe("!Copied");
  dispatch(fake(v.el), "click");
  expect(clicks).toEqual(["b"]);

  resetOps();
  v.update(["copy done", () => clicks.push("c"), "!", "Copied"]);
  expect(Object.values(ops).reduce((a, b) => a + b, 0)).toBe(0); // an unchanged update writes nothing
});

test("the root must have the tag asked for", () => {
  const Span = html`<span>${text()}</span>`;
  expect(() => new ElementView("button", Span, ["x"])).toThrow("not a <button>");
});
