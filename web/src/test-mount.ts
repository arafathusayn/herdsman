import { afterEach } from "bun:test";

import { attach } from "@reactre/core";
import type { Child } from "@reactre/core/jsx-runtime";

import type { FakeElement } from "./test-dom-core.ts";

const mounted: (() => void)[] = [];
afterEach(() => {
  for (const dispose of mounted.splice(0).reverse()) dispose();
});

/** Mount a real component, retaining its owner until the test ends. */
export function mount(view: () => Child, box: HTMLElement = document.createElement("div")): FakeElement {
  mounted.push(attach(view, box));
  const root = [...box.childNodes].find((node) => node.nodeType === 1);
  if (!root) throw new Error("The component did not render an element");
  return root as unknown as FakeElement;
}
