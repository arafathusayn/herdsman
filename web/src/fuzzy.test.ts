import { beforeEach, describe, expect, test } from "bun:test";

import { fuzzy, highlight, type HighlightNode } from "./fuzzy.ts";
import { child, html, render } from "./herdsman-blocks-view.ts";
import { type FakeNode, installFakeDocument } from "./herdsman-blocks.testdom.ts";
import { dump } from "./testdom.ts";
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

describe("highlight as runs", () => {
  beforeEach(installFakeDocument);
  const Name = html`<span class="name">${child<HighlightNode>()}</span>`;
  // A value, its hits, and the markup: a mark per run of hits.
  const cases: [string, number[], string][] = [
    ["", [], '<span class="name"></span>'],
    ["plain", [], '<span class="name">plain</span>'],
    ["rv-codex", [0, 1, 3], '<span class="name"><mark>rv</mark>-<mark>c</mark>odex</span>'],
    [
      "Pane: Split im-adjutant right",
      [6, 7, 8, 9, 10, 25],
      '<span class="name">Pane: <mark>Split</mark> im-adjutant r<mark>i</mark>ght</span>',
    ],
    ["abc", [0, 1, 2], '<span class="name"><mark>abc</mark></span>'],
    ["abc", [2], '<span class="name">ab<mark>c</mark></span>'],
  ];

  test("renders a mark per run of hits", () => {
    for (const [value, hits, want] of cases) {
      const box = document.createElement("div");
      render(box, Name([highlight(value, hits)]));
      expect(dump(box.firstChild as unknown as FakeNode)).toBe(want);
    }
  });

  test("a new query patches the runs in place", () => {
    const box = document.createElement("div");
    render(box, Name([highlight("rv-codex", [0, 1])]));
    const span = box.firstChild!;
    const mark = span.firstChild;
    render(box, Name([highlight("rv-codex", [0, 1, 2])]));
    expect(box.firstChild).toBe(span);
    expect(span.firstChild).toBe(mark); // the first run is still a mark: patched, not rebuilt
    expect(dump(span as unknown as FakeNode)).toBe('<span class="name"><mark>rv-</mark>codex</span>');
  });
});
