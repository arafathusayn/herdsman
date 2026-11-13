import { setup, types } from "@reactre/state";
import { reenter } from "@reactre/state/reenter";

import { mergeInput } from "./input-queue-data.ts";
import { runRequest } from "./request-service.ts";

const queue = setup({
  use: [reenter],
  input: types<{ service: string }>(),
  states: {
    idle: types<{ service: string }>(),
    sending: types<{ service: string; input: string; pending: string[] }>(),
  },
  events: { push: types<{ input: string }>(), cancel: types<{}>() },
  internal: { sent: types<{ accepted: boolean }>() },
});
export const inputQueueMachine = queue.machine({
  start: ({ input, to }) => to("idle", { service: input.service }),
  states: {
    idle: {
      on: {
        push: ({ data, event, to }) => to("sending", { ...data, input: event.input, pending: [] }),
        cancel: () => null,
      },
    },
    sending: {
      task: {
        run: async ({ data, signal }) => ({
          type: "sent",
          accepted: (await runRequest(data.service, data.input, signal)) === "true",
        }),
        error: ({ data, to }) => to("idle", { service: data.service }),
      },
      on: {
        push: ({ data, event, keep }) => keep("sending", { ...data, pending: mergeInput(data.pending, event.input) }),
        cancel: ({ data, to }) => to("idle", { service: data.service }),
        sent: ({ data, event, to, reenter }) => {
          const input = data.pending[0];
          return event.accepted && input !== undefined
            ? reenter("sending", { service: data.service, input, pending: data.pending.slice(1) })
            : to("idle", { service: data.service });
        },
      },
    },
  },
});
