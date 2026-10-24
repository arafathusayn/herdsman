import { expect, test } from "bun:test";

import { stateLabel, unread } from "./channel.ts";
import type { Channel, ChannelMessage } from "./types.ts";

const msg = (state: ChannelMessage["state"], acknowledged = false): ChannelMessage => ({
  id: "m000000000001",
  from: "front",
  to: "boss",
  kind: "brief",
  reply_to: "",
  time: 0,
  state,
  acknowledged,
});

test("a message's badge follows its queue state and intake", () => {
  expect([msg("new"), msg("ready"), msg("cur"), msg("cur", true), msg("quarantine")].map(stateLabel)).toEqual([
    "Queued",
    "Delivered",
    "Read",
    "Taken in",
    "Quarantined",
  ]);
});

test("unread counts what no reader took in, across boxes", () => {
  const box = (unacked: number) => ({
    name: "x",
    orphan: false,
    counts: {},
    unacked,
    claim: "none" as const,
    suspicious: 0,
  });
  expect(unread(null)).toBe(0);
  expect(unread({ boxes: [box(2), box(1)] } as unknown as Channel)).toBe(3);
});
