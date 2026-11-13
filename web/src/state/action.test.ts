import { expect, test } from "bun:test";

import { createActor } from "@reactre/state";

import { actionMachine } from "./action.machine.ts";
import { registerRequest } from "./request-service.ts";

for (const approved of [false, true])
  test(`action requires confirmation before force (${approved})`, async () => {
    const calls: string[] = [];
    const service = registerRequest(async (json) => {
      const { kind } = JSON.parse(json) as { kind: string };
      calls.push(kind);
      return kind === "confirm"
        ? String(approved)
        : JSON.stringify({ status: kind === "send" ? "confirm" : "ok", message: "busy" });
    });
    const actor = createActor(actionMachine, { input: { service: service.id, action: "{}" } });
    try {
      await Bun.sleep(0);
      expect(actor.snapshot().state).toBe(approved ? "succeeded" : "cancelled");
      expect(calls).toEqual(approved ? ["send", "confirm", "force"] : ["send", "confirm"]);
    } finally {
      actor.stop();
      service.dispose();
    }
  });

test("cancelling confirmation aborts its dialog task and never forces", async () => {
  let signal!: AbortSignal;
  let confirm!: (answer: string) => void;
  let calls = 0;
  const service = registerRequest(async (_, current) => {
    if (++calls === 1) return JSON.stringify({ status: "confirm", message: "busy" });
    signal = current;
    return new Promise<string>((resolve) => {
      confirm = resolve;
    });
  });
  const actor = createActor(actionMachine, { input: { service: service.id, action: "{}" } });
  try {
    await Bun.sleep(0);
    expect(actor.snapshot().state).toBe("confirming");
    actor.send({ type: "cancel" });
    expect(signal.aborted).toBe(true);
    confirm("true");
    await Bun.sleep(0);
    expect(actor.snapshot().state).toBe("cancelled");
    expect(calls).toBe(2);
  } finally {
    actor.stop();
    service.dispose();
  }
});
