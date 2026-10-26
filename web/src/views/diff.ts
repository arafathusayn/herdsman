// The Changes tab: what each task's worktree or branch changed against its
// base branch, any other worktree an agent works in, and the project
// checkout's own changes. Built once per route; it polls while shown, and a
// poll that finds nothing new costs a 304. A changed diff patches the drawn
// lines by their identity, so rows that stay keep their nodes and the
// scroll position holds.
import { AuthError, routeDiff } from "../api.ts";
import { highlighting } from "../appearance.ts";
import { type DiffLine, parseSection, sideBySide, splitPatch } from "../diff.ts";
import { icon, type IconNode, icons, mountElement } from "../dom.ts";
import { fuzzy, highlight, type HighlightNode } from "../fuzzy.ts";
import {
  attr,
  child,
  cls,
  html,
  list,
  type ListNode,
  on,
  renderDirect,
  runs,
  type RunsNode,
  text,
  type ViewNodeOf,
} from "../herdsman-blocks-view.ts";
import { languageOf, type Piece, type State, tokenizeLine } from "../highlight.ts";
import { store } from "../store.ts";
import type { DiffFile, DiffSource } from "../types.ts";
import { toast } from "../ui.ts";

const pollMs = 5000;
/** Rows drawn before "Show all"; a generated file can have tens of thousands. */
const rowCap = 3000;

type Layout = "inline" | "split";

const layoutKey = "herdsman.diff-layout";
/** A viewer narrower than this has no room for two columns of code: the diff shows inline whatever the choice. */
const splitMin = 640;

function loadLayout(): Layout {
  try {
    return localStorage.getItem(layoutKey) === "split" ? "split" : "inline";
  } catch {
    return "inline";
  }
}

let layout = loadLayout();

const statusName: Record<string, string> = {
  A: "Added",
  M: "Modified",
  D: "Deleted",
  R: "Renamed",
  C: "Copied",
  T: "Type changed",
  "?": "Untracked",
};

function totals(files: DiffFile[]): { add: number; del: number } {
  let add = 0;
  let del = 0;
  for (const f of files) {
    add += f.add;
    del += f.del;
  }
  return { add, del };
}

const Counts = html`<span class="counts"
  ><span class="plus">${text()}</span><span class="minus">${text()}</span></span
>`;
type CountsNode = ViewNodeOf<typeof Counts>;

function counts(add: number, del: number): CountsNode {
  return Counts([`+${add}`, `−${del}`]);
}

// A highlighted token, as highlight.ts's renderPieces makes it.
const Token = html`<span class=${cls()}>${text()}</span>`;
/** A line's code as runs of text and token spans. */
type ColoredNode = RunsNode<ViewNodeOf<typeof Token>>;

/**
 * Highlights the code of diff lines. The old and new sides each carry their
 * own state (a comment opened in a removed line does not color added ones),
 * and a hunk starts both afresh, since the lines between hunks are unknown.
 */
function colorLines(lines: DiffLine[], lang: string): (ColoredNode | null)[] {
  if (!lang) return lines.map(() => null);
  let oldState: State | null = null;
  let newState: State | null = null;
  return lines.map((l) => {
    if (l.kind === "hunk") {
      oldState = newState = null;
      return null;
    }
    if (l.kind === "note") return null;
    let pieces: Piece[];
    if (l.kind === "del") ({ pieces, state: oldState } = tokenizeLine(lang, l.text, oldState));
    else {
      ({ pieces, state: newState } = tokenizeLine(lang, l.text, newState));
      if (l.kind === "ctx") oldState = tokenizeLine(lang, l.text, oldState).state;
    }
    return pieces.length ? runs(pieces.map(([k, t]) => (k ? Token([`hl-${k}`, t]) : t))) : null;
  });
}

/**
 * A line's identity within its file's diff: its kind and line numbers, or a
 * hunk's header. A note ("\ No newline at end of file", or a binary file's)
 * has neither, so it is known by the line before it.
 */
function lineKey(l: DiffLine, before: string): string {
  if (l.kind === "hunk") return `@${l.text}`;
  if (l.kind === "note") return `${before}!`;
  return `${l.kind}:${l.old ?? ""}:${l.new ?? ""}`;
}

