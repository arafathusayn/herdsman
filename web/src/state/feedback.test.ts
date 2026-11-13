import { expect, test } from "bun:test";

import { createActor } from "@reactre/state";

import { copyMachine } from "./copy.machine.ts";
import { noticeMachine } from "./notice.machine.ts";
import { registerRequest } from "./request-service.ts";

test("clipboard replacement and closing reject stale feedback", async () => {
  const pending: { signal: AbortSignal; finish: (result: string) => void }[] = [];
  const service = registerRequest((_, signal) => new Promise((finish) => pending.push({ signal, finish })));
  const actor = createActor(copyMachine, { input: { service: service.id } });
  try {
    actor.send({ type: "copy", text: "first" });
    actor.send({ type: "copy", text: "second" });
    expect(pending[0]!.signal.aborted).toBe(true);
    pending[1]!.finish("false");
    await Bun.sleep(0);
    expect(actor.snapshot()).toMatchObject({ state: "shown", data: { ok: false } });
    pending[0]!.finish("true");
    await Bun.sleep(0);
    expect(actor.snapshot()).toMatchObject({ state: "shown", data: { ok: false } });
    actor.send({ type: "close" });
    expect(actor.snapshot().state).toBe("closed");
  } finally {
    actor.stop();
    service.dispose();
  }
});
test("notification moves through its exit animation and removes itself", async () => {
  const actor = createActor(noticeMachine, { input: { duration: 1 } });
  try {
    expect(actor.snapshot().state).toBe("showing");
    await Bun.sleep(20);
    expect(actor.snapshot().state).toBe("leaving");
    await Bun.sleep(420);
    expect(actor.snapshot().state).toBe("removed");
  } finally {
    actor.stop();
  }
});
