import { expect, test } from "bun:test";

import { createActor } from "@reactre/state";

import { registerRequest } from "./request-service.ts";
import { startupMachine } from "./startup.machine.ts";

for (const trust of [true, false])
  test(`startup selects a host only after required trust (${trust})`, async () => {
    const steps: string[] = [];
    const service = registerRequest(async (json) => {
      const { step } = JSON.parse(json) as { step: string };
      steps.push(step);
      return JSON.stringify({
        status:
          step === "inspecting"
            ? "trust"
            : step === "trusting"
              ? trust
                ? "select"
                : "unavailable"
              : step === "selecting"
                ? "check"
                : "paired",
        context: "{}",
      });
    });
    const actor = createActor(startupMachine, { input: { service: service.id, context: "" } });
    try {
      await Bun.sleep(0);
      expect(actor.snapshot().state).toBe(trust ? "connected" : "unavailable");
      expect(steps).toEqual(trust ? ["inspecting", "trusting", "selecting", "checking"] : ["inspecting", "trusting"]);
    } finally {
      actor.stop();
      service.dispose();
    }
  });
test("startup cancellation ignores a late discovery result", async () => {
  let finish!: (result: string) => void;
  let signal!: AbortSignal;
  const service = registerRequest((_, current) => {
    signal = current;
    return new Promise((resolve) => {
      finish = resolve;
    });
  });
  const actor = createActor(startupMachine, { input: { service: service.id, context: "" } });
  actor.send({ type: "cancel" });
  expect(signal.aborted).toBe(true);
  finish(JSON.stringify({ status: "select", context: "{}" }));
  await Bun.sleep(0);
  expect(actor.snapshot().state).toBe("cancelled");
  actor.stop();
  service.dispose();
});
