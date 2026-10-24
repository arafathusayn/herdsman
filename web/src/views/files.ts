// The route's files: a quick search, files grouped by folder, and a viewer.
// Built once per route and patched on state changes, so the search box keeps
// its focus and text while the route updates live.
import { routeFile } from "../api.ts";
import { highlighting } from "../appearance.ts";
import { h, icon, icons, mount } from "../dom.ts";
import { fuzzy, highlight } from "../fuzzy.ts";
import { codeBlock, renderMarkdown } from "../markdown.ts";
import { ago } from "../model.ts";
import { store } from "../store.ts";
import type { Route, RouteFile } from "../types.ts";
import { toast } from "../ui.ts";

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

export class FilesView {
  readonly el: HTMLElement;
  private readonly search = h("input", {
    type: "search",
    placeholder: "Search files…",
    "aria-label": "Search files",
    autocomplete: "off",
    spellcheck: false,
  });
  private readonly count = h("span", { class: "files-count" });
  private readonly list = h("div", {
    class: "file-groups",
    role: "listbox",
    "aria-label": "Files",
  });
  private readonly viewerHead = h("div", { class: "viewer-head" });
  private readonly viewerBody = h("div", { class: "viewer-body" });
  private route: Route | null = null;
  private selected = "";
  private loadedKey = "";
  private text = "";
  private textKey = "";
  private raw = false;
  private active = 0;
  private results: RouteFile[] = [];
  private readonly open = new Map<string, boolean>();

  constructor(readonly routeName: string) {
    this.search.addEventListener("input", () => {
      this.active = 0;
      this.renderList();
    });
    this.search.addEventListener("keydown", (e) => this.onKey(e));
    this.el = h(
      "div",
      { class: "files" },
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

  update(route: Route, selected: string): void {
    this.route = route;
    this.selected = selected || "00-route.md";
    this.renderList();
    this.renderViewer();
  }

  private files(): RouteFile[] {
    return this.route?.files ?? [];
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
      const f = this.results[this.active];
      if (f) this.choose(f.path);
    }
  }

  private choose(path: string): void {
    store.go({ file: path }, false);
  }

  private row(f: RouteFile, hits: number[], cursor: boolean, showFolder: boolean): HTMLElement {
    const slash = f.path.lastIndexOf("/");
    const name = f.path.slice(slash + 1);
    const dir = slash < 0 ? "" : f.path.slice(0, slash + 1);
    const nameHits = hits.filter((i) => i > slash).map((i) => i - slash - 1);
    const dirHits = hits.filter((i) => i <= slash);
    const cls = ["file-row", f.path === this.selected ? "active" : "", cursor ? "cursor" : ""]
      .filter(Boolean)
      .join(" ");
    return h(
      "button",
      {
        class: cls,
        role: "option",
        "aria-selected": String(f.path === this.selected),
        title: f.path,
        onclick: () => this.choose(f.path),
      },
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

  private renderList(): void {
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
      this.count.textContent = `${matches.length}/${files.length}`;
      mount(
        this.list,
        matches.length
          ? matches.map((m, i) => this.row(m.file, m.hits, i === this.active, true))
          : h("p", { class: "file-empty" }, `No file matches “${q}”.`),
      );
      return;
    }
    this.results = [];
    this.count.textContent = String(files.length);
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
    mount(
      this.list,
      order.map((d) => {
        const items = (groups.get(d) ?? []).sort((a, b) => a.path.localeCompare(b.path));
        const holdsSelected = items.some((f) => f.path === this.selected);
        const isOpen = this.open.get(d) ?? (holdsSelected || !collapsedByDefault.has(d));
        const details = h(
          "details",
          { class: "file-group", open: isOpen },
          h(
            "summary",
            null,
            icon(icons.folder),
            h("span", null, d ? `${d}/` : "Route files"),
            h("span", { class: "count" }, String(items.length)),
          ),
          items.map((f) => this.row(f, [], false, false)),
        );
        details.addEventListener("toggle", () => this.open.set(d, details.open));
        return details;
      }),
    );
  }

  private renderViewer(): void {
    const r = this.route;
    if (!r) return;
    const f = this.files().find((x) => x.path === this.selected);
    const slash = this.selected.lastIndexOf("/");
    mount(
      this.viewerHead,
      h(
        "div",
        { class: "viewer-title" },
        h(
          "h2",
          { title: this.selected },
          slash >= 0 ? h("span", { class: "dir" }, this.selected.slice(0, slash + 1)) : null,
          this.selected.slice(slash + 1),
        ),
        f ? h("p", null, `${size(f.size)} · ${ago(f.modified)}`) : null,
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
              "aria-checked": String(this.raw === (label === "Raw")),
              class: this.raw === (label === "Raw") ? "active" : "",
              onclick: () => {
                this.raw = label === "Raw";
                this.loadedKey = "";
                this.renderViewer();
              },
            },
            label,
          ),
        ),
      ),
      h(
        "button",
        {
          class: "icon-btn",
          title: "Copy the file path",
          "aria-label": "Copy the file path",
          onclick: async () => {
            try {
              await navigator.clipboard.writeText(`${r.dir}/${this.selected}`);
              toast("Copied the file path.");
            } catch {
              toast("The browser blocked the clipboard.", "error");
            }
          },
        },
        icon(icons.copy),
      ),
    );
    const fileKey = `${r.name}/${this.selected}/${f?.modified ?? ""}`;
    const key = `${fileKey}/${this.raw}/${highlighting()}`;
    if (key === this.loadedKey) return;
    this.loadedKey = key;
    const show = () =>
      mount(
        this.viewerBody,
        this.raw ? h("pre", { class: "raw" }, codeBlock(this.text.split("\n"), "md")) : renderMarkdown(this.text),
      );
    if (fileKey === this.textKey) {
      show(); // only the Rendered/Raw switch changed
      return;
    }
    mount(this.viewerBody, h("p", { class: "muted" }, "Loading…"));
    routeFile(r.name, this.selected)
      .then((text) => {
        if (this.loadedKey !== key) return;
        this.text = text;
        this.textKey = fileKey;
        show();
      })
      .catch((e: unknown) => {
        if (this.loadedKey !== key) return;
        // Forget the failed load, so the next update or a click tries again.
        this.loadedKey = "";
        const retry = (): void => {
          this.loadedKey = "";
          this.renderViewer();
        };
        mount(
          this.viewerBody,
          h("p", { class: "error" }, e instanceof Error ? e.message : String(e)),
          h("button", { type: "button", class: "btn", onclick: retry }, "Try again"),
        );
      });
  }
}
