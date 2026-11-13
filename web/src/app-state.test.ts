import { expect, test } from "bun:test";

import { batch, effect, memo } from "@reactre/core";
import { createRoot } from "@reactre/core/features/owner";

import { type App, createAppState, type View } from "./app-state.ts";
import type { Channel } from "./types.ts";

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

test("navigation and activity retain unrelated server projections without rerunning their readers", () => {
  createRoot((dispose) => {
    const start = initial();
    start.server = {
      herdr: { version: "test", protocol: 1 },
      snapshot: {
        version: "test",
        protocol: 1,
        focused_workspace_id: null,
        focused_tab_id: null,
        focused_pane_id: null,
        workspaces: [],
        tabs: [],
        panes: [],
        layouts: [],
        agents: [],
      },
      routes: [],
    };
    const state = createAppState(start);
    const first = state.app;
    const server = memo(() => state.state().server);
    let reads = 0;
    effect(() => {
      server();
      reads++;
    });
    for (let i = 0; i < 30; i++) {
      state.go({ tab: i % 2 ? "team" : "checklist" });
      state.addEntries([{ at: String(i), source: "web", kind: "event" }]);
      state.set({ theme: i % 2 ? "dark" : "light" });
    }
    expect(state.app.server).toBe(first.server);
    expect(reads).toBe(1);
    expect(first.entries).toHaveLength(0);
    expect(state.app.entries).toHaveLength(30);
    expect(Object.isFrozen(state.app)).toBe(true);
    dispose();
  });
});

test("a multi-domain update publishes one consistent snapshot", () => {
  createRoot((dispose) => {
    const state = createAppState(initial());
    const seen: App[] = [];
    effect(() => {
      seen.push(state.state());
    });
    state.set({ connected: true, fatal: "offline", theme: "dark" });
    expect(seen).toHaveLength(2);
    expect(seen[1]).toMatchObject({ connected: true, fatal: "offline", theme: "dark" });
    dispose();
  });
});

test("optional JSON fields and dynamic channel records are copied and frozen at ingress", () => {
  createRoot((dispose) => {
    const counts = { ready: 2 };
    const channel: Channel = {
      configured: true,
      approved: false,
      pending: true,
      peers: [],
      health: [],
      health_after: 0,
      flags: [],
      holds: [{ name: "route" }],
      boxes: [{ name: "inbox", orphan: false, counts, unacked: 2, claim: "none", suspicious: 0 }],
      messages: [],
      alerts: [],
    };
    const state = createAppState(initial());
    state.set({ channel });
    const before = state.app;
    counts.ready = 99;
    expect(before.channel?.boxes[0]?.counts["ready"]).toBe(2);
    expect(Object.hasOwn(before.channel!, "error")).toBe(false);
    expect(Object.hasOwn(before.channel!.holds[0]!, "owners")).toBe(false);
    expect(() => Object.assign(before.channel!.boxes[0]!.counts, { ready: 3 })).toThrow();
    state.set({ channel: { ...channel, error: "offline" } });
    expect(state.app.channel?.error).toBe("offline");
    expect(before.channel?.error).toBeUndefined();
    dispose();
  });
});

test("equal JSON payloads keep identity and do not notify application readers", () => {
  createRoot((dispose) => {
    const state = createAppState(initial());
    state.addEntries([{ at: "now", source: "web", kind: "status" }]);
    const before = state.app;
    let reads = 0;
    effect(() => {
      state.state();
      reads++;
    });
    state.set({ view: { ...before.view }, entries: before.entries.map((entry) => ({ ...entry })) });
    expect(state.app).toBe(before);
    expect(reads).toBe(1);
    state.set({ entries: [{ ...before.entries[0]!, text: "" }] });
    expect(state.app.entries).not.toBe(before.entries);
    expect(reads).toBe(2);
    dispose();
  });
});

test("invalid JSON updates fail atomically without invoking getters", () => {
  createRoot((dispose) => {
    const state = createAppState(initial());
    const before = state.app;
    let reads = 0;
    const invalid = {
      connected: true,
      get fatal() {
        reads++;
        return "invalid";
      },
    };
    expect(() => state.set(invalid)).toThrow("enumerable data property");
    expect(reads).toBe(0);
    expect(() => Reflect.apply(state.set, undefined, [{ connected: true, channel: undefined }])).toThrow();
    expect(state.app).toBe(before);
    dispose();
  });
});

test("application payloads are frozen again after JSON hydration and stop with their owner", () => {
  const restored: App = JSON.parse(JSON.stringify(initial()));
  expect(Object.isFrozen(restored.view)).toBe(false);
  const owned = createRoot((dispose) => ({ dispose, state: createAppState(restored) }));
  const before = owned.state.app;
  expect(Object.isFrozen(before.view)).toBe(true);
  restored.view.route = "outside";
  expect(before.view.route).toBe("route");
  owned.dispose();
  owned.state.set({ connected: true });
  expect(owned.state.app).toBe(before);
});
