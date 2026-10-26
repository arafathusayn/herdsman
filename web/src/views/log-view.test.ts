import { beforeAll, expect, mock, test } from "bun:test";

import { all, type FakeElement, find, installDom } from "../testdom.ts";
import type { LogLine, Route } from "../types.ts";

beforeAll(installDom);

const toasts: [string, string | undefined][] = [];
void mock.module("../ui.ts", () => ({ toast: (m: string, kind?: string) => void toasts.push([m, kind]) }));

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
