import { afterEach, beforeEach, expect, test } from "bun:test";

import { render } from "./herdsman-blocks-view.ts";
import { FakeElement, type FakeNode, installFakeDocument } from "./herdsman-blocks.testdom.ts";
import { icon, icons } from "./icons.ts";
import { type Command, openPalette, paletteOpen } from "./palette.ts";
import { dispatch, dump, markup } from "./testdom.ts";

const g = globalThis as unknown as { document: Record<string, unknown> };
const proto = FakeElement.prototype as unknown as Record<string, unknown>;
const stubs: Record<string, (this: FakeElement, ...a: never[]) => unknown> = {
  showModal() {
    this["open"] = true;
  },
  close() {
    this["open"] = false;
    dispatch(this, "close");
  },
  append(n: FakeNode) {
    this.appendChild(n);
  },
  remove() {
    this.parentNode?.removeChild(this);
  },
  focus() {},
  setSelectionRange() {},
  querySelector() {
    return null;
  },
};
let body: FakeElement;
beforeEach(() => {
  installFakeDocument();
  body = new FakeElement("BODY");
  g.document["body"] = body;
  Object.assign(proto, stubs);
});
afterEach(() => {
  for (const k in stubs) delete proto[k];
});

const elements = (n: FakeNode): FakeElement[] => n.childNodes.filter((c): c is FakeElement => c.nodeType === 1);
const iconMarkup = (paths: readonly string[], label?: string): string => {
  const box = document.createElement("div");
  render(box, icon(paths, label));
  return dump(box.firstChild as unknown as FakeNode);
};

test("palette: markup of the h() version, filtering and moving keep row nodes, asking and back", () => {
  const sent: string[] = [];
  const ran: string[] = [];
  const cmds: Command[] = [
    { id: "a", kind: "agent", title: "Agent: orch", detail: "w1:p1", run: () => ran.push("a") },
    { id: "b", kind: "command", title: "Toggle the sidebar", keys: "⌘B", run: () => ran.push("b") },
    {
      id: "c",
      kind: "command",
      title: "Prompt orch",
      ask: { prompt: "Prompt for orch", placeholder: "What to do", submit: (t) => sent.push(t) },
    },
  ];
  openPalette(() => cmds);
  expect(paletteOpen()).toBe(true);
  const dlg = elements(body)[0]!;
  // The palette around its list: the input, pointing at the cursor's row if
  // there is one, the ask line, and the key hints.
  const palette = (cursor: string | null, list: string): string => markup`
    <dialog aria-label="Command palette" class="palette">
      <div class="palette-input">
        ${iconMarkup(icons.search)}
        <input${cursor ? ` aria-activedescendant="${cursor}"` : ""} aria-controls="palette-list" aria-expanded="true" aria-label="Command palette" autocomplete="off" placeholder="Search agents, routes and files, or type > for commands" role="combobox" spellcheck="false" type="text"></input>
      </div>
      <div class="palette-ask"></div>
      <div class="palette-list" id="palette-list" role="listbox">${list}</div>
      <div class="palette-foot">
        <span><kbd>↑</kbd><kbd>↓</kbd> move</span>
        <span><kbd>↵</kbd> run</span>
        <span><kbd>esc</kbd> close</span>
        <span class="grow"></span>
        <span><kbd>></kbd> commands</span>
        <span><kbd>@</kbd> agents</span>
        <span><kbd>#</kbd> files</span>
      </div>
    </dialog>
  `;
  // A row: pal-<i>, with the cursor or not, and what it shows.
  const row = (i: number, cursor: boolean, shows: string): string =>
    `<div aria-selected="${cursor}" class="palette-row${cursor ? " active" : ""}" id="pal-${i}" role="option">${shows}</div>`;
  const agentOrch = `${iconMarkup(icons.users)}<span class="palette-title">Agent: orch</span><span class="palette-detail">w1:p1</span>`;
  const toggle = `${iconMarkup(icons.command)}<span class="palette-title">Toggle the sidebar</span><kbd>⌘B</kbd>`;
  const promptOrch = `${iconMarkup(icons.command)}<span class="palette-title">Prompt orch</span>`;
  expect(dump(dlg)).toBe(palette("pal-0", row(0, true, agentOrch) + row(1, false, toggle) + row(2, false, promptOrch)));
  const input = elements(elements(dlg)[0]!)[1]!;
  const listbox = elements(dlg)[2]!;
  const [rowA, rowB, rowC] = elements(listbox);

  // Moving the selection patches the rows in place.
  dispatch(input, "keydown", { key: "ArrowDown" });
  expect(dump(dlg)).toBe(palette("pal-1", row(0, false, agentOrch) + row(1, true, toggle) + row(2, false, promptOrch)));
  expect(elements(listbox)).toEqual([rowA!, rowB!, rowC!]);
  dispatch(rowC!, "mousemove");
  expect(dump(dlg)).toBe(palette("pal-2", row(0, false, agentOrch) + row(1, false, toggle) + row(2, true, promptOrch)));

  // Filtering keeps the rows that stay; marked titles come from highlight().
  input["value"] = ">";
  dispatch(input, "input");
  expect(dump(dlg)).toBe(palette("pal-0", row(0, true, toggle) + row(1, false, promptOrch)));
  expect(elements(listbox)).toEqual([rowB!, rowC!]);
  input["value"] = "zzz";
  dispatch(input, "input");
  expect(dump(dlg)).toBe(palette(null, '<p class="palette-empty">Nothing matches “zzz”.</p>'));

  // A command that asks turns the box into an input; Esc goes back.
  input["value"] = "prompt";
  dispatch(input, "input");
  dispatch(input, "keydown", { key: "Enter" });
  const rows = elements(listbox);
  expect(input["placeholder"]).toBe("What to do");
  expect(elements(dlg)[1]!.className).toBe("palette-ask on");
  expect(listbox.hasAttribute("hidden")).toBe(true);
  expect(elements(listbox)).toEqual(rows); // kept, hidden
  dispatch(input, "keydown", { key: "Escape" });
  expect(dump(dlg)).toBe(palette("pal-0", row(0, true, agentOrch) + row(1, false, toggle) + row(2, false, promptOrch)));

  // Asking again and sending closes the palette.
  input["value"] = "prompt";
  dispatch(input, "input");
  dispatch(elements(listbox)[0]!, "click"); // the row "prompt" left (rowC left with "zzz")
  input["value"] = "fix the tests";
  dispatch(input, "keydown", { key: "Enter" });
  expect(sent).toEqual(["fix the tests"]);
  expect(paletteOpen()).toBe(false);
  expect(elements(body)).toEqual([]);
  expect(ran).toEqual([]);
});
