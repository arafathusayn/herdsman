import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

import { parseSync } from "oxc-parser";

const subscriptionAdapters = new Set(["state/value.ts", "state/request.ts", "state/observe.ts"]);
const forbidden = new Set([
  "signal",
  "state",
  "store",
  "createStore",
  "createMutable",
  "createContext",
  "useContext",
  "useState",
  "useReducer",
]);
function competingImports(file: string, code: string): string[] {
  const problems: string[] = [];
  for (const node of parseSync(file, code).program.body) {
    if (node.type !== "ImportDeclaration" || node.importKind === "type") continue;
    const source = node.source.value;
    if (source === "react" || /(?:zustand|redux|mobx|jotai|valtio)/.test(source)) problems.push(source);
    if (!source.startsWith("@reactre/core")) continue;
    for (const specifier of node.specifiers) {
      if (specifier.type !== "ImportSpecifier") {
        problems.push("core namespace/default import");
        continue;
      }
      if (specifier.importKind === "type") continue;
      const name = specifier.imported.type === "Identifier" ? specifier.imported.name : specifier.imported.value;
      if (forbidden.has(name) && !(name === "signal" && subscriptionAdapters.has(file))) problems.push(name);
    }
  }
  return problems;
}

test("UI modules cannot create a competing state owner", () => {
  const root = join(import.meta.dir, "src");
  const problems: string[] = [];
  for (const file of readdirSync(root, { recursive: true, encoding: "utf8" })) {
    if (!/\.tsx?$/.test(file) || /\.test\.tsx?$|(^|\/)test[^/]*\.tsx?$/.test(file)) continue;
    for (const issue of competingImports(file, readFileSync(join(root, file), "utf8")))
      problems.push(`${file}: ${issue}`);
  }
  expect(problems).toEqual([]);
});
test("ownership guard catches aliases and namespaces but permits derived values", () => {
  expect(competingImports("view.ts", 'import {signal as local} from "@reactre/core";')).toEqual(["signal"]);
  expect(competingImports("view.ts", 'import * as core from "@reactre/core";')).toEqual([
    "core namespace/default import",
  ]);
  expect(competingImports("view.ts", 'import {memo, effect} from "@reactre/core";')).toEqual([]);
  expect(competingImports("state/value.ts", 'import {signal} from "@reactre/core";')).toEqual([]);
});
