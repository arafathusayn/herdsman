import { expect, test } from "bun:test";

import {
  type Input,
  InputQueue,
  keyInput,
  type KeyLike,
  loadZoom,
  nextZoom,
  saveZoom,
  typedText,
  WheelLines,
  zoomKey,
} from "./terminal.ts";

const key = (k: Partial<KeyLike> & { key: string }): KeyLike => ({
  code: "",
  ctrlKey: false,
  altKey: false,
  shiftKey: false,
  metaKey: false,
  ...k,
});

test("keys map to herdr names, raw bytes, or the browser", () => {
  const cases: [KeyLike, boolean, Input | null][] = [
    [key({ key: "a", code: "KeyA" }), true, null],
    [key({ key: "Enter" }), true, { keys: ["enter"] }],
    [key({ key: "Enter", shiftKey: true }), true, { keys: ["shift+enter"] }],
    [key({ key: "Backspace", altKey: true }), true, { keys: ["alt+backspace"] }],
    [key({ key: "Tab", shiftKey: true }), true, { keys: ["shift+tab"] }],
    [key({ key: "Escape" }), true, { keys: ["esc"] }],
    [key({ key: "ArrowLeft", ctrlKey: true, shiftKey: true }), true, { keys: ["ctrl+shift+left"] }],
    [key({ key: "F5" }), true, { keys: ["f5"] }],
    [key({ key: "PageUp" }), true, { text: "\x1b[5~" }],
    [key({ key: "Delete" }), true, { text: "\x1b[3~" }],
    [key({ key: "c", code: "KeyC", ctrlKey: true }), true, { keys: ["ctrl+c"] }],
    [key({ key: "C", code: "KeyC", ctrlKey: true, shiftKey: true }), true, { keys: ["ctrl+c"] }],
    [key({ key: "]", code: "BracketRight", ctrlKey: true }), true, { keys: ["ctrl+]"] }],
    [key({ key: " ", code: "Space", ctrlKey: true }), true, { keys: ["ctrl+space"] }],
    // Option types characters on a Mac; Alt is a modifier elsewhere.
    [key({ key: "∫", code: "KeyB", altKey: true }), true, null],
    [key({ key: "b", code: "KeyB", altKey: true }), false, { keys: ["alt+b"] }],
    [key({ key: "ArrowLeft", altKey: true }), true, { keys: ["alt+left"] }],
    // ⌘ shortcuts, AltGr characters and IME composition stay with the browser.
    [key({ key: "v", code: "KeyV", metaKey: true }), true, null],
    // ⌘ line editing on a Mac, as Terminal.app sends it.
    [key({ key: "Backspace", metaKey: true }), true, { keys: ["ctrl+u"] }],
    [key({ key: "ArrowLeft", metaKey: true }), true, { keys: ["ctrl+a"] }],
    [key({ key: "ArrowRight", metaKey: true }), true, { keys: ["ctrl+e"] }],
    [key({ key: "Backspace", metaKey: true }), false, null],
    [key({ key: "@", code: "KeyQ", ctrlKey: true, altKey: true, altGraph: true }), false, null],
    [key({ key: "Enter", isComposing: true }), true, null],
    // Off a Mac, Ctrl+Shift+B is the sidebar's; plain Ctrl+B stays the pane's.
    [key({ key: "B", code: "KeyB", ctrlKey: true, shiftKey: true }), false, null],
    [key({ key: "b", code: "KeyB", ctrlKey: true }), false, { keys: ["ctrl+b"] }],
    [key({ key: "B", code: "KeyB", ctrlKey: true, shiftKey: true }), true, { keys: ["ctrl+b"] }],
  ];
  for (const [e, mac, want] of cases) expect([e.key, keyInput(e, mac)]).toEqual([e.key, want]);
});

test("typed text uses line feeds", () => {
  expect(typedText("a\r\nb\rc\nd")).toBe("a\nb\nc\nd");
});

test("wheel deltas add up to whole lines, up is positive", () => {
  const w = new WheelLines();
  expect(w.add(-100, 0, 40)).toBe(3);
  expect(w.add(10, 0, 40)).toBe(0);
  expect(w.add(10, 0, 40)).toBe(0);
  expect(w.add(20, 0, 40)).toBe(-1);
  expect(w.add(2, 1, 40)).toBe(-2);
  // A page up, less the 0.18 line still owed downward.
  expect(w.add(-1, 2, 40)).toBe(39);
});

