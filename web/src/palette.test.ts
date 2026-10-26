import { afterEach, beforeEach, expect, test } from "bun:test";

import { h, icon, icons, unkeyed } from "./dom.ts";
import { highlight } from "./fuzzy.ts";
import { FakeElement, type FakeNode, installFakeDocument } from "./herdsman-blocks.testdom.ts";
import { type Command, openPalette, paletteOpen } from "./palette.ts";
import { dispatch } from "./testdom.ts";

const fake = (n: unknown): FakeElement => n as FakeElement;
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

const dump = (n: FakeNode): string => {
  if (n.nodeType === 3) return n.textContent;
  if (n.nodeType !== 1) return "";
  const e = fake(n);
  const attrs = e.attributes
    .filter((a) => a.name !== "data-key")
    .map((a) => `${a.name}="${a.value}"`)
    .sort()
    .join(" ");
  return `<${e.tagName.toLowerCase()} ${attrs}>${e.childNodes.map(dump).join("")}</${e.tagName.toLowerCase()}>`;
};
const elements = (n: FakeNode): FakeElement[] => n.childNodes.filter((c): c is FakeElement => c.nodeType === 1);

const kindIcon = { command: icons.command, agent: icons.users, route: icons.list, file: icons.file };

// The old h() palette, with its properties written as the attributes the
// browser reflects them to (type, placeholder, spellcheck, hidden).
const oldPalette = (
  rows: { c: Command; hits: number[] }[],
  active: number,
  opts: { ask?: string; placeholder?: string; empty?: string } = {},
): HTMLElement =>
  h(
    "dialog",
    { class: "palette", "aria-label": "Command palette" },
    h(
      "div",
      { class: "palette-input" },
      icon(icons.search),
      h("input", {
        type: "text",
        placeholder: opts.placeholder ?? "Search agents, routes and files, or type > for commands",
        "aria-label": "Command palette",
        autocomplete: "off",
        spellcheck: "false",
        role: "combobox",
        "aria-expanded": "true",
        "aria-controls": "palette-list",
        "aria-activedescendant": rows.length ? `pal-${active}` : null,
      }),
    ),
    opts.ask
      ? h(
          "div",
          { class: "palette-ask on" },
          icon(icons.arrow),
          h("span", null, opts.ask),
          h("small", null, "↵ send · esc back"),
        )
      : h("div", { class: "palette-ask" }),
    h(
      "div",
      { id: "palette-list", class: "palette-list", role: "listbox", hidden: !!opts.ask },
      rows.length
        ? unkeyed(
            rows.map(({ c, hits }, i) =>
              h(
                "div",
                {
                  id: `pal-${i}`,
                  class: i === active ? "palette-row active" : "palette-row",
                  role: "option",
                  "aria-selected": String(i === active),
                },
                icon(c.icon ?? kindIcon[c.kind]),
                h("span", { class: "palette-title" }, highlight(c.title, hits)),
                c.detail ? h("span", { class: "palette-detail" }, c.detail) : null,
                c.keys ? h("kbd", null, c.keys) : null,
              ),
            ),
          )
        : h("p", { class: "palette-empty" }, opts.empty ?? "Nothing here yet."),
    ),
    h(
      "div",
      { class: "palette-foot" },
      h("span", null, h("kbd", null, "↑"), h("kbd", null, "↓"), " move"),
      h("span", null, h("kbd", null, "↵"), " run"),
      h("span", null, h("kbd", null, "esc"), " close"),
      h("span", { class: "grow" }),
      h("span", null, h("kbd", null, ">"), " commands"),
      h("span", null, h("kbd", null, "@"), " agents"),
      h("span", null, h("kbd", null, "#"), " files"),
    ),
  );

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
  const all = cmds.map((c) => ({ c, hits: [] }));
  expect(dump(dlg)).toBe(dump(fake(oldPalette(all, 0))));
  const input = elements(elements(dlg)[0]!)[1]!;
  const listbox = elements(dlg)[2]!;
  const [rowA, rowB, rowC] = elements(listbox);

  // Moving the selection patches the rows in place.
  dispatch(input, "keydown", { key: "ArrowDown" } as never);
  expect(dump(dlg)).toBe(dump(fake(oldPalette(all, 1))));
  expect(elements(listbox)).toEqual([rowA!, rowB!, rowC!]);
  dispatch(rowC!, "mousemove");
  expect(dump(dlg)).toBe(dump(fake(oldPalette(all, 2))));

  // Filtering keeps the rows that stay; marked titles come from highlight().
  input["value"] = ">";
  dispatch(input, "input");
  expect(dump(dlg)).toBe(dump(fake(oldPalette([all[1]!, all[2]!], 0))));
  expect(elements(listbox)).toEqual([rowB!, rowC!]);
  input["value"] = "zzz";
  dispatch(input, "input");
  expect(dump(dlg)).toBe(dump(fake(oldPalette([], 0, { empty: "Nothing matches “zzz”." }))));

  // A command that asks turns the box into an input; Esc goes back.
  input["value"] = "prompt";
  dispatch(input, "input");
  dispatch(input, "keydown", { key: "Enter" } as never);
  const rows = elements(listbox);
  expect(input["placeholder"]).toBe("What to do");
  expect(elements(dlg)[1]!.className).toBe("palette-ask on");
  expect(listbox.hasAttribute("hidden")).toBe(true);
  expect(elements(listbox)).toEqual(rows); // kept, hidden
  dispatch(input, "keydown", { key: "Escape" } as never);
  expect(dump(dlg)).toBe(
    dump(fake(oldPalette(all, 0, { placeholder: "Search agents, routes and files, or type > for commands" }))),
  );

  // Asking again and sending closes the palette.
  input["value"] = "prompt";
  dispatch(input, "input");
  dispatch(rowC!, "click");
  input["value"] = "fix the tests";
  dispatch(input, "keydown", { key: "Enter" } as never);
  expect(sent).toEqual(["fix the tests"]);
  expect(paletteOpen()).toBe(false);
  expect(elements(body)).toEqual([]);
  expect(ran).toEqual([]);
});
