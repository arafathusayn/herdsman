import { attach, batch, effect, For, memo, Show, signal } from "@reactre/core";

import { Highlight } from "./components/highlight.tsx";
import { Icon } from "./components/icons.tsx";
import { fuzzy } from "./fuzzy.ts";
import { icons } from "./icons.ts";

export type CommandKind = "command" | "agent" | "route" | "file";

export interface Command {
  id: string;
  kind: CommandKind;
  title: string;
  detail?: string;
  icon?: string[];
  keys?: string;
  run?: () => unknown;
  ask?: { prompt: string; placeholder: string; submit: (text: string) => unknown };
}

const prefixes: Record<string, CommandKind> = { ">": "command", "@": "agent", "#": "file" };
const maxRows = 80;
const searchHint = "Search agents, routes and files, or type > for commands";
export const isMac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
export const paletteKeys = isMac ? "⌘K" : "Ctrl+K";
export const sidebarKeys = isMac ? "⌘B" : "Ctrl+B or Ctrl+Shift+B";
const kindIcon: Record<CommandKind, string[]> = {
  command: icons.command,
  agent: icons.users,
  route: icons.list,
  file: icons.file,
};

function matching(commands: Command[], raw: string) {
  const kind = prefixes[raw.charAt(0)];
  const query = (kind ? raw.slice(1) : raw).trim();
  const pool = kind ? commands.filter((command) => command.kind === kind) : commands;
  if (!query) return pool.slice(0, maxRows).map((c) => ({ c, hits: [] as number[] }));
  const scored: { c: Command; hits: number[]; score: number }[] = [];
  for (const c of pool) {
    const match = fuzzy(query, c.title);
    const detail = match ? null : c.detail ? fuzzy(query, c.detail) : null;
    if (match) scored.push({ c, hits: match.hits, score: match.score + 1 });
    else if (detail) scored.push({ c, hits: [], score: detail.score - 2 });
  }
  return scored.sort((a, b) => b.score - a.score).slice(0, maxRows);
}

let openInput: HTMLInputElement | null = null;
export const paletteOpen = (): boolean => openInput !== null;

