import { afterEach, beforeEach, expect, test } from "bun:test";

import { render, type ViewNode } from "../herdsman-blocks-view.ts";
import { FakeElement as BaseElement, type FakeNode } from "../herdsman-blocks.testdom.ts";
import { icon, icons } from "../icons.ts";
import { type Member, roleName } from "../model.ts";
import { dispatch, FakeElement, FakeEvent, installDom } from "../testdom.ts";
import { pill } from "../ui.ts";

// What the panel needs beyond the test DOM: the store's URL and frames, the
// window's timers and selection, a resize observer, and fetch (pane reads
// never answer; actions are recorded and succeed).
const g = globalThis as unknown as Record<string, unknown>;
g["location"] ??= { search: "", pathname: "/" };
g["window"] ??= {};
Object.assign(g["window"] as object, {
  addEventListener: () => {},
  setTimeout: (fn: () => void, ms: number) => setTimeout(fn, ms),
  getSelection: () => ({ isCollapsed: true }),
});
g["history"] ??= { pushState: () => {}, replaceState: () => {} };
g["requestAnimationFrame"] ??= () => 0;
g["ResizeObserver"] ??= class {
  observe(): void {}
  disconnect(): void {}
};
const { AgentPanel } = await import("./agent.ts");
const { arrowKeys, keySet } = await import("../agent-actions.ts");
const { store } = await import("../store.ts");

// The panel's own elements are clones of its templates: the base fake DOM's
// elements, which keep properties as plain fields.
const proto = BaseElement.prototype as unknown as Record<string, unknown>;
const realFetch = globalThis.fetch;
let actions: unknown[] = [];
let panels: { destroy(): void }[] = [];
let focused: FakeElement | null = null;
beforeEach(() => {
  installDom();
  actions = [];
  focused = null;
  const doc = g["document"] as Record<string, unknown>;
  Object.defineProperty(doc, "activeElement", { get: () => focused, configurable: true });
  proto["focus"] = function (this: FakeElement) {
    focused = this;
  };
  proto["blur"] = function (this: FakeElement) {
    if (focused === this) focused = null;
  };
  proto["append"] = function (this: BaseElement, ...ns: FakeNode[]) {
    for (const n of ns) this.appendChild(n);
  };
  globalThis.fetch = (async (url: string, init?: RequestInit) => {
    if (!String(url).includes("/api/actions")) return new Promise(() => {});
    actions.push(JSON.parse(String(init?.body)));
    return new Response(JSON.stringify({ ok: true }));
  }) as unknown as typeof fetch;
  try {
    localStorage.clear();
  } catch {
    // No storage: the panel's choices last for the test only.
  }
});
afterEach(() => {
  for (const p of panels) p.destroy();
  panels = [];
  globalThis.fetch = realFetch;
  delete proto["focus"];
  delete proto["blur"];
  delete proto["append"];
});

const panel = (pane = "p7") => {
  const p = new AgentPanel(pane, () => ["claude"]);
  panels.push(p);
  return p;
};
const fake = (n: unknown): FakeElement => n as FakeElement;
const elements = (n: FakeNode): FakeElement[] => n.childNodes.filter((c): c is FakeElement => c.nodeType === 1);
const all = (n: FakeElement): FakeElement[] => [n, ...elements(n).flatMap(all)];
const byClass = (root: unknown, c: string): FakeElement => {
  const found = all(fake(root)).find((e) => e.getAttribute("class") === c);
  if (!found) throw new Error(`no .${c}`);
  return found;
};
// A key press with everything keyLike() reads (code, isComposing,
// getModifierState), which dispatch()'s events do not carry.
const key = (target: BaseElement, k: { key: string; code: string; ctrlKey?: boolean }): FakeEvent => {
  const e = Object.assign(new FakeEvent("keydown"), {
    ctrlKey: false,
    metaKey: false,
    altKey: false,
    shiftKey: false,
    isComposing: false,
    getModifierState: () => false,
    ...k,
  });
  target.dispatchEvent(e);
  return e;
};
const flush = () => new Promise((r) => setTimeout(r, 0));

// The attributes a browser reflects from the properties the panel assigns,
// which the test DOMs' elements keep as plain fields.
const reflected = (e: BaseElement): { name: string; value: string }[] =>
  e["disabled"] === true && e.getAttribute("disabled") === null ? [{ name: "disabled", value: "" }] : [];
