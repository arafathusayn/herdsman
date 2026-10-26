// Artifacts: the HTML pages agents leave in a route's workspace folders
// (reports, review pages, plans), listed like the Docs tab and shown in a
// sandboxed frame. The list refreshes while the tab shows. A view belongs to
// one route, so it never shows another route's list.
import { listArtifacts, openArtifact } from "../api.ts";
import { h, icon, type IconNode, icons, mountElement } from "../dom.ts";
import { fuzzy, highlight } from "../fuzzy.ts";
import {
  attr,
  child,
  cls,
  html,
  list,
  type ListNode,
  on,
  render,
  text,
  type ViewNodeOf,
} from "../herdsman-blocks-view.ts";
import { ago } from "../model.ts";
import { store } from "../store.ts";
import type { Artifact } from "../types.ts";
import { toast } from "../ui.ts";

const refreshEvery = 5000;

/** An artifact's full path, the key the URL keeps. */
export const artifactPath = (a: Artifact): string => `${a.root}/${a.path}`;

/** A workspace folder as the list names it: its last two parts. */
export function rootLabel(root: string): string {
  const parts = root.split("/").filter(Boolean);
  return parts.slice(-2).join("/") || "/";
}

function size(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

// Descriptions are inert, so the icons that never change are made once.
const searchIcon = icon(icons.search);
const artifactIcon = icon(icons.artifact);
const folderIcon = icon(icons.folder);
const refreshIcon = icon(icons.refresh);
const externalIcon = icon(icons.external);
const copyIcon = icon(icons.copy);

const SearchInput = html`<input
  type="search"
  placeholder="Search pages…"
  aria-label="Search artifacts"
  autocomplete="off"
  spellcheck="false"
  oninput=${on<"input", HTMLInputElement>()}
  onkeydown=${on<"keydown", HTMLInputElement>()}
/>`;

const Name = html`<span class="name">${text()}</span>`;
type NameNode = ViewNodeOf<typeof Name>;
// A row's folder: the workspace folder when the list shows it, then the page's own.
const Dir = html`<span class="dir">${text()}${text()}</span>`;
type DirNode = ViewNodeOf<typeof Dir>;

const Row = html`<button
  class=${cls()}
  role="option"
  aria-selected=${attr<"true" | "false">()}
  title=${attr()}
  onclick=${on<"click">()}
>
  ${child<IconNode>()}<span class="file-name"
    >${child<NameNode | HTMLSpanElement>()}${child<DirNode | HTMLSpanElement | null>()}</span
  ><small>${text()}</small>
</button>`;
type RowNode = ViewNodeOf<typeof Row>;

const Group = html`<details class="file-group" open=${attr<boolean>()} ontoggle=${on<"toggle", HTMLDetailsElement>()}>
  <summary title=${attr()}>${child<IconNode>()}<span>${text()}</span><span class="count">${text()}</span></summary>
  ${child<ListNode<RowNode, string>>()}
</details>`;
type GroupNode = ViewNodeOf<typeof Group>;

const Empty = html`<p class=${cls()}>${text()}</p>`;
type EmptyNode = ViewNodeOf<typeof Empty>;

const PathDir = html`<span class="dir">${text()}</span>`;
const Title = html`<div class="viewer-title">
  <h2 title=${attr()}>${child<ViewNodeOf<typeof PathDir> | null>()}${text()}</h2>
  <p>${text()}</p>
</div>`;
type TitleNode = ViewNodeOf<typeof Title>;

const Reload = html`<button
  class="icon-btn"
  data-tip="Reload the page"
  aria-label="Reload the page"
  onclick=${on<"click">()}
>
  ${child<IconNode>()}
</button>`;
const OpenTab = html`<button
  class="icon-btn"
  data-tip="Open in a new tab"
  aria-label="Open in a new tab"
  onclick=${on<"click">()}
>
  ${child<IconNode>()}
</button>`;
const Copy = html`<button
  class="icon-btn"
  data-tip="Copy the file path"
  aria-label="Copy the file path"
  onclick=${on<"click">()}
>
  ${child<IconNode>()}
</button>`;
type Head = [
  TitleNode | null,
  ViewNodeOf<typeof Reload> | null,
  ViewNodeOf<typeof OpenTab> | null,
  ViewNodeOf<typeof Copy> | null,
];

// The frame gets scripts, forms and popups, never the app's origin.
const Frame = html`<iframe
  class="artifact-frame"
  src=${attr()}
  title=${attr()}
  sandbox="allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads"
  referrerpolicy="no-referrer"
></iframe>`;
const Note = html`<p class=${cls()}>${text()}</p>`;
type Body = ViewNodeOf<typeof Frame> | ViewNodeOf<typeof Note> | null;

type ListContent = ListNode<RowNode, string> | ListNode<GroupNode, string> | EmptyNode | null;

const Shell = html`<div class="files artifacts">
  <div class="file-pane">
    <label class="file-search"
      >${child<IconNode>()}${child<HTMLInputElement>()}<span class="files-count">${text()}</span></label
    >
    <div class="file-groups" role="listbox" aria-label="Artifacts">${child<ListContent>()}</div>
  </div>
  <section class="viewer" aria-live="polite">
    <div class="viewer-head">${child<Head[0]>()}${child<Head[1]>()}${child<Head[2]>()}${child<Head[3]>()}</div>
    <div class="viewer-body artifact-body">${child<Body>()}</div>
  </section>
</div>`;

// A checked narrowing, not a cast: the shell's root is a <div>.
const isDiv = (n: Node | null): n is HTMLDivElement => n !== null && n.nodeType === 1 && n.nodeName === "DIV";

export class ArtifactsView {
  readonly el: HTMLElement;
  private readonly search: HTMLInputElement;
  // The shell renders into this holder, which stays out of the document; its
  // root, the element main.ts places, is `el`. Later renders patch it there.
  private readonly holder = document.createElement("div");
  private items: Artifact[] = [];
  private loaded = false;
  private error = "";
  private selected = "";
  private shownKey = "";
  private frameUrl = "";
  private results: Artifact[] = [];
  private active = 0;
  private count = "";
  private body: Body = null;
  private timer = 0;
  // Each start is a new polling run; a run left from before a stop ends at its next tick.
  private run = 0;
  private readonly open = new Map<string, boolean>();

  constructor(readonly routeId: string) {
    this.search = mountElement("input", SearchInput, [
      () => {
        this.active = 0;
        this.draw();
      },
      (e) => this.onKey(e),
    ]);
    this.draw();
    const root = this.holder.firstChild;
    if (!isDiv(root)) throw new Error("ArtifactsView: the shell rendered no root");
    this.el = root;
  }

  focusSearch(): void {
    this.search.focus();
    this.search.select();
  }

  /** Polls the list while the tab shows. */
  start(): void {
    if (this.timer) return;
    const run = ++this.run;
    const tick = async (): Promise<void> => {
      await this.load();
      if (this.timer && run === this.run) this.timer = window.setTimeout(() => void tick(), refreshEvery);
    };
    this.timer = window.setTimeout(() => void tick(), 0);
  }

  stop(): void {
    clearTimeout(this.timer);
    this.timer = 0;
    this.run++;
  }

  update(selected: string): void {
    this.selected = selected;
    this.syncBody();
    this.draw();
  }

  /** The open page: the one in the URL, else the newest. */
  private current(): string {
    if (this.selected) return this.selected;
    const newest = this.items[0];
    return newest ? artifactPath(newest) : "";
  }

  private async load(): Promise<void> {
    try {
      const l = await listArtifacts(this.routeId);
      const before = JSON.stringify(this.items);
      this.items = l.artifacts ?? [];
      const recovered = this.error !== "";
      this.error = "";
      const first = !this.loaded;
      this.loaded = true;
      if (first || recovered || before !== JSON.stringify(this.items)) {
        this.syncBody();
        this.draw();
      }
    } catch (e) {
      this.error = e instanceof Error ? e.message : String(e);
      this.loaded = true;
      this.draw();
    }
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
      const a = this.results[this.active];
      if (a) store.go({ artifact: artifactPath(a) }, false);
    }
  }

  private draw(): void {
    const items = this.listContent(); // sets the count
    render(this.holder, Shell([searchIcon, this.search, this.count, items, ...this.head(), this.body]));
  }

  private row(a: Artifact, hits: number[], cursor: boolean, showRoot: boolean): RowNode {
    const full = artifactPath(a);
    const slash = a.path.lastIndexOf("/");
    const name = a.path.slice(slash + 1);
    const dir = slash < 0 ? "" : a.path.slice(0, slash + 1);
    const nameHits = hits.filter((i) => i > slash).map((i) => i - slash - 1);
    const dirHits = hits.filter((i) => i <= slash);
    const on = full === this.current();
    const rootPart = showRoot ? `${rootLabel(a.root)}/` : "";
    return Row([
      ["file-row", on ? "active" : "", cursor ? "cursor" : ""].filter(Boolean).join(" "),
      on ? "true" : "false",
      full,
      () => store.go({ artifact: full }, false),
      artifactIcon,
      // runs(): migrate when inline runs land
      nameHits.length ? h("span", { class: "name" }, highlight(name, nameHits)) : Name([name]),
      !dir && !showRoot
        ? null
        : dirHits.length
          ? h("span", { class: "dir" }, rootPart, highlight(dir, dirHits)) // runs(): migrate when inline runs land
          : Dir([rootPart, dir]),
      ago(a.modified),
    ]);
  }

  private listContent(): ListContent {
    if (!this.loaded) {
      this.count = "";
      return Empty(["file-empty", "Looking for HTML pages…"]);
    }
    if (this.error && !this.items.length) return Empty(["file-empty error", this.error]);
    const q = this.search.value.trim();
    if (q) {
      const matches = this.items
        .map((a) => ({ a, m: fuzzy(q, a.path) }))
        .filter((x): x is { a: Artifact; m: NonNullable<ReturnType<typeof fuzzy>> } => !!x.m)
        .sort((x, y) => y.m.score - x.m.score);
      this.results = matches.map((x) => x.a);
      this.active = Math.min(this.active, Math.max(0, matches.length - 1));
      this.count = `${matches.length}/${this.items.length}`;
      const cursor = this.results[this.active];
      return matches.length
        ? list(
            matches,
            (x) => artifactPath(x.a),
            (x) => this.row(x.a, x.m.hits, x.a === cursor, true),
          )
        : Empty(["file-empty", `No page matches “${q}”.`]);
    }
    this.results = [];
    this.count = String(this.items.length);
    if (!this.items.length) {
      return Empty([
        "file-empty",
        "No HTML pages in this route's workspace folders yet. Pages agents write (for example under .lavish/ or reports/) show here.",
      ]);
    }
    // Pages come newest first, so a folder with fresh pages leads.
    const groups = new Map<string, Artifact[]>();
    for (const a of this.items) groups.set(a.root, [...(groups.get(a.root) ?? []), a]);
    const open = this.open;
    return list(
      [...groups.keys()],
      (root) => root,
      (root) => {
        const items = groups.get(root) ?? [];
        return Group([
          open.get(root) ?? true,
          function (this: HTMLDetailsElement) {
            open.set(root, this.open);
          },
          root,
          folderIcon,
          rootLabel(root),
          String(items.length),
          list(items, artifactPath, (a) => this.row(a, [], false, false)),
        ]);
      },
    );
  }

  private head(): Head {
    const a = this.items.find((x) => artifactPath(x) === this.current());
    if (!a) return [null, null, null, null];
    const full = artifactPath(a);
    const slash = a.path.lastIndexOf("/");
    return [
      Title([
        full,
        slash >= 0 ? PathDir([a.path.slice(0, slash + 1)]) : null,
        a.path.slice(slash + 1),
        `${rootLabel(a.root)} · ${size(a.size)} · ${ago(a.modified)}`,
      ]),
      Reload([
        () => {
          this.shownKey = "";
          this.syncBody();
          this.draw();
        },
        refreshIcon,
      ]),
      OpenTab([
        () => {
          if (this.frameUrl) window.open(this.frameUrl, "_blank", "noopener,noreferrer");
        },
        externalIcon,
      ]),
      Copy([
        async () => {
          try {
            await navigator.clipboard.writeText(full);
            toast("Copied the file path.");
          } catch {
            toast("The browser blocked the clipboard.", "error");
          }
        },
        copyIcon,
      ]),
    ];
  }

  /** Opens the current page in the frame when it, or its modified time, changed. */
  private syncBody(): void {
    const a = this.items.find((x) => artifactPath(x) === this.current());
    if (!a) {
      this.body = Note([
        "muted artifact-empty",
        this.loaded && this.selected
          ? "This page is no longer in the route's workspace folders."
          : "Pick a page to show it.",
      ]);
      this.shownKey = "";
      return;
    }
    // A new modified time reloads the frame, so a page an agent rewrites stays current.
    const key = `${artifactPath(a)}|${a.modified}`;
    if (key === this.shownKey) return;
    this.shownKey = key;
    this.body = Note(["muted", "Loading…"]);
    openArtifact(this.routeId, a.root, a.path)
      .then((url) => {
        if (this.shownKey !== key) return;
        this.frameUrl = url;
        this.body = Frame([url, a.path]);
        this.draw();
      })
      .catch((e: unknown) => {
        if (this.shownKey !== key) return;
        this.body = Note(["error", e instanceof Error ? e.message : String(e)]);
        this.draw();
      });
  }
}
