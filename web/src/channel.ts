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
  side_agent_data: "Side agent",
};

/** What each health flag means, as a chip and its tip. */
export const flagLabel: Record<string, [string, string]> = {
  "timing-invalid": ["Timing invalid", "state/timing.json is invalid; the default health threshold is used"],
  "mutation-lock-stale": ["Mutation lock stale", "A herdr mutation lock is older than the health threshold"],
  "channel-lock-stale": ["Channel lock stale", "The channel lock is older than the health threshold"],
  "alert-lock-stale": ["Alert lock stale", "An alert lock is older than the health threshold"],
  "courier-lock-stale": [
    "Courier lock stale",
    "The courier lock has no daemon record and is older than the health threshold",
  ],
  "watch-lock-stale": [
    "Watch lock stale",
    "The watch lock has no daemon record and is older than the health threshold",
  ],
  "automation-held": ["Automation held", "Automation is held until the human releases it"],
};

/** What each health entry watches: file mode's courier and watcher, or a daemon-owned inbox's daemon. */
const healthNames: Record<string, string> = { courier: "Courier", watcher: "Watcher", daemon: "Daemon" };

/** A health entry's chip name; an unknown entry shows as it is named. */
export function healthLabel(name: string): string {
  return healthNames[name] ?? name;
}

/** A flag's chip text and tip; an unknown flag shows as it is named. */
export function describeFlag(flag: string): [string, string] {
  return flagLabel[flag] ?? [flag, "Reported by the message channel"];
}

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
