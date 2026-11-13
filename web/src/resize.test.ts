import { expect, test } from "bun:test";

import { clampWidth, navResizer } from "./resize.tsx";
import { type FakeElement, installFakeDocument } from "./test-dom-core.ts";
import { mount } from "./test-mount.ts";

test("the panel width stays between its minimum and the room left", () => {
  expect(clampWidth(200, 900)).toBe(340);
  expect(clampWidth(700, 900)).toBe(700);
  expect(clampWidth(1200, 900)).toBe(900);
  // A window too small for both still leaves the panel its minimum.
  expect(clampWidth(500, 100)).toBe(340);
});

test("a resize handle has the markup the h() version had", () => {
  installFakeDocument();
  document.body = document.createElement("body");
  // The small DOM fixture does not implement DOMTokenList.
  Object.defineProperty(document.body, "classList", {
    value: {
      toggle(name: string, on: boolean) {
        const classes = new Set(document.body.className.split(/\s+/).filter(Boolean));
        if (on) classes.add(name);
        else classes.delete(name);
        document.body.className = [...classes].join(" ");
        return on;
      },
    },
  });
  const col = document.createElement("aside");
  const handle = mount(() => navResizer(col)) as FakeElement;
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
