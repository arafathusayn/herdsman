import { attach } from "@reactre/core";

import { store } from "../../src/store.ts";
import type { Artifact } from "../../src/types.ts";
import { artifactPath, ArtifactsView, createArtifactsState } from "../../src/views/artifacts.tsx";
import { check, equal, flush, q, run, typeIn } from "./lib.ts";

await run(async () => {
  const items: Artifact[] = [
    { root: "/work/reports", path: "plan.html", size: 200, modified: "2026-11-03T10:00:00Z" },
    { root: "/work/reports", path: "deep/review.html", size: 300, modified: "2026-11-03T10:00:00Z" },
  ];
  let reads = 0;
  const original = window.fetch;
  window.fetch = (async (input: RequestInfo | URL) => {
    if (String(input).includes("/api/artifacts/open")) {
      reads++;
      return Response.json({ url: "data:text/html,%3Cp%3EArtifact%3C/p%3E" });
    }
    return Response.json({ artifacts: items });
  }) as typeof fetch;
  const state = createArtifactsState("route");
  const root = q(document, "#app");
  const dispose = attach(() => <ArtifactsView state={state} />, root);
  try {
    state.start();
    for (let attempt = 0; attempt < 100 && !root.querySelector("iframe"); attempt++) {
      const error = root.querySelector(".artifact-body .error");
      if (error) throw new Error(error.textContent ?? "Preview failed");
      await flush();
    }
    state.stop();
    equal("lists the route's artifacts", root.querySelectorAll(".file-row").length, 2);
    const frame = q<HTMLIFrameElement>(root, "iframe");
    check(
      "preview permits scripts with an isolated origin",
      frame.sandbox.contains("allow-scripts") && !frame.sandbox.contains("allow-same-origin"),
    );
    state.update(artifactPath(items[0]!));
    check("selecting the same page retains its frame", q(root, "iframe") === frame);
    equal("an unchanged selection does not reopen the artifact", reads, 1);
    const search = q<HTMLInputElement>(root, "input[type=search]");
    state.focusSearch();
    check("search receives focus", document.activeElement === search);
    typeIn(search, "review");
    equal("search highlights its match", q(root, ".file-row mark").textContent, "review");
    check("typing keeps the search field focused", document.activeElement === search);
    search.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    equal("Enter opens the matched page", store.app.view.artifact, artifactPath(items[1]!));
    search.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    equal("Escape clears the filter", search.value, "");
    const group = q<HTMLDetailsElement>(root, "details");
    group.open = false;
    await flush();
    state.update(artifactPath(items[1]!));
    await flush();
    check("selection keeps closed folders closed", q(root, "details") === group && !group.open);
    q<HTMLElement>(root, '[aria-label="Reload the page"]').click();
    await flush();
    equal("reload refetches the page", reads, 3);
  } finally {
    state.stop();
    dispose();
    window.fetch = original;
  }
  check("disposal removes the preview", root.querySelector("iframe") === null);
});
