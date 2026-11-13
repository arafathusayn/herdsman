import { onCleanup, signal } from "@reactre/core";

/** Subscription glue only: reads always come from the actor's snapshot. */
export function observeActor<S>(actor: {
  snapshot(): S;
  subscribe(listener: (snapshot: S) => void): () => void;
  stop(): void;
}) {
  const [revision, notify] = signal(0);
  const off = actor.subscribe(() => notify((n) => n + 1));
  const stop = () => {
    off();
    actor.stop();
  };
  onCleanup(stop);
  return {
    read: () => {
      revision();
      return actor.snapshot();
    },
    stop,
  };
}
