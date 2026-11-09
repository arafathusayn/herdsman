import { expect, test } from "bun:test";

import { batch, effect } from "@reactre/core";
import { createRoot } from "@reactre/core/features/owner";

import { type App, createAppState, type View } from "./app-state.ts";

const initial = (): App => ({
  server: null,
  entries: [],
  connected: false,
  theme: "system",
  fatal: "",
  channel: null,
  view: {
    page: "",
    route: "route",
    tab: "docs",
    file: "readme",
    source: "",
    diffFile: "",
    artifact: "",
    pane: "",
    ws: "",
  },
});

test("application updates notify signal readers without mutating previous snapshots", () => {
  const start = initial();
  const state = createAppState(start);
  const seen: boolean[] = [];
  const dispose = createRoot((stop) => {
    effect(() => {
      seen.push(state.state().connected);
    });
    return stop;
  });
  state.set({ connected: true });
  expect(seen).toEqual([false, true]);
  expect(start.connected).toBe(false);
  expect(state.app).not.toBe(start);
  dispose();
  state.set({ connected: false });
  expect(seen).toEqual([false, true]);
});

test("batched updates publish one settled application snapshot", () => {
  const state = createAppState(initial());
  const seen: App[] = [];
  const stop = createRoot((dispose) => {
    effect(() => {
      seen.push(state.state());
    });
    return dispose;
  });
  batch(() => {
    state.set({ connected: true });
    state.set({ fatal: "offline" });
  });
  expect(seen).toHaveLength(2);
  expect(seen[1]).toMatchObject({ connected: true, fatal: "offline" });
  stop();
});

test("signal navigation preserves the route transition rules and URL history mode", () => {
  const written: [View, boolean][] = [];
  const start = initial();
  start.view.artifact = "page.html";
  start.view.ws = "workspace";
  const state = createAppState(start, (view, push) => written.push([view, push]));
  state.go({ route: "other" }, false);
  expect(state.app.view.file).toBe("readme");
  expect(state.app.view.artifact).toBe("");
  expect(state.app.view.ws).toBe("");
  expect(written).toEqual([[state.app.view, false]]);
  expect(state.app.view.route).toBe("other");
});

test("the activity signal retains the newest 400 events in order", () => {
  const state = createAppState(initial());
  state.addEntries(Array.from({ length: 450 }, (_, i) => ({ at: String(i), source: "web", kind: "event" })));
  expect(state.app.entries).toHaveLength(400);
  expect(state.app.entries[0]?.at).toBe("50");
  expect(state.app.entries.at(-1)?.at).toBe("449");
});
