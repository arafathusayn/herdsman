// Terminal mode: turns browser keys and wheel turns into herdr input, and
// sends it in order without flooding the server.

/** One thing to send: herdr key names, literal text, or a wheel scroll. */
export type Input = { keys: string[] } | { text: string } | { lines: number; col: number; row: number };

/** Keys herdr has no name for go as the bytes an xterm sends for them. */
const rawKeys: Record<string, string> = {
  Delete: "\x1b[3~",
  Home: "\x1b[H",
  End: "\x1b[F",
  PageUp: "\x1b[5~",
  PageDown: "\x1b[6~",
  Insert: "\x1b[2~",
};

const namedKeys: Record<string, string> = {
  Enter: "enter",
  Tab: "tab",
  Escape: "esc",
  Backspace: "backspace",
  ArrowUp: "up",
  ArrowDown: "down",
  ArrowLeft: "left",
  ArrowRight: "right",
};

/** ⌘ line editing on a Mac, as Terminal.app and iTerm2 send it: the readline keys for the same edit. */
const macCommandKeys: Record<string, string> = {
  Backspace: "ctrl+u",
  Delete: "ctrl+k",
  ArrowLeft: "ctrl+a",
  ArrowRight: "ctrl+e",
};

export interface KeyLike {
  key: string;
  code: string;
  ctrlKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
  metaKey: boolean;
  isComposing?: boolean;
  /** AltGr is held: the layout types a character with it (Windows, Linux). */
  altGraph?: boolean;
}

/**
 * What a key press sends, or null when the browser should keep it. Plain
 * typing is left to the input event, so IME and phone keyboards work. ⌘
 * shortcuts stay with the browser (copy, paste, the palette), except ⌘ line
 * editing on a Mac. On a Mac, Option with a character types that character,
 * as in Terminal.app.
 */
export function keyInput(e: KeyLike, mac: boolean): Input | null {
  if (e.isComposing || e.altGraph) return null;
  if (e.metaKey) {
    const edit = mac && !e.ctrlKey && !e.altKey && !e.shiftKey ? macCommandKeys[e.key] : undefined;
    return edit ? { keys: [edit] } : null;
  }
  const raw = rawKeys[e.key];
  if (raw) return { text: raw };
  const mods = (e.ctrlKey ? "ctrl+" : "") + (e.altKey ? "alt+" : "");
  const name = namedKeys[e.key] ?? (/^F([1-9]|1[0-2])$/.test(e.key) ? e.key.toLowerCase() : undefined);
  if (name) {
    if (name === "tab" && e.shiftKey && !mods) return { keys: ["shift+tab"] };
    const shift = e.shiftKey && name !== "tab" ? "shift+" : "";
    return { keys: [mods + shift + name] };
  }
  if (!e.ctrlKey && !e.altKey) return null;
  if (e.altKey && !e.ctrlKey && mac) return null;
  // Ctrl+Shift+B toggles the sidebar off a Mac, even while typing here.
  if (!mac && e.ctrlKey && e.shiftKey && !e.altKey && e.code === "KeyB") return null;
  let ch = "";
  if (e.code.startsWith("Key")) ch = e.code.slice(3).toLowerCase();
  else if (e.code.startsWith("Digit")) ch = e.code.slice(5);
  else if (e.key === " ") ch = "space";
  else if (e.key.length === 1) ch = e.key.toLowerCase();
  return ch ? { keys: [mods + ch] } : null;
}

export type Zoom = "in" | "out" | "reset";

/**
 * ⌘ + / − / 0 on a Mac, Ctrl elsewhere: the terminal view's own zoom, as in
 * a terminal app, instead of the page's. The typed character decides first,
 * so layouts with + or = elsewhere (German + is its own key, = is Shift+0)
 * work; the key's place is the fallback for a character the modifier
 * changed. Ctrl+Shift+− stays the pane's (readline's undo is Ctrl+_).
 */
