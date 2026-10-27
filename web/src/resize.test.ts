import { expect, test } from "bun:test";

import { type FakeElement, installFakeDocument } from "./herdsman-blocks.testdom.ts";
import { clampWidth, navResizer } from "./resize.ts";

test("the panel width stays between its minimum and the room left", () => {
  expect(clampWidth(200, 900)).toBe(340);
  expect(clampWidth(700, 900)).toBe(700);
  expect(clampWidth(1200, 900)).toBe(900);
  // A window too small for both still leaves the panel its minimum.
  expect(clampWidth(500, 100)).toBe(340);
});

test("a resize handle has the markup the h() version had", () => {
  installFakeDocument();
  const col = document.createElement("aside");
  const handle = navResizer(col) as unknown as FakeElement;
  // The h() version set tabIndex as a property, which the browser reflects as tabindex.
  const attrs = handle.attributes.map((a) => `${a.name}=${a.value}`).sort();
  expect(handle.tagName).toBe("DIV");
  expect(attrs).toEqual([
    "aria-label=Resize the sidebar",
    "aria-orientation=vertical",
    "class=col-resize right",
    "role=separator",
    "tabindex=0",
    "title=Drag to resize · double-click for the default width",
  ]);
});
