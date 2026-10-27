// The route's files: a quick search, files grouped by folder, and a viewer.
// Built once per route and re-rendered on state changes; the search box is an
// element the view owns, so it keeps its focus and text while the route
// updates live.
import { routeFile } from "../api.ts";
import { highlighting } from "../appearance.ts";
import { fuzzy, highlight, type HighlightNode } from "../fuzzy.ts";
import {
  attr,
  child,
  cls,
  html,
  list,
  type ListNode,
  on,
  text,
  update,
  type ViewNodeOf,
} from "../herdsman-blocks-view.ts";
import { icon, type IconNode, icons } from "../icons.ts";
import { codeBlock, type CodeNode, type MarkdownNode, renderMarkdown } from "../markdown.ts";
import { ago } from "../model.ts";
import { store } from "../store.ts";
import type { Route, RouteFile } from "../types.ts";
import { toast } from "../ui.ts";
import { element } from "./element-view.ts";

/** Folder order in the list; anything else sorts between doc/ref and probes. */
const folderRank = ["", "reports", "doc", "ref"];
const collapsedByDefault = new Set(["probes"]);

interface Match {
  file: RouteFile;
  score: number;
  hits: number[];
}

function folderOf(path: string): string {
  const i = path.indexOf("/");
  return i < 0 ? "" : path.slice(0, i);
}