// Descriptions are inert, so the icons that never change are made once.
const searchIcon = icon(icons.search);
const copyIcon = icon(icons.copy);
const folderIcon = icon(icons.folder);
const branchIcon = icon(icons.branch);

const SearchInput = html`<input
  type="search"
  placeholder="Search changed files…"
  aria-label="Search changed files"
  autocomplete="off"
  spellcheck="false"
  oninput=${on<"input", HTMLInputElement>()}
  onkeydown=${on<"keydown", HTMLInputElement>()}
/>`;

const Empty = html`<p class="file-empty">${text()}</p>`;
type EmptyNode = ViewNodeOf<typeof Empty>;

const Unavailable = html`<span class="chip warn">unavailable</span>`;
const Who = html`<span class="who">${text()}</span>`;
const Agents = html`<span class="diff-source-agents" title="Agents working in this folder"
  >${child<ListNode<ViewNodeOf<typeof Who>, string>>()}</span
>`;
const FileCount = html`<span class="n">${text()}</span>`;
const Source = html`<button
  class=${cls()}
  role="option"
  aria-selected=${attr<"true" | "false">()}
  title=${attr()}
  onclick=${on<"click">()}
>
  <span class="diff-source-top"><strong>${text()}</strong>${child<ViewNodeOf<typeof Unavailable> | CountsNode>()}</span
  >${child<ViewNodeOf<typeof Agents> | null>()}<span class="diff-source-sub"
    >${child<IconNode>()}${text()}${child<ViewNodeOf<typeof FileCount> | null>()}</span
  >
</button>`;
type SourceNode = ViewNodeOf<typeof Source>;

// A row's name and folder, with the search's hits marked.
const Name = html`<span class="name">${child<HighlightNode>()}</span>`;
const Dir = html`<span class="dir">${child<HighlightNode>()}</span>`;
type NameNode = ViewNodeOf<typeof Name>;
const PathDir = html`<span class="dir">${text()}</span>`;

const Row = html`<button
  class=${cls()}
  role="option"
  aria-selected=${attr<"true" | "false">()}
  title=${attr()}
  onclick=${on<"click">()}
>
  <span class=${cls()} title=${attr()}>${text()}</span
  ><span class="file-name">${child<NameNode>()}${child<NameNode | null>()}</span>${child<CountsNode | null>()}
</button>`;
type RowNode = ViewNodeOf<typeof Row>;

const PlainTitle = html`<div class="viewer-title"><h2 class="plain">${text()}</h2></div>`;
const FileTitle = html`<div class="viewer-title">
  <h2 title=${attr()}>${child<ViewNodeOf<typeof PathDir> | null>()}${text()}</h2>
  <p>${text()}${text()}${text<" · " | "">()}${child<CountsNode | null>()}</p>
</div>`;

const layouts: readonly { id: Layout; label: string; tip: string }[] = [
  { id: "inline", label: "Inline", tip: "Removed and added lines in one column" },
  { id: "split", label: "Side by side", tip: "The old file on the left, the new one on the right" },
];
const LayoutButton = html`<button
  type="button"
  class=${cls()}
  aria-pressed=${attr<"true" | "false">()}
  title=${attr()}
  onclick=${on<"click">()}
>
  ${text()}
</button>`;
const LayoutSwitch = html`<div class="view-switch diff-layout" role="group" aria-label="Diff layout">
  ${child<ListNode<ViewNodeOf<typeof LayoutButton>, Layout>>()}
</div>`;

const Copy = html`<button
  class="icon-btn"
  title="Copy the file path"
  aria-label="Copy the file path"
  onclick=${on<"click">()}
>
  ${child<IconNode>()}
</button>`;

type Head = [
  ViewNodeOf<typeof PlainTitle> | ViewNodeOf<typeof FileTitle> | null,
  ViewNodeOf<typeof LayoutSwitch> | null,
  ViewNodeOf<typeof Copy> | null,
];

