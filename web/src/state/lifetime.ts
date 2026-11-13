import { onCleanup } from "@reactre/core";
import { createActor } from "@reactre/state";

import { lifetimeMachine } from "./lifetime.machine.ts";

/** Exactly one close wins, including when a native close event reenters. */
export function lifetime() {
  const actor = createActor(lifetimeMachine);
  const close = () => actor.send({ type: "close" }).ok;
  onCleanup(() => {
    close();
    actor.stop();
  });
  return { close, closed: () => actor.snapshot().state === "closed" };
}
