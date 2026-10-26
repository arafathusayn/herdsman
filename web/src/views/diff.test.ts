import { afterEach, beforeEach, expect, test } from "bun:test";

import { type DiffLine, parseSection, sideBySide, splitPatch } from "../diff.ts";
import { icon, icons } from "../dom.ts";
import { fuzzy, highlight } from "../fuzzy.ts";
import type { Child } from "../herdsman-blocks-view.ts";
import { FakeElement, type FakeNode, installFakeDocument, ops, resetOps } from "../herdsman-blocks.testdom.ts";
import { languageOf, type Piece, type State, tokenizeLine } from "../highlight.ts";
import { dispatch } from "../testdom.ts";
import { h, unkeyed } from "../testref.ts";
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
  proto["append"] = function (this: FakeElement, ...ns: (FakeNode | string)[]) {
    for (const n of ns) this.appendChild(typeof n === "string" ? fake(document.createTextNode(n)) : n);
  };
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
  delete proto["append"];
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
const writes = () =>
  ops.inserts + ops.removes + ops.moves + ops.clones + ops.classWrites + ops.attrWrites + ops.textWrites;

// ---------------------------------------------------------------------------
// The h() markup DiffView made before it was a view. The browser reflects the
// properties h() set (type, spellcheck) as attributes, as the fake records
// them here; h() also set an empty class on inactive layout buttons, which a
// class hole leaves out.
// ---------------------------------------------------------------------------

const statusName: Record<string, string> = { A: "Added", M: "Modified", D: "Deleted", "?": "Untracked" };
const oldCounts = (add: number, del: number) =>
  h("span", { class: "counts" }, h("span", { class: "plus" }, `+${add}`), h("span", { class: "minus" }, `−${del}`));

function oldSource(s: DiffSource, active: boolean): HTMLElement {
  const files = s.diff?.files ?? [];
  const add = files.reduce((n, f) => n + f.add, 0);
  const del = files.reduce((n, f) => n + f.del, 0);
  return h(
    "button",
    {
      class: active ? "diff-source active" : "diff-source",
      role: "option",
      "aria-selected": String(active),
      title: s.dir ?? s.label,
    },
    h(
      "span",
      { class: "diff-source-top" },
      h("strong", null, s.label),
      s.error ? h("span", { class: "chip warn" }, "unavailable") : oldCounts(add, del),
    ),
    s.agents?.length
      ? h(
          "span",
          { class: "diff-source-agents", title: "Agents working in this folder" },
          unkeyed(s.agents.map((a) => h("span", { class: "who" }, a))),
        )
      : null,
    h(
      "span",
      { class: "diff-source-sub" },
      icon(s.kind === "checkout" ? icons.folder : icons.branch),
      s.diff ? `${s.diff.head} ← ${s.diff.base}` : (s.branch ?? s.dir ?? ""),
      s.diff ? h("span", { class: "n" }, `${files.length} ${files.length === 1 ? "file" : "files"}`) : null,
    ),
  );
}

function oldRow(f: DiffFile, hits: number[], active: boolean): HTMLElement {
  const slash = f.path.lastIndexOf("/");
  const name = f.path.slice(slash + 1);
  const dir = slash < 0 ? "" : f.path.slice(0, slash + 1);
  return h(
    "button",
    {
      class: active ? "file-row active" : "file-row",
      role: "option",
      "aria-selected": String(active),
      title: f.old_path ? `${f.old_path} → ${f.path}` : f.path,
    },
    h(
      "span",
      { class: `st st-${f.status === "?" ? "u" : f.status}`, title: statusName[f.status] ?? f.status },
      f.status,
    ),
    h(
      "span",
      { class: "file-name" },
      h(
        "span",
        { class: "name" },
        highlight(
          name,
          hits.filter((i) => i > slash).map((i) => i - slash - 1),
        ),
      ),
      dir
        ? h(
            "span",
            { class: "dir" },
            highlight(
              dir,
              hits.filter((i) => i <= slash),
            ),
          )
        : null,
    ),
    f.binary || f.status === "?" ? null : oldCounts(f.add, f.del),
  );
}

