import { attach } from "@reactre/core";

import { applyCodeTheme } from "../../src/appearance.ts";
import type { DiffSource } from "../../src/types.ts";
import { createDiffState, DiffView } from "../../src/views/diff.tsx";
import { check, equal, flush, q, run, typeIn } from "./lib.ts";

await run(async () => {
  const patch =
    "diff --git a/src/a.ts b/src/a.ts\n--- a/src/a.ts\n+++ b/src/a.ts\n@@ -1,2 +1,2 @@\n const a = 1;\n-const b = 2;\n+const b = 3;\n";
  let sources: DiffSource[] = [
    {
      id: "task",
      label: "Task",
      kind: "task",
      dir: "/work",
      agents: ["impl"],
      diff: {
        base: "main",
        head: "task",
        merge_base: "base",
        patch,
        files: [{ path: "src/a.ts", status: "M", add: 1, del: 1 }],
      },
    },
  ];
  const original = window.fetch;
  let decoded = 0;
  window.fetch = (async (_input: RequestInfo | URL) => {
    const response = Response.json({ sources });
    const json = response.json.bind(response);
    response.json = async () => {
      const body: unknown = await json();
      decoded++;
      return body;
    };
    return response;
  }) as typeof fetch;
  const root = q(document, "#app");
  // Exercise the wide layout independently of the host browser's viewport.
  root.style.width = "1100px";
  const state = createDiffState("browser-diff");
  const dispose = attach(() => <DiffView state={state} />, root);
  try {
    state.update("task", "src/a.ts");
    state.start();
    for (let attempt = 0; attempt < 100 && !root.querySelector("tbody tr"); attempt++) await flush();
    state.stop();
    equal("inline diff has all four rows", root.querySelectorAll("tbody tr").length, 4);
    const rows = [...root.querySelectorAll("tbody tr")];
    const previous = decoded;
    state.start();
    for (let attempt = 0; attempt < 100 && decoded === previous; attempt++) await flush();
    state.stop();
    check(
      "an unchanged poll retains every row",
      [...root.querySelectorAll("tbody tr")].every((row, index) => row === rows[index]),
    );
    sources = [{ ...sources[0]!, diff: { ...sources[0]!.diff!, patch: patch + "@@ -10 +10 @@\n-old\n+new\n" } }];
    state.start();
    for (let attempt = 0; attempt < 100 && root.querySelectorAll("tbody tr").length < 7; attempt++) await flush();
    state.stop();
    check(
      "a new hunk keeps existing rows",
      rows.every((row, index) => root.querySelectorAll("tbody tr")[index] === row),
    );
    equal("a new hunk is appended", root.querySelectorAll("tbody tr").length, 7);
    const search = q<HTMLInputElement>(root, "input[type=search]");
    state.focusSearch();
    typeIn(search, "a.ts");
    check("filtering retains search focus", document.activeElement === search);
    equal("file search marks its match", q(root, ".file-row mark").textContent, "a.ts");
    q<HTMLElement>(root, '[title="The old file on the left, the new one on the right"]').click();
    check("layout switch renders both sides", root.querySelector(".diff-code.split colgroup") !== null);
    equal("a context row has four cells", q(root, "tbody tr:nth-child(2)").children.length, 4);
    const viewer = q<HTMLElement>(root, ".viewer");
    viewer.style.width = "400px";
    for (let attempt = 0; attempt < 100 && root.querySelector(".diff-layout"); attempt++) await flush();
    check(
      "a narrow viewer uses inline and hides the switch",
      root.querySelector(".diff-code.inline") !== null && root.querySelector(".diff-layout") === null,
    );
    viewer.style.width = "900px";
    for (let attempt = 0; attempt < 100 && !root.querySelector(".diff-code.split"); attempt++) await flush();
    check("widening restores the chosen layout", root.querySelector(".diff-code.split") !== null);
    applyCodeTheme("off", false);
    check("highlighting can turn off in the open diff", root.querySelector('td.code [class^="hl-"]') === null);
    applyCodeTheme("match", false);
    check("highlighting can turn back on", root.querySelector('td.code [class^="hl-"]') !== null);
  } finally {
    state.stop();
    dispose();
    window.fetch = original;
  }
  check("disposal removes the diff", root.querySelector("table") === null);
});