// A hunk header or a note across the row: three columns inline, four side by side.
const MarkRow = html`<tr class=${cls()}>
  <td colspan=${attr<"3" | "4">()} class="code">${text()}</td>
</tr>`;
type MarkRowNode = ViewNodeOf<typeof MarkRow>;
const CodeCell = html`<td class=${cls()}>${child<string | ColoredNode | null>()}</td>`;
type Code = ViewNodeOf<typeof CodeCell>;
const InlineRow = html`<tr class=${cls()}>
  <td class="ln">${text<number | null>()}</td>
  <td class="ln">${text<number | null>()}</td>
  ${child<Code>()}
</tr>`;
type InlineRowNode = ViewNodeOf<typeof InlineRow>;
const SplitRow = html`<tr>
  <td class=${cls()}>${text<number | null>()}</td>
  ${child<Code>()}
  <td class=${cls()}>${text<number | null>()}</td>
  ${child<Code>()}
</tr>`;
type SplitRowNode = ViewNodeOf<typeof SplitRow>;

const InlineTable = html`<table>
  <tbody>
    ${child<ListNode<MarkRowNode | InlineRowNode, string>>()}
  </tbody>
</table>`;
const SplitTable = html`<table>
  <colgroup>
    <col class="ln-col" />
    <col />
    <col class="ln-col" />
    <col />
  </colgroup>
  <tbody>
    ${child<ListNode<MarkRowNode | SplitRowNode, string>>()}
  </tbody>
</table>`;
const More = html`<button class="btn ghost diff-more" onclick=${on<"click">()}>${text()}</button>`;
const Table = html`<div>
  <div class=${cls()}>${child<ViewNodeOf<typeof InlineTable> | ViewNodeOf<typeof SplitTable>>()}</div>
  ${child<ViewNodeOf<typeof More> | null>()}
</div>`;

const Muted = html`<p class="muted">${text()}</p>`;
const Failure = html`<p class="error">${text()}</p>`;
const Cut = html`<p class="diff-cut" role="note">${text()}</p>`;
const cutNote =
  "The diff reached its size cut in this file, so lines past this point are not shown. Open the file in the worktree to see all of it.";
type Body = [
  ViewNodeOf<typeof Muted> | ViewNodeOf<typeof Failure> | ViewNodeOf<typeof Cut> | null,
  ViewNodeOf<typeof Muted> | ViewNodeOf<typeof Table> | null,
];

const Shell = html`<div class="files diff">
  <div class="file-pane">
    <div class="diff-sources" role="listbox" aria-label="Changes">
      ${child<ListNode<SourceNode, string> | EmptyNode>()}
    </div>
    <label class="file-search"
      >${child<IconNode>()}${child<HTMLInputElement>()}<span class="files-count">${text()}</span></label
    >
    <div class="file-groups" role="listbox" aria-label="Changed files">
      ${child<ListNode<RowNode, string> | EmptyNode | null>()}
    </div>
  </div>
  <section class="viewer" aria-live="polite">
    <div class="viewer-head">${child<Head[0]>()}${child<Head[1]>()}${child<Head[2]>()}</div>
    <div class="viewer-body diff-body">${child<Body[0]>()}${child<Body[1]>()}</div>
  </section>
</div>`;

// Checked narrowings, not casts: the shell's root is a <div>, its last child the viewer <section>.
const isDiv = (n: Node | null): n is HTMLDivElement => n !== null && n.nodeType === 1 && n.nodeName === "DIV";
const isElement = (n: Node | null): n is Element => n !== null && n.nodeType === 1;

export class DiffView {
  readonly el: HTMLElement;
  private readonly search: HTMLInputElement;
  // The shell renders into this holder, which stays out of the document; its
  // root, the element main.ts places, is `el`. Later renders patch it there.
  // The root moves out of the holder, so it renders with renderDirect:
  // delegated handlers would listen on the holder, which no event reaches.
  private readonly holder = document.createElement("div");
  /** The viewer is too narrow for side by side. */
  private narrow = false;
  private sources: DiffSource[] | null = null;
  private error = "";
  private sourceId = "";
  private file = "";
  private sections = new Map<string, string[]>();
  private sectionsOf: DiffSource | null = null;
  private sorted: DiffFile[] = [];
  private sortedOf: DiffSource | null = null;
  /** The files the list shows, in order; Enter opens the first. */
  private listed: DiffFile[] = [];
  private count = "";
  private drawnKey = "";
  private drawnSource: DiffSource | null = null;
  private head: Head = [null, null, null];
  private body: Body = [null, null];
  private showAll = false;
  private timer = 0;
  private loading = false;

