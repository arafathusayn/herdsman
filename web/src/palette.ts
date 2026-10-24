// The command palette (⌘K / Ctrl+K), in the pattern of VS Code: one box that
// finds agents, routes and files and runs every action. A leading ">" shows
// only commands, "@" only agents and "#" only files. A command that needs
// text (a prompt) turns the box into an input for that text.
import { h, icon, icons, mount } from "./dom.ts";
import { fuzzy, highlight } from "./fuzzy.ts";

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

let openDialog: HTMLDialogElement | null = null;

export function paletteOpen(): boolean {
  return openDialog !== null;
}

export function openPalette(provider: () => Command[], initial = ""): void {
  if (openDialog) {
    openDialog.querySelector("input")?.focus();
    return;
  }
  const returnFocus = document.activeElement as HTMLElement | null;
  const input = h("input", {
    type: "text",
    value: initial,
    placeholder: searchHint,
    "aria-label": "Command palette",
    autocomplete: "off",
    spellcheck: false,
    role: "combobox",
    "aria-expanded": "true",
    "aria-controls": "palette-list",
  });
  const list = h("div", {
    id: "palette-list",
    class: "palette-list",
    role: "listbox",
  });
  const askLabel = h("div", { class: "palette-ask" });
  const foot = h(
    "div",
    { class: "palette-foot" },
    h("span", null, h("kbd", null, "↑"), h("kbd", null, "↓"), " move"),
    h("span", null, h("kbd", null, "↵"), " run"),
    h("span", null, h("kbd", null, "esc"), " close"),
    h("span", { class: "grow" }),
    h("span", null, h("kbd", null, ">"), " commands"),
    h("span", null, h("kbd", null, "@"), " agents"),
    h("span", null, h("kbd", null, "#"), " files"),
  );
  const dlg = h(
    "dialog",
    { class: "palette", "aria-label": "Command palette" },
    h("div", { class: "palette-input" }, icon(icons.search), input),
    askLabel,
    list,
    foot,
  );

  let all = provider();
  let shown: Command[] = [];
  let active = 0;
  let asking: Command | null = null;

  const close = (): void => {
    dlg.close();
  };

  const execute = async (c: Command): Promise<void> => {
    if (c.ask) {
      asking = c;
      input.value = "";
      input.placeholder = c.ask.placeholder;
      mount(askLabel, icon(icons.arrow), h("span", null, c.ask.prompt), h("small", null, "↵ send · esc back"));
      askLabel.classList.add("on");
      list.hidden = true;
      return;
    }
    close();
    await c.run?.();
  };

  const render = (): void => {
    if (asking) return;
    const raw = input.value;
    const kind = prefixes[raw.charAt(0)];
    const q = (kind ? raw.slice(1) : raw).trim();
    const pool = kind ? all.filter((c) => c.kind === kind) : all;
    let rows: { c: Command; hits: number[] }[];
    if (!q) rows = pool.map((c) => ({ c, hits: [] }));
    else {
      rows = [];
      const scored: { c: Command; hits: number[]; score: number }[] = [];
      for (const c of pool) {
        const m = fuzzy(q, c.title);
        const d = m ? null : c.detail ? fuzzy(q, c.detail) : null;
        if (m) scored.push({ c, hits: m.hits, score: m.score + 1 });
        else if (d) scored.push({ c, hits: [], score: d.score - 2 });
      }
      scored.sort((a, b) => b.score - a.score);
      rows = scored;
    }
    rows = rows.slice(0, maxRows);
    shown = rows.map((r) => r.c);
    active = Math.min(active, Math.max(0, shown.length - 1));
    if (!rows.length) {
      mount(list, h("p", { class: "palette-empty" }, q ? `Nothing matches “${q}”.` : "Nothing here yet."));
      input.removeAttribute("aria-activedescendant");
      return;
    }
    mount(
      list,
      rows.map(({ c, hits }, i) =>
        h(
          "div",
          {
            id: `pal-${i}`,
            class: i === active ? "palette-row active" : "palette-row",
            role: "option",
            "aria-selected": String(i === active),
            onmousemove: () => {
              if (active !== i) {
                active = i;
                render();
              }
            },
            onmousedown: (e: Event) => e.preventDefault(),
            onclick: () => void execute(c),
          },
          icon(c.icon ?? kindIcon[c.kind]),
          h("span", { class: "palette-title" }, highlight(c.title, hits)),
          c.detail ? h("span", { class: "palette-detail" }, c.detail) : null,
          c.keys ? h("kbd", null, c.keys) : null,
        ),
      ),
    );
    input.setAttribute("aria-activedescendant", `pal-${active}`);
    list.querySelector(".palette-row.active")?.scrollIntoView({ block: "nearest" });
  };

  input.addEventListener("input", () => {
    active = 0;
    render();
  });
  const back = (): void => {
    asking = null;
    askLabel.classList.remove("on");
    askLabel.replaceChildren();
    list.hidden = false;
    input.value = "";
    input.placeholder = searchHint;
    render();
  };

  input.addEventListener("keydown", (e) => {
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
      render();
    } else if (e.key === "Enter") {
      e.preventDefault();
      const c = shown[active];
      if (c) void execute(c);
    } else if (e.key === "Escape") {
      e.preventDefault();
      close();
    }
  });
  dlg.addEventListener("click", (e) => {
    if (e.target === dlg) close(); // a click on the backdrop
  });
  // Esc: leave the text input first, then the palette.
  dlg.addEventListener("cancel", (e) => {
    e.preventDefault();
    if (asking) back();
    else close();
  });
  dlg.addEventListener("close", () => {
    dlg.remove();
    openDialog = null;
    returnFocus?.focus?.();
  });

  document.body.append(dlg);
  openDialog = dlg;
  dlg.showModal();
  all = provider();
  render();
  input.focus();
  input.setSelectionRange(input.value.length, input.value.length);
}

const kindIcon: Record<CommandKind, string[]> = {
  command: icons.command,
  agent: icons.users,
  route: icons.list,
  file: icons.file,
};
