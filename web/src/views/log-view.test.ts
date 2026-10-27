import { beforeAll, expect, mock, test } from "bun:test";

import { render } from "../herdsman-blocks-view.ts";
import type { FakeElement } from "../herdsman-blocks.testdom.ts";
import { all, dispatch, dump, find, installDom, markup } from "../testdom.ts";
import type { LogLine, Route } from "../types.ts";
import type { PageReader } from "./log-view.ts";

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
const isTag = (tag: string) => (e: FakeElement) => e.tagName.toLowerCase() === tag;

/**
 * Renders route r's log into a box, as the app does: the view asks for a
 * redraw when a page arrives, and the box is rendered again from the same
 * route (the app would use its newest). Returns the box.
 */
async function logBox(r: Route, page?: PageReader, failed?: (msg: string) => void): Promise<FakeElement> {
  const { logView } = await import("./log-view.ts");
  const box = document.createElement("div");
  const draw = (): void => render(box, logView(r, draw, page, failed));
  draw();
  return box as unknown as FakeElement;
}

// The route view pages the newest 2,000 records only; past them it says
// where the older history is.
test("the log pages down to the window, then names the route file", async () => {
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
  const view = await logBox(r, page);
  const list = find(view, isTag("ol"))!;
  const more = find(view, isTag("button"))!;
  expect(more.textContent).toBe("Show 100 earlier lines");
  expect(text(view)).not.toContain("Older history is in 00-route.md");
  dispatch(more, "click");
  // The button waits for the page.
  expect(more.hasAttribute("disabled")).toBe(true);
  await flush();
  expect(asked).toEqual([600]);
  expect(find(view, isTag("button"))).toBeUndefined();
  expect(text(view)).toContain("Older history is in 00-route.md");
  // The page went into the list that was there: its node stays.
  expect(find(view, isTag("ol"))).toBe(list);
  expect(all(list).filter(isTag("li")).length).toBe(101);
});

test("a route inside the window has no note", async () => {
  const r = { id: "r1", run_state: [{ time: "10:00", text: "only" }] } as unknown as Route;
  const view = await logBox(r, () => Promise.reject(new Error("no")));
  expect(text(view)).not.toContain("Older history");
});

test("a failed page keeps the button and reports the error", async () => {
  const r = { id: "r1", run_state: [{ time: "10:00", text: "n" }], older_run_state: 5 } as unknown as Route;
  const errors: string[] = [];
  const view = await logBox(
    r,
    () => Promise.reject(new Error("gone")),
    (m) => void errors.push(m),
  );
  const more = find(view, isTag("button"))!;
  dispatch(more, "click");
  await flush();
  expect(errors).toEqual(["Could not load earlier lines: gone"]);
  // The same button, enabled again.
  expect(find(view, isTag("button"))).toBe(more);
  expect(more.hasAttribute("disabled")).toBe(false);
  // The app's own reader and toast are the defaults.
  const own = await logBox(r);
  expect(find(own, isTag("button"))?.textContent).toBe("Show 5 earlier lines");
  const empty = await logBox({ id: "r2", run_state: [] } as unknown as Route);
  expect(empty.textContent).toBe("No run-state lines yet.");
});

// The app's own failure report: a page that fails shows an error toast.
test("the default failure report is an error toast", async () => {
  const r = { id: "r1", run_state: [{ time: "10:00", text: "n" }], older_run_state: 5 } as unknown as Route;
  const view = await logBox(r, () => Promise.reject(new Error("gone")));
  dispatch(find(view, isTag("button"))!, "click");
  await flush();
  expect(toasts).toEqual([["Could not load earlier lines: gone", "error"]]);
});

// The app describes the view again on every update: the pages already loaded
// stay, joined to the new newest lines, and paging goes on from them.
test("loaded pages stay when a new line rebuilds the view", async () => {
  const span = (a: number, b: number): LogLine[] =>
    Array.from({ length: b - a }, (_, i) => ({ time: "09:00", text: `l${a + i}` }));
  const page = (_id: string, before: number) =>
    Promise.resolve({ lines: span(before - 100, before), older: before - 100 });
  const items = (v: FakeElement) => all(v).filter(isTag("li")).length;
  const button = (v: FakeElement) => find(v, isTag("button"));
  const first = await logBox({ id: "kept", run_state: span(300, 400), older_run_state: 300 } as unknown as Route, page);
  dispatch(button(first)!, "click");
  await flush();
  const next = await logBox({
    id: "kept",
    run_state: span(301, 401),
    older_run_state: 301,
  } as unknown as Route);
  expect(items(next)).toBe(201);
  expect(text(next)).toContain("l200");
  expect(button(next)?.textContent).toBe("Show 200 earlier lines");
  // A file that was replaced (fewer records) starts over.
  const reset = await logBox({
    id: "kept",
    run_state: span(0, 50),
    older_run_state: 0,
  } as unknown as Route);
  expect(items(reset)).toBe(50);
  // Records that differ where they overlap, or a gap: the newest alone.
  const load = async (id: string) => {
    const v = await logBox({ id, run_state: span(300, 400), older_run_state: 300 } as unknown as Route, page);
    dispatch(button(v)!, "click");
    await flush();
  };
  await load("edited");
  const edited = span(301, 401);
  edited[0] = { time: "09:00", text: "changed" };
  expect(items(await logBox({ id: "edited", run_state: edited, older_run_state: 301 } as unknown as Route))).toBe(100);
  await load("gap");
  expect(
    items(
      await logBox({
        id: "gap",
        run_state: span(500, 600),
        older_run_state: 500,
      } as unknown as Route),
    ),
  ).toBe(100);
});