function oldShell(srcs: Child[], count: string, list: Child[], head: Child[], body: Child[]): HTMLElement {
  return h(
    "div",
    { class: "files diff" },
    h(
      "div",
      { class: "file-pane" },
      h("div", { class: "diff-sources", role: "listbox", "aria-label": "Changes" }, ...srcs),
      h(
        "label",
        { class: "file-search" },
        icon(icons.search),
        h("input", {
          type: "search",
          placeholder: "Search changed files…",
          "aria-label": "Search changed files",
          autocomplete: "off",
          spellcheck: "false",
        }),
        h("span", { class: "files-count" }, count),
      ),
      h("div", { class: "file-groups", role: "listbox", "aria-label": "Changed files" }, ...list),
    ),
    h(
      "section",
      { class: "viewer", "aria-live": "polite" },
      h("div", { class: "viewer-head" }, ...head),
      h("div", { class: "viewer-body diff-body" }, ...body),
    ),
  );
}

function oldHead(f: DiffFile, layout: "inline" | "split" | null): Node[] {
  const slash = f.path.lastIndexOf("/");
  const noLines = f.binary || f.status === "?";
  return [
    h(
      "div",
      { class: "viewer-title" },
      h(
        "h2",
        { title: f.path },
        slash >= 0 ? h("span", { class: "dir" }, f.path.slice(0, slash + 1)) : null,
        f.path.slice(slash + 1),
      ),
      h(
        "p",
        null,
        statusName[f.status] ?? f.status,
        f.old_path ? ` from ${f.old_path}` : "",
        noLines ? null : [" · ", oldCounts(f.add, f.del)],
      ),
    ),
    ...(layout === null
      ? []
      : [
          h(
            "div",
            { class: "view-switch diff-layout", role: "group", "aria-label": "Diff layout" },
            unkeyed(
              (
                [
                  ["inline", "Inline", "Removed and added lines in one column"],
                  ["split", "Side by side", "The old file on the left, the new one on the right"],
                ] as const
              ).map(([v, label, tip]) =>
                h(
                  "button",
                  {
                    type: "button",
                    class: v === layout ? "active" : undefined,
                    "aria-pressed": String(v === layout),
                    title: tip,
                  },
                  label,
                ),
              ),
            ),
          ),
        ]),
    h(
      "button",
      { class: "icon-btn", title: "Copy the file path", "aria-label": "Copy the file path" },
      icon(icons.copy),
    ),
  ];
}

// What renderPieces' fragment put in a cell: text, and a span per token.
const paint = (pieces: Piece[]) => unkeyed(pieces.map(([k, t]) => (k ? h("span", { class: `hl-${k}` }, t) : t)));

function oldColor(lines: DiffLine[], lang: string): (Piece[] | null)[] {
  if (!lang) return lines.map(() => null);
  let oldState: State | null = null;
  let newState: State | null = null;
  return lines.map((l) => {
    if (l.kind === "hunk") {
      oldState = newState = null;
      return null;
    }
    if (l.kind === "note") return null;
    let r;
    if (l.kind === "del") ({ state: oldState, ...r } = tokenizeLine(lang, l.text, oldState));
    else {
      ({ state: newState, ...r } = tokenizeLine(lang, l.text, newState));
      if (l.kind === "ctx") oldState = tokenizeLine(lang, l.text, oldState).state;
    }
    return r.pieces.length ? r.pieces : null;
  });
}

function oldTable(lines: DiffLine[], path: string, layout: "inline" | "split", total: number): HTMLElement {
  const code = oldColor(lines, dataset["code"] === "off" ? "" : languageOf(path));
  let table: HTMLElement;
  if (layout === "split") {
    const map = new Map(lines.map((l, i) => [l, code[i] ?? null]));
    const side = (l: DiffLine | null, num: number | undefined): [HTMLElement, HTMLElement] => {
      if (!l) return [h("td", { class: "ln blank" }), h("td", { class: "code blank" })];
      const colored = map.get(l);
      const text = colored ? paint(colored) : l.text || " ";
      return [
        h("td", { class: `ln ${l.kind}` }, num === undefined ? "" : String(num)),
        h("td", { class: `code ${l.kind}` }, text),
      ];
    };
    table = h(
      "table",
      null,
      h("colgroup", null, h("col", { class: "ln-col" }), h("col"), h("col", { class: "ln-col" }), h("col")),
      h(
        "tbody",
        null,
        unkeyed(
          sideBySide(lines).map((r) =>
            "kind" in r
              ? h("tr", { class: `d-${r.kind}` }, h("td", { colspan: "4", class: "code" }, r.text))
              : h("tr", null, side(r.left, r.left?.old), side(r.right, r.right?.new)),
          ),
        ),
      ),
    );
  } else {
    table = h(
      "table",
      null,
      h(
        "tbody",
        null,
        unkeyed(
          lines.map((l, i) =>
            l.kind === "hunk" || l.kind === "note"
              ? h("tr", { class: `d-${l.kind}` }, h("td", { colspan: "3", class: "code" }, l.text))
              : h(
                  "tr",
                  { class: `d-${l.kind}` },
                  h("td", { class: "ln" }, l.old === undefined ? "" : String(l.old)),
                  h("td", { class: "ln" }, l.new === undefined ? "" : String(l.new)),
                  h("td", { class: "code" }, code[i] ? paint(code[i]) : l.text || " "),
                ),
          ),
        ),
      ),
    );
  }
  return h(
    "div",
    null,
    h("div", { class: `diff-code ${layout}` }, table),
    lines.length < total ? h("button", { class: "btn ghost diff-more" }, `Show all ${total} lines`) : null,
  );
}

