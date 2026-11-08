// The page shell every app shares: a toolbar of operation buttons the runner
// clicks, and the route body the app renders into. The app gets a fresh
// snapshot on every click and never learns which button caused it.

import { World, type State } from "./state.ts";

export interface App {
  /** Renders the snapshot into the root given to createApp. Called once per click. */
  render(s: State): void;
}

/** Each app's entry calls this with its factory. onSelect is what a card's click calls. */
export function start(createApp: (root: HTMLElement, onSelect: (pane: string) => void) => App): void {
  const root = document.getElementById("main")!;
  let world: World | null = null;
  const app = createApp(root, (pane) => {
    if (!world) return;
    world.s.selected = pane;
    app.render(world.snapshot());
  });
  const toolbar = document.getElementById("toolbar")!;
  const button = (id: string, run: () => void) => {
    const b = document.createElement("button");
    b.id = id;
    b.type = "button";
    b.textContent = id;
    b.addEventListener("click", run);
    toolbar.append(b);
  };
  button("board", () => {
    world = new World();
    app.render(world.snapshot());
  });
  for (const name of Object.keys(new World().ops)) {
    button(name, () => {
      if (!world) return;
      world.ops[name]!();
      app.render(world.snapshot());
    });
  }
}

declare global {
  interface Window {
    /** Read by the runner: the rendered DOM in a canonical form, and node identity checks. */
    __bench: { canon(): string; tag(): number; kept(): number };
  }
}

/** Tags, sorted attributes and text; empty class attributes, data-key, comments and blank text dropped. */
function canon(n: Node, out: string[], depth: number): void {
  if (n.nodeType === 3) {
    const t = n.textContent ?? "";
    if (t.trim()) out.push(`${" ".repeat(depth)}"${t}"`);
    return;
  }
  if (n.nodeType !== 1) return;
  const el = n as Element;
  const attrs = [...el.attributes]
    .filter((a) => a.name !== "data-key" && !(a.name === "class" && !a.value.trim()))
    .map((a) => `${a.name}=${JSON.stringify(a.name === "class" ? a.value.trim().split(/\s+/).sort().join(" ") : a.value)}`)
    .sort();
  out.push(`${" ".repeat(depth)}<${el.localName}${attrs.length ? " " + attrs.join(" ") : ""}>`);
  // Adjacent text nodes join, so apps may split text however they like.
  let text = "";
  const flush = () => {
    if (text.trim()) out.push(`${" ".repeat(depth + 1)}"${text}"`);
    text = "";
  };
  for (const c of el.childNodes) {
    if (c.nodeType === 3) text += c.textContent ?? "";
    else if (c.nodeType === 1) {
      flush();
      canon(c, out, depth + 1);
    }
  }
  flush();
}

const MARK = Symbol("bench-mark");
const marked = () => [...document.querySelectorAll("#main *")] as (Element & { [MARK]?: true })[];

window.__bench = {
  canon() {
    const out: string[] = [];
    for (const c of document.getElementById("main")!.childNodes) canon(c, out, 0);
    return out.join("\n");
  },
  /** Marks every element under #main; returns how many. */
  tag() {
    const all = marked();
    for (const el of all) el[MARK] = true;
    return all.length;
  },
  /** How many elements under #main carry a mark from the last tag(). */
  kept() {
    return marked().filter((el) => el[MARK]).length;
  },
};
