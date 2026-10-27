import { afterEach, beforeEach, expect, test } from "bun:test";

import { render } from "../herdsman-blocks-view.ts";
import { FakeElement, type FakeNode, installFakeDocument, ops, resetOps } from "../herdsman-blocks.testdom.ts";
import { icon, icons } from "../icons.ts";
import { ago } from "../model.ts";
import { dispatch, markup } from "../testdom.ts";
import type { Route, RouteFile } from "../types.ts";

// The store reads the URL and listens for history changes when it loads.
const g = globalThis as unknown as Record<string, unknown>;
g["location"] ??= { search: "", pathname: "/" };
g["window"] ??= { addEventListener: () => {} };
const { store } = await import("../store.ts");
const { FilesView } = await import("./files.ts");

const settle = () => new Promise((r) => setTimeout(r, 0));
const fake = (n: unknown): FakeElement => n as FakeElement;
const proto = FakeElement.prototype as unknown as Record<string, unknown>;
const realFetch = globalThis.fetch;
const realGo = store.go;
let went: unknown[] = [];
let served: Record<string, string | null> = {};
let fetched: string[] = [];

beforeEach(() => {
  installFakeDocument();
  const doc = g["document"] as Record<string, unknown>;
  doc["documentElement"] = { dataset: {} }; // code highlighting on
  // The view scrolls the cursor row into view.
  proto["querySelector"] = () => null;
  proto["value"] = ""; // the search input's
  went = [];
  fetched = [];
  served = {};
  store.go = (patch) => void went.push(patch);
  globalThis.fetch = (async (url: string) => {
    const path = new URL(url, "http://x").searchParams.get("path") ?? "";
    fetched.push(path);
    const body = served[path];
    return body == null ? new Response("no", { status: 404 }) : new Response(body);
  }) as typeof fetch;
});
afterEach(async () => {
  await settle(); // loads still pending render before the fakes go
  delete proto["querySelector"];
  delete proto["value"];
  store.go = realGo;
  globalThis.fetch = realFetch;
});

// Elements and text with attributes sorted; a slot's empty anchor text is
// not content.
const dump = (n: FakeNode): string => {
  if (n.nodeType === 3) return n.textContent;
  if (n.nodeType !== 1) return "";
  const e = fake(n);
  const attrs = e.attributes
    .map((a) => `${a.name}="${a.value}"`)
    .sort()
    .join(" ");
  return `<${e.tagName.toLowerCase()} ${attrs}>${e.childNodes.map(dump).join("")}</${e.tagName.toLowerCase()}>`;
};
const elements = (n: FakeNode): FakeElement[] => n.childNodes.filter((c): c is FakeElement => c.nodeType === 1);
const iconMarkup = (paths: readonly string[], label?: string): string => {
  const box = document.createElement("div");
  render(box, icon(paths, label));
  return dump(box.firstChild as unknown as FakeNode);
};
const child = (n: unknown, ...path: number[]): FakeElement => {
  let e = fake(n);
  for (const i of path) e = elements(e)[i]!;
  return e;
};
const when = "2020-01-01T00:00:00Z";
const file = (path: string, size = 10): RouteFile => ({ path, size, modified: when });
const routeOf = (files: RouteFile[]): Route => ({
  id: "r1",
  dir: "/work/r1",
  name: "r1",
  title: "R1",
  orchestrator_pane: "p0",
  tasks: null,
  checklist: null,
  run_state: null,
  files,
  modified: when,
  live: true,
});

const files = [file("00-route.md"), file("reports/b.md"), file("reports/a.md"), file("probes/p.md"), file("doc/x.md")];

test("before the first update it is an empty shell", () => {
  const v = new FilesView("r1");
  expect(dump(fake(v.el))).toBe(markup`
    <div class="files">
      <div class="file-pane">
        <label class="file-search">
          ${iconMarkup(icons.search)}
          <input aria-label="Search files" autocomplete="off" placeholder="Search files…" spellcheck="false" type="search"></input>
          <span class="files-count"></span>
        </label>
        <div aria-label="Files" class="file-groups" role="listbox"></div>
      </div>
      <section aria-live="polite" class="viewer">
        <div class="viewer-head"></div>
        <div class="viewer-body"></div>
      </section>
    </div>
  `);
});