  constructor(readonly routeId: string) {
    this.search = mountElement("input", SearchInput, [
      () => this.draw(),
      (e) => {
        if (e.key === "Escape") {
          if (this.search.value) {
            e.preventDefault();
            this.search.value = "";
            this.draw();
          } else this.search.blur();
        } else if (e.key === "Enter") {
          const first = this.listed[0];
          if (first) this.choose(first);
        }
      },
    ]);
    this.draw();
    const root = this.holder.firstChild;
    if (!isDiv(root)) throw new Error("DiffView: the shell rendered no root");
    this.el = root;
    const viewer = root.lastChild;
    if (!isElement(viewer)) throw new Error("DiffView: the shell has no viewer");
    new ResizeObserver(([e]) => {
      const narrow = (e?.contentRect.width ?? 0) < splitMin;
      if (narrow === this.narrow) return;
      this.narrow = narrow;
      this.draw();
    }).observe(viewer);
  }

  /** The layout drawn: the user's choice, or inline in a narrow viewer. */
  private layout(): Layout {
    return this.narrow ? "inline" : layout;
  }

  /** Starts polling; the tab calls it each time it shows this view. */
  start(): void {
    if (this.timer) return;
    void this.load();
    this.timer = window.setInterval(() => {
      if (document.visibilityState === "visible") void this.load();
    }, pollMs);
  }

  stop(): void {
    window.clearInterval(this.timer);
    this.timer = 0;
  }

  focusSearch(): void {
    this.search.focus();
    this.search.select();
  }

  update(source: string, file: string): void {
    if (source !== this.sourceId || file !== this.file) {
      this.sourceId = source;
      this.file = file;
      this.showAll = false;
    }
    // A store update can also be the highlighting setting; the viewer's key
    // holds it, so the viewer redraws only when something it shows changed.
    this.draw();
  }

  private async load(): Promise<void> {
    if (this.loading) return;
    this.loading = true;
    try {
      const next = await routeDiff(this.routeId);
      if (next !== this.sources || this.error) {
        this.sources = next;
        this.error = "";
        this.draw();
      }
    } catch (e) {
      if (e instanceof AuthError) location.reload();
      this.error = e instanceof Error ? e.message : String(e);
      this.draw();
    } finally {
      this.loading = false;
    }
  }

  /** The open source: the one in the URL, else the first with changes. */
  private source(): DiffSource | null {
    const all = this.sources ?? [];
    return (
      all.find((s) => s.id === this.sourceId) ?? all.find((s) => (s.diff?.files?.length ?? 0) > 0) ?? all[0] ?? null
    );
  }

  /** The open source's files, by path. */
  private files(): DiffFile[] {
    const src = this.source();
    if (src !== this.sortedOf) {
      this.sorted = [...(src?.diff?.files ?? [])].sort((a, b) => a.path.localeCompare(b.path));
      this.sortedOf = src;
    }
    return this.sorted;
  }

  private selected(): DiffFile | null {
    const files = this.files();
    return files.find((f) => f.path === this.file) ?? files[0] ?? null;
  }

  private choose(f: DiffFile): void {
    store.go({ diffFile: f.path, source: this.source()?.id ?? "" }, false);
  }

  private draw(): void {
    const files = this.fileList(); // sets the count
    this.syncViewer();
    renderDirect(
      this.holder,
      Shell([this.sourceList(), searchIcon, this.search, this.count, files, ...this.head, ...this.body]),
    );
  }

  private sourceList(): ListNode<SourceNode, string> | EmptyNode {
    const all = this.sources;
    if (!all) return Empty([this.error || "Reading the changes…"]);
    const cur = this.source();
    return list(
      all,
      (s) => s.id,
      (s) => {
        const files = s.diff?.files ?? [];
        const t = totals(files);
        const active = s.id === cur?.id;
        return Source([
          active ? "diff-source active" : "diff-source",
          active ? "true" : "false",
          s.dir ?? s.label,
          () => store.go({ source: s.id, diffFile: "" }, false),
          s.label,
          s.error ? Unavailable([]) : counts(t.add, t.del),
          s.agents?.length
            ? Agents([
                list(
                  [...new Set(s.agents)], // the server lists each once; keys must be unique
                  (a) => a,
                  (a) => Who([a]),
                ),
              ])
            : null,
          s.kind === "checkout" ? folderIcon : branchIcon,
          s.diff ? `${s.diff.head} ← ${s.diff.base}` : (s.branch ?? s.dir ?? ""),
          s.diff ? FileCount([`${files.length} ${files.length === 1 ? "file" : "files"}`]) : null,
        ]);
      },
    );
  }

