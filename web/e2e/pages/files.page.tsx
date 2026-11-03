import { attach } from "@reactre/core";

import { applyCodeTheme } from "../../src/appearance.ts";
import type { Route } from "../../src/types.ts";
import { createFilesState, FilesView } from "../../src/views/files.tsx";
import { check, equal, flush, q, run, typeIn } from "./lib.ts";

await run(async () => {
  const source =
    "# Title\n\n**<img src=x onerror=alert(1)>** [file](javascript:alert(1))\n\n```ts\nconst answer = 42;\n```";
  const route: Route = {
    id: "docs",
    name: "docs",
    dir: "/work/docs",
    title: "Docs",
    live: true,
    orchestrator_pane: "p1",
    modified: "2026-11-03T10:00:00Z",
    tasks: null,
    checklist: null,
    run_state: null,
    files: [{ path: "00-route.md", size: source.length, modified: "2026-11-03T10:00:00Z" }],
  };
  const original = window.fetch;
  let reads = 0;
  window.fetch = (async (_input: RequestInfo | URL) => {
    reads++;
    return new Response(source);
  }) as typeof fetch;
  const state = createFilesState(route.id);
  const root = q(document, "#app");
  const dispose = attach(() => <FilesView state={state} />, root);
  try {
    state.update(route, "");
    for (let attempt = 0; attempt < 100 && !root.querySelector(".md"); attempt++) await flush();
    equal("renders Markdown headings", q(root, ".md h2").textContent, "Title");
    check(
      "markup in file text stays inert",
      root.querySelector("img") === null && q(root, ".md strong").textContent?.startsWith("<img") === true,
    );
    check(
      "non-web links do not navigate",
      root.querySelector(".md a") === null && root.querySelector(".md .md-link") !== null,
    );
    const pre = q(root, ".md pre");
    check("known code is highlighted", pre.querySelector('[class^="hl-"]') !== null);
    applyCodeTheme("off", false);
    check(
      "highlighting turns off without replacing the code block",
      q(root, ".md pre") === pre && !pre.querySelector("span"),
    );
    equal("highlighting preserves code text", pre.textContent, "const answer = 42;");
    state.raw(true);
    equal("Raw shows the complete source", q(root, ".raw").textContent, source);
    state.raw(false);
    equal("view and highlighting switches reuse the file", reads, 1);
    const search = q<HTMLInputElement>(root, "input[type=search]");
    state.focusSearch();
    typeIn(search, "route");
    check(
      "search keeps its input and focus",
      q(root, "input[type=search]") === search && document.activeElement === search,
    );
    equal("search marks matched text", q(root, ".file-row mark").textContent, "route");
    const heading = q(root, ".md h2");
    state.update({ ...route }, "");
    check("an unchanged file update retains rendered blocks", q(root, ".md h2") === heading);
  } finally {
    dispose();
    window.fetch = original;
    applyCodeTheme("match", false);
  }
  check("disposal removes the document", root.querySelector(".md") === null);
});
