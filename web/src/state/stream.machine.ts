import { setup, types } from "@reactre/state";

import { delayRequest, runRequest } from "./request-service.ts";

const stream = setup({
  input: types<{ service: string }>(),
  states: {
    connecting: types<{ service: string; delay: number }>(),
    streaming: types<{ service: string }>(),
    waiting: types<{ service: string; delay: number }>(),
    unauthorized: types<{}>(),
    closed: types<{}>(),
  },
  events: { close: types<{}>() },
  internal: { authorized: types<{}>(), denied: types<{}>(), ended: types<{}>(), elapsed: types<{}>() },
});
export const streamMachine = stream.machine({
  start: ({ input, to }) => to("connecting", { service: input.service, delay: 500 }),
  states: {
    connecting: {
      task: {
        run: async ({ data, signal }) =>
          (await runRequest(data.service, "open", signal)) === "ready" ? { type: "authorized" } : { type: "denied" },
        error: ({ data, to }) => to("waiting", { ...data }),
      },
      on: {
        authorized: ({ data, to }) => to("streaming", { service: data.service }),
        denied: ({ to }) => to("unauthorized"),
        close: ({ to }) => to("closed"),
      },
    },
    streaming: {
      task: {
        run: async ({ data, signal }) => {
          await runRequest(data.service, "read", signal);
          return { type: "ended" };
        },
        error: ({ data, to }) => to("waiting", { ...data, delay: 500 }),
      },
      on: {
        ended: ({ data, to }) => to("waiting", { ...data, delay: 500 }),
        close: ({ to }) => to("closed"),
      },
    },
    waiting: {
      task: {
        run: async ({ data, signal }) => {
          await delayRequest(data.delay, signal);
          return { type: "elapsed" };
        },
        error: ({ to }) => to("closed"),
      },
      on: {
        elapsed: ({ data, to }) => to("connecting", { ...data, delay: Math.min(data.delay * 2, 8000) }),
        close: ({ to }) => to("closed"),
      },
    },
    unauthorized: { final: true },
    closed: { final: true },
  },
});
