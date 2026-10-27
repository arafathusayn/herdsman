import { beforeAll, expect, mock, test } from "bun:test";

import { all, type FakeElement, find, installDom } from "../testdom.ts";
import type { LogLine, Route } from "../types.ts";

beforeAll(installDom);

const toasts: [string, string | undefined][] = [];
// A module mock lasts for the whole run, so it keeps ui.ts's other exports
// for the test files that load after this one.
const realUi = { ...(await import("../ui.ts")) };
void mock.module("../ui.ts", () => ({
  ...realUi,
  toast: (m: string, kind?: string) => void toasts.push([m, kind]),
}));

const flush = () => new Promise((r) => setTimeout(r, 0));
const text = (b: FakeElement) =>
  all(b)
    .map((e) => e.textContent ?? "")
    .join("\n");

// The route view pages the newest 2,000 records only; past them it says
// where the older history is.
test("the log pages down to the window, then names the route file", async () => {
  const { logView } = await import("./log-view.ts");
  const asked: number[] = [];
  const r = {
    id: "r1",
    run_state: [{ time: "10:00", text: "newest" }],
    older_run_state: 600,
    run_state_from: 500,
  } as unknown as Route;
  const page = (_id: string, before: number) => {
    asked.push(before);
    const lines: LogLine[] = Array.from({ length: before - 500 }, (_, i) => ({ time: "09:00", text: `l${500 + i}` }));
    return Promise.resolve({ lines, older: 500 });
  };
  const view = logView(r, page) as unknown as FakeElement;
  const more = find(view, (e) => e.tagName === "BUTTON" || e.tagName === "button")!;
  expect(more.textContent).toBe("Show 100 earlier lines");
  expect(text(view)).not.toContain("Older history is in 00-route.md");
  more.dispatch("click");
  await flush();
  expect(asked).toEqual([600]);
  expect(find(view, (e) => e.tagName === "BUTTON" || e.tagName === "button")).toBeUndefined();
  expect(text(view)).toContain("Older history is in 00-route.md");
});

test("a route inside the window has no note", async () => {
  const { logView } = await import("./log-view.ts");
  const r = { id: "r1", run_state: [{ time: "10:00", text: "only" }] } as unknown as Route;
  const view = logView(r, () => Promise.reject(new Error("no"))) as unknown as FakeElement;
  expect(text(view)).not.toContain("Older history");
});

test("a failed page keeps the button and reports the error", async () => {
  const { logView } = await import("./log-view.ts");
  const r = { id: "r1", run_state: [{ time: "10:00", text: "n" }], older_run_state: 5 } as unknown as Route;
  const errors: string[] = [];
  const view = logView(
    r,
    () => Promise.reject(new Error("gone")),
    (m) => void errors.push(m),
  ) as unknown as FakeElement;
  const more = find(view, (e) => e.tagName === "button")!;
  more.dispatch("click");
  await flush();
  expect(errors).toEqual(["Could not load earlier lines: gone"]);
  expect(more.disabled).toBe(false);
  // The app's own reader and toast are the defaults.
  const own = logView(r) as unknown as FakeElement;
  expect(find(own, (e) => e.tagName === "button")?.textContent).toBe("Show 5 earlier lines");
  const empty = logView({ id: "r2", run_state: [] } as unknown as Route) as unknown as FakeElement;
  expect(empty.textContent).toBe("No run-state lines yet.");
});

// The app's own failure report: a page that fails shows an error toast.
test("the default failure report is an error toast", async () => {
  const { logView } = await import("./log-view.ts");
  const r = { id: "r1", run_state: [{ time: "10:00", text: "n" }], older_run_state: 5 } as unknown as Route;
  const view = logView(r, () => Promise.reject(new Error("gone"))) as unknown as FakeElement;
  find(view, (e) => e.tagName === "button")!.dispatch("click");
  await flush();
  expect(toasts).toEqual([["Could not load earlier lines: gone", "error"]]);
});