  private row(f: DiffFile, hits: number[], active: boolean): RowNode {
    const slash = f.path.lastIndexOf("/");
    const name = f.path.slice(slash + 1);
    const dir = slash < 0 ? "" : f.path.slice(0, slash + 1);
    return Row([
      active ? "file-row active" : "file-row",
      active ? "true" : "false",
      f.old_path ? `${f.old_path} → ${f.path}` : f.path,
      () => this.choose(f),
      `st st-${f.status === "?" ? "u" : f.status}`,
      statusName[f.status] ?? f.status,
      f.status,
      Name([
        highlight(
          name,
          hits.filter((i) => i > slash).map((i) => i - slash - 1),
        ),
      ]),
      dir
        ? Dir([
            highlight(
              dir,
              hits.filter((i) => i <= slash),
            ),
          ])
        : null,
      f.binary || f.status === "?" ? null : counts(f.add, f.del),
    ]);
  }

  private fileList(): ListNode<RowNode, string> | EmptyNode | null {
    const src = this.source();
    const files = this.files();
    if (!src || src.error || !files.length) {
      this.count = "";
      this.listed = [];
      return src && !src.error ? Empty(["No changed files."]) : null;
    }
    const open = this.selected()?.path;
    const q = this.search.value.trim();
    if (!q) {
      this.count = String(files.length);
      this.listed = files;
      return list(
        files,
        (f) => f.path,
        (f) => this.row(f, [], f.path === open),
      );
    }
    const matches = files
      .map((f) => ({ f, m: fuzzy(q, f.path) }))
      .filter((x) => x.m)
      .sort((a, b) => (b.m?.score ?? 0) - (a.m?.score ?? 0));
    this.count = `${matches.length}/${files.length}`;
    this.listed = matches.map((x) => x.f);
    return matches.length
      ? list(
          matches,
          (x) => x.f.path,
          (x) => this.row(x.f, x.m?.hits ?? [], x.f.path === open),
        )
      : Empty([`No changed file matches “${q}”.`]);
  }

  /** Rebuilds the viewer's head and body when what they show changed. */
  private syncViewer(): void {
    const src = this.source();
    const f = this.selected();
    // A new diff is a new source object (an unchanged one comes back from the
    // ETag cache as the same object), so any new patch redraws.
    const key = `${highlighting()}|${layout}|${this.narrow}|${src?.id}|${f?.path}|${this.showAll}|${this.sources === null}|${this.error}`;
    if (key === this.drawnKey && src === this.drawnSource) return;
    this.drawnKey = key;
    this.drawnSource = src;
    if (!src) {
      this.head = [null, null, null];
      this.body = [Muted([this.error || "Reading the changes…"]), null];
      return;
    }
    if (src.error || !src.diff) {
      this.head = [PlainTitle([src.label]), null, null];
      this.body = [Failure([src.error ?? "No diff."]), src.dir ? Muted([`Folder: ${src.dir}`]) : null];
      return;
    }
    if (!f) {
      this.head = [PlainTitle([src.label]), null, null];
      this.body = [Muted([`No changes against ${src.diff.base}.`]), null];
      return;
    }
    const slash = f.path.lastIndexOf("/");
    const noLines = f.binary || f.status === "?";
    this.head = [
      FileTitle([
        f.path,
        slash >= 0 ? PathDir([f.path.slice(0, slash + 1)]) : null,
        f.path.slice(slash + 1),
        statusName[f.status] ?? f.status,
        f.old_path ? ` from ${f.old_path}` : "",
        noLines ? "" : " · ",
        noLines ? null : counts(f.add, f.del),
      ]),
      noLines || this.narrow ? null : this.layoutSwitch(),
      Copy([
        async () => {
          try {
            await navigator.clipboard.writeText(src.dir ? `${src.dir}/${f.path}` : f.path);
            toast("Copied the file path.");
          } catch {
            toast("The browser blocked the clipboard.", "error");
          }
        },
        copyIcon,
      ]),
    ];
    if (this.sectionsOf !== src) {
      this.sections = splitPatch(src.diff.patch);
      this.sectionsOf = src;
    }
    if (f.status === "?") {
      this.body = [Muted(["Untracked file: git has no diff for it until it is added."]), null];
      return;
    }
    const section = this.sections.get(f.path);
    const lines = section ? parseSection(section) : [];
    if (!lines.length) {
      this.body = [
        Muted([
          !section && src.diff.truncated
            ? "This file is past the size cut of the diff; open it in the worktree to see its lines."
            : f.binary
              ? "Binary file; no lines to show."
              : f.old_path
                ? `Moved from ${f.old_path} with no line changes.`
                : "No line changes; only the file mode changed.",
        ]),
        null,
      ];
      return;
    }
    // The size cut stops the patch inside its last section, so that file may show only part of its lines.
    const cut = src.diff.truncated && section === [...this.sections.values()].at(-1);
    this.body = [cut ? Cut([cutNote]) : null, this.table(lines, f.path)];
  }

