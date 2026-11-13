import { memo, onCleanup, signal } from "@reactre/core";
import { createActor } from "@reactre/state";

import { readonlyValue } from "./readonly.ts";
import { registerRequest } from "./request-service.ts";
import { requestMachine } from "./request.machine.ts";
import { decodeValue, encodeValue } from "./wire.ts";

export type RequestOutcome<T> =
  { status: "done"; value: T } | { status: "failed"; error: unknown } | { status: "cancelled" };

/** Latest request wins. A transition aborts the previous task and the library
 * drops its eventual result even when a transport cannot cancel the request. */
export function createRequest<Input, Output>(run: (input: Input, signal: AbortSignal) => Promise<Output>) {
  const service = registerRequest(async (payload, signal) =>
    encodeValue(await run(decodeValue<Input>(payload), signal)),
  );
  const actor = createActor(requestMachine, { input: { service: service.id, result: encodeValue(undefined) } });
  const [revision, notify] = signal(0);
  const unsubscribe = actor.subscribe(() => notify((n) => n + 1));
  const snapshot = () => {
    revision();
    return actor.snapshot();
  };
  const resultJson = memo(() => snapshot().data.result);
  const data = memo(() => readonlyValue(decodeValue<Output | undefined>(resultJson())));
  const error = memo(() => {
    const s = snapshot();
    return s.state === "failed" ? decodeValue<unknown>(s.data.error) : undefined;
  });
  const waiters = new Set<(outcome: RequestOutcome<Output>) => void>();
  const cancelWaiters = () => {
    for (const done of waiters) done({ status: "cancelled" });
    waiters.clear();
  };
  const stop = () => {
    actor.stop();
    unsubscribe();
    service.dispose();
    cancelWaiters();
  };
  onCleanup(stop);
  return {
    actor,
    snapshot,
    data,
    error,
    loading: () => snapshot().state === "loading",
    cancel: () => {
      if (actor.snapshot().state === "loading") actor.send({ type: "cancel" });
      cancelWaiters();
    },
    clear: () => {
      actor.send({ type: "clear", result: encodeValue(undefined) });
      cancelWaiters();
    },
    stop,
    run(input: Input): Promise<RequestOutcome<Output>> {
      cancelWaiters();
      const sent = actor.send({ type: "run", payload: encodeValue(input) });
      if (!sent.ok) return Promise.resolve({ status: "cancelled" });
      return new Promise((resolve) => {
        const done = (outcome: RequestOutcome<Output>) => {
          off();
          waiters.delete(done);
          resolve(outcome);
        };
        const off = actor.subscribe((s) => {
          if (s.state === "ready") done({ status: "done", value: decodeValue<Output>(s.data.result) });
          else if (s.state === "failed") done({ status: "failed", error: decodeValue<unknown>(s.data.error) });
          else if (s.state !== "loading") done({ status: "cancelled" });
        });
        waiters.add(done);
      });
    },
  };
}