test("groups, the viewer and the loaded markdown render what the h() version did", async () => {
  served["00-route.md"] = "# Route\n\nSome *text*.";
  const v = new FilesView("r1");
  v.update(routeOf(files), "");
  // The groups (probes closed), the head of 00-route.md, and a body.
  const shell = (body: string): string => markup`
    <div class="files">
      <div class="file-pane">
        <label class="file-search">
          ${iconMarkup(icons.search)}
          <input aria-label="Search files" autocomplete="off" placeholder="Search files…" spellcheck="false" type="search"></input>
          <span class="files-count">5</span>
        </label>
        <div aria-label="Files" class="file-groups" role="listbox">
          <details class="file-group" open="">
            <summary >
              ${iconMarkup(icons.folder)}
              <span >Route files</span>
              <span class="count">1</span>
            </summary>
            <button aria-selected="true" class="file-row active" role="option" title="00-route.md">
              ${iconMarkup(icons.file)}
              <span class="file-name"><span class="name">00-route.md</span></span>
              <small >${ago(when)}</small>
            </button>
          </details>
          <details class="file-group" open="">
            <summary >
              ${iconMarkup(icons.folder)}
              <span >reports/</span>
              <span class="count">2</span>
            </summary>
            <button aria-selected="false" class="file-row" role="option" title="reports/a.md">
              ${iconMarkup(icons.file)}
              <span class="file-name"><span class="name">a.md</span></span>
              <small >${ago(when)}</small>
            </button>
            <button aria-selected="false" class="file-row" role="option" title="reports/b.md">
              ${iconMarkup(icons.file)}
              <span class="file-name"><span class="name">b.md</span></span>
              <small >${ago(when)}</small>
            </button>
          </details>
          <details class="file-group" open="">
            <summary >
              ${iconMarkup(icons.folder)}
              <span >doc/</span>
              <span class="count">1</span>
            </summary>
            <button aria-selected="false" class="file-row" role="option" title="doc/x.md">
              ${iconMarkup(icons.file)}
              <span class="file-name"><span class="name">x.md</span></span>
              <small >${ago(when)}</small>
            </button>
          </details>
          <details class="file-group">
            <summary >
              ${iconMarkup(icons.folder)}
              <span >probes/</span>
              <span class="count">1</span>
            </summary>
            <button aria-selected="false" class="file-row" role="option" title="probes/p.md">
              ${iconMarkup(icons.file)}
              <span class="file-name"><span class="name">p.md</span></span>
              <small >${ago(when)}</small>
            </button>
          </details>
        </div>
      </div>
      <section aria-live="polite" class="viewer">
        <div class="viewer-head">
          <div class="viewer-title">
            <h2 title="00-route.md">00-route.md</h2>
            <p >10 B · ${ago(when)}</p>
          </div>
          <div aria-label="View" class="seg" role="radiogroup">
            <button aria-checked="true" class="active" role="radio" title="Show the file formatted">Rendered</button>
            <button aria-checked="false" role="radio" title="Show the Markdown source">Raw</button>
          </div>
          <button aria-label="Copy the file path" class="icon-btn" title="Copy the file path">
            ${iconMarkup(icons.copy)}
          </button>
        </div>
        <div class="viewer-body">${body}</div>
      </section>
    </div>
  `;
  expect(dump(fake(v.el))).toBe(shell('<p class="muted">Loading…</p>'));
  await settle();
  expect(fetched).toEqual(["00-route.md"]);
  expect(dump(fake(v.el))).toBe(shell('<div class="md"><h2 >Route</h2><p >Some *text*.</p></div>'));
});

test("an unchanged update writes nothing and keeps every node", async () => {
  served["00-route.md"] = "hello";
  const v = new FilesView("r1");
  const route = routeOf(files);
  v.update(route, "");
  await settle();
  const rows = elements(child(v.el, 0, 1));
  resetOps();
  v.update(route, "");
  expect(ops.inserts + ops.removes + ops.moves + ops.clones + ops.classWrites).toBe(0);
  expect(elements(child(v.el, 0, 1))).toEqual(rows);
  expect(fetched).toEqual(["00-route.md"]);
});

