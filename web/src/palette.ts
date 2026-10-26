// The command palette (⌘K / Ctrl+K), in the pattern of VS Code: one box that
// finds agents, routes and files and runs every action. A leading ">" shows
// only commands, "@" only agents and "#" only files. A command that needs
// text (a prompt) turns the box into an input for that text.
import { icon, type IconNode, icons } from "./dom.ts";
import { fuzzy, highlight, type HighlightNode } from "./fuzzy.ts";
import {
  attr,
  child,
  cls,
  html,
  list,
  type ListNode,
  on,
  prop,
  text,
  type ViewNodeOf,
} from "./herdsman-blocks-view.ts";
import { element, ElementView } from "./views/element-view.ts";

export type CommandKind = "command" | "agent" | "route" | "file";

export interface Command {
  id: string;
  kind: CommandKind;
  /** What the row says, e.g. "Agent: Prompt im-auth…". */
  title: string;
  /** Muted text after the title: a pane id, a status, a path. */
  detail?: string;
  icon?: string[];
  /** A shortcut hint shown on the right. */
  keys?: string;
  run?: () => unknown;
  ask?: {
    prompt: string;
    placeholder: string;
    submit: (text: string) => unknown;
  };
}

const prefixes: Record<string, CommandKind> = {
  ">": "command",
  "@": "agent",
  "#": "file",
};
const maxRows = 80;
const searchHint = "Search agents, routes and files, or type > for commands";

export const isMac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
export const paletteKeys = isMac ? "⌘K" : "Ctrl+K";
export const sidebarKeys = isMac ? "⌘B" : "Ctrl+B or Ctrl+Shift+B";

// The search field is an element the palette owns: the palette reads its
// value, clears it, changes its placeholder and focuses it.
const SearchInput = html`<input
  type="text"
  .value=${prop<HTMLInputElement, "value">()}
  placeholder=${attr()}
  aria-label="Command palette"
  autocomplete="off"
  spellcheck="false"
  role="combobox"
  aria-expanded="true"
  aria-controls="palette-list"
  oninput=${on<"input", HTMLInputElement>()}
  onkeydown=${on<"keydown", HTMLInputElement>()}
/>`;

const Detail = html`<span class="palette-detail">${text()}</span>`;
const Keys = html`<kbd>${text()}</kbd>`;
// prettier-ignore
const Row = html`<div
  id=${attr()}
  class=${cls()}
  role="option"
  aria-selected=${attr<"true" | "false">()}
  onmousemove=${on<"mousemove">()}
  onmousedown=${on<"mousedown">()}
  onclick=${on<"click">()}
>
  ${child<IconNode>()}<span class="palette-title">${child<HighlightNode>()}</span>${child<ViewNodeOf<typeof Detail> | null>()}${child<ViewNodeOf<typeof Keys> | null>()}
</div>`;
const Empty = html`<p class="palette-empty">${text()}</p>`;

type ListContent = ListNode<ViewNodeOf<typeof Row>, string> | ViewNodeOf<typeof Empty>;

// The list is an element of its own so the palette can scroll its active row
// into view; it keeps its rows, hidden, while the box asks for text.
const ListBox = html`<div id="palette-list" class="palette-list" role="listbox" hidden=${attr<boolean>()}>
  ${child<ListContent>()}
</div>`;

const AskPrompt = html`<span>${text()}</span>`;
const AskHint = html`<small>↵ send · esc back</small>`;

// prettier-ignore
const Palette = html`<dialog
  class="palette"
  aria-label="Command palette"
  onclick=${on<"click", HTMLDialogElement>()}
  oncancel=${on<"cancel", HTMLDialogElement>()}
  onclose=${on<"close", HTMLDialogElement>()}
>
  <div class="palette-input">${child<IconNode>()}${child<HTMLInputElement>()}</div>
  <div class=${cls()}>${child<IconNode | null>()}${child<ViewNodeOf<typeof AskPrompt> | null>()}${child<ViewNodeOf<typeof AskHint> | null>()}</div>
  ${child<HTMLDivElement>()}
  <div class="palette-foot">
    <span><kbd>↑</kbd><kbd>↓</kbd> move</span>
    <span><kbd>↵</kbd> run</span>
    <span><kbd>esc</kbd> close</span>
    <span class="grow"></span>
    <span><kbd>></kbd> commands</span>
    <span><kbd>@</kbd> agents</span>
    <span><kbd>#</kbd> files</span>
  </div>
</dialog>`;

// The open palette's search field, or null when the palette is closed.
let openInput: HTMLInputElement | null = null;

export function paletteOpen(): boolean {
  return openInput !== null;
}

