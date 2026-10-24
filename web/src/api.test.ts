import { expect, test } from "bun:test";

import { sseParser } from "./api.ts";

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