test("search: marked matches, a cursor, Enter opens the cursor's file and Escape clears", () => {
  const v = new FilesView("r1");
  v.update(routeOf(files), "doc/x.md");
  const search = child(v.el, 0, 0, 1);
  search["value"] = "reports";
  dispatch(search, "input");
  const list = child(v.el, 0, 1);
  const rows = elements(list);
  expect(rows.length).toBe(2);
  expect(child(v.el, 0, 0, 2).textContent).toBe("2/5");
  // Equal scores keep the route's order.
  expect(dump(rows[0]!)).toBe(markup`
    <button aria-selected="false" class="file-row cursor" role="option" title="reports/b.md">
      ${iconMarkup(icons.file)}
      <span class="file-name">
        <span class="name">b.md</span>
        <span class="dir"><mark >reports</mark>/</span>
      </span>
      <small >${ago(when)}</small>
    </button>
  `);
  expect(dump(rows[1]!)).toBe(markup`
    <button aria-selected="false" class="file-row" role="option" title="reports/a.md">
      ${iconMarkup(icons.file)}
      <span class="file-name">
        <span class="name">a.md</span>
        <span class="dir"><mark >reports</mark>/</span>
      </span>
      <small >${ago(when)}</small>
    </button>
  `);

  dispatch(search, "keydown", { key: "ArrowDown" });
  expect(elements(list)).toEqual(rows); // the cursor moved; the rows stayed
  expect(rows[1]!.className).toBe("file-row cursor");
  expect(rows[0]!.className).toBe("file-row");
  dispatch(search, "keydown", { key: "Enter" });
  expect(went).toEqual([{ file: "reports/a.md" }]);

  search["value"] = "zzz";
  dispatch(search, "input");
  expect(dump(child(v.el, 0, 1))).toBe(
    markup`
      <div aria-label="Files" class="file-groups" role="listbox">
        <p class="file-empty">No file matches “zzz”.</p>
      </div>
    `,
  );
  dispatch(search, "keydown", { key: "Escape" });
  expect(search["value"]).toBe("");
  expect(elements(child(v.el, 0, 1)).length).toBe(4); // the groups again
});

test("a row click opens its file; a closed group stays closed across updates", () => {
  const v = new FilesView("r1");
  const route = routeOf(files);
  v.update(route, "");
  const reports = child(v.el, 0, 1, 1);
  dispatch(child(reports, 2), "click");
  expect(went).toEqual([{ file: "reports/b.md" }]);
  expect(reports.hasAttribute("open")).toBe(true);
  reports.removeAttribute("open"); // the user closes it
  reports["open"] = false;
  dispatch(reports, "toggle");
  v.update(route, "reports/a.md");
  expect(child(v.el, 0, 1, 1)).toBe(reports);
  expect(reports.hasAttribute("open")).toBe(false);
  // probes opens when it holds the open file, as before
  v.update(route, "probes/p.md");
  expect(child(v.el, 0, 1, 3).hasAttribute("open")).toBe(true);
});

test("Raw shows the source; a failed load offers Try again", async () => {
  (g["document"] as { documentElement: { dataset: Record<string, string> } }).documentElement.dataset["code"] = "off";
  served["00-route.md"] = "# Title";
  const v = new FilesView("r1");
  v.update(routeOf(files), "");
  await settle();
  dispatch(child(v.el, 1, 0, 1, 1), "click"); // Raw
  const body = child(v.el, 1, 1);
  expect(dump(body)).toBe('<div class="viewer-body"><pre class="raw"># Title</pre></div>');
  expect(child(v.el, 1, 0, 1, 1).getAttribute("aria-checked")).toBe("true");
  expect(fetched).toEqual(["00-route.md"]); // the switch reuses the text

  v.update(routeOf(files), "doc/x.md");
  await settle();
  expect(dump(body)).toBe(
    markup`
      <div class="viewer-body">
        <p class="error">cannot read doc/x.md (404)</p>
        <button class="btn" type="button">Try again</button>
      </div>
    `,
  );
  served["doc/x.md"] = "x";
  dispatch(child(body, 1), "click");
  await settle();
  expect(dump(body)).toBe('<div class="viewer-body"><pre class="raw">x</pre></div>');
});
