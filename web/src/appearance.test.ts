import { expect, test } from "bun:test";

import { contrast } from "./ansi.ts";
import { appThemes, appVars } from "./appearance.ts";

test("every color theme keeps its text, muted text and accent readable", () => {
  const weak: string[] = [];
  for (const { id } of appThemes) {
    const v = appVars(id) ?? {};
    const bg = v["--surface"] ?? "";
    for (const [k, min] of [
      ["--text", 4.5],
      ["--muted", 4.5],
      ["--accent", 3],
      ["--danger", 3],
      ["--s-working", 3],
    ] as const) {
      if (contrast(v[k] ?? "", bg) < min) weak.push(`${id} ${k}`);
    }
  }
  expect(weak).toEqual([]);
});

test("an unknown theme has no variables", () => {
  expect(appVars("nope")).toBeNull();
});