// Elements and text with attributes sorted; empty text (a slot's anchor) is
// not content, and adjacent text joins.
const dump = (n: FakeNode): string => {
  if (n.nodeType === 3) return n.textContent;
  if (n.nodeType !== 1) return "";
  const e = fake(n);
  const attrs = [...e.attributes, ...reflected(e)]
    .map((a) => `${a.name}="${a.value}"`)
    .sort()
    .join(" ");
  return `<${e.tagName.toLowerCase()} ${attrs}>${e.childNodes.map(dump).join("")}</${e.tagName.toLowerCase()}>`;
};

// ---------------------------------------------------------------------------
// The panel as the h() version built it, as the browser shows it: h() set
// tabIndex, rows, spellcheck, type and disabled as properties, which the
// browser reflects as these attributes.
// ---------------------------------------------------------------------------

type Kid = FakeNode | string | null;
const el = (tag: string, attrs: Record<string, string> | null, ...kids: Kid[]): FakeElement => {
  const e = fake(document.createElement(tag));
  for (const [k, v] of Object.entries(attrs ?? {})) e.setAttribute(k, v);
  for (const k of kids) if (k !== null) e.append(k);
  return e;
};
const node = (v: ViewNode): FakeNode => {
  const box = document.createElement("div");
  render(box, v);
  const n = fake(box).firstChild!;
  fake(box).removeChild(n);
  return n;
};

function oldPanel(m: Member | null, o: { view: "log" | "terminal"; open: boolean; resizer: FakeNode }): FakeElement {
  const term = o.view === "terminal";
  const gone = m?.status === "gone";
  const tool = (glyph: readonly string[], label: string, danger = false) =>
    el(
      "button",
      { class: danger ? "icon-btn danger" : "icon-btn", title: label, "aria-label": label },
      node(icon(glyph)),
    );
  const head = el(
    "div",
    { class: "panel-head" },
    ...(m === null
      ? []
      : [
          el(
            "button",
            { class: "icon-btn", title: "Close panel", "aria-label": "Close panel" },
            node(icon(icons.close)),
          ),
          el(
            "div",
            { class: "panel-title" },
            el("h2", null, m.title),
            el(
              "div",
              { class: "sub" },
              node(pill(m.status)),
              m.role === "adjutant" || m.role === "orchestrator"
                ? el("span", { class: `chip role-${m.role}` }, roleName[m.role])
                : null,
              m.agent?.agent ? el("span", { class: "chip" }, m.agent.display_agent ?? m.agent.agent) : null,
            ),
          ),
          el(
            "div",
            { class: "panel-tools" },
            tool(icons.focus, "Focus in herdr"),
            tool(icons.play, "Start an agent in this pane"),
            tool(icons.tag, "Rename"),
            tool(icons.split, "Split right"),
            tool(icons.splitDown, "Split down"),
            tool(icons.trash, "Delete pane", true),
          ),
        ]),
  );
  const row = (k: string, v: string) => [el("dt", null, k), el("dd", { title: v }, v)];
  const meta = el(
    "dl",
    { class: "meta", id: `meta-${m?.pane_id ?? "p7"}`, ...(o.open ? {} : { hidden: "" }) },
    ...(m === null
      ? []
      : [
          ...row("Pane", m.pane_id),
          ...(m.tab ? row("Tab", m.tab.label || m.tab.tab_id) : []),
          ...(m.task?.task ? row("Task", m.task.task) : []),
          ...(m.task?.branch ? row("Branch", m.task.branch) : []),
          ...(m.task?.contract ? row("Contract", m.task.contract) : []),
          ...(m.pane?.cwd ? row("Folder", m.pane.cwd) : []),
        ]),
  );
  const viewBtn = (v: "log" | "terminal", glyph: readonly string[], label: string, tip: string) =>
    el(
      "button",
      {
        type: "button",
        title: tip,
        ...(v === o.view ? { class: "active" } : {}),
        "aria-pressed": String(v === o.view),
      },
      node(icon(glyph)),
      label,
    );
  return el(
    "aside",
    { class: ["panel", term && "terminal-view"].filter(Boolean).join(" "), "aria-label": "Agent" },
    head,
    el(
      "div",
      { class: "pane-bar" },
      el(
        "button",
        {
          type: "button",
          class: "pane-info",
          title: "Show or hide the pane's details",
          "aria-controls": meta.getAttribute("id")!,
          "aria-expanded": String(o.open),
        },
        node(icon(icons.chevronDown)),
        el("span", { class: "pane-info-label" }, "Details"),
        el(
          "span",
          { class: "pane-info-line" },
          m ? [m.pane_id, m.tab ? m.tab.label || m.tab.tab_id : ""].filter(Boolean).join(" · ") : "",
        ),
      ),
      el("div", { class: "term-hint" }, term ? "Click the screen to type" : ""),
      el(
        "div",
        { class: "view-switch", role: "group", "aria-label": "Pane view" },
        viewBtn("log", icons.list, "Log", "The pane's recent output, with a box to message the agent"),
        viewBtn(
          "terminal",
          icons.terminal,
          "Terminal",
          "The pane's screen: type straight into it, and the wheel scrolls it",
        ),
      ),
    ),
    meta,
    el(
      "div",
      { class: "tail-wrap" },
      el("pre", {
        class: "tail",
        tabindex: "0",
        "aria-label": term ? "Pane screen: press Enter to type into it" : "Pane output",
      }),
      el("div", { class: "tail-note" }),
      el("textarea", {
        class: "term-input",
        rows: "1",
        autocapitalize: "off",
        autocomplete: "off",
        autocorrect: "off",
        "aria-label": "Type into the pane",
        spellcheck: "false",
        ...(!term || gone ? { disabled: "" } : {}),
      }),
    ),
    el(
      "div",
      { class: "keys", role: "group", "aria-label": "Send a key" },
      node(icon(icons.keyboard)),
      ...keySet.map((k) => el("button", { class: "key", title: k.hint }, k.label)),
      el(
        "span",
        { class: "key-group", role: "group", "aria-label": "Arrow keys" },
        ...arrowKeys.map((k) =>
          el("button", { class: "key arrow", title: `${k.name} arrow`, "aria-label": `${k.name} arrow` }, k.label),
        ),
      ),
      el("button", { class: "key", title: "Send other key names, separated by spaces" }, "Other…"),
    ),
    el(
      "form",
      { class: "composer" },
      el("textarea", {
        rows: "3",
        placeholder: "Message this agent…  (⌘/Ctrl+Enter to send)",
        spellcheck: "true",
        "aria-label": "Message",
        ...(gone ? { disabled: "" } : {}),
      }),
      el(
        "div",
        { class: "composer-bar" },
        el(
          "label",
          { class: "select" },
          el(
            "select",
            { "aria-label": "Send as", title: "Prompt goes through the agent; Type sends raw text into the pane" },
            el("option", { value: "prompt" }, "Prompt"),
            el("option", { value: "text" }, "Type raw"),
          ),
          node(icon(icons.chevronDown)),
        ),
        el("span", { class: "grow" }),
        el(
          "button",
          { class: "btn primary", type: "submit", ...(gone ? { disabled: "" } : {}) },
          node(icon(icons.send)),
          "Send",
        ),
      ),
    ),
    el("div", { class: "panel-want", "aria-hidden": "true" }),
    o.resizer,
  );
}

