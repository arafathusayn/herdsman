import { beforeEach, expect, test } from "bun:test";

import { signal } from "@reactre/core";

import { contrast, type Palette } from "../ansi.ts";
import { ops, resetOps } from "../test-dom-core.ts";
import { mount } from "../test-mount.ts";
import { all, installDom } from "../testdom.ts";
import { AnsiText } from "./ansi.tsx";

beforeEach(installDom);
const palette: Palette = { fg: "#111111", bg: "#ffffff", ansi: Array(16).fill("#eeeeee") as string[] };

test("ANSI output stays text and its styled runs retain readable colors", () => {
  const pre = mount(() => (
    <pre>
      <AnsiText text={"<img src=x>\u001b[1;31;44mred\u001b[0m plain"} palette={palette} />
    </pre>
  ));
  expect(pre.textContent).toBe("<img src=x>red plain");
  expect(all(pre).some((element) => element.tagName === "IMG")).toBe(false);
  const span = all(pre).find((element) => element.className === "a-b")!;
  expect(span.className).toBe("a-b");
  expect(span.style.getPropertyValue("color")).not.toBe("");
  expect(span.style.getPropertyValue("background-color")).not.toBe("");
  expect(
    contrast(span.style.getPropertyValue("color"), span.style.getPropertyValue("background-color")),
  ).toBeGreaterThanOrEqual(4.5);
});

test("ANSI runs change kind, shrink, and recolor without retaining obsolete text or styles", () => {
  const [text, setText] = signal("plain\u001b[1;31mred\u001b[0m end");
  const [colors, setColors] = signal(palette);
  const pre = mount(() => (
    <pre>
      <AnsiText text={text()} palette={colors()} />
    </pre>
  ));
  setText("\u001b[1mfirst\u001b[0m last");
  expect(pre.textContent).toBe("first last");
  const span = all(pre).find((element) => element.className === "a-b")!;
  setColors({ ...palette, fg: "#ffffff", bg: "#000000" });
  expect(all(pre).find((element) => element.className === "a-b")).toBe(span);
  expect(span.style.getPropertyValue("color")).toBe("#ffffff");
  setText("");
  expect(pre.textContent).toBe("");
  expect(all(pre).filter((element) => element.className === "a-b")).toHaveLength(0);
});

test("an unrelated snapshot keeps terminal runs untouched; new text updates the same span", () => {
  const [state, setState] = signal({ text: "\u001b[1mfirst", palette });
  const pre = mount(() => (
    <pre>
      <AnsiText text={state().text} palette={state().palette} />
    </pre>
  ));
  const span = all(pre).find((element) => element.className === "a-b")!;
  resetOps();
  setState({ ...state() });
  expect(Object.values(ops).every((count) => count === 0)).toBe(true);
  setState({ ...state(), text: "\u001b[1mnext" });
  expect(all(pre).find((element) => element.className === "a-b")).toBe(span);
  expect(pre.textContent).toBe("next");
});