// ---------------------------------------------------------------------------

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
const appLines = () => parseSection(splitPatch(appPatch).get("src/app.ts")!);
const sorted = [...appFiles].sort((a, b) => a.path.localeCompare(b.path));

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
  const reading = (cls: string) => [h("p", { class: cls }, "Reading the changes…")];
  expect(dump(fake(v.el))).toBe(dump(fake(oldShell(reading("file-empty"), "", [], [], reading("muted")))));
  expect(observed).toBe(child(v.el, 1)); // the viewer's width picks the layout

  const w = await opened();
  const lines = appLines();
  const want = oldShell(
    [oldSource(task, true), oldSource(checkout, false)],
    "3",
    sorted.map((f) => oldRow(f, [], f.path === "src/app.ts")),
    oldHead(appFiles[0]!, "inline"),
    [oldTable(lines, "src/app.ts", "inline", lines.length)],
  );
  expect(dump(fake(w.el))).toBe(dump(fake(want)));
});

test("side by side, with highlighting: both cells of a context line show its colored code", async () => {
  dataset["code"] = "on";
  const v = await opened();
  dispatch(child(v.el, 1, 0, 1, 1), "click"); // Side by side
  const lines = appLines();
  expect(dump(child(v.el, 1, 1))).toBe(
    dump(fake(h("div", { class: "viewer-body diff-body" }, oldTable(lines, "src/app.ts", "split", lines.length)))),
  );
  expect(dump(child(v.el, 1, 0, 1))).toBe(dump(fake(oldHead(appFiles[0]!, "split")[1]!)));
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
  expect(dump(child(v.el, 0, 2, 0))).toBe(dump(fake(oldRow(appFiles[0]!, fuzzy("app", "src/app.ts")!.hits, true))));
  dispatch(search, "keydown", { key: "Enter" });
  expect(went).toEqual([{ diffFile: "src/app.ts", source: "t1" }]);
  search["value"] = "zzz";
  dispatch(search, "input");
  expect(dump(child(v.el, 0, 2, 0))).toBe(
    dump(fake(h("p", { class: "file-empty" }, "No changed file matches “zzz”."))),
  );
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
    dump(
      fake(
        h(
          "section",
          { class: "viewer", "aria-live": "polite" },
          h("div", { class: "viewer-head" }, ...oldHead(appFiles[2]!, null)),
          h(
            "div",
            { class: "viewer-body diff-body" },
            h("p", { class: "muted" }, "Untracked file: git has no diff for it until it is added."),
          ),
        ),
      ),
    ),
  );
  v.update("co", "");
  expect(dump(child(v.el, 1))).toBe(
    dump(
      fake(
        h(
          "section",
          { class: "viewer", "aria-live": "polite" },
          h(
            "div",
            { class: "viewer-head" },
            h("div", { class: "viewer-title" }, h("h2", { class: "plain" }, "checkout")),
          ),
          h(
            "div",
            { class: "viewer-body diff-body" },
            h("p", { class: "error" }, "not a git folder"),
            h("p", { class: "muted" }, "Folder: /w"),
          ),
        ),
      ),
    ),
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
  const lines = parseSection(splitPatch(big).get("gen.txt")!);
  expect(dump(child(v.el, 1, 1, 0))).toBe(
    dump(fake(oldTable(lines.slice(0, 3000), "gen.txt", "inline", lines.length))),
  );
  const tbody = child(v.el, 1, 1, 0, 0, 0, 0);
  const first = elements(tbody);
  resetOps();
  dispatch(child(v.el, 1, 1, 0, 1), "click"); // Show all
  expect(dump(child(v.el, 1, 1, 0))).toBe(dump(fake(oldTable(lines, "gen.txt", "inline", lines.length))));
  const all = elements(tbody);
  for (let i = 0; i < first.length; i++) expect(all[i]).toBe(first[i]!);
  expect(ops.moves + ops.removes).toBe(1); // only the button goes
});