  private layoutSwitch(): ViewNodeOf<typeof LayoutSwitch> {
    return LayoutSwitch([
      list(
        layouts,
        (o) => o.id,
        (o) =>
          LayoutButton([
            o.id === layout ? "active" : "",
            o.id === layout ? "true" : "false",
            o.tip,
            () => {
              layout = o.id;
              try {
                localStorage.setItem(layoutKey, o.id);
              } catch {
                // The choice lasts for this page only.
              }
              this.draw();
            },
            o.label,
          ]),
      ),
    ]);
  }

  private table(lines: DiffLine[], path: string): ViewNodeOf<typeof Table> {
    const shown = this.showAll ? lines : lines.slice(0, rowCap);
    const colored = colorLines(shown, highlighting() ? languageOf(path) : "");
    const code = new Map(shown.map((l, i) => [l, colored[i] ?? null]));
    const lay = this.layout();
    return Table([
      `diff-code ${lay}`,
      lay === "split" ? splitTable(shown, code) : inlineTable(shown, code),
      shown.length < lines.length
        ? More([
            () => {
              this.showAll = true;
              this.draw();
            },
            `Show all ${lines.length} lines`,
          ])
        : null,
    ]);
  }
}

/** A code cell: the colored code, or the plain text (a space keeps an empty line's height). */
function codeCell(className: string, l: DiffLine, colored: ColoredNode | null): Code {
  return CodeCell([className, colored ?? (l.text || " ")]);
}

/** One column: old and new line numbers, then the code. */
function inlineTable(lines: DiffLine[], code: Map<DiffLine, ColoredNode | null>): ViewNodeOf<typeof InlineTable> {
  let before = "";
  const rows = lines.map((l) => ({ l, key: (before = lineKey(l, before)) }));
  return InlineTable([
    list(
      rows,
      (r) => r.key,
      ({ l }) =>
        l.kind === "hunk" || l.kind === "note"
          ? MarkRow([`d-${l.kind}`, "3", l.text])
          : InlineRow([`d-${l.kind}`, l.old ?? null, l.new ?? null, codeCell("code", l, code.get(l) ?? null)]),
    ),
  ]);
}

/**
 * The side-by-side table: old lines on the left, new on the right. Long
 * lines wrap so both sides stay in view. A context line shows on both
 * sides, both from the same colored runs.
 */
function splitTable(lines: DiffLine[], code: Map<DiffLine, ColoredNode | null>): ViewNodeOf<typeof SplitTable> {
  let before = "";
  const rows = sideBySide(lines).map((r) => ({
    r,
    key: (before =
      "kind" in r
        ? r.kind === "hunk"
          ? `@${r.text}`
          : `${before}!`
        : `${r.left ? lineKey(r.left, "") : ""}/${r.right ? lineKey(r.right, "") : ""}`),
  }));
  const side = (l: DiffLine | null, right: boolean): [string, number | null, Code] => {
    if (!l) return ["ln blank", null, CodeCell(["code blank", null])];
    const colored = code.get(l) ?? null;
    return [`ln ${l.kind}`, (right ? l.new : l.old) ?? null, codeCell(`code ${l.kind}`, l, colored)];
  };
  return SplitTable([
    list(
      rows,
      (x) => x.key,
      ({ r }) =>
        "kind" in r
          ? MarkRow([`d-${r.kind}`, "4", r.text])
          : SplitRow([...side(r.left, false), ...side(r.right, true)]),
    ),
  ]);
}