const member: Member = {
  pane_id: "p7",
  role: "orchestrator",
  title: "Lead",
  status: "working",
  task: { task: "Ship it", branch: "main", worktree: "", agent: "", pane: "p7", contract: "c.md" },
  agent: {
    pane_id: "p7",
    tab_id: "t1",
    workspace_id: "w1",
    name: null,
    agent: "codex",
    display_agent: null,
    agent_status: "working",
    focused: false,
    cwd: null,
    terminal_title_stripped: null,
    interactive_ready: true,
    launch_pending: false,
    state_change_seq: 1,
  },
  pane: null,
  tab: { tab_id: "t1", workspace_id: "w1", number: 1, label: "", focused: false, pane_count: 1, agent_status: "idle" },
};
const plain: Member = { ...member, role: "implementer", agent: null, task: null, tab: null, status: "gone" };

// A copy of the panel's resize handle (resize.ts makes it), for the reference.
const resizerOf = (p: { el: HTMLElement }) => elements(fake(p.el)).at(-1)!.cloneNode(true);

test("the panel renders what the h() version did, before and after update", () => {
  const p = panel();
  expect(dump(fake(p.el))).toBe(dump(oldPanel(null, { view: "log", open: false, resizer: resizerOf(p) })));
  p.update(member, null);
  expect(dump(fake(p.el))).toBe(dump(oldPanel(member, { view: "log", open: false, resizer: resizerOf(p) })));
  p.update(plain, null);
  expect(dump(fake(p.el))).toBe(dump(oldPanel(plain, { view: "log", open: false, resizer: resizerOf(p) })));
});

test("an update patches the panel in place and keeps the owned elements", () => {
  const p = panel();
  const el = p.el;
  p.update(member, null);
  const title = byClass(el, "panel-title");
  const tail = byClass(el, "tail");
  const input = byClass(el, "term-input");
  p.update({ ...member, title: "Lead two", status: "idle" }, null);
  expect(p.el).toBe(el);
  expect(byClass(el, "panel-title")).toBe(title);
  expect(elements(title)[0]!.textContent).toBe("Lead two");
  expect(byClass(el, "tail")).toBe(tail);
  expect(byClass(el, "term-input")).toBe(input);
});

