import { setup, types } from "@reactre/state";
import { reenter } from "@reactre/state/reenter";

import { runRequest } from "./request-service.ts";
import { encodeValue } from "./wire.ts";

const request = setup({
  use: [reenter],
  input: types<{ service: string; result: string }>(),
  states: {
    idle: types<{ service: string; result: string }>(),
    loading: types<{ service: string; result: string; payload: string }>(),
    ready: types<{ service: string; result: string }>(),
    failed: types<{ service: string; result: string; error: string }>(),
  },
  events: {
    run: types<{ payload: string }>(),
    cancel: types<{}>(),
    clear: types<{ result: string }>(),
  },
  internal: { resolved: types<{ result: string }>() },
});

export const requestMachine = request.machine({
  start: ({ input, to }) => to("idle", { service: input.service, result: input.result }),
  states: {
    idle: {
      on: {
        run: ({ data, event, to }) => to("loading", { ...data, payload: event.payload }),
        clear: ({ data, event, to }) => to("idle", { service: data.service, result: event.result }),
        cancel: () => null,
      },
    },
    loading: {
      task: {
        run: async ({ data, signal }) => ({
          type: "resolved",
          result: await runRequest(data.service, data.payload, signal),
        }),
        error: ({ data, error, to }) =>
          to("failed", {
            service: data.service,
            result: data.result,
            error: encodeValue(error),
          }),
      },
      on: {
        resolved: ({ data, event, to }) => to("ready", { service: data.service, result: event.result }),
        run: ({ data, event, reenter }) => reenter("loading", { ...data, payload: event.payload }),
        cancel: ({ data, to }) => to("idle", { service: data.service, result: data.result }),
        clear: ({ data, event, to }) => to("idle", { service: data.service, result: event.result }),
      },
    },
    ready: {
      on: {
        run: ({ data, event, to }) => to("loading", { ...data, payload: event.payload }),
        clear: ({ data, event, to }) => to("idle", { service: data.service, result: event.result }),
        cancel: ({ data, to }) => to("idle", { ...data }),
      },
    },
    failed: {
      on: {
        run: ({ data, event, to }) =>
          to("loading", {
            service: data.service,
            result: data.result,
            payload: event.payload,
          }),
        clear: ({ data, event, to }) => to("idle", { service: data.service, result: event.result }),
        cancel: ({ data, to }) => to("idle", { service: data.service, result: data.result }),
      },
    },
  },
});
