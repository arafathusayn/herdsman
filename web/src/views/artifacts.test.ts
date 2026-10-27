import { afterEach, beforeEach, expect, test } from "bun:test";

import { render } from "../herdsman-blocks-view.ts";
import { FakeElement, type FakeNode, installFakeDocument, ops, resetOps } from "../herdsman-blocks.testdom.ts";
import { icon, icons } from "../icons.ts";
import { ago } from "../model.ts";
import { dispatch, markup } from "../testdom.ts";
import type { Artifact } from "../types.ts";

// The store reads the URL and listens for history changes when it loads.
const g = globalThis as unknown as Record<string, unknown>;
g["location"] ??= { search: "", pathname: "/" };
g["window"] ??= { addEventListener: () => {} };
const win = g["window"] as Record<string, unknown>;
const { store } = await import("../store.ts");
const { ArtifactsView, artifactPath } = await import("./artifacts.ts");

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
const page = (root: string, path: string): Artifact => ({ root, path, size: 2048, modified: when });

const a1 = page("/w/one/.lavish", "plan.html");
const a2 = page("/w/one/.lavish", "deep/review.html");
const b1 = page("/w/two/reports", "report.html");

test("before the list loads, then the groups, the head and the frame of the newest page", async () => {
  pages = [a1, a2, b1];
  const v = new ArtifactsView("r1");
  v.update("");
  expect(dump(fake(v.el))).toBe(markup`
    <div class="files artifacts">
      <div class="file-pane">
        <label class="file-search">
          ${iconMarkup(icons.search)}
          <input aria-label="Search artifacts" autocomplete="off" placeholder="Search pages…" spellcheck="false" type="search"></input>
          <span class="files-count"></span>
        </label>
        <div aria-label="Artifacts" class="file-groups" role="listbox">
          <p class="file-empty">Looking for HTML pages…</p>
        </div>
      </div>
      <section aria-live="polite" class="viewer">
        <div class="viewer-head"></div>
        <div class="viewer-body artifact-body">
          <p class="muted artifact-empty">Pick a page to show it.</p>
        </div>
      </section>
    </div>
  `);

  v.start();
  await settle();
  await settle();
  v.stop();
  const cur = artifactPath(a1);
  expect(opened).toEqual([cur]);
  expect(dump(fake(v.el))).toBe(markup`
    <div class="files artifacts">
      <div class="file-pane">
        <label class="file-search">
          ${iconMarkup(icons.search)}
          <input aria-label="Search artifacts" autocomplete="off" placeholder="Search pages…" spellcheck="false" type="search"></input>
          <span class="files-count">3</span>
        </label>
        <div aria-label="Artifacts" class="file-groups" role="listbox">
          <details class="file-group" open="">
            <summary title="/w/one/.lavish">
              ${iconMarkup(icons.folder)}
              <span >one/.lavish</span>
              <span class="count">2</span>
            </summary>
            <button aria-selected="true" class="file-row active" role="option" title="/w/one/.lavish/plan.html">
              ${iconMarkup(icons.artifact)}
              <span class="file-name"><span class="name">plan.html</span></span>
              <small >${ago(when)}</small>
            </button>
            <button aria-selected="false" class="file-row" role="option" title="/w/one/.lavish/deep/review.html">
              ${iconMarkup(icons.artifact)}
              <span class="file-name">
                <span class="name">review.html</span>
                <span class="dir">deep/</span>
              </span>
              <small >${ago(when)}</small>
            </button>
          </details>
          <details class="file-group" open="">
            <summary title="/w/two/reports">
              ${iconMarkup(icons.folder)}
              <span >two/reports</span>
              <span class="count">1</span>
            </summary>
            <button aria-selected="false" class="file-row" role="option" title="/w/two/reports/report.html">
              ${iconMarkup(icons.artifact)}
              <span class="file-name"><span class="name">report.html</span></span>
              <small >${ago(when)}</small>
            </button>
          </details>
        </div>
      </div>
      <section aria-live="polite" class="viewer">
        <div class="viewer-head">
          <div class="viewer-title">
            <h2 title="/w/one/.lavish/plan.html">plan.html</h2>
            <p >one/.lavish · 2.0 KB · ${ago(when)}</p>
          </div>
          <button aria-label="Reload the page" class="icon-btn" data-tip="Reload the page">
            ${iconMarkup(icons.refresh)}
          </button>
          <button aria-label="Open in a new tab" class="icon-btn" data-tip="Open in a new tab">
            ${iconMarkup(icons.external)}
          </button>
          <button aria-label="Copy the file path" class="icon-btn" data-tip="Copy the file path">
            ${iconMarkup(icons.copy)}
          </button>
        </div>
        <div class="viewer-body artifact-body">
          <iframe class="artifact-frame" referrerpolicy="no-referrer" sandbox="allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads" src="/frame/plan.html" title="plan.html"></iframe>
        </div>
      </section>
    </div>
  `);
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
    markup`
      <section aria-live="polite" class="viewer">
        <div class="viewer-head"></div>
        <div class="viewer-body artifact-body">
          <p class="muted artifact-empty">This page is no longer in the route's workspace folders.</p>
        </div>
      </section>
    `,
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
  expect(dump(rows[0]!)).toBe(markup`
    <button aria-selected="false" class="file-row cursor" role="option" title="/w/one/.lavish/deep/review.html">
      ${iconMarkup(icons.artifact)}
      <span class="file-name">
        <span class="name"><mark >review</mark>.html</span>
        <span class="dir">one/.lavish/deep/</span>
      </span>
      <small >${ago(when)}</small>
    </button>
  `);
  dispatch(search, "keydown", { key: "Enter" });
  expect(went).toEqual([{ artifact: artifactPath(a2) }]);
  dispatch(rows[0]!, "click");
  expect(went.length).toBe(2);

  search["value"] = "zzz";
  dispatch(search, "input");
  expect(dump(child(v.el, 0, 1, 0))).toBe('<p class="file-empty">No page matches “zzz”.</p>');

  dispatch(child(v.el, 1, 0, 1), "click"); // Reload
  expect(child(v.el, 1, 1, 0).textContent).toBe("Loading…");
  await settle();
  expect(dump(child(v.el, 1, 1, 0))).toBe(markup`
    <iframe class="artifact-frame" referrerpolicy="no-referrer" sandbox="allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads" src="/frame/plan.html" title="plan.html"></iframe>
  `);
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
    markup`
      <div aria-label="Artifacts" class="file-groups" role="listbox">
        <p class="file-empty error">cannot list artifacts (500)</p>
      </div>
    `,
  );
});
