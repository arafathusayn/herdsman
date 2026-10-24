// Artifacts: the HTML pages agents leave in the session's folders (reports,
// review pages, plans), listed like the Docs tab and shown in a sandboxed
// frame. The list refreshes while the tab shows.
import { listArtifacts, openArtifact } from "../api.ts";
import { h, icon, icons, mount } from "../dom.ts";
import { fuzzy, highlight } from "../fuzzy.ts";
import { ago } from "../model.ts";
import { store } from "../store.ts";
import type { Artifact } from "../types.ts";
import { toast } from "../ui.ts";

const refreshEvery = 5000;

/** An artifact's full path, the key the URL keeps. */
export const artifactPath = (a: Artifact): string => `${a.root}/${a.path}`;

/** A session folder as the list names it: its last two parts. */
export function rootLabel(root: string): string {
  const parts = root.split("/").filter(Boolean);
  return parts.slice(-2).join("/") || "/";
}

function size(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

// The frame gets scripts, forms and popups, never the app's origin.
const sandbox = "allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads";

export class ArtifactsView {
  readonly el: HTMLElement;
  private readonly search = h("input", {
    type: "search",
    placeholder: "Search pages…",
    "aria-label": "Search artifacts",
    autocomplete: "off",
    spellcheck: false,
  });
  private readonly count = h("span", { class: "files-count" });
  private readonly list = h("div", { class: "file-groups", role: "listbox", "aria-label": "Artifacts" });
  private readonly viewerHead = h("div", { class: "viewer-head" });
  private readonly viewerBody = h("div", { class: "viewer-body artifact-body" });
  private items: Artifact[] = [];
  private loaded = false;
  private error = "";
  private selected = "";
  private shownKey = "";
  private frameUrl = "";
  private results: Artifact[] = [];
  private active = 0;
  private timer = 0;
  // Each start is a new polling run; a run left from before a stop ends at its next tick.
  private run = 0;
  private readonly open = new Map<string, boolean>();

  constructor() {
    this.search.addEventListener("input", () => {
      this.active = 0;
      this.renderList();
    });
    this.search.addEventListener("keydown", (e) => this.onKey(e));
    this.el = h(
      "div",
      { class: "files artifacts" },
      h(
        "div",
        { class: "file-pane" },
        h("label", { class: "file-search" }, icon(icons.search), this.search, this.count),
        this.list,
      ),
      h("section", { class: "viewer", "aria-live": "polite" }, this.viewerHead, this.viewerBody),
    );
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
    this.renderList();
    this.renderViewer();
  }

  /** The open page: the one in the URL, else the newest. */
  private current(): string {
    if (this.selected) return this.selected;
    const newest = this.items[0];
    return newest ? artifactPath(newest) : "";
  }

  private async load(): Promise<void> {
    try {
      const l = await listArtifacts();
      const before = JSON.stringify(this.items);
      this.items = l.artifacts ?? [];
      const recovered = this.error !== "";
      this.error = "";
      const first = !this.loaded;
      this.loaded = true;
      if (first || recovered || before !== JSON.stringify(this.items)) {
        this.renderList();
        this.renderViewer();
      }
    } catch (e) {
      this.error = e instanceof Error ? e.message : String(e);
      this.loaded = true;
      this.renderList();
    }
  }

  private onKey(e: KeyboardEvent): void {
    if (e.key === "Escape") {
      if (this.search.value) {
        e.preventDefault();
        this.search.value = "";
        this.renderList();
      } else this.search.blur();
      return;
    }
    if (!this.results.length) return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const n = this.results.length;
      this.active = (this.active + (e.key === "ArrowDown" ? 1 : n - 1)) % n;
      this.renderList();
      this.list.querySelector(".file-row.cursor")?.scrollIntoView({ block: "nearest" });
    } else if (e.key === "Enter") {
      e.preventDefault();
      const a = this.results[this.active];
      if (a) store.go({ artifact: artifactPath(a) }, false);
    }
  }

  private row(a: Artifact, hits: number[], cursor: boolean, showRoot: boolean): HTMLElement {
    const full = artifactPath(a);
    const slash = a.path.lastIndexOf("/");
    const name = a.path.slice(slash + 1);
    const dir = slash < 0 ? "" : a.path.slice(0, slash + 1);
    const nameHits = hits.filter((i) => i > slash).map((i) => i - slash - 1);
    const dirHits = hits.filter((i) => i <= slash);
    const on = full === this.current();
    const cls = ["file-row", on ? "active" : "", cursor ? "cursor" : ""].filter(Boolean).join(" ");
    return h(
      "button",
      {
        class: cls,
        role: "option",
        "aria-selected": String(on),
        title: full,
        onclick: () => store.go({ artifact: full }, false),
      },
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

  private renderList(): void {
    if (!this.loaded) {
      this.count.textContent = "";
      mount(this.list, h("p", { class: "file-empty" }, "Looking for HTML pages…"));
      return;
    }
    if (this.error && !this.items.length) {
      mount(this.list, h("p", { class: "file-empty error" }, this.error));
      return;
    }
    const q = this.search.value.trim();
    if (q) {
      const matches = this.items
        .map((a) => ({ a, m: fuzzy(q, a.path) }))
        .filter((x): x is { a: Artifact; m: NonNullable<ReturnType<typeof fuzzy>> } => !!x.m)
        .sort((x, y) => y.m.score - x.m.score);
      this.results = matches.map((x) => x.a);
      this.active = Math.min(this.active, Math.max(0, matches.length - 1));
      this.count.textContent = `${matches.length}/${this.items.length}`;
      mount(
        this.list,
        matches.length
          ? matches.map((x, i) => this.row(x.a, x.m.hits, i === this.active, true))
          : h("p", { class: "file-empty" }, `No page matches “${q}”.`),
      );
      return;
    }
    this.results = [];
    this.count.textContent = String(this.items.length);
    if (!this.items.length) {
      mount(
        this.list,
        h(
          "p",
          { class: "file-empty" },
          "No HTML pages in the session's folders yet. Pages agents write (for example under .lavish/ or reports/) show here.",
        ),
      );
      return;
    }
    // Pages come newest first, so a folder with fresh pages leads.
    const groups = new Map<string, Artifact[]>();
    for (const a of this.items) groups.set(a.root, [...(groups.get(a.root) ?? []), a]);
    mount(
      this.list,
      [...groups.keys()].map((root) => {
        const items = groups.get(root) ?? [];
        const isOpen = this.open.get(root) ?? true;
        const details = h(
          "details",
          { class: "file-group", open: isOpen },
          h(
            "summary",
            { title: root },
            icon(icons.folder),
            h("span", null, rootLabel(root)),
            h("span", { class: "count" }, String(items.length)),
          ),
          items.map((a) => this.row(a, [], false, false)),
        );
        details.addEventListener("toggle", () => this.open.set(root, details.open));
        return details;
      }),
    );
  }

  private renderViewer(): void {
    const a = this.items.find((x) => artifactPath(x) === this.current());
    if (!a) {
      mount(this.viewerHead);
      mount(
        this.viewerBody,
        h(
          "p",
          { class: "muted artifact-empty" },
          this.loaded && this.selected ? "This page is no longer in the session's folders." : "Pick a page to show it.",
        ),
      );
      this.shownKey = "";
      return;
    }
    const full = artifactPath(a);
    const slash = a.path.lastIndexOf("/");
    const reload = (): void => {
      this.shownKey = "";
      this.renderViewer();
    };
    mount(
      this.viewerHead,
      h(
        "div",
        { class: "viewer-title" },
        h(
          "h2",
          { title: full },
          slash >= 0 ? h("span", { class: "dir" }, a.path.slice(0, slash + 1)) : null,
          a.path.slice(slash + 1),
        ),
        h("p", null, `${rootLabel(a.root)} · ${size(a.size)} · ${ago(a.modified)}`),
      ),
      h(
        "button",
        { class: "icon-btn", "data-tip": "Reload the page", "aria-label": "Reload the page", onclick: reload },
        icon(icons.refresh),
      ),
      h(
        "button",
        {
          class: "icon-btn",
          "data-tip": "Open in a new tab",
          "aria-label": "Open in a new tab",
          onclick: () => {
            if (this.frameUrl) window.open(this.frameUrl, "_blank", "noopener,noreferrer");
          },
        },
        icon(icons.external),
      ),
      h(
        "button",
        {
          class: "icon-btn",
          "data-tip": "Copy the file path",
          "aria-label": "Copy the file path",
          onclick: async () => {
            try {
              await navigator.clipboard.writeText(full);
              toast("Copied the file path.");
            } catch {
              toast("The browser blocked the clipboard.", "error");
            }
          },
        },
        icon(icons.copy),
      ),
    );
    // A new modified time reloads the frame, so a page an agent rewrites stays current.
    const key = `${full}|${a.modified}`;
    if (key === this.shownKey) return;
    this.shownKey = key;
    mount(this.viewerBody, h("p", { class: "muted" }, "Loading…"));
    openArtifact(a.root, a.path)
      .then((url) => {
        if (this.shownKey !== key) return;
        this.frameUrl = url;
        mount(
          this.viewerBody,
          h("iframe", {
            class: "artifact-frame",
            src: url,
            title: a.path,
            sandbox,
            referrerPolicy: "no-referrer",
          }),
        );
      })
      .catch((e: unknown) => {
        if (this.shownKey === key)
          mount(this.viewerBody, h("p", { class: "error" }, e instanceof Error ? e.message : String(e)));
      });
  }
}
