import { memo, onCleanup, signal, untrack } from "@reactre/core";
import { createActor } from "@reactre/state";

import { readonlyValue } from "./readonly.ts";
import { valueMachine } from "./value.machine.ts";
import { decodeValue, encodeValue } from "./wire.ts";

/** An actor owns the value. The signal is only an invalidation notification;
 * the memo is a derived, decoded read of its immutable JSON snapshot. */
export function valueState<T>(initial: T): readonly [() => T, (next: T | ((previous: T) => T)) => void] {
  const actor = createActor(valueMachine, {
    input: { json: encodeValue(initial) },
  });
  const [revision, notify] = signal(0);
  const unsubscribe = actor.subscribe(() => notify((n) => n + 1));
  const read = memo(() => {
    revision();
    return readonlyValue(decodeValue<T>(actor.snapshot().data.json));
  });
  onCleanup(() => {
    unsubscribe();
    actor.stop();
  });
  const write = (next: T | ((previous: T) => T)): void => {
    const value = typeof next === "function" ? (next as (previous: T) => T)(untrack(read)) : next;
    const json = encodeValue(value);
    if (json !== actor.snapshot().data.json) actor.send({ type: "replace", json });
  };
  return [read, write];
}
