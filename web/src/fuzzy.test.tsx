import { beforeEach, describe, expect, test } from "bun:test";

import { signal } from "@reactre/core";

import { Highlight } from "./components/highlight.tsx";
import { fuzzy } from "./fuzzy.ts";
import { installFakeDocument } from "./herdsman-blocks.testdom.ts";
import { mount } from "./test-mount.ts";
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
      expect(
        dump(
          mount(() => (
            <span class="name">
              <Highlight value={value} hits={hits} />
            </span>
          )),
        ),
      ).toBe(want);
    }
  });

  test("a new query patches the runs in place", () => {
    const [hits, setHits] = signal([0, 1]);
    const span = mount(() => (
      <span class="name">
        <Highlight value="rv-codex" hits={hits()} />
      </span>
    ));
    const mark = span.childNodes.find((node) => node.nodeType === 1);
    setHits([0, 1, 2]);
    expect(span.childNodes.find((node) => node.nodeType === 1)).toBe(mark);
    expect(dump(span)).toBe('<span class="name"><mark>rv-</mark>codex</span>');
  });
});