/** A mounted command palette: query, cursor and prompt mode are independent signals. */
export function openPalette(provider: () => Command[], initial = ""): void {
  if (openInput) {
    openInput.focus();
    return;
  }
  const returnFocus = document.activeElement;
  const [commands, setCommands] = signal(provider());
  const [query, setQuery] = signal(initial);
  const [active, setActive] = signal(0);
  const [asking, setAsking] = signal<Command | null>(null);
  let input!: HTMLInputElement;
  let dialog!: HTMLDialogElement;
  let listbox!: HTMLDivElement;
  let shown!: () => Command[];
  let dispose = (): void => {};
  let closed = false;

  const close = (): void => {
    dialog.close();
  };
  const back = (): void =>
    batch(() => {
      input.value = "";
      setAsking(null);
      setQuery("");
      setActive(0);
    });
  const execute = (command: Command): void => {
    if (command.ask) {
      input.value = "";
      setAsking(command);
    } else {
      close();
      void command.run?.();
    }
  };
  const keydown = (event: KeyboardEvent): void => {
    const command = asking();
    if (command) {
      if (event.key === "Escape") {
        event.preventDefault();
        back();
      } else if (event.key === "Enter" && input.value.trim()) {
        event.preventDefault();
        const text = input.value;
        close();
        void command.ask?.submit(text);
      }
      return;
    }
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const length = shown().length;
      if (length) setActive((at) => (at + (event.key === "ArrowDown" ? 1 : length - 1)) % length);
    } else if (event.key === "Enter") {
      event.preventDefault();
      const selected = shown()[active()];
      if (selected) execute(selected);
    } else if (event.key === "Escape") {
      event.preventDefault();
      close();
    }
  };

  const View = () => {
    const matches = memo(() => matching(commands(), query()));
    shown = () => matches().map((match) => match.c);
    const index = (command: Command): number => matches().findIndex((match) => match.c.id === command.id);
    const hits = (command: Command): number[] => matches().find((match) => match.c.id === command.id)?.hits ?? [];
    effect(() => {
      active();
      matches();
      if (!asking()) listbox?.querySelector(".palette-row.active")?.scrollIntoView({ block: "nearest" });
    });
    return (
      <dialog
        class="palette"
        aria-label="Command palette"
        ref={(element) => {
          dialog = element;
        }}
        onClick={(event) => {
          if (event.target === dialog) close();
        }}
        onCancel={(event) => {
          event.preventDefault();
          if (asking()) back();
          else close();
        }}
        onClose={() => {
          if (closed) return;
          closed = true;
          openInput = null;
          dispose();
          if (returnFocus && "focus" in returnFocus && typeof returnFocus.focus === "function") returnFocus.focus();
        }}
      >
        <div class="palette-input">
          <Icon paths={icons.search} />
          <input
            type="text"
            value={initial}
            placeholder={asking()?.ask?.placeholder ?? searchHint}
            aria-label="Command palette"
            autocomplete="off"
            spellcheck="false"
            role="combobox"
            aria-expanded="true"
            aria-controls="palette-list"
            aria-activedescendant={shown().length ? `pal-${active()}` : null}
            ref={(element) => {
              input = element;
            }}
            onInput={() => {
              if (!asking())
                batch(() => {
                  setActive(0);
                  setQuery(input.value);
                });
            }}
            onKeyDown={keydown}
          />
        </div>
        <div class={asking() ? "palette-ask on" : "palette-ask"}>
          <Show when={asking()}>
            <Icon paths={icons.arrow} />
            <span>{asking()?.ask?.prompt}</span>
            <small>↵ send · esc back</small>
          </Show>
        </div>
        <div
          id="palette-list"
          class="palette-list"
          role="listbox"
          hidden={asking() !== null}
          ref={(element) => {
            listbox = element;
          }}
        >
          <For
            each={shown()}
            key={(command) => command.id}
            fallback={
              <p class="palette-empty">
                {query().trim()
                  ? `Nothing matches “${query()
                      .replace(/^[>@#]/, "")
                      .trim()}”.`
                  : "Nothing here yet."}
              </p>
            }
          >
            {(command) => (
              <CommandRow
                command={command}
                index={index(command)}
                hits={hits(command)}
                active={active()}
                select={setActive}
                execute={execute}
              />
            )}
          </For>
        </div>
        <div class="palette-foot">
          <span>
            <kbd>↑</kbd>
            <kbd>↓</kbd> move
          </span>
          <span>
            <kbd>↵</kbd> run
          </span>
          <span>
            <kbd>esc</kbd> close
          </span>
          <span class="grow" />
          <span>
            <kbd>&gt;</kbd> commands
          </span>
          <span>
            <kbd>@</kbd> agents
          </span>
          <span>
            <kbd>#</kbd> files
          </span>
        </div>
      </dialog>
    );
  };
  dispose = attach(View, document.body);
  openInput = input;
  dialog.showModal();
  setCommands(provider());
  input.focus();
  input.setSelectionRange(input.value.length, input.value.length);
}

function CommandRow(props: {
  command: Command;
  index: number;
  hits: number[];
  active: number;
  select: (index: number) => void;
  execute: (command: Command) => void;
}) {
  return (
    <div
      id={`pal-${props.index}`}
      class={props.index === props.active ? "palette-row active" : "palette-row"}
      role="option"
      aria-selected={props.index === props.active ? "true" : "false"}
      onMouseMove={() => {
        if (props.index !== props.active) props.select(props.index);
      }}
      onMouseDown={(event) => event.preventDefault()}
      onClick={() => props.execute(props.command)}
    >
      <Icon paths={props.command.icon ?? kindIcon[props.command.kind]} />
      <span class="palette-title">
        <Highlight value={props.command.title} hits={props.hits} />
      </span>
      <Show when={props.command.detail}>
        <span class="palette-detail">{props.command.detail}</span>
      </Show>
      <Show when={props.command.keys}>
        <kbd>{props.command.keys}</kbd>
      </Show>
    </div>
  );
}
