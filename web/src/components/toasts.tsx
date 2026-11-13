import { attach, effect, For } from "@reactre/core";
import { createActor } from "@reactre/state";

import { noticeMachine } from "../state/notice.machine.ts";
import { observeActor } from "../state/observe.ts";
import { valueState } from "../state/value.ts";

interface Toast {
  id: number;
  message: string;
  kind: "ok" | "error";
}

const [items, setItems] = valueState<Toast[]>([]);
let host: HTMLDivElement | null = null;
let disposeHost = (): void => {};
let nextId = 0;

function Notification(props: { item: Toast }) {
  const actor = createActor(noticeMachine, { input: { duration: props.item.kind === "error" ? 6000 : 3000 } });
  const state = observeActor(actor).read;
  effect(() => {
    if (state().state === "removed") setItems((items) => items.filter((item) => item.id !== props.item.id));
  });
  return (
    <div class={`toast ${props.item.kind}${state().state === "leaving" ? " leave" : ""}`}>{props.item.message}</div>
  );
}
function Toasts() {
  return (
    <div
      class="toasts"
      role="status"
      aria-live="polite"
      ref={(element) => {
        host = element;
      }}
    >
      <For each={items()} key={(item) => item.id}>
        {(item) => <Notification item={item} />}
      </For>
    </div>
  );
}

/** Toast contents and exit animations follow the notification actor and its timed statechart. */
export function toast(message: string, kind: "ok" | "error" = "ok"): void {
  if (host?.parentNode !== document.body) {
    disposeHost();
    setItems([]);
    disposeHost = attach(Toasts, document.body);
  }
  const id = nextId++;
  setItems((current) => [...current, { id, message, kind }]);
}