function size(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

// Descriptions are inert, so the icons that never change are made once.
const searchIcon = icon(icons.search);
const fileIcon = icon(icons.file);
const folderIcon = icon(icons.folder);
const copyIcon = icon(icons.copy);

const SearchInput = html`<input
  type="search"
  placeholder="Search files…"
  aria-label="Search files"
  autocomplete="off"
  spellcheck="false"
  oninput=${on<"input", HTMLInputElement>()}
  onkeydown=${on<"keydown", HTMLInputElement>()}
/>`;

// A row's name and folder, with the search's hits marked.
const Name = html`<span class="name">${child<HighlightNode>()}</span>`;
const Dir = html`<span class="dir">${child<HighlightNode>()}</span>`;
type NameNode = ViewNodeOf<typeof Name>;

const Row = html`<button
  class=${cls()}
  role="option"
  aria-selected=${attr<"true" | "false">()}
  title=${attr()}
  onclick=${on<"click">()}
>
  ${child<IconNode>()}<span class="file-name">${child<NameNode>()}${child<NameNode | null>()}</span
  ><small>${text()}</small>
</button>`;
type RowNode = ViewNodeOf<typeof Row>;

const Group = html`<details class="file-group" open=${attr<boolean>()} ontoggle=${on<"toggle", HTMLDetailsElement>()}>
  <summary>${child<IconNode>()}<span>${text()}</span><span class="count">${text()}</span></summary>
  ${child<ListNode<RowNode, string>>()}
</details>`;
type GroupNode = ViewNodeOf<typeof Group>;

const Empty = html`<p class="file-empty">${text()}</p>`;
type EmptyNode = ViewNodeOf<typeof Empty>;

const Meta = html`<p>${text()}</p>`;
const PathDir = html`<span class="dir">${text()}</span>`;
const Title = html`<div class="viewer-title">
  <h2 title=${attr()}>${child<ViewNodeOf<typeof PathDir> | null>()}${text()}</h2>
  ${child<ViewNodeOf<typeof Meta> | null>()}
</div>`;
type TitleNode = ViewNodeOf<typeof Title>;

type Mode = "Rendered" | "Raw";
const modes: readonly Mode[] = ["Rendered", "Raw"];
const ModeButton = html`<button
  role="radio"
  title=${attr()}
  aria-checked=${attr<"true" | "false">()}
  class=${cls()}
  onclick=${on<"click">()}
>
  ${text<Mode>()}
</button>`;
const Modes = html`<div class="seg" role="radiogroup" aria-label="View">
  ${child<ListNode<ViewNodeOf<typeof ModeButton>, Mode>>()}
</div>`;
type ModesNode = ViewNodeOf<typeof Modes>;

const Copy = html`<button
  class="icon-btn"
  title="Copy the file path"
  aria-label="Copy the file path"
  onclick=${on<"click">()}
>
  ${child<IconNode>()}
</button>`;
type CopyNode = ViewNodeOf<typeof Copy>;

const Muted = html`<p class="muted">${text()}</p>`;
const Raw = html`<pre class="raw">${child<CodeNode>()}</pre>`;
const Failure = html`<p class="error">${text()}</p>`;
const Retry = html`<button type="button" class="btn" onclick=${on<"click">()}>Try again</button>`;
type RetryNode = ViewNodeOf<typeof Retry>;

/** What the viewer body shows. */
type Body = ViewNodeOf<typeof Muted> | ViewNodeOf<typeof Raw> | ViewNodeOf<typeof Failure> | MarkdownNode | null;

type ListContent = ListNode<RowNode, string> | ListNode<GroupNode, string> | EmptyNode | null;

const Shell = html`<div class="files">
  <div class="file-pane">
    <label class="file-search"
      >${child<IconNode>()}${child<HTMLInputElement>()}<span class="files-count">${text()}</span></label
    >
    <div class="file-groups" role="listbox" aria-label="Files">${child<ListContent>()}</div>
  </div>
  <section class="viewer" aria-live="polite">
    <div class="viewer-head">${child<TitleNode | null>()}${child<ModesNode | null>()}${child<CopyNode | null>()}</div>
    <div class="viewer-body">${child<Body>()}${child<RetryNode | null>()}</div>
  </section>
</div>`;

export class FilesView {
  readonly el: HTMLElement;
  private readonly search: HTMLInputElement;
  // The shell's root, the element main.ts places (`el`): mounted on the
  // first draw, patched where it stands after.
  private root: HTMLDivElement | null = null;
  private route: Route | null = null;
  private selected = "";
  private loadedKey = "";
  private text = "";
  private textKey = "";
  private raw = false;
  private active = 0;
  private results: RouteFile[] = [];
  private count = "";
  private body: Body = null;
  private retry: RetryNode | null = null;
  private readonly open = new Map<string, boolean>();

  constructor(readonly routeId: string) {
    this.search = element(
      "input",
      SearchInput([
        () => {
          this.active = 0;
          this.draw();
        },
        (e) => this.onKey(e),
      ]),
    );
    this.el = this.draw();
  }

  focusSearch(): void {
    this.search.focus();
    this.search.select();
  }

  update(route: Route, selected: string): void {
    this.route = route;
    this.selected = selected || "00-route.md";
    this.syncBody();
    this.draw();
  }

  private files(): RouteFile[] {
    return this.route?.files ?? [];
  }

  private onKey(e: KeyboardEvent): void {
    if (e.key === "Escape") {
      if (this.search.value) {
        e.preventDefault();
        this.search.value = "";
        this.draw();
      } else this.search.blur();
      return;
    }
    if (!this.results.length) return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const n = this.results.length;
      this.active = (this.active + (e.key === "ArrowDown" ? 1 : n - 1)) % n;
      this.draw();
      this.el.querySelector(".file-groups .file-row.cursor")?.scrollIntoView({ block: "nearest" });
    } else if (e.key === "Enter") {
      e.preventDefault();
      const f = this.results[this.active];
      if (f) this.choose(f.path);
    }
  }

  private choose(path: string): void {
    store.go({ file: path }, false);
  }

  private draw(): HTMLDivElement {
    const items = this.listContent(); // sets the count
    const d = Shell([searchIcon, this.search, this.count, items, ...this.head(), this.body, this.retry]);
    if (this.root === null) this.root = element("div", d);
    else update(this.root, d);
    return this.root;
  }

  private row(f: RouteFile, hits: number[], cursor: boolean, showFolder: boolean): RowNode {
    const slash = f.path.lastIndexOf("/");
    const name = f.path.slice(slash + 1);
    const dir = slash < 0 ? "" : f.path.slice(0, slash + 1);
    const nameHits = hits.filter((i) => i > slash).map((i) => i - slash - 1);
    const dirHits = hits.filter((i) => i <= slash);
    const active = f.path === this.selected;
    return Row([
      ["file-row", active ? "active" : "", cursor ? "cursor" : ""].filter(Boolean).join(" "),
      active ? "true" : "false",
      f.path,
      () => this.choose(f.path),
      fileIcon,
      Name([highlight(name, nameHits)]),
      showFolder && dir ? Dir([highlight(dir, dirHits)]) : null,
      ago(f.modified),
    ]);
  }

  private listContent(): ListContent {
    if (!this.route) return null; // nothing until the first update
    const files = this.files();
    const q = this.search.value.trim();
    if (q) {
      const matches: Match[] = [];
      for (const file of files) {
        const m = fuzzy(q, file.path);
        if (m) matches.push({ file, ...m });
      }
      matches.sort((a, b) => b.score - a.score);
      this.results = matches.map((m) => m.file);
      this.active = Math.min(this.active, Math.max(0, matches.length - 1));
      this.count = `${matches.length}/${files.length}`;
      const cursor = this.results[this.active]?.path;
      return matches.length
        ? list(
            matches,
            (m) => m.file.path,
            (m) => this.row(m.file, m.hits, m.file.path === cursor, true),
          )
        : Empty([`No file matches “${q}”.`]);
    }
    this.results = [];
    this.count = String(files.length);
    const groups = new Map<string, RouteFile[]>();
    for (const f of files) {
      const d = folderOf(f.path);
      groups.set(d, [...(groups.get(d) ?? []), f]);
    }
    const rank = (d: string) => {
      const i = folderRank.indexOf(d);
      return i >= 0 ? i : collapsedByDefault.has(d) ? 99 : 50;
    };
    const order = [...groups.keys()].sort((a, b) => rank(a) - rank(b) || a.localeCompare(b));
    const open = this.open;
    return list(
      order,
      (d) => d,
      (d) => {
        const items = (groups.get(d) ?? []).sort((a, b) => a.path.localeCompare(b.path));
        const holdsSelected = items.some((f) => f.path === this.selected);
        return Group([
          open.get(d) ?? (holdsSelected || !collapsedByDefault.has(d)),
          function (this: HTMLDetailsElement) {
            open.set(d, this.open);
          },
          folderIcon,
          d ? `${d}/` : "Route files",
          String(items.length),
          list(
            items,
            (f) => f.path,
            (f) => this.row(f, [], false, false),
          ),
        ]);
      },
    );
  }

  private head(): [TitleNode | null, ModesNode | null, CopyNode | null] {
    const r = this.route;
    if (!r) return [null, null, null];
    const f = this.files().find((x) => x.path === this.selected);
    const slash = this.selected.lastIndexOf("/");
    return [
      Title([
        this.selected,
        slash >= 0 ? PathDir([this.selected.slice(0, slash + 1)]) : null,
        this.selected.slice(slash + 1),
        f ? Meta([`${size(f.size)} · ${ago(f.modified)}`]) : null,
      ]),
      Modes([
        list(
          modes,
          (m) => m,
          (m) =>
            ModeButton([
              m === "Raw" ? "Show the Markdown source" : "Show the file formatted",
              this.raw === (m === "Raw") ? "true" : "false",
              this.raw === (m === "Raw") ? "active" : "",
              () => {
                this.raw = m === "Raw";
                this.loadedKey = "";
                this.syncBody();
                this.draw();
              },
              m,
            ]),
        ),
      ]),
      Copy([
        async () => {
          try {
            await navigator.clipboard.writeText(`${r.dir}/${this.selected}`);
            toast("Copied the file path.");
          } catch {
            toast("The browser blocked the clipboard.", "error");
          }
        },
        copyIcon,
      ]),
    ];
  }

  /** The text, as markdown or as raw source. */
  private shown(): Body {
    return this.raw ? Raw([codeBlock(this.text.split("\n"), "md")]) : renderMarkdown(this.text);
  }

  /** Loads the open file into the body when it, its version or the way it shows changed. */
  private syncBody(): void {
    const r = this.route;
    if (!r) return;
    const f = this.files().find((x) => x.path === this.selected);
    const fileKey = `${r.id}/${this.selected}/${f?.modified ?? ""}`;
    const key = `${fileKey}/${this.raw}/${highlighting()}`;
    if (key === this.loadedKey) return;
    this.loadedKey = key;
    this.retry = null;
    if (fileKey === this.textKey) {
      this.body = this.shown(); // only the Rendered/Raw switch changed
      return;
    }
    this.body = Muted(["Loading…"]);
    routeFile(r.id, this.selected)
      .then((text) => {
        if (this.loadedKey !== key) return;
        this.text = text;
        this.textKey = fileKey;
        this.body = this.shown();
        this.draw();
      })
      .catch((e: unknown) => {
        if (this.loadedKey !== key) return;
        // Forget the failed load, so the next update or a click tries again.
        this.loadedKey = "";
        this.body = Failure([e instanceof Error ? e.message : String(e)]);
        this.retry = Retry([
          () => {
            this.loadedKey = "";
            this.syncBody();
            this.draw();
          },
        ]);
        this.draw();
      });
  }
}