// The app builds the view again on every update: the pages already loaded
// stay, joined to the new newest lines, and paging goes on from them.
test("loaded pages stay when a new line rebuilds the view", async () => {
  const { logView } = await import("./log-view.ts");
  const span = (a: number, b: number): LogLine[] =>
    Array.from({ length: b - a }, (_, i) => ({ time: "09:00", text: `l${a + i}` }));
  const page = (_id: string, before: number) =>
    Promise.resolve({ lines: span(before - 100, before), older: before - 100 });
  const items = (v: FakeElement) => all(v).filter((e) => e.tagName === "li").length;
  const button = (v: FakeElement) => find(v, (e) => e.tagName === "button");
  const first = logView({ id: "kept", run_state: span(300, 400), older_run_state: 300 } as unknown as Route, page);
  button(first as unknown as FakeElement)!.dispatch("click");
  await flush();
  const next = logView({
    id: "kept",
    run_state: span(301, 401),
    older_run_state: 301,
  } as unknown as Route) as unknown as FakeElement;
  expect(items(next)).toBe(201);
  expect(text(next)).toContain("l200");
  expect(button(next)?.textContent).toBe("Show 200 earlier lines");
  // A file that was replaced (fewer records) starts over.
  const reset = logView({
    id: "kept",
    run_state: span(0, 50),
    older_run_state: 0,
  } as unknown as Route) as unknown as FakeElement;
  expect(items(reset)).toBe(50);
  // Records that differ where they overlap, or a gap: the newest alone.
  const load = async (id: string) => {
    const v = logView({ id, run_state: span(300, 400), older_run_state: 300 } as unknown as Route, page);
    button(v as unknown as FakeElement)!.dispatch("click");
    await flush();
  };
  await load("edited");
  const edited = span(301, 401);
  edited[0] = { time: "09:00", text: "changed" };
  expect(
    items(
      logView({ id: "edited", run_state: edited, older_run_state: 301 } as unknown as Route) as unknown as FakeElement,
    ),
  ).toBe(100);
  await load("gap");
  expect(
    items(
      logView({
        id: "gap",
        run_state: span(500, 600),
        older_run_state: 500,
      } as unknown as Route) as unknown as FakeElement,
    ),
  ).toBe(100);
});

// The loaded pages are kept for the few routes viewed last only: a route
// not viewed since starts again from its newest lines.
test("loaded pages are kept for the last few routes only", async () => {
  const { logView, SEEN_ROUTES } = await import("./log-view.ts");
  const span = (a: number, b: number): LogLine[] =>
    Array.from({ length: b - a }, (_, i) => ({ time: "09:00", text: `l${a + i}` }));
  const page = (_id: string, before: number) =>
    Promise.resolve({ lines: span(before - 100, before), older: before - 100 });
  const items = (v: FakeElement) => all(v).filter((e) => e.tagName === "li").length;
  const visit = async (id: string) => {
    const v = logView({ id, run_state: span(300, 400), older_run_state: 300 } as unknown as Route, page);
    find(v as unknown as FakeElement, (e) => e.tagName === "button")!.dispatch("click");
    await flush();
  };
  for (let i = 0; i <= SEEN_ROUTES; i++) await visit(`lru${i}`);
  const again = (id: string) =>
    items(
      logView({ id, run_state: span(301, 401), older_run_state: 301 } as unknown as Route) as unknown as FakeElement,
    );
  expect(again(`lru${SEEN_ROUTES}`)).toBe(201);
  expect(again("lru0")).toBe(100);
});

// An edit of a loaded older record gives the route a new history: the view
// drops the pages it loaded, while an append under the same history keeps
// them.
test("a new history drops the loaded pages", async () => {
  const { logView } = await import("./log-view.ts");
  const span = (a: number, b: number): LogLine[] =>
    Array.from({ length: b - a }, (_, i) => ({
      time: "09:00",
      text: `l${a + i}`,
    }));
  const page = (_id: string, before: number) =>
    Promise.resolve({
      lines: span(before - 100, before),
      older: before - 100,
      history: "g1",
    });
  const view = (r: object) => logView({ id: "hist", ...r } as unknown as Route, page) as unknown as FakeElement;
  const first = view({
    run_state: span(300, 400),
    older_run_state: 300,
    history: "g1",
  });
  find(first, (e) => e.tagName === "button")!.dispatch("click");
  await flush();
  expect(text(first)).toContain("l250");
  expect(text(view({ run_state: span(301, 401), older_run_state: 301, history: "g1" }))).toContain("l250");
  const edited = view({
    run_state: span(301, 401),
    older_run_state: 301,
    history: "g2",
  });
  expect(text(edited)).not.toContain("l250");
  expect(all(edited).filter((e) => e.tagName === "li").length).toBe(100);
});

// A page read from a newer history than the view's is not joined to it.
test("a page from another history is refused", async () => {
  const { logView } = await import("./log-view.ts");
  const before = toasts.length;
  const lines = [{ time: "09:00", text: "from g2" }];
  const v = logView(
    {
      id: "hist2",
      run_state: [{ time: "10:00", text: "newest" }],
      older_run_state: 1,
      history: "g1",
    } as unknown as Route,
    () => Promise.resolve({ lines, older: 0, history: "g2" }),
  ) as unknown as FakeElement;
  find(v, (e) => e.tagName === "button")!.dispatch("click");
  await flush();
  expect(text(v)).not.toContain("from g2");
  expect(toasts.length).toBe(before + 1);
});
