import { expect, test } from "bun:test";

import { fuzzy } from "./fuzzy.ts";
test("fuzzy", () => {
  expect(fuzzy("split", "probes/rv-codex/cases-1792832794680869000/replaced-recipient/x.md")).toBeNull();
  expect(fuzzy("split", "Pane: Split im-adjutant right")).not.toBeNull();
  expect(fuzzy("spr", "Pane: Split im-adjutant right")).not.toBeNull();
  expect(fuzzy("rvc", "rv-codex")).not.toBeNull();
  expect(fuzzy("rev", "reports/review-design-claude-0.md")!.hits).toEqual([8, 9, 10]);
  expect(fuzzy("route", "00-route.md")).not.toBeNull();
  const a = fuzzy("review", "reports/review-design-codex-0.md")!.score;
  const b = fuzzy("review", "probes/rv-codex/cases/review-x/inbox/route-orchestrator/q.md")!.score;
  expect(a).toBeGreaterThan(b);
});
