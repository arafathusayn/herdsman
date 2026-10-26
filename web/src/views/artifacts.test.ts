import { afterEach, beforeEach, expect, test } from "bun:test";

import { icon, icons } from "../dom.ts";
import { fuzzy, highlight } from "../fuzzy.ts";
import type { Child } from "../herdsman-blocks-view.ts";
import { FakeElement, type FakeNode, installFakeDocument, ops, resetOps } from "../herdsman-blocks.testdom.ts";
import { ago } from "../model.ts";
import { dispatch } from "../testdom.ts";
import { each, h } from "../testref.ts";
import type { Artifact } from "../types.ts";

// The store reads the URL and listens for history changes when it loads.
const g = globalThis as unknown as Record<string, unknown>;
g["location"] ??= { search: "", pathname: "/" };
g["window"] ??= { addEventListener: () => {} };
const win = g["window"] as Record<string, unknown>;
const { store } = await import("../store.ts");
const { ArtifactsView, artifactPath, rootLabel } = await import("./artifacts.ts");

const settle = () => new Promise((r) => setTimeout(r, 0));
const fake = (n: unknown): FakeElement => n as FakeElement;
const proto = FakeElement.prototype as unknown as Record<string, unknown>;
const realFetch = globalThis.fetch;
const realGo = store.go;
let went: unknown[] = [];
let pages: Artifact[] = [];
let listFails = false;
let opened: string[] = [];