test("the queue sends in order, one at a time, merging what waits", async () => {
  const sent: Input[] = [];
  let release: (ok: boolean) => void = () => {};
  const q = new InputQueue(
    (i) =>
      new Promise((resolve) => {
        sent.push(structuredClone(i));
        release = resolve;
      }),
  );
  q.push({ text: "a" }); // goes at once
  q.push({ text: "b" });
  q.push({ text: "c" });
  q.push({ keys: ["enter"] });
  q.push({ keys: ["up"] });
  q.push({ lines: 3, col: 1, row: 1 });
  q.push({ lines: -1, col: 5, row: 6 });
  expect(sent).toEqual([{ text: "a" }]);
  for (let n = 1; n < 4; n++) {
    release(true);
    await Bun.sleep(0);
  }
  expect(sent).toEqual([{ text: "a" }, { text: "bc" }, { keys: ["enter", "up"] }, { lines: 2, col: 5, row: 6 }]);
  release(true);
  await Bun.sleep(0);
});

test("a failed send drops what queued behind it", async () => {
  const sent: Input[] = [];
  let release: (ok: boolean) => void = () => {};
  const q = new InputQueue(
    (i) =>
      new Promise((resolve) => {
        sent.push(structuredClone(i));
        release = resolve;
      }),
  );
  q.push({ text: "a" });
  q.push({ keys: ["enter"] });
  release(false);
  await Bun.sleep(0);
  q.push({ text: "b" });
  release(true);
  await Bun.sleep(0);
  expect(sent).toEqual([{ text: "a" }, { text: "b" }]);
});

test("⌘ + / − / 0 (Ctrl elsewhere) zoom the terminal view", () => {
  const cases: [KeyLike, boolean, string | null][] = [
    [key({ key: "=", code: "Equal", metaKey: true }), true, "in"],
    [key({ key: "+", code: "Equal", metaKey: true, shiftKey: true }), true, "in"],
    [key({ key: "-", code: "Minus", metaKey: true }), true, "out"],
    [key({ key: "0", code: "Digit0", metaKey: true }), true, "reset"],
    [key({ key: "=", code: "Equal", ctrlKey: true }), false, "in"],
    [key({ key: "-", code: "Minus", ctrlKey: true }), false, "out"],
    // Ctrl on a Mac and ⌘ elsewhere stay the pane's; so does readline's Ctrl+_.
    [key({ key: "-", code: "Minus", ctrlKey: true }), true, null],
    [key({ key: "_", code: "Minus", ctrlKey: true, shiftKey: true }), false, null],
    [key({ key: "=", code: "Equal" }), true, null],
  ];
  for (const [e, mac, want] of cases) expect([e.code, zoomKey(e, mac)]).toEqual([e.code, want]);
  expect([
    nextZoom(1, "in"),
    nextZoom(1, "out"),
    nextZoom(1.25, "reset"),
    nextZoom(3, "in"),
    nextZoom(0.5, "out"),
  ]).toEqual([1.1, 0.9, 1, 3, 0.5]);
});

test("zoom keys follow the typed character on other layouts", () => {
  const cases: [KeyLike, boolean, string | null][] = [
    // German: + is its own key, - sits where / is on US, = is Shift+0.
    [key({ key: "+", code: "BracketRight", metaKey: true }), true, "in"],
    [key({ key: "-", code: "Slash", ctrlKey: true }), false, "out"],
    [key({ key: "=", code: "Digit0", ctrlKey: true, shiftKey: true }), false, "in"],
    // Numpad, and IME composition left alone.
    [key({ key: "+", code: "NumpadAdd", ctrlKey: true }), false, "in"],
    [key({ key: "-", code: "NumpadSubtract", metaKey: true }), true, "out"],
    [key({ key: "0", code: "Numpad0", metaKey: true }), true, "reset"],
    [key({ key: "=", code: "Equal", metaKey: true, isComposing: true }), true, null],
    [key({ key: "=", code: "Equal", metaKey: true, altKey: true }), true, null],
  ];
  for (const [e, mac, want] of cases) expect([e.code, zoomKey(e, mac)]).toEqual([e.code, want]);
});

test("the saved zoom must be a zoom level", () => {
  const store = new Map<string, string>();
  globalThis.localStorage = {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
  } as Storage;
  localStorage.setItem("herdsman.term-zoom", "1.25");
  expect(loadZoom()).toBe(1.25);
  localStorage.setItem("herdsman.term-zoom", "7");
  expect(loadZoom()).toBe(1);
  saveZoom(1);
  expect(localStorage.getItem("herdsman.term-zoom")).toBeNull();
});
