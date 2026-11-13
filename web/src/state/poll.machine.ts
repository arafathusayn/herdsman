import { setup, types } from "@reactre/state";
import { reenter } from "@reactre/state/reenter";

import { delayRequest, runRequest } from "./request-service.ts";

const poll = setup({
  use: [reenter],
  input: types<{ service: string; retry: number }>(),
  states: {
    idle: types<{ service: string; retry: number }>(),
    reading: types<{ service: string; retry: number; again: boolean }>(),
    waiting: types<{ service: string; retry: number; delay: number }>(),
  },
  events: { start: types<{}>(), stop: types<{}>(), wake: types<{}>() },
  internal: { read: types<{ delay: number }>(), elapsed: types<{}>() },
});
export const pollMachine = poll.machine({
  start: ({ input, to }) => to("idle", { service: input.service, retry: input.retry }),
  states: {
    idle: {
      on: {
        start: ({ data, to }) => to("reading", { ...data, again: false }),
        wake: () => null,
        stop: () => null,
      },
    },
    reading: {
      task: {
        run: async ({ data, signal }) => ({ type: "read", delay: Number(await runRequest(data.service, "", signal)) }),
        error: ({ data, to }) => to("waiting", { service: data.service, retry: data.retry, delay: data.retry }),
      },
      on: {
        read: ({ data, event, to, reenter }) =>
          data.again
            ? reenter("reading", { ...data, again: false })
            : to("waiting", { service: data.service, retry: data.retry, delay: event.delay }),
        wake: ({ data, keep }) => keep("reading", { ...data, again: true }),
        start: () => null,
        stop: ({ data, to }) => to("idle", { service: data.service, retry: data.retry }),
      },
    },
    waiting: {
      task: {
        run: async ({ data, signal }) => {
          await delayRequest(data.delay, signal);
          return { type: "elapsed" };
        },
        error: ({ data, to }) => to("idle", { service: data.service, retry: data.retry }),
      },
      on: {
        elapsed: ({ data, to }) => to("reading", { service: data.service, retry: data.retry, again: false }),
        wake: ({ data, to }) => to("reading", { service: data.service, retry: data.retry, again: false }),
        start: () => null,
        stop: ({ data, to }) => to("idle", { service: data.service, retry: data.retry }),
      },
    },
  },
});
