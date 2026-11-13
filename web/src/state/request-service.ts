// Resource registry only: callback implementations are not application state.
// Each actor removes its service on disposal; the actor owns all request data.
const services = new Map<string, (payload: string, signal: AbortSignal) => Promise<string>>();
let nextService = 0;
export function registerRequest(run: (payload: string, signal: AbortSignal) => Promise<string>) {
  const id = String(nextService++);
  services.set(id, run);
  return {
    id,
    dispose: () => {
      services.delete(id);
    },
  };
}
export function runRequest(id: string, payload: string, signal: AbortSignal): Promise<string> {
  const run = services.get(id);
  if (!run) return Promise.reject(new Error("Request owner was disposed"));
  signal.throwIfAborted();
  return run(payload, signal);
}

export function delayRequest(ms: number, signal: AbortSignal): Promise<void> {
  signal.throwIfAborted();
  return new Promise((resolve, reject) => {
    const abort = () => {
      clearTimeout(timer);
      reject(signal.reason);
    };
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", abort);
      resolve();
    }, ms);
    signal.addEventListener("abort", abort, { once: true });
  });
}