test("the details fold open and closed", () => {
  const p = panel();
  p.update(member, null);
  const info = byClass(p.el, "pane-info");
  dispatch(info, "click");
  expect(info.getAttribute("aria-expanded")).toBe("true");
  expect(byClass(p.el, "meta").getAttribute("hidden")).toBe(null);
  expect(dump(fake(p.el))).toBe(dump(oldPanel(member, { view: "log", open: true, resizer: resizerOf(p) })));
  dispatch(info, "click");
  expect(info.getAttribute("aria-expanded")).toBe("false");
  expect(byClass(p.el, "meta").getAttribute("hidden")).toBe("");
});

test("the terminal view takes typing and keeps keys from the page", async () => {
  const p = panel();
  p.update(member, null);
  const [, termBtn] = elements(byClass(p.el, "view-switch"));
  dispatch(termBtn!, "click");
  const input = byClass(p.el, "term-input");
  expect(focused).toBe(input);
  dispatch(input, "focus"); // what the browser fires on focus()
  expect(input.disabled).toBe(false);
  expect(fake(p.el).getAttribute("class")).toBe("panel terminal-view typing");
  expect(byClass(p.el, "term-hint").textContent).toBe("Typing into the pane · click outside to stop");
  focused = null;
  dispatch(input, "blur");
  expect(dump(fake(p.el))).toBe(dump(oldPanel(member, { view: "terminal", open: false, resizer: resizerOf(p) })));

  // A key the pane takes does not reach the page's shortcuts above the panel.
  const page = new BaseElement("BODY");
  page.appendChild(fake(p.el));
  const seen: string[] = [];
  page.addEventListener("keydown", (ev) => seen.push(ev.type));
  const e = key(input, { key: "Escape", code: "Escape" });
  expect(e.defaultPrevented).toBe(true);
  expect(e.cancelBubble).toBe(true);
  expect(seen).toEqual([]);
  // In the log view the typing field lets keys through.
  key(byClass(p.el, "tail"), { key: "a", code: "KeyA" });
  expect(seen).toEqual(["keydown"]);
  input.value = "ls";
  dispatch(input, "input");
  expect(input.value).toBe("");
  // Composed text (IME, dictation) waits for the composition to end.
  input.value = "日本";
  dispatch(input, "input", { isComposing: true });
  expect(input.value).toBe("日本");
  dispatch(input, "compositionend");
  expect(input.value).toBe("");
  await flush();
  expect(actions).toEqual([
    { type: "input", pane: "p7", keys: ["esc"] },
    { type: "input", pane: "p7", text: "ls日本" }, // queued text goes out together
  ]);

  // Back to the log view: the typing field is off and keys go to the pane as actions.
  dispatch(elements(byClass(p.el, "view-switch"))[0]!, "click");
  expect(input.disabled).toBe(true);
  const keys = elements(byClass(p.el, "keys"));
  dispatch(
    keys.find((k) => k.textContent === "Esc")!,
    "click",
  );
  await flush();
  expect(actions.at(-1)).toEqual({ type: "keys", pane: "p7", keys: ["esc"] });
});

test("the composer sends with Ctrl+Enter and submit, and the close button closes the panel", async () => {
  const p = panel();
  p.update(member, null, true);
  const box = elements(byClass(p.el, "composer"))[0]!;
  expect(box["placeholder"]).toBe(
    "This route has an adjutant: talk through it. Text here bypasses the message channel.",
  );
  box.value = "hello";
  dispatch(box, "keydown", { key: "Enter", ctrlKey: true });
  const send = all(byClass(p.el, "composer-bar")).find((e) => e.tagName === "BUTTON")!;
  expect(send.getAttribute("disabled")).toBe("");
  await flush();
  await flush();
  expect(actions).toEqual([{ type: "prompt", pane: "p7", text: "hello" }]);
  expect(send.getAttribute("disabled")).toBe(null);
  expect(focused).toBe(box);

  box.value = "again";
  dispatch(byClass(p.el, "composer"), "submit");
  await flush();
  await flush();
  expect(actions.at(-1)).toEqual({ type: "prompt", pane: "p7", text: "again" });

  store.app.view.pane = "p7";
  dispatch(
    all(byClass(p.el, "panel-head")).find((e) => e.getAttribute("title") === "Close panel")!,
    "click",
  );
  expect(store.app.view.pane).toBe("");
});
