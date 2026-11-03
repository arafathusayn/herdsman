import { attach, For, onCleanup, signal } from "@reactre/core";

interface Toast {
  id: number;
  message: string;
  kind: "ok" | "error";
  leaving: boolean;
}

const [items, setItems] = signal<Toast[]>([]);
const timers = new Set<ReturnType<typeof setTimeout>>();
let host: HTMLDivElement | null = null;
let disposeHost = (): void => {};
let nextId = 0;

function Toasts() {
  onCleanup(() => {
    for (const timer of timers) clearTimeout(timer);
    timers.clear();
  });
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
        {(item) => <div class={`toast ${item.kind}${item.leaving ? " leave" : ""}`}>{item.message}</div>}
      </For>
    </div>
  );
}

function later(fn: () => void, delay: number): void {
  const timer = setTimeout(() => {
    timers.delete(timer);
    fn();
  }, delay);
  timers.add(timer);
}

/** Toast contents and exit animations follow the shared notification signal. */
export function toast(message: string, kind: "ok" | "error" = "ok"): void {
  if (host?.parentNode !== document.body) {
    disposeHost();
    setItems([]);
    disposeHost = attach(Toasts, document.body);
  }
  const id = nextId++;
  setItems((current) => [...current, { id, message, kind, leaving: false }]);
  later(
    () => setItems((current) => current.map((item) => (item.id === id ? { ...item, leaving: true } : item))),
    kind === "error" ? 6000 : 3000,
  );
  later(() => setItems((current) => current.filter((item) => item.id !== id)), kind === "error" ? 6400 : 3400);
}
