import { expect, test } from "bun:test";

import { clampWidth } from "./resize.ts";

test("the panel width stays between its minimum and the room left", () => {
  expect(clampWidth(200, 900)).toBe(340);
  expect(clampWidth(700, 900)).toBe(700);
  expect(clampWidth(1200, 900)).toBe(900);
  // A window too small for both still leaves the panel its minimum.
  expect(clampWidth(500, 100)).toBe(340);
});
