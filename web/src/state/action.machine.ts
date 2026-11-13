import { setup, types } from "@reactre/state";

import { actionReply, confirmReply } from "./action-data.ts";
import { runRequest } from "./request-service.ts";
import { errorMessage } from "./wire.ts";

const action = setup({
  input: types<{ service: string; action: string }>(),
  states: {
    sending: types<{ service: string; action: string }>(),
    confirming: types<{ service: string; action: string; reason: string }>(),
    forcing: types<{ service: string; action: string }>(),
    succeeded: types<{ summary: string }>(),
    failed: types<{ error: string }>(),
    cancelled: types<{}>(),
  },
  events: { cancel: types<{}>() },
  internal: {
    completed: types<{ status: string; message: string }>(),
    confirmed: types<{}>(),
    declined: types<{}>(),
  },
});
export const actionMachine = action.machine({
  start: ({ input, to }) => to("sending", { service: input.service, action: input.action }),
  states: {
    sending: {
      task: {
        run: async ({ data, signal }) =>
          actionReply(
            await runRequest(data.service, JSON.stringify({ kind: "send", action: data.action, reason: "" }), signal),
          ),
        error: ({ error, to }) => to("failed", { error: errorMessage(error) }),
      },
      on: {
        completed: ({ data, event, to }) =>
          event.status === "confirm"
            ? to("confirming", { ...data, reason: event.message })
            : event.status === "ok"
              ? to("succeeded", { summary: event.message })
              : to("failed", { error: event.message }),
        cancel: ({ to }) => to("cancelled"),
      },
    },
    confirming: {
      task: {
        run: async ({ data, signal }) =>
          confirmReply(
            await runRequest(
              data.service,
              JSON.stringify({ kind: "confirm", action: data.action, reason: data.reason }),
              signal,
            ),
          ),
        error: ({ error, to }) => to("failed", { error: errorMessage(error) }),
      },
      on: {
        confirmed: ({ data, to }) => to("forcing", { service: data.service, action: data.action }),
        declined: ({ to }) => to("cancelled"),
        cancel: ({ to }) => to("cancelled"),
      },
    },
    forcing: {
      task: {
        run: async ({ data, signal }) =>
          actionReply(
            await runRequest(data.service, JSON.stringify({ kind: "force", action: data.action, reason: "" }), signal),
          ),
        error: ({ error, to }) => to("failed", { error: errorMessage(error) }),
      },
      on: {
        completed: ({ event, to }) =>
          event.status === "ok" ? to("succeeded", { summary: event.message }) : to("failed", { error: event.message }),
        cancel: ({ to }) => to("cancelled"),
      },
    },
    succeeded: { final: true },
    failed: { final: true },
    cancelled: { final: true },
  },
});