export function openPalette(provider: () => Command[], initial = ""): void {
  if (openInput) {
    openInput.focus();
    return;
  }
  const returnFocus = document.activeElement;

  let all = provider();
  let shown: Command[] = [];
  let active = 0;
  let asking: Command | null = null;
  let rows: ListContent = Empty(["Nothing here yet."]);

  const input = element(
    "input",
    SearchInput([
      initial,
      searchHint,
      () => {
        active = 0;
        draw();
      },
      (e) => keydown(e),
    ]),
  );
  const listbox = new ElementView("div", ListBox([false, rows]));
  const frame = (): ViewNodeOf<typeof Palette> =>
    Palette([
      function (e) {
        if (e.target === this) close(); // a click on the backdrop
      },
      // Esc: leave the text input first, then the palette.
      (e) => {
        e.preventDefault();
        if (asking) back();
        else close();
      },
      function () {
        this.remove();
        openInput = null;
        if (returnFocus && "focus" in returnFocus && typeof returnFocus.focus === "function") returnFocus.focus();
      },
      icon(icons.search),
      input,
      asking ? "palette-ask on" : "palette-ask",
      asking ? icon(icons.arrow) : null,
      asking?.ask ? AskPrompt([asking.ask.prompt]) : null,
      asking ? AskHint([]) : null,
      listbox.el,
    ]);
  const dlg = new ElementView("dialog", frame());

  const close = (): void => {
    dlg.el.close();
  };

  const execute = async (c: Command): Promise<void> => {
    if (c.ask) {
      asking = c;
      input.value = "";
      input.placeholder = c.ask.placeholder;
      draw();
      return;
    }
    close();
    await c.run?.();
  };

  // Finds the rows for the query; while the box asks for text, the rows stay
  // as they were, hidden.
  const query = (): void => {
    const raw = input.value;
    const kind = prefixes[raw.charAt(0)];
    const q = (kind ? raw.slice(1) : raw).trim();
    const pool = kind ? all.filter((c) => c.kind === kind) : all;
    let found: { c: Command; hits: number[] }[];
    if (!q) found = pool.map((c) => ({ c, hits: [] }));
    else {
      const scored: { c: Command; hits: number[]; score: number }[] = [];
      for (const c of pool) {
        const m = fuzzy(q, c.title);
        const d = m ? null : c.detail ? fuzzy(q, c.detail) : null;
        if (m) scored.push({ c, hits: m.hits, score: m.score + 1 });
        else if (d) scored.push({ c, hits: [], score: d.score - 2 });
      }
      scored.sort((a, b) => b.score - a.score);
      found = scored;
    }
    found = found.slice(0, maxRows);
    shown = found.map((r) => r.c);
    active = Math.min(active, Math.max(0, shown.length - 1));
    if (!found.length) {
      rows = Empty([q ? `Nothing matches “${q}”.` : "Nothing here yet."]);
      input.removeAttribute("aria-activedescendant");
      return;
    }
    rows = list(
      found.map((r, i) => ({ ...r, i })),
      ({ c }) => c.id,
      ({ c, hits, i }) =>
        Row([
          `pal-${i}`,
          i === active ? "palette-row active" : "palette-row",
          i === active ? "true" : "false",
          () => {
            if (active !== i) {
              active = i;
              draw();
            }
          },
          (e) => e.preventDefault(),
          () => void execute(c),
          icon(c.icon ?? kindIcon[c.kind]),
          highlight(c.title, hits),
          c.detail ? Detail([c.detail]) : null,
          c.keys ? Keys([c.keys]) : null,
        ]),
    );
    input.setAttribute("aria-activedescendant", `pal-${active}`);
  };

  const draw = (): void => {
    if (!asking) query();
    listbox.update(ListBox([asking !== null, rows]));
    dlg.update(frame());
    if (!asking) listbox.el.querySelector(".palette-row.active")?.scrollIntoView({ block: "nearest" });
  };

  const back = (): void => {
    asking = null;
    input.value = "";
    input.placeholder = searchHint;
    draw();
  };

  const keydown = (e: KeyboardEvent): void => {
    if (asking) {
      if (e.key === "Escape") {
        e.preventDefault();
        back();
      } else if (e.key === "Enter" && input.value.trim()) {
        e.preventDefault();
        const c = asking;
        const text = input.value;
        close();
        void c.ask?.submit(text);
      }
      return;
    }
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const n = shown.length;
      if (n) active = (active + (e.key === "ArrowDown" ? 1 : n - 1)) % n;
      draw();
    } else if (e.key === "Enter") {
      e.preventDefault();
      const c = shown[active];
      if (c) void execute(c);
    } else if (e.key === "Escape") {
      e.preventDefault();
      close();
    }
  };

  document.body.append(dlg.el);
  openInput = input;
  dlg.el.showModal();
  all = provider();
  draw();
  input.focus();
  input.setSelectionRange(input.value.length, input.value.length);
}

const kindIcon: Record<CommandKind, string[]> = {
  command: icons.command,
  agent: icons.users,
  route: icons.list,
  file: icons.file,
};
