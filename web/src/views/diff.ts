// The Changes tab: what each task's worktree or branch changed against its
// base branch, any other worktree an agent works in, and the project
// checkout's own changes. Built once per route; it polls while shown, and a
// poll that finds nothing new costs a 304.
import { AuthError, routeDiff } from "../api.ts";
import { highlighting } from "../appearance.ts";
import { type DiffLine, parseSection, sideBySide, splitPatch } from "../diff.ts";
import { h, icon, icons, mount } from "../dom.ts";
import { fuzzy, highlight } from "../fuzzy.ts";
import { languageOf, type Piece, renderPieces, type State, tokenizeLine } from "../highlight.ts";
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

function counts(add: number, del: number): HTMLElement {
  return h(
    "span",
    { class: "counts" },
    h("span", { class: "plus" }, `+${add}`),
    h("span", { class: "minus" }, `−${del}`),
  );
}

/**
 * Highlights the code of diff lines. The old and new sides each carry their
 * own state (a comment opened in a removed line does not color added ones),
 * and a hunk starts both afresh, since the lines between hunks are unknown.
 */
function colorLines(lines: DiffLine[], lang: string): (DocumentFragment | null)[] {
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
    return pieces.length ? renderPieces(pieces) : null;
  });
}

/**
 * The side-by-side table: old lines on the left, new on the right. Long
 * lines wrap so both sides stay in view. A context line shows on both
 * sides; the left one, drawn first, gets a copy of its colored code, since
 * appending a fragment empties it.
 */
function splitTable(lines: DiffLine[], code: Map<DiffLine, DocumentFragment | null>): HTMLElement {
  const side = (l: DiffLine | null, num: number | undefined, right: boolean): HTMLElement[] => {
    if (!l) return [h("td", { class: "ln blank" }), h("td", { class: "code blank" })];
    const colored = code.get(l);
    const text = colored ? (!right && l.kind === "ctx" ? colored.cloneNode(true) : colored) : l.text || " ";
    return [
      h("td", { class: `ln ${l.kind}` }, num === undefined ? "" : String(num)),
      h("td", { class: `code ${l.kind}` }, text),
    ];
  };
  return h(
    "table",
    null,
    h("colgroup", null, h("col", { class: "ln-col" }), h("col"), h("col", { class: "ln-col" }), h("col")),
    h(
      "tbody",
      null,
      sideBySide(lines).map((r) =>
        "kind" in r
          ? h("tr", { class: `d-${r.kind}` }, h("td", { colspan: "4", class: "code" }, r.text))
          : h("tr", null, side(r.left, r.left?.old, false), side(r.right, r.right?.new, true)),
      ),
    ),
  );
}

export class DiffView {
  readonly el: HTMLElement;
  private readonly sourceList = h("div", {
    class: "diff-sources",
    role: "listbox",
    "aria-label": "Changes",
  });
  private readonly search = h("input", {
    type: "search",
    placeholder: "Search changed files…",
    "aria-label": "Search changed files",
    autocomplete: "off",
    spellcheck: false,
  });
  private readonly count = h("span", { class: "files-count" });
  private readonly list = h("div", {
    class: "file-groups",
    role: "listbox",
    "aria-label": "Changed files",
  });
  private readonly viewerHead = h("div", { class: "viewer-head" });
  private readonly viewerBody = h("div", { class: "viewer-body diff-body" });
  private readonly viewer = h("section", { class: "viewer", "aria-live": "polite" }, this.viewerHead, this.viewerBody);
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
  private drawnKey = "";
  private drawnSource: DiffSource | null = null;
  private showAll = false;
  private timer = 0;
  private loading = false;

  constructor(readonly routeName: string) {
    this.search.addEventListener("input", () => this.renderList());
    this.search.addEventListener("keydown", (e) => {
      if (e.key === "Escape") {
        if (this.search.value) {
          e.preventDefault();
          this.search.value = "";
          this.renderList();
        } else this.search.blur();
      } else if (e.key === "Enter") {
        const first = this.list.querySelector<HTMLButtonElement>(".file-row");
        first?.click();
      }
    });
    this.el = h(
      "div",
      { class: "files diff" },
      h(
        "div",
        { class: "file-pane" },
        this.sourceList,
        h("label", { class: "file-search" }, icon(icons.search), this.search, this.count),
        this.list,
      ),
      this.viewer,
    );
    new ResizeObserver(([e]) => {
      const narrow = (e?.contentRect.width ?? 0) < splitMin;
      if (narrow === this.narrow) return;
      this.narrow = narrow;
      this.renderViewer();
    }).observe(this.viewer);
    this.render();
  }

