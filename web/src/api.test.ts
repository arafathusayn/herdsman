import { expect, test } from "bun:test";

import { forgetPane, readPane, sseParser } from "./api.ts";

// A read still in flight when its panel closes must not bring back the ETag
// the close dropped: the reopened panel would get a 304 with nothing to show.
test("a late pane read does not undo forgetPane", async () => {
  const sent: (string | null)[] = [];
  let answer: (r: Response) => void = () => {};
  const real = globalThis.fetch;
  globalThis.fetch = ((_: string, init?: RequestInit) => {
    sent.push(new Headers(init?.headers).get("If-None-Match"));
    return new Promise<Response>((resolve) => {
      answer = resolve;
    });
  }) as typeof fetch;
  try {
    const first = readPane("w1:p9");
    forgetPane("w1:p9");
    answer(Response.json({ text: "old", revision: 1 }, { headers: { ETag: '"r1"' } }));
    await first;
    const second = readPane("w1:p9");
    answer(Response.json({ text: "new", revision: 2 }, { headers: { ETag: '"r2"' } }));
    await second;
    expect(sent).toEqual([null, null]);
  } finally {
    globalThis.fetch = real;
  }
});

test("the event stream parser splits events across chunks and skips pings", () => {
  const got: [string, string][] = [];
  const feed = sseParser((e, d) => got.push([e, d]));
  feed('event: state\ndata: {"a"');
  feed(":1}\n\n: ping\n\n");
  feed("event: entry\r\ndata: one\r\ndata: two\r\n\r\ndata: plain\n\n");
  expect(got).toEqual([
    ["state", '{"a":1}'],
    ["entry", "one\ntwo"],
    ["message", "plain"],
  ]);
});
