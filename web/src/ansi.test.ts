import { expect, test } from "bun:test";

import { contrast, parseAnsi, readable, xterm256 } from "./ansi.ts";

test("SGR codes become styled runs; reset clears them", () => {
  expect(parseAnsi("a\x1b[1;31mred\x1b[0m b")).toEqual([
    { text: "a", style: {} },
    { text: "red", style: { bold: true, fg: 1 } },
    { text: " b", style: {} },
  ]);
});

test("bright, 256 and truecolor codes", () => {
  const runs = parseAnsi("\x1b[92mx\x1b[38;5;208my\x1b[48;2;33;58;43mz\x1b[38:2::255:0:10mw");
  expect(runs.map((r) => r.style)).toEqual([
    { fg: 10 },
    { fg: 208 },
    { fg: 208, bg: "#213a2b" },
    { fg: "#ff000a", bg: "#213a2b" },
  ]);
});

test("attribute resets undo one attribute each", () => {
  const runs = parseAnsi("\x1b[1;2;3;4mA\x1b[22;23mB\x1b[24;39mC");
  expect(runs.map((r) => r.style)).toEqual([
    { bold: true, dim: true, italic: true, underline: true },
    { underline: true },
    {},
  ]);
});

test("other escape sequences and carriage returns are dropped, never shown", () => {
  expect(parseAnsi("\x1b]0;title\x07a\x1b[2Kb\r\n\x1b[?25lc\x1b(B")).toEqual([{ text: "ab\nc", style: {} }]);
});

test("xterm256 maps the cube and the grays", () => {
  expect(xterm256(16)).toBe("#000000");
  expect(xterm256(196)).toBe("#ff0000");
  expect(xterm256(208)).toBe("#ff8700");
  expect(xterm256(232)).toBe("#080808");
  expect(xterm256(255)).toBe("#eeeeee");
});

test("contrast follows WCAG", () => {
  expect(contrast("#000000", "#ffffff")).toBeCloseTo(21, 5);
  expect(contrast("#777777", "#777777")).toBeCloseTo(1, 5);
});

test("readable keeps colors that read and fixes ones that do not", () => {
  expect(readable("#1b1c1a", "#fbfbf9", 4.5)).toBe("#1b1c1a");
  // Codex's light gray, drawn for a dark terminal, on the light terminal.
  const fixed = readable("#cdd6f4", "#fbfbf9", 4.5);
  expect(contrast(fixed, "#fbfbf9")).toBeGreaterThanOrEqual(4.5);
  // A dark color on a dark terminal moves toward white instead.
  const lifted = readable("#3e4052", "#121513", 4.5);
  expect(contrast(lifted, "#121513")).toBeGreaterThanOrEqual(4.5);
  expect(lifted > "#3e4052").toBe(true);
});