export function zoomKey(e: KeyLike, mac: boolean): Zoom | null {
  const mod = mac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
  if (!mod || e.altKey || e.isComposing) return null;
  if (e.key === "+" || e.key === "=" || e.code === "NumpadAdd") return "in";
  if (e.key === "-" || e.code === "NumpadSubtract") return "out";
  if (e.key === "0" || e.code === "Numpad0") return "reset";
  if (e.shiftKey) return null;
  if (e.code === "Equal") return "in";
  if (e.code === "Minus") return "out";
  if (e.code === "Digit0") return "reset";
  return null;
}

const zoomStore = "herdsman.term-zoom";

/** The terminal view's saved zoom; 1 when none is saved or it is not a level. */
export function loadZoom(): number {
  try {
    const z = Number(localStorage.getItem(zoomStore));
    return zoomSteps.includes(z) ? z : 1;
  } catch {
    return 1;
  }
}

export function saveZoom(z: number): void {
  try {
    if (z === 1) localStorage.removeItem(zoomStore);
    else localStorage.setItem(zoomStore, String(z));
  } catch {
    // The zoom lasts for this page only.
  }
}

/** The terminal view's zoom levels, as browsers step theirs. */
const zoomSteps = [0.5, 0.67, 0.75, 0.8, 0.9, 1, 1.1, 1.25, 1.5, 1.75, 2, 2.5, 3];

/** The zoom one step in or out from z, or 1 on reset. */
export function nextZoom(z: number, how: Zoom): number {
  if (how === "reset") return 1;
  if (how === "in") return zoomSteps.find((s) => s > z + 0.001) ?? zoomSteps.at(-1) ?? z;
  return zoomSteps.findLast((s) => s < z - 0.001) ?? zoomSteps[0] ?? z;
}

/** Text from the input field as a terminal takes it: line feeds, no CR LF pairs. */
export function typedText(value: string): string {
  return value.replace(/\r\n?/g, "\n");
}

/** About three lines per wheel notch, the usual terminal speed. */
const pxPerLine = 33;

/**
 * Wheel deltas in whole lines, keeping the fraction for the next event, so
 * a trackpad's small steps add up. Positive lines scroll back (wheel up).
 */
export class WheelLines {
  private acc = 0;

  add(deltaY: number, deltaMode: number, pageLines: number): number {
    const lines = deltaMode === 1 ? deltaY : deltaMode === 2 ? deltaY * pageLines : deltaY / pxPerLine;
    this.acc += lines;
    const n = Math.trunc(this.acc);
    this.acc -= n;
    return n === 0 ? 0 : -n;
  }
}

/**
 * Sends input one request at a time, in order. Whatever queues while a
 * request is in flight merges with its neighbor of the same kind, so fast
 * typing and wheel spins become a few requests, not hundreds.
 */
export class InputQueue {
  private queue: Input[] = [];
  private busy = false;

  constructor(private readonly send: (i: Input) => Promise<boolean>) {}

  push(i: Input): void {
    const last = this.queue.at(-1);
    if (last && "text" in last && "text" in i) last.text += i.text;
    else if (last && "keys" in last && "keys" in i) last.keys.push(...i.keys);
    else if (last && "lines" in last && "lines" in i) {
      last.lines += i.lines;
      last.col = i.col;
      last.row = i.row;
    } else this.queue.push("keys" in i ? { keys: [...i.keys] } : { ...i });
    void this.flush();
  }

  private async flush(): Promise<void> {
    if (this.busy) return;
    this.busy = true;
    try {
      for (let i = this.queue.shift(); i; i = this.queue.shift()) {
        if ("lines" in i && i.lines === 0) continue;
        // A failed send drops what queued after it: typing on past an
        // error would land keys out of context.
        if (!(await this.send(i))) this.queue = [];
      }
    } finally {
      this.busy = false;
    }
  }
}
