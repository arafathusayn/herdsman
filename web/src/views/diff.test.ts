import { afterEach, beforeEach, expect, test } from "bun:test";

import { render } from "../herdsman-blocks-view.ts";
import { FakeElement, type FakeNode, installFakeDocument, ops, resetOps } from "../herdsman-blocks.testdom.ts";
import { icon, icons } from "../icons.ts";
import { dispatch, markup } from "../testdom.ts";
import type { DiffFile, DiffSource } from "../types.ts";

// The store reads the URL and listens for history changes when it loads.
const g = globalThis as unknown as Record<string, unknown>;
g["location"] ??= { search: "", pathname: "/" };
g["window"] ??= { addEventListener: () => {} };
const win = g["window"] as Record<string, unknown>;

// The viewer's width decides whether side by side fits; the test sets it.
let resized: ((entries: { contentRect: { width: number } }[]) => void) | null = null;
let observed: unknown = null;
g["ResizeObserver"] = class {
  constructor(fn: typeof resized) {
    resized = fn;
  }
  observe(el: unknown): void {
    observed = el;
  }
  // Other test files share this global; the agent panel disconnects its observer.
  disconnect(): void {}
};

const { store } = await import("../store.ts");
const { DiffView } = await import("./diff.ts");

const settle = () => new Promise((r) => setTimeout(r, 0));
const fake = (n: unknown): FakeElement => n as FakeElement;
const proto = FakeElement.prototype as unknown as Record<string, unknown>;
const realFetch = globalThis.fetch;
const realGo = store.go;
let went: unknown[] = [];
let sources: DiffSource[] = [];
let dataset: Record<string, string> = {};

