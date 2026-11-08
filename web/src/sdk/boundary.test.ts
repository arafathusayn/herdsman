import { afterEach, describe, expect, test } from "bun:test";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";

import { checkBoundary, legacyClients } from "./boundary.ts";

const roots: string[] = [];
afterEach(() => void roots.splice(0));

function tree(files: Record<string, string>): string {
  const root = mkdtempSync(join(tmpdir(), "boundary-"));
  roots.push(root);
  for (const [name, text] of Object.entries(files)) {
    mkdirSync(dirname(join(root, name)), { recursive: true });
    writeFileSync(join(root, name), text);
  }
  return root;
}

const problems = (files: Record<string, string>, legacy: readonly string[] = []) =>
  checkBoundary(tree(files), legacy).violations.map((v) => `${v.file}: ${v.rule}`);

describe("checkBoundary", () => {
  test("passes a page on the SDK and on local files", () => {
    expect(
      problems({
        "a.ts": `import { createClient } from "@herdsman/sdk";
          import { RouteService } from "@herdsman/sdk/herdsman/daemon/v1/route";
          import { signal } from "@reactre/core";
          import { b } from "./b.ts";
          export default [createClient, RouteService, signal, b];`,
        "b.ts": "export const b = 1;",
      }),
    ).toEqual([]);
  });

  test("rejects a planted import of the old HTTP client", () => {
    expect(
      problems({ "api.ts": "export const x = 1;", "pages/p.ts": `import { x } from "../api.ts"; export { x };` }),
    ).toEqual(["pages/p.ts: imports the legacy client ../api.ts"]);
  });

  test("rejects another client library and a path into the SDK's sources", () => {
    expect(
      problems({
        "a.ts": `import axios from "axios"; export { axios };`,
        "b.ts": `import { createClient } from "../../sdk/ts/src/client.ts"; export { createClient };`,
      }),
    ).toEqual([
      "a.ts: imports axios outside @herdsman/sdk",
      "b.ts: reaches into the SDK by path: ../../sdk/ts/src/client.ts",
    ]);
  });

  test("rejects a planted import of React in a JSX file", () => {
    expect(problems({ "a.tsx": `import React from "react"; export const a = <p>{React}</p>;` })).toEqual([
      "a.tsx: imports react outside @herdsman/sdk",
    ]);
  });

  test("rejects a dynamic import of the old client", () => {
    expect(problems({ "api.ts": "export const x = 1;", "a.ts": `export const m = import("./api.ts");` })).toEqual([
      "a.ts: imports the legacy client ./api.ts",
    ]);
  });

  test.each([
    ["fetch", `export const r = fetch("/rpc/x");`],
    ["fetch", `export const r = window.fetch("/rpc/x");`],
    ["fetch", `const f = fetch; export { f };`],
    ["WebSocket", `export const s = new WebSocket("ws://x");`],
    ["EventSource", `export const s = new EventSource("/x");`],
    ["XMLHttpRequest", `export const s = new XMLHttpRequest();`],
    ["sendBeacon", `export const s = navigator.sendBeacon("/x");`],
  ])("rejects a direct %s call", (name, source) => {
    expect(problems({ "a.ts": source })).toEqual([`a.ts: calls ${name} directly`]);
  });

  test("does not read words in comments, strings or JSX text as calls", () => {
    expect(
      problems({
        "a.tsx": `// fetch("/x") and new WebSocket
          export const a = "please fetch the WebSocket";
          export const b = \`fetch\`;
          export const c = <p>fetch your EventSource</p>;
          export type D = { webSocket: WebSocketLike };`,
      }),
    ).toEqual([]);
  });

  test("skips test files", () => {
    expect(problems({ "a.test.ts": `import axios from "axios"; fetch("/x"); export { axios };` })).toEqual([]);
  });

  test("reports an allowlisted file as skipped and does not fail it", () => {
    const result = checkBoundary(tree({ "api.ts": `export const r = fetch("/x");` }), ["api.ts"]);
    expect(result.violations).toEqual([]);
    expect(result.skipped).toEqual(["api.ts"]);
  });

  test("fails an allowlist entry that no longer needs its place", () => {
    expect(problems({ "a.ts": "export const a = 1;" }, ["a.ts"])).toEqual([
      "a.ts: is on the legacy list but uses no legacy client; remove it from the list",
    ]);
  });

  test("fails an allowlist entry whose file is gone", () => {
    expect(problems({ "a.ts": "export const a = 1;" }, ["gone.ts"])).toEqual([
      "gone.ts: is on the legacy list but does not exist; remove it from the list",
    ]);
  });
});

test("the app itself passes", () => {
  const result = checkBoundary(join(import.meta.dir, ".."), legacyClients);
  expect(result.violations).toEqual([]);
  expect(result.skipped).toEqual([...legacyClients].sort());
});
