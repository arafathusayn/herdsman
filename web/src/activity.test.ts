import { expect, test } from "bun:test";

import { activityPinger } from "./activity.ts";

test("user events send activity at most once per interval", () => {
  let t = 0;
  let sent = 0;
  const ping = activityPinger(
    () => sent++,
    10_000,
    () => t,
  );
  ping();
  ping();
  expect(sent).toBe(1);
  t = 9_999;
  ping();
  expect(sent).toBe(1);
  t = 10_000;
  ping();
  expect(sent).toBe(2);
});
