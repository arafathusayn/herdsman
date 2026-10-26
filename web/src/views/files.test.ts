import { afterEach, beforeEach, expect, test } from "bun:test";

import { fuzzy, highlight } from "../fuzzy.ts";
import type { Child } from "../herdsman-blocks-view.ts";
import { FakeElement, type FakeNode, installFakeDocument, ops, resetOps } from "../herdsman-blocks.testdom.ts";
import { icon, icons } from "../icons.ts";
import { renderMarkdown } from "../markdown.ts";
import { ago } from "../model.ts";
import { dispatch } from "../testdom.ts";
import { each, h } from "../testref.ts";
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
  // markdown.ts appends; the view scrolls the cursor row into view.
  proto["append"] = function (this: FakeElement, ...ns: FakeNode[]) {
    for (const n of ns) this.appendChild(n);
  };
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
  delete proto["append"];
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

// The h() markup FilesView made before it was a view. The browser reflects
// the properties h() set (type, spellcheck, open) as attributes, as the fake
// records them here.
function oldRow(f: RouteFile, selected: string, hits: number[], cursor: boolean, showFolder: boolean): HTMLElement {
  const slash = f.path.lastIndexOf("/");
  const name = f.path.slice(slash + 1);
  const dir = slash < 0 ? "" : f.path.slice(0, slash + 1);
  const nameHits = hits.filter((i) => i > slash).map((i) => i - slash - 1);
  const dirHits = hits.filter((i) => i <= slash);
  const cls = ["file-row", f.path === selected ? "active" : "", cursor ? "cursor" : ""].filter(Boolean).join(" ");
  return h(
    "button",
    { class: cls, role: "option", "aria-selected": String(f.path === selected), title: f.path },
    icon(icons.file),
    h(
      "span",
      { class: "file-name" },
      h("span", { class: "name" }, highlight(name, nameHits)),
      showFolder && dir ? h("span", { class: "dir" }, highlight(dir, dirHits)) : null,
    ),
    h("small", null, ago(f.modified)),
  );
}
function oldGroup(d: string, items: RouteFile[], selected: string, open: boolean): HTMLElement {
  return h(
    "details",
    { class: "file-group", open },
    h(
      "summary",
      null,
      icon(icons.folder),
      h("span", null, d ? `${d}/` : "Route files"),
      h("span", { class: "count" }, String(items.length)),
    ),
    each(
      items,
      (f) => f.path,
      (f) => oldRow(f, selected, [], false, false),
    ),
  );
}
function oldShell(count: string, list: Child[], head: Child[], body: Child[]): HTMLElement {
  return h(
    "div",
    { class: "files" },
    h(
      "div",
      { class: "file-pane" },
      h(
        "label",
        { class: "file-search" },
        icon(icons.search),
        h("input", {
          type: "search",
          placeholder: "Search files…",
          "aria-label": "Search files",
          autocomplete: "off",
          spellcheck: "false",
        }),
        h("span", { class: "files-count" }, count),
      ),
      h("div", { class: "file-groups", role: "listbox", "aria-label": "Files" }, ...list),
    ),
    h(
      "section",
      { class: "viewer", "aria-live": "polite" },
      h("div", { class: "viewer-head" }, ...head),
      h("div", { class: "viewer-body" }, ...body),
    ),
  );
}
function oldHead(selected: string, f: RouteFile | undefined, raw: boolean): Node[] {
  const slash = selected.lastIndexOf("/");
  return [
    h(
      "div",
      { class: "viewer-title" },
      h(
        "h2",
        { title: selected },
        slash >= 0 ? h("span", { class: "dir" }, selected.slice(0, slash + 1)) : null,
        selected.slice(slash + 1),
      ),
      f ? h("p", null, `${f.size} B · ${ago(f.modified)}`) : null,
    ),
    h(
      "div",
      { class: "seg", role: "radiogroup", "aria-label": "View" },
      ...(["Rendered", "Raw"] as const).map((label) =>
        h(
          "button",
          {
            role: "radio",
            title: label === "Raw" ? "Show the Markdown source" : "Show the file formatted",
            "aria-checked": String(raw === (label === "Raw")),
            // h() set an empty class on the inactive button; a class hole leaves none.
            class: raw === (label === "Raw") ? "active" : undefined,
          },
          label,
        ),
      ),
    ),
    h(
      "button",
      { class: "icon-btn", title: "Copy the file path", "aria-label": "Copy the file path" },
      icon(icons.copy),
    ),
  ];
}