// The loaded pages are kept for the few routes viewed last only: a route
// not viewed since starts again from its newest lines.
test("loaded pages are kept for the last few routes only", async () => {
  const { SEEN_ROUTES } = await import("./log-view.ts");
  const span = (a: number, b: number): LogLine[] =>
    Array.from({ length: b - a }, (_, i) => ({ time: "09:00", text: `l${a + i}` }));
  const page = (_id: string, before: number) =>
    Promise.resolve({ lines: span(before - 100, before), older: before - 100 });
  const items = (v: FakeElement) => all(v).filter(isTag("li")).length;
  const visit = async (id: string) => {
    const v = await logBox({ id, run_state: span(300, 400), older_run_state: 300 } as unknown as Route, page);
    dispatch(find(v, isTag("button"))!, "click");
    await flush();
  };
  for (let i = 0; i <= SEEN_ROUTES; i++) await visit(`lru${i}`);
  const again = async (id: string) =>
    items(await logBox({ id, run_state: span(301, 401), older_run_state: 301 } as unknown as Route));
  expect(await again(`lru${SEEN_ROUTES}`)).toBe(201);
  expect(await again("lru0")).toBe(100);
});

// An edit of a loaded older record gives the route a new history: the view
// drops the pages it loaded, while an append under the same history keeps
// them.
test("a new history drops the loaded pages", async () => {
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
  const view = (r: object) => logBox({ id: "hist", ...r } as unknown as Route, page);
  const first = await view({
    run_state: span(300, 400),
    older_run_state: 300,
    history: "g1",
  });
  dispatch(find(first, isTag("button"))!, "click");
  await flush();
  expect(text(first)).toContain("l250");
  expect(text(await view({ run_state: span(301, 401), older_run_state: 301, history: "g1" }))).toContain("l250");
  const edited = await view({
    run_state: span(301, 401),
    older_run_state: 301,
    history: "g2",
  });
  expect(text(edited)).not.toContain("l250");
  expect(all(edited).filter(isTag("li")).length).toBe(100);
});

// A page read from a newer history than the view's is not joined to it.
test("a page from another history is refused", async () => {
  const before = toasts.length;
  const lines = [{ time: "09:00", text: "from g2" }];
  const v = await logBox(
    {
      id: "hist2",
      run_state: [{ time: "10:00", text: "newest" }],
      older_run_state: 1,
      history: "g1",
    } as unknown as Route,
    () => Promise.resolve({ lines, older: 0, history: "g2" }),
  );
  dispatch(find(v, isTag("button"))!, "click");
  await flush();
  expect(text(v)).not.toContain("from g2");
  expect(toasts.length).toBe(before + 1);
});

// The markup the h() version drew: the lines newest first, then the button
// for earlier lines, or the note that the rest is in the route file.
test("the log's markup", async () => {
  const lines = [
    { time: "10:00", text: "started" },
    { time: "10:05", text: "web: prompted" },
  ];
  const paged = await logBox({ id: "m1", run_state: lines, older_run_state: 7 } as unknown as Route, () =>
    Promise.reject(new Error("no")),
  );
  expect(dump(paged.firstChild!)).toBe(
    markup`
      <div>
        <ol class="timeline">
          <li class="web"><time>10:05</time><p>web: prompted</p></li>
          <li><time>10:00</time><p>started</p></li>
        </ol>
        <button class="btn ghost" type="button">Show 7 earlier lines</button>
      </div>
    `,
  );
  const windowed = await logBox({
    id: "m2",
    run_state: lines,
    older_run_state: 7,
    run_state_from: 7,
  } as unknown as Route);
  expect(dump(windowed.firstChild!)).toBe(
    markup`
      <div>
        <ol class="timeline">
          <li class="web"><time>10:05</time><p>web: prompted</p></li>
          <li><time>10:00</time><p>started</p></li>
        </ol>
        <p class="muted pad">Older history is in 00-route.md</p>
      </div>
    `,
  );
  const whole = await logBox({ id: "m3", run_state: lines } as unknown as Route);
  expect(dump(whole.firstChild!)).toBe(
    markup`
      <ol class="timeline">
        <li class="web"><time>10:05</time><p>web: prompted</p></li>
        <li><time>10:00</time><p>started</p></li>
      </ol>
    `,
  );
});

// A route whose history starts at its first record: after the last page,
// nothing follows the lines, and the list keeps its node (it gets no wrapper
// of its own then).
test("the last page of a whole history keeps the list's node", async () => {
  const r = { id: "whole", run_state: [{ time: "10:00", text: "newest" }], older_run_state: 2 } as unknown as Route;
  const view = await logBox(r, () =>
    Promise.resolve({
      lines: [
        { time: "09:00", text: "a" },
        { time: "09:01", text: "b" },
      ],
      older: 0,
    }),
  );
  const list = find(view, isTag("ol"))!;
  dispatch(find(view, isTag("button"))!, "click");
  await flush();
  expect(find(view, isTag("ol"))).toBe(list);
  expect(find(view, isTag("button"))).toBeUndefined();
  expect(dump(view.firstChild!)).toBe(
    markup`
      <div>
        <ol class="timeline">
          <li><time>10:00</time><p>newest</p></li>
          <li><time>09:01</time><p>b</p></li>
          <li><time>09:00</time><p>a</p></li>
        </ol>
      </div>
    `,
  );
});
