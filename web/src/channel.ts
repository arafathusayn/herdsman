// The message channel's words: what each kind and queue state is called.
import type { Channel, ChannelMessage, MessageKind } from "./types.ts";

export const kindLabel: Record<MessageKind, string> = {
  brief: "Brief",
  observation: "Observation",
  question: "Question",
  result: "Result",
  cancel: "Cancel",
  user_answer: "User answer",
  receipt: "Receipt",
};

/** Where a message stands in the queue, as one word for its badge. */
export function stateLabel(m: ChannelMessage): string {
  if (m.state === "quarantine") return "Quarantined";
  if (m.state === "new") return "Queued";
  if (m.state === "ready") return "Delivered";
  return m.acknowledged ? "Taken in" : "Read";
}

export const stateTip: Record<string, string> = {
  Quarantined: "Failed the courier's checks; never trusted",
  Queued: "Waiting for the courier to verify it",
  Delivered: "Verified, not yet read",
  Read: "Claimed by its reader; intake not recorded yet",
  "Taken in": "The reader recorded it in the route",
};

/** Messages a reader has not taken in: delivered or read without an intake record. */
export function unread(ch: Channel | null): number {
  return (ch?.boxes ?? []).reduce((n, b) => n + b.unacked, 0);
}
