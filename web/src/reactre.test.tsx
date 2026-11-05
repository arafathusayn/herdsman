import { expect, test } from "bun:test";

import { attach, signal } from "@reactre/core";

import { isServer, makeMemo } from "../test-fixtures/memo.ts";
import { mount } from "../test-fixtures/reactre.tsx";

test("direct-import TSX uses the Reactre compiler in bun test", () => {
  const root = document.createElement("main");
  const [count, setCount] = signal(0);
  const Counter = () => <button>{count()}</button>;
  const dispose = attach(Counter, root);
  expect(root.textContent).toBe("0");
  setCount(1);
  expect(root.textContent).toBe("1");
  dispose();
  expect(root.textContent).toBe("");
});

test("a component imported from a TSX module shares its runtime with the test", () => {
  const root = document.createElement("main");
  const app = mount(root);
  app.setCount(2);
  expect(root.textContent).toContain("2updated");
  app.dispose();
  expect(root.textContent).toBe("");
});

test("direct-import .ts memo options are compiled and the server entry is the browser's", async () => {
  expect(isServer).toBe(false);
  const state = makeMemo();
  expect(state.runs).toBe(0);
  const dispose = state.subscribe();
  expect(state.seen).toBe(2);
  state.setValue(2);
  expect(state.seen).toBe(4);
  dispose();
  await Promise.resolve();
  expect(state.releases).toBe(1);
  state.setValue(3);
  expect(state.runs).toBe(2);
});
