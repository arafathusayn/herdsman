import { beforeEach, describe, expect, test } from "bun:test";

import { fuzzy, highlight, type HighlightNode } from "./fuzzy.ts";
import { child, html, render } from "./herdsman-blocks-view.ts";
import { type FakeNode, installFakeDocument } from "./herdsman-blocks.testdom.ts";
import { dump } from "./testdom.ts";
import { h, type Unkeyed, unkeyed } from "./testref.ts";
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
  // The h() version highlight() replaced, kept to prove the runs render the same DOM.
  const oldHighlight = (value: string, hits: number[]): Unkeyed => {
    if (!hits.length) return unkeyed([value]);
    const set = new Set(hits);
    const out: (Node | string)[] = [];
    let run = "";
    let inHit = false;
    for (let i = 0; i < value.length; i++) {
      const hit = set.has(i);
      if (hit !== inHit && run) {
        out.push(inHit ? h("mark", null, run) : run);
        run = "";
      }
      inHit = hit;
      run += value[i];
    }
    if (run) out.push(inHit ? h("mark", null, run) : run);
    return unkeyed(out);
  };
  const Name = html`<span class="name">${child<HighlightNode>()}</span>`;
  const cases: [string, number[]][] = [
    ["", []],
    ["plain", []],
    ["rv-codex", [0, 1, 3]],
    ["Pane: Split im-adjutant right", [6, 7, 8, 9, 10, 25]],
    ["abc", [0, 1, 2]],
    ["abc", [2]],
  ];

  test("renders what the h() version did", () => {
    for (const [value, hits] of cases) {
      const old = dump(h("span", { class: "name" }, oldHighlight(value, hits)) as unknown as FakeNode);
      const box = document.createElement("div");
      render(box, Name([highlight(value, hits)]));
      expect(dump(box.firstChild as unknown as FakeNode)).toBe(old);
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