  /** The layout drawn: the user's choice, or inline in a narrow viewer. */
  private layout(): Layout {
    return this.narrow ? "inline" : layout;
  }

  private layoutSwitch(): HTMLElement {
    return h(
      "div",
      { class: "view-switch diff-layout", role: "group", "aria-label": "Diff layout" },
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
            class: v === layout ? "active" : "",
            "aria-pressed": String(v === layout),
            title: tip,
            onclick: () => {
              layout = v;
              try {
                localStorage.setItem(layoutKey, v);
              } catch {
                // The choice lasts for this page only.
              }
              this.renderViewer();
            },
          },
          label,
        ),
      ),
    );
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
    if (source === this.sourceId && file === this.file) return;
    this.sourceId = source;
    this.file = file;
    this.showAll = false;
    this.render();
  }

  private async load(): Promise<void> {
    if (this.loading) return;
    this.loading = true;
    try {
      const next = await routeDiff(this.routeName);
      if (next !== this.sources || this.error) {
        this.sources = next;
        this.error = "";
        this.render();
      }
    } catch (e) {
      if (e instanceof AuthError) location.reload();
      this.error = e instanceof Error ? e.message : String(e);
      this.render();
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

  private render(): void {
    this.renderSources();
    this.renderList();
    this.renderViewer();
  }

  private renderSources(): void {
    const all = this.sources;
    if (!all) {
      mount(this.sourceList, h("p", { class: "file-empty" }, this.error || "Reading the changes…"));
      return;
    }
    const cur = this.source();
    mount(
      this.sourceList,
      all.map((s) => {
        const files = s.diff?.files ?? [];
        const t = totals(files);
        const active = s.id === cur?.id;
        return h(
          "button",
          {
            class: active ? "diff-source active" : "diff-source",
            role: "option",
            "aria-selected": String(active),
            title: s.dir ?? s.label,
            onclick: () => store.go({ source: s.id, diffFile: "" }, false),
          },
          h(
            "span",
            { class: "diff-source-top" },
            h("strong", null, s.label),
            s.error ? h("span", { class: "chip warn" }, "unavailable") : counts(t.add, t.del),
          ),
          s.agents?.length
            ? h(
                "span",
                {
                  class: "diff-source-agents",
                  title: "Agents working in this folder",
                },
                s.agents.map((a) => h("span", { class: "who" }, a)),
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
      }),
    );
  }

  private row(f: DiffFile, hits: number[], showDir: boolean): HTMLElement {
    const slash = f.path.lastIndexOf("/");
    const name = f.path.slice(slash + 1);
    const dir = slash < 0 ? "" : f.path.slice(0, slash + 1);
    const active = f.path === this.selected()?.path;
    return h(
      "button",
      {
        class: active ? "file-row active" : "file-row",
        role: "option",
        "aria-selected": String(active),
        title: f.old_path ? `${f.old_path} → ${f.path}` : f.path,
        onclick: () => store.go({ diffFile: f.path, source: this.source()?.id ?? "" }, false),
      },
      h(
        "span",
        {
          class: `st st-${f.status === "?" ? "u" : f.status}`,
          title: statusName[f.status] ?? f.status,
        },
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
        showDir && dir
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
      f.binary || f.status === "?" ? null : counts(f.add, f.del),
    );
  }

  private renderList(): void {
    const src = this.source();
    const files = this.files();
    if (!src || src.error || !files.length) {
      this.count.textContent = "";
      mount(this.list, src && !src.error ? h("p", { class: "file-empty" }, "No changed files.") : null);
      return;
    }
    const q = this.search.value.trim();
    if (!q) {
      this.count.textContent = String(files.length);
      mount(
        this.list,
        files.map((f) => this.row(f, [], true)),
      );
      return;
    }
    const matches = files
      .map((f) => ({ f, m: fuzzy(q, f.path) }))
      .filter((x) => x.m)
      .sort((a, b) => (b.m?.score ?? 0) - (a.m?.score ?? 0));
    this.count.textContent = `${matches.length}/${files.length}`;
    mount(
      this.list,
      matches.length
        ? matches.map((x) => this.row(x.f, x.m?.hits ?? [], true))
        : h("p", { class: "file-empty" }, `No changed file matches “${q}”.`),
    );
  }

  private renderViewer(): void {
    const src = this.source();
    const f = this.selected();
    // A new diff is a new source object (an unchanged one comes back from the
    // ETag cache as the same object), so any new patch redraws.
    const key = `${highlighting()}|${layout}|${this.narrow}|${src?.id}|${f?.path}|${this.showAll}|${this.sources === null}|${this.error}`;
    if (key === this.drawnKey && src === this.drawnSource) return;
    this.drawnKey = key;
    this.drawnSource = src;
    if (!src) {
      mount(this.viewerHead);
      mount(this.viewerBody, h("p", { class: "muted" }, this.error || "Reading the changes…"));
      return;
    }
    if (src.error || !src.diff) {
      mount(this.viewerHead, h("div", { class: "viewer-title" }, h("h2", { class: "plain" }, src.label)));
      mount(
        this.viewerBody,
        h("p", { class: "error" }, src.error ?? "No diff."),
        src.dir ? h("p", { class: "muted" }, `Folder: ${src.dir}`) : null,
      );
      return;
    }
    if (!f) {
      mount(this.viewerHead, h("div", { class: "viewer-title" }, h("h2", { class: "plain" }, src.label)));
      mount(this.viewerBody, h("p", { class: "muted" }, `No changes against ${src.diff.base}.`));
      return;
    }
    const slash = f.path.lastIndexOf("/");
    mount(
      this.viewerHead,
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
          f.binary || f.status === "?" ? null : [" · ", counts(f.add, f.del)],
        ),
      ),
      f.binary || f.status === "?" || this.narrow ? null : this.layoutSwitch(),
      h(
        "button",
        {
          class: "icon-btn",
          title: "Copy the file path",
          "aria-label": "Copy the file path",
          onclick: async () => {
            try {
              await navigator.clipboard.writeText(src.dir ? `${src.dir}/${f.path}` : f.path);
              toast("Copied the file path.");
            } catch {
              toast("The browser blocked the clipboard.", "error");
            }
          },
        },
        icon(icons.copy),
      ),
    );
    if (this.sectionsOf !== src) {
      this.sections = splitPatch(src.diff.patch);
      this.sectionsOf = src;
    }
    if (f.status === "?") {
      mount(this.viewerBody, h("p", { class: "muted" }, "Untracked file: git has no diff for it until it is added."));
      return;
    }
    const section = this.sections.get(f.path);
    const lines = section ? parseSection(section) : [];
    if (!lines.length) {
      mount(
        this.viewerBody,
        h(
          "p",
          { class: "muted" },
          !section && src.diff.truncated
            ? "This file is past the size cut of the diff; open it in the worktree to see its lines."
            : f.binary
              ? "Binary file; no lines to show."
              : f.old_path
                ? `Moved from ${f.old_path} with no line changes.`
                : "No line changes; only the file mode changed.",
        ),
      );
      return;
    }
    // The size cut stops the patch inside its last section, so that file may show only part of its lines.
    const cut = src.diff.truncated && section === [...this.sections.values()].at(-1);
    mount(
      this.viewerBody,
      cut
        ? h(
            "p",
            { class: "diff-cut", role: "note" },
            "The diff reached its size cut in this file, so lines past this point are not shown. Open the file in the worktree to see all of it.",
          )
        : null,
      this.table(lines, f.path),
    );
  }

  private table(lines: DiffLine[], path: string): HTMLElement {
    const shown = this.showAll ? lines : lines.slice(0, rowCap);
    const code = colorLines(shown, highlighting() ? languageOf(path) : "");
    const table =
      this.layout() === "split"
        ? splitTable(shown, new Map(shown.map((l, i) => [l, code[i] ?? null])))
        : h(
            "table",
            null,
            h(
              "tbody",
              null,
              shown.map((l, i) =>
                l.kind === "hunk" || l.kind === "note"
                  ? h("tr", { class: `d-${l.kind}` }, h("td", { colspan: "3", class: "code" }, l.text))
                  : h(
                      "tr",
                      { class: `d-${l.kind}` },
                      h("td", { class: "ln" }, l.old === undefined ? "" : String(l.old)),
                      h("td", { class: "ln" }, l.new === undefined ? "" : String(l.new)),
                      h("td", { class: "code" }, code[i] ?? (l.text || " ")),
                    ),
              ),
            ),
          );
    return h(
      "div",
      null,
      h("div", { class: `diff-code ${this.layout()}` }, table),
      shown.length < lines.length
        ? h(
            "button",
            {
              class: "btn ghost diff-more",
              onclick: () => {
                this.showAll = true;
                this.renderViewer();
              },
            },
            `Show all ${lines.length} lines`,
          )
        : null,
    );
  }
}