beforeEach(() => {
  installFakeDocument();
  proto["querySelector"] = () => null;
  proto["value"] = ""; // the search input's
  win["setTimeout"] = setTimeout;
  went = [];
  opened = [];
  listFails = false;
  store.go = (patch) => void went.push(patch);
  globalThis.fetch = (async (url: string, init?: RequestInit) => {
    if (url.startsWith("/api/artifacts/open")) {
      const { root, path } = JSON.parse(String(init?.body)) as { root: string; path: string };
      opened.push(`${root}/${path}`);
      return Response.json({ url: `/frame/${path}` });
    }
    return listFails ? new Response("", { status: 500 }) : Response.json({ roots: [], artifacts: pages });
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
const child = (n: unknown, ...path: number[]): FakeElement => {
  let e = fake(n);
  for (const i of path) e = elements(e)[i]!;
  return e;
};
const when = "2020-01-01T00:00:00Z";
const page = (root: string, path: string): Artifact => ({ root, path, size: 2048, modified: when });

// The h() markup ArtifactsView made before it was a view. The browser
// reflects the properties h() set (type, spellcheck, open, referrerPolicy) as
// attributes, as the fake records them here.
function oldRow(a: Artifact, current: string, hits: number[], cursor: boolean, showRoot: boolean): HTMLElement {
  const full = artifactPath(a);
  const slash = a.path.lastIndexOf("/");
  const name = a.path.slice(slash + 1);
  const dir = slash < 0 ? "" : a.path.slice(0, slash + 1);
  const nameHits = hits.filter((i) => i > slash).map((i) => i - slash - 1);
  const dirHits = hits.filter((i) => i <= slash);
  const on = full === current;
  const cls = ["file-row", on ? "active" : "", cursor ? "cursor" : ""].filter(Boolean).join(" ");
  return h(
    "button",
    { class: cls, role: "option", "aria-selected": String(on), title: full },
    icon(icons.artifact),
    h(
      "span",
      { class: "file-name" },
      h("span", { class: "name" }, highlight(name, nameHits)),
      dir || showRoot
        ? h("span", { class: "dir" }, showRoot ? `${rootLabel(a.root)}/` : "", highlight(dir, dirHits))
        : null,
    ),
    h("small", null, ago(a.modified)),
  );
}
function oldGroup(root: string, items: Artifact[], current: string, open: boolean): HTMLElement {
  return h(
    "details",
    { class: "file-group", open },
    h(
      "summary",
      { title: root },
      icon(icons.folder),
      h("span", null, rootLabel(root)),
      h("span", { class: "count" }, String(items.length)),
    ),
    each(items, artifactPath, (a) => oldRow(a, current, [], false, false)),
  );
}
function oldShell(count: string, list: Child[], head: Child[], body: Child[]): HTMLElement {
  return h(
    "div",
    { class: "files artifacts" },
    h(
      "div",
      { class: "file-pane" },
      h(
        "label",
        { class: "file-search" },
        icon(icons.search),
        h("input", {
          type: "search",
          placeholder: "Search pages…",
          "aria-label": "Search artifacts",
          autocomplete: "off",
          spellcheck: "false",
        }),
        h("span", { class: "files-count" }, count),
      ),
      h("div", { class: "file-groups", role: "listbox", "aria-label": "Artifacts" }, ...list),
    ),
    h(
      "section",
      { class: "viewer", "aria-live": "polite" },
      h("div", { class: "viewer-head" }, ...head),
      h("div", { class: "viewer-body artifact-body" }, ...body),
    ),
  );
}
function oldHead(a: Artifact): Node[] {
  const full = artifactPath(a);
  const slash = a.path.lastIndexOf("/");
  const btn = (tip: string, glyph: string[]) =>
    h("button", { class: "icon-btn", "data-tip": tip, "aria-label": tip }, icon(glyph));
  return [
    h(
      "div",
      { class: "viewer-title" },
      h(
        "h2",
        { title: full },
        slash >= 0 ? h("span", { class: "dir" }, a.path.slice(0, slash + 1)) : null,
        a.path.slice(slash + 1),
      ),
      h("p", null, `${rootLabel(a.root)} · 2.0 KB · ${ago(a.modified)}`),
    ),
    btn("Reload the page", icons.refresh),
    btn("Open in a new tab", icons.external),
    btn("Copy the file path", icons.copy),
  ];
}
const frame = (a: Artifact) =>
  h("iframe", {
    class: "artifact-frame",
    src: `/frame/${a.path}`,
    title: a.path,
    sandbox: "allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads",
    referrerpolicy: "no-referrer",
  });

const a1 = page("/w/one/.lavish", "plan.html");
const a2 = page("/w/one/.lavish", "deep/review.html");
const b1 = page("/w/two/reports", "report.html");

test("before the list loads, then the groups, the head and the frame of the newest page", async () => {
  pages = [a1, a2, b1];
  const v = new ArtifactsView("r1");
  v.update("");
  const empty = (text: string) => [h("p", { class: "muted artifact-empty" }, text)];
  const looking = [h("p", { class: "file-empty" }, "Looking for HTML pages…")];
  expect(dump(fake(v.el))).toBe(dump(fake(oldShell("", looking, [], empty("Pick a page to show it.")))));

  v.start();
  await settle();
  await settle();
  v.stop();
  const cur = artifactPath(a1);
  const groups = [oldGroup(a1.root, [a1, a2], cur, true), oldGroup(b1.root, [b1], cur, true)];
  expect(opened).toEqual([cur]);
  expect(dump(fake(v.el))).toBe(dump(fake(oldShell("3", groups, oldHead(a1), [frame(a1)]))));
});

test("a poll that finds nothing new writes nothing; a gone page says so", async () => {
  pages = [a1, b1];
  const v = new ArtifactsView("r1");
  v.update(artifactPath(b1));
  v.start();
  await settle();
  await settle();
  v.stop();
  const frameEl = child(v.el, 1, 1, 0);
  resetOps();
  v.update(artifactPath(b1));
  v.start();
  await settle();
  v.stop();
  expect(ops.inserts + ops.removes + ops.moves + ops.clones + ops.classWrites).toBe(0);
  expect(child(v.el, 1, 1, 0)).toBe(frameEl);
  expect(opened).toEqual([artifactPath(b1)]);

  v.update("/w/gone/x.html");
  expect(dump(child(v.el, 1))).toBe(
    dump(
      fake(
        h(
          "section",
          { class: "viewer", "aria-live": "polite" },
          h("div", { class: "viewer-head" }),
          h(
            "div",
            { class: "viewer-body artifact-body" },
            h("p", { class: "muted artifact-empty" }, "This page is no longer in the route's workspace folders."),
          ),
        ),
      ),
    ),
  );
});

test("search marks hits with the workspace folder; Enter opens the cursor's page; reload refetches", async () => {
  pages = [a1, a2, b1];
  const v = new ArtifactsView("r1");
  v.update(artifactPath(a1));
  v.start();
  await settle();
  await settle();
  v.stop();
  const search = child(v.el, 0, 0, 1);
  search["value"] = "review";
  dispatch(search, "input");
  const rows = elements(child(v.el, 0, 1));
  expect(child(v.el, 0, 0, 2).textContent).toBe("1/3");
  expect(dump(rows[0]!)).toBe(dump(fake(oldRow(a2, artifactPath(a1), fuzzy("review", a2.path)!.hits, true, true))));
  dispatch(search, "keydown", { key: "Enter" });
  expect(went).toEqual([{ artifact: artifactPath(a2) }]);
  dispatch(rows[0]!, "click");
  expect(went.length).toBe(2);

  search["value"] = "zzz";
  dispatch(search, "input");
  expect(dump(child(v.el, 0, 1, 0))).toBe(dump(fake(h("p", { class: "file-empty" }, "No page matches “zzz”."))));

  dispatch(child(v.el, 1, 0, 1), "click"); // Reload
  expect(child(v.el, 1, 1, 0).textContent).toBe("Loading…");
  await settle();
  expect(dump(child(v.el, 1, 1, 0))).toBe(dump(fake(frame(a1))));
  expect(opened).toEqual([artifactPath(a1), artifactPath(a1)]);
});

test("a closed group stays closed; a failed first load shows the error", async () => {
  pages = [a1, b1];
  const v = new ArtifactsView("r1");
  v.update("");
  v.start();
  await settle();
  await settle();
  v.stop();
  const group = child(v.el, 0, 1, 0);
  group.removeAttribute("open");
  group["open"] = false;
  dispatch(group, "toggle");
  v.update(artifactPath(b1));
  expect(child(v.el, 0, 1, 0)).toBe(group);
  expect(group.hasAttribute("open")).toBe(false);

  listFails = true;
  const w = new ArtifactsView("r2");
  w.start();
  await settle();
  w.stop();
  expect(dump(child(w.el, 0, 1))).toBe(
    dump(
      fake(
        h(
          "div",
          { class: "file-groups", role: "listbox", "aria-label": "Artifacts" },
          h("p", { class: "file-empty error" }, "cannot list artifacts (500)"),
        ),
      ),
    ),
  );
});
