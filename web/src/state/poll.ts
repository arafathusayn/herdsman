import { onCleanup } from "@reactre/core";
import { createActor } from "@reactre/state";

import { pollMachine } from "./poll.machine.ts";
import { registerRequest } from "./request-service.ts";

/** A scoped read/wait lifecycle. Wake coalesces while reading; stop aborts
 * both active work and delays. The callback checks signal before effects. */
export function createPoll(read: (signal: AbortSignal) => Promise<number>, retry = 5000) {
  const service = registerRequest(async (_, signal) => String(await read(signal)));
  const actor = createActor(pollMachine, { input: { service: service.id, retry } });
  const dispose = () => {
    actor.stop();
    service.dispose();
  };
  onCleanup(dispose);
  return {
    actor,
    start: () => {
      actor.send({ type: "start" });
    },
    stop: () => {
      actor.send({ type: "stop" });
    },
    wake: () => {
      actor.send({ type: "wake" });
    },
    dispose,
  };
}