beforeEach(() => {
  installFakeDocument();
  dataset = { code: "off" };
  const doc = g["document"] as Record<string, unknown>;
  doc["documentElement"] = { dataset };
  doc["visibilityState"] = "visible";
  proto["value"] = ""; // the search input's
  win["setInterval"] = () => 1;
  win["clearInterval"] = () => {};
  went = [];
  store.go = (patch) => void went.push(patch);
  // A fresh body every call: no ETag, so every poll is a new diff object.
  globalThis.fetch = (async () => Response.json({ sources: structuredClone(sources) })) as unknown as typeof fetch;
});
afterEach(async () => {
  await settle();
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
const writes = () =>
  ops.inserts + ops.removes + ops.moves + ops.clones + ops.classWrites + ops.attrWrites + ops.textWrites;

const appPatch = [
  "diff --git a/src/app.ts b/src/app.ts",
  "index 1111111..2222222 100644",
  "--- a/src/app.ts",
  "+++ b/src/app.ts",
  "@@ -1,4 +1,4 @@",
  " const a = 1; // one",
  "-const b = 2;",
  "+const b = 3;",
  " const c = 4;",
  "-end",
  "\\ No newline at end of file",
  "+end;",
  "\\ No newline at end of file",
  "diff --git a/README.md b/README.md",
  "new file mode 100644",
  "--- /dev/null",
  "+++ b/README.md",
  "@@ -0,0 +1 @@",
  "+# hi",
  "",
].join("\n");
const appFiles: DiffFile[] = [
  { path: "src/app.ts", status: "M", add: 2, del: 2 },
  { path: "README.md", status: "A", add: 1, del: 0 },
  { path: "notes.txt", status: "?", add: 0, del: 0 },
];
const task: DiffSource = {
  id: "t1",
  kind: "task",
  label: "task one",
  dir: "/w/t1",
  agents: ["impl", "rev"],
  diff: { base: "main", merge_base: "m", head: "t1", files: appFiles, patch: appPatch },
};
const checkout: DiffSource = { id: "co", kind: "checkout", label: "checkout", dir: "/w", error: "not a git folder" };

async function opened(file = "src/app.ts"): Promise<InstanceType<typeof DiffView>> {
  sources = [task, checkout];
  const v = new DiffView("r1");
  v.update("", file);
  v.start();
  await settle();
  v.stop();
  return v;
}

test("before the first poll, then sources, files, the head and an inline diff as the h() version drew them", async () => {
  const v = new DiffView("r1");
  v.update("", "");
  expect(dump(fake(v.el))).toBe(markup`
    <div class="files diff">
      <div class="file-pane">
        <div aria-label="Changes" class="diff-sources" role="listbox">
          <p class="file-empty">Reading the changes…</p>
        </div>
        <label class="file-search">
          ${iconMarkup(icons.search)}
          <input aria-label="Search changed files" autocomplete="off" placeholder="Search changed files…" spellcheck="false" type="search"></input>
          <span class="files-count"></span>
        </label>
        <div aria-label="Changed files" class="file-groups" role="listbox"></div>
      </div>
      <section aria-live="polite" class="viewer">
        <div class="viewer-head"></div>
        <div class="viewer-body diff-body"><p class="muted">Reading the changes…</p></div>
      </section>
    </div>
  `);
  expect(observed).toBe(child(v.el, 1)); // the viewer's width picks the layout

  const w = await opened();
  expect(dump(fake(w.el))).toBe(markup`
    <div class="files diff">
      <div class="file-pane">
        <div aria-label="Changes" class="diff-sources" role="listbox">
          <button aria-selected="true" class="diff-source active" role="option" title="/w/t1">
            <span class="diff-source-top">
              <strong >task one</strong>
              <span class="counts"><span class="plus">+3</span><span class="minus">−2</span></span>
            </span>
            <span class="diff-source-agents" title="Agents working in this folder">
              <span class="who">impl</span>
              <span class="who">rev</span>
            </span>
            <span class="diff-source-sub">
              ${iconMarkup(icons.branch)}
              t1 ← main
              <span class="n">3 files</span>
            </span>
          </button>
          <button aria-selected="false" class="diff-source" role="option" title="/w">
            <span class="diff-source-top">
              <strong >checkout</strong>
              <span class="chip warn">unavailable</span>
            </span>
            <span class="diff-source-sub">${iconMarkup(icons.folder)}/w</span>
          </button>
        </div>
        <label class="file-search">
          ${iconMarkup(icons.search)}
          <input aria-label="Search changed files" autocomplete="off" placeholder="Search changed files…" spellcheck="false" type="search"></input>
          <span class="files-count">3</span>
        </label>
        <div aria-label="Changed files" class="file-groups" role="listbox">
          <button aria-selected="false" class="file-row" role="option" title="notes.txt">
            <span class="st st-u" title="Untracked">?</span>
            <span class="file-name"><span class="name">notes.txt</span></span>
          </button>
          <button aria-selected="false" class="file-row" role="option" title="README.md">
            <span class="st st-A" title="Added">A</span>
            <span class="file-name"><span class="name">README.md</span></span>
            <span class="counts"><span class="plus">+1</span><span class="minus">−0</span></span>
          </button>
          <button aria-selected="true" class="file-row active" role="option" title="src/app.ts">
            <span class="st st-M" title="Modified">M</span>
            <span class="file-name"><span class="name">app.ts</span><span class="dir">src/</span></span>
            <span class="counts"><span class="plus">+2</span><span class="minus">−2</span></span>
          </button>
        </div>
      </div>
      <section aria-live="polite" class="viewer">
        <div class="viewer-head">
          <div class="viewer-title">
            <h2 title="src/app.ts"><span class="dir">src/</span>app.ts</h2>
            <p >
              Modified · <span class="counts"><span class="plus">+2</span><span class="minus">−2</span></span>
            </p>
          </div>
          <div aria-label="Diff layout" class="view-switch diff-layout" role="group">
            <button aria-pressed="true" class="active" title="Removed and added lines in one column" type="button">Inline</button>
            <button aria-pressed="false" title="The old file on the left, the new one on the right" type="button">Side by side</button>
          </div>
          <button aria-label="Copy the file path" class="icon-btn" title="Copy the file path">
            ${iconMarkup(icons.copy)}
          </button>
        </div>
        <div class="viewer-body diff-body">
          <div >
            <div class="diff-code inline">
              <table >
                <tbody >
                  <tr class="d-hunk"><td class="code" colspan="3">@@ -1,4 +1,4 @@</td></tr>
                  <tr class="d-ctx">
                    <td class="ln">1</td>
                    <td class="ln">1</td>
                    <td class="code">const a = 1; // one</td>
                  </tr>
                  <tr class="d-del">
                    <td class="ln">2</td>
                    <td class="ln"></td>
                    <td class="code">const b = 2;</td>
                  </tr>
                  <tr class="d-add">
                    <td class="ln"></td>
                    <td class="ln">2</td>
                    <td class="code">const b = 3;</td>
                  </tr>
                  <tr class="d-ctx">
                    <td class="ln">3</td>
                    <td class="ln">3</td>
                    <td class="code">const c = 4;</td>
                  </tr>
                  <tr class="d-del">
                    <td class="ln">4</td>
                    <td class="ln"></td>
                    <td class="code">end</td>
                  </tr>
                  <tr class="d-note"><td class="code" colspan="3">No newline at end of file</td></tr>
                  <tr class="d-add">
                    <td class="ln"></td>
                    <td class="ln">4</td>
                    <td class="code">end;</td>
                  </tr>
                  <tr class="d-note"><td class="code" colspan="3">No newline at end of file</td></tr>
                </tbody>
              </table>
            </div>
          </div>
        </div>
      </section>
    </div>
  `);
});

test("side by side, with highlighting: both cells of a context line show its colored code", async () => {
  dataset["code"] = "on";
  const v = await opened();
  dispatch(child(v.el, 1, 0, 1, 1), "click"); // Side by side
  expect(dump(child(v.el, 1, 1))).toBe(
    markup`
      <div class="viewer-body diff-body">
        <div >
          <div class="diff-code split">
            <table >
              <colgroup >
                <col class="ln-col"></col>
                <col ></col>
                <col class="ln-col"></col>
                <col ></col>
              </colgroup>
              <tbody >
                <tr class="d-hunk"><td class="code" colspan="4">@@ -1,4 +1,4 @@</td></tr>
                <tr >
                  <td class="ln ctx">1</td>
                  <td class="code ctx">
                    <span class="hl-keyword">const</span> a = <span class="hl-number">1</span>; <span class="hl-comment">// one</span>
                  </td>
                  <td class="ln ctx">1</td>
                  <td class="code ctx">
                    <span class="hl-keyword">const</span> a = <span class="hl-number">1</span>; <span class="hl-comment">// one</span>
                  </td>
                </tr>
                <tr >
                  <td class="ln del">2</td>
                  <td class="code del">
                    <span class="hl-keyword">const</span> b = <span class="hl-number">2</span>
                    ;
                  </td>
                  <td class="ln add">2</td>
                  <td class="code add">
                    <span class="hl-keyword">const</span> b = <span class="hl-number">3</span>
                    ;
                  </td>
                </tr>
                <tr >
                  <td class="ln ctx">3</td>
                  <td class="code ctx">
                    <span class="hl-keyword">const</span> c = <span class="hl-number">4</span>
                    ;
                  </td>
                  <td class="ln ctx">3</td>
                  <td class="code ctx">
                    <span class="hl-keyword">const</span> c = <span class="hl-number">4</span>
                    ;
                  </td>
                </tr>
                <tr >
                  <td class="ln del">4</td>
                  <td class="code del">end</td>
                  <td class="ln blank"></td>
                  <td class="code blank"></td>
                </tr>
                <tr class="d-note"><td class="code" colspan="4">No newline at end of file</td></tr>
                <tr >
                  <td class="ln blank"></td>
                  <td class="code blank"></td>
                  <td class="ln add">4</td>
                  <td class="code add">end;</td>
                </tr>
                <tr class="d-note"><td class="code" colspan="4">No newline at end of file</td></tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>
    `,
  );
  expect(dump(child(v.el, 1, 0, 1))).toBe(markup`
    <div aria-label="Diff layout" class="view-switch diff-layout" role="group">
      <button aria-pressed="false" title="Removed and added lines in one column" type="button">Inline</button>
      <button aria-pressed="true" class="active" title="The old file on the left, the new one on the right" type="button">Side by side</button>
    </div>
  `);
  // A narrow viewer draws inline and hides the switch, whatever the choice.
  resized!([{ contentRect: { width: 400 } }]);
  expect(elements(child(v.el, 1, 0)).length).toBe(2);
  expect(child(v.el, 1, 1, 0, 0).className).toBe("diff-code inline");
  resized!([{ contentRect: { width: 900 } }]);
  dispatch(child(v.el, 1, 0, 1, 0), "click"); // back to Inline for the other tests
  expect(child(v.el, 1, 1, 0, 0).className).toBe("diff-code inline");
});

test("a poll with an equal diff writes nothing; a changed one keeps the rows it still has", async () => {
  const v = await opened();
  const tbody = child(v.el, 1, 1, 0, 0, 0, 0);
  const rows = elements(tbody);
  resetOps();
  v.start();
  await settle();
  v.stop();
  expect(writes()).toBe(0);
  expect(elements(tbody)).toEqual(rows);

  // The task's file gains a hunk at its end.
  sources = [
    {
      ...task,
      diff: {
        ...task.diff!,
        patch: appPatch.replace("diff --git a/README.md", "@@ -10 +10 @@\n-x\n+y\ndiff --git a/README.md"),
      },
    },
    checkout,
  ];
  v.start();
  await settle();
  v.stop();
  const now = elements(tbody);
  expect(now.length).toBe(rows.length + 3);
  for (let i = 0; i < rows.length; i++) expect(now[i]).toBe(rows[i]!);
});

test("search, Enter, row and source clicks, and the untracked file's note", async () => {
  const v = await opened();
  const search = child(v.el, 0, 1, 1);
  search["value"] = "app";
  dispatch(search, "input");
  expect(child(v.el, 0, 1, 2).textContent).toBe("1/3");
  expect(dump(child(v.el, 0, 2, 0))).toBe(markup`
    <button aria-selected="true" class="file-row active" role="option" title="src/app.ts">
      <span class="st st-M" title="Modified">M</span>
      <span class="file-name">
        <span class="name"><mark >app</mark>.ts</span>
        <span class="dir">src/</span>
      </span>
      <span class="counts"><span class="plus">+2</span><span class="minus">−2</span></span>
    </button>
  `);
  dispatch(search, "keydown", { key: "Enter" });
  expect(went).toEqual([{ diffFile: "src/app.ts", source: "t1" }]);
  search["value"] = "zzz";
  dispatch(search, "input");
  expect(dump(child(v.el, 0, 2, 0))).toBe('<p class="file-empty">No changed file matches “zzz”.</p>');
  dispatch(search, "keydown", { key: "Escape" });
  expect(elements(child(v.el, 0, 2)).length).toBe(3);

  dispatch(child(v.el, 0, 2, 0), "click");
  dispatch(child(v.el, 0, 0, 1), "click");
  expect(went.slice(1)).toEqual([
    { diffFile: "notes.txt", source: "t1" },
    { source: "co", diffFile: "" },
  ]);

  v.update("t1", "notes.txt");
  expect(dump(child(v.el, 1))).toBe(
    markup`
      <section aria-live="polite" class="viewer">
        <div class="viewer-head">
          <div class="viewer-title"><h2 title="notes.txt">notes.txt</h2><p >Untracked</p></div>
          <button aria-label="Copy the file path" class="icon-btn" title="Copy the file path">
            ${iconMarkup(icons.copy)}
          </button>
        </div>
        <div class="viewer-body diff-body">
          <p class="muted">Untracked file: git has no diff for it until it is added.</p>
        </div>
      </section>
    `,
  );
  v.update("co", "");
  expect(dump(child(v.el, 1))).toBe(
    markup`
      <section aria-live="polite" class="viewer">
        <div class="viewer-head"><div class="viewer-title"><h2 class="plain">checkout</h2></div></div>
        <div class="viewer-body diff-body">
          <p class="error">not a git folder</p>
          <p class="muted">Folder: /w</p>
        </div>
      </section>
    `,
  );
});

test("a long file draws its first 3000 lines; Show all adds the rest and keeps the rows drawn", async () => {
  const n = 3100;
  const big = [
    "diff --git a/gen.txt b/gen.txt",
    "--- a/gen.txt",
    "+++ b/gen.txt",
    `@@ -0,0 +1,${n} @@`,
    ...Array.from({ length: n }, (_, i) => `+line ${i}`),
    "",
  ].join("\n");
  sources = [
    {
      ...task,
      diff: { ...task.diff!, files: [{ path: "gen.txt", status: "A", add: n, del: 0 }], patch: big },
    },
  ];
  const v = new DiffView("r1");
  v.update("t1", "gen.txt");
  v.start();
  await settle();
  v.stop();
  // 3101 lines are too many to write out: the table is built as a string, the
  // hunk's row and then a row per added line, up to rows lines.
  const table = (rows: number, more: string): string => markup`
    <div >
      <div class="diff-code inline">
        <table >
          <tbody >
            <tr class="d-hunk"><td class="code" colspan="3">@@ -0,0 +1,3100 @@</td></tr>
            ${Array.from({ length: rows - 1 }, (_, i) => `<tr class="d-add"><td class="ln"></td><td class="ln">${i + 1}</td><td class="code">line ${i}</td></tr>`).join("")}
          </tbody>
        </table>
      </div>
      ${more}
    </div>
  `;
  expect(dump(child(v.el, 1, 1, 0))).toBe(
    table(3000, '<button class="btn ghost diff-more">Show all 3101 lines</button>'),
  );
  const tbody = child(v.el, 1, 1, 0, 0, 0, 0);
  const first = elements(tbody);
  resetOps();
  dispatch(child(v.el, 1, 1, 0, 1), "click"); // Show all
  expect(dump(child(v.el, 1, 1, 0))).toBe(table(3101, ""));
  const all = elements(tbody);
  for (let i = 0; i < first.length; i++) expect(all[i]).toBe(first[i]!);
  expect(ops.moves + ops.removes).toBe(1); // only the button goes
});
