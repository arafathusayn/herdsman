import { isServer, makeMemo } from "../../test-fixtures/memo.ts";
import { mount } from "../../test-fixtures/reactre.tsx";
import { check, equal, q, run } from "./lib.ts";

await run(async () => {
  equal("uses the browser server entry", isServer, false);
  const root = q(document, "#app");
  const app = mount(root);
  const button = q<HTMLButtonElement>(root, "button");
  button.click();
  equal("compiles imported TSX and handles clicks", button.textContent, "1");
  equal("reactive control flow renders", q(root, "p").textContent, "updated");
  const rows = [...root.querySelectorAll("li")];
  app.setRows([
    { id: 2, label: "TWO" },
    { id: 1, label: "one" },
  ]);
  check("keeps keyed DOM nodes", root.querySelector("li") === rows[1]);
  app.dispose();
  equal("disposes the view", root.childNodes.length, 0);
  const state = makeMemo();
  equal(".ts memo starts lazy", state.runs, 0);
  const dispose = state.subscribe();
  equal("memo computes on observation", state.seen, 2);
  dispose();
  await Promise.resolve();
  equal("memo reports its last reader leaving", state.releases, 1);
  state.setValue(2);
  equal("unobserved memo stops computing", state.runs, 1);
});