const files = [file("00-route.md"), file("reports/b.md"), file("reports/a.md"), file("probes/p.md"), file("doc/x.md")];

test("before the first update it is an empty shell", () => {
  const v = new FilesView("r1");
  expect(dump(fake(v.el))).toBe(dump(fake(oldShell("", [], [], []))));
});

test("groups, the viewer and the loaded markdown render what the h() version did", async () => {
  served["00-route.md"] = "# Route\n\nSome *text*.";
  const v = new FilesView("r1");
  v.update(routeOf(files), "");
  const loading = [h("p", { class: "muted" }, "Loading…")];
  const groups = (open: boolean) => [
    oldGroup("", [files[0]!], "00-route.md", true),
    oldGroup("reports", [files[2]!, files[1]!], "00-route.md", true),
    oldGroup("doc", [files[4]!], "00-route.md", true),
    oldGroup("probes", [files[3]!], "00-route.md", open),
  ];
  const head = () => oldHead("00-route.md", files[0], false);
  expect(dump(fake(v.el))).toBe(dump(fake(oldShell("5", groups(false), head(), loading))));
  await settle();
  expect(fetched).toEqual(["00-route.md"]);
  const md = renderMarkdown(served["00-route.md"]!);
  expect(dump(fake(v.el))).toBe(dump(fake(oldShell("5", groups(false), head(), [md]))));
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
  const hits = fuzzy("reports", "reports/b.md")!.hits;
  expect(dump(rows[0]!)).toBe(dump(fake(oldRow(files[1]!, "doc/x.md", hits, true, true))));
  expect(dump(rows[1]!)).toBe(dump(fake(oldRow(files[2]!, "doc/x.md", hits, false, true))));

  dispatch(search, "keydown", { key: "ArrowDown" });
  expect(elements(list)).toEqual(rows); // the cursor moved; the rows stayed
  expect(rows[1]!.className).toBe("file-row cursor");
  expect(rows[0]!.className).toBe("file-row");
  dispatch(search, "keydown", { key: "Enter" });
  expect(went).toEqual([{ file: "reports/a.md" }]);

  search["value"] = "zzz";
  dispatch(search, "input");
  expect(dump(child(v.el, 0, 1))).toBe(
    dump(
      fake(
        h(
          "div",
          { class: "file-groups", role: "listbox", "aria-label": "Files" },
          h("p", { class: "file-empty" }, "No file matches “zzz”."),
        ),
      ),
    ),
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
  expect(dump(body)).toBe(dump(fake(h("div", { class: "viewer-body" }, h("pre", { class: "raw" }, "# Title")))));
  expect(child(v.el, 1, 0, 1, 1).getAttribute("aria-checked")).toBe("true");
  expect(fetched).toEqual(["00-route.md"]); // the switch reuses the text

  v.update(routeOf(files), "doc/x.md");
  await settle();
  expect(dump(body)).toBe(
    dump(
      fake(
        h(
          "div",
          { class: "viewer-body" },
          h("p", { class: "error" }, "cannot read doc/x.md (404)"),
          h("button", { type: "button", class: "btn" }, "Try again"),
        ),
      ),
    ),
  );
  served["doc/x.md"] = "x";
  dispatch(child(body, 1), "click");
  await settle();
  expect(dump(body)).toBe(dump(fake(h("div", { class: "viewer-body" }, h("pre", { class: "raw" }, "x")))));
});
