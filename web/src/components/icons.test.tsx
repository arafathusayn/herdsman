import { expect, test } from "bun:test";

import { attach, signal } from "@reactre/core";

import { icons } from "../icons.ts";
import { type FakeElement, ops, resetOps, SVG_NS } from "../test-dom-core.ts";
import { Icon } from "./icons.tsx";

test("compiled icons keep the SVG and shared paths when the signal changes", () => {
  const [paths, setPaths] = signal<readonly string[]>(icons.sidebar);
  const root = document.createElement("div");
  const dispose = attach(() => <Icon paths={paths()} />, root);
  const svg = root.firstChild as unknown as FakeElement;
  const frame = svg.childNodes.find((node) => node.nodeType === 1);
  expect(svg.namespaceURI).toBe(SVG_NS);
  setPaths(icons.sidebarOpen);
  expect(root.firstChild as unknown).toBe(svg);
  expect(svg.childNodes.find((node) => node.nodeType === 1)).toBe(frame);
  expect(
    svg.childNodes.filter((node) => node.nodeType === 1).map((node) => (node as FakeElement).getAttribute("d")),
  ).toEqual(icons.sidebarOpen);
  dispose();
  resetOps();
  setPaths(icons.sidebar);
  expect(Object.values(ops).every((count) => count === 0)).toBe(true);
  expect(root.childNodes).toHaveLength(0);
});
