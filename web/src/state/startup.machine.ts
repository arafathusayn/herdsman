import { setup, types } from "@reactre/state";

import { runRequest } from "./request-service.ts";
import { startupReply } from "./startup-data.ts";
import { errorMessage } from "./wire.ts";
const startup = setup({
  input: types<{ service: string; context: string }>(),
  states: {
    inspecting: types<{ service: string; context: string }>(),
    trusting: types<{ service: string; context: string }>(),
    selecting: types<{ service: string; context: string }>(),
    checking: types<{ service: string; context: string }>(),
    connected: types<{}>(),
    pairing: types<{}>(),
    unavailable: types<{}>(),
    failed: types<{ error: string }>(),
    cancelled: types<{}>(),
  },
  events: { cancel: types<{}>() },
  internal: { settled: types<{ status: string; context: string }>() },
});
export const startupMachine = startup.machine({
  start: ({ input, to }) => to("inspecting", { service: input.service, context: input.context }),
  states: {
    inspecting: {
      task: {
        run: async ({ data, signal }) =>
          startupReply(
            await runRequest(data.service, JSON.stringify({ step: "inspecting", context: data.context }), signal),
          ),
        error: ({ error, to }) => to("failed", { error: errorMessage(error) }),
      },
      on: {
        settled: ({ data, event, to }) =>
          event.status === "trust"
            ? to("trusting", { ...data, context: event.context })
            : to("selecting", { ...data, context: event.context }),
        cancel: ({ to }) => to("cancelled"),
      },
    },
    trusting: {
      task: {
        run: async ({ data, signal }) =>
          startupReply(
            await runRequest(data.service, JSON.stringify({ step: "trusting", context: data.context }), signal),
          ),
        error: ({ error, to }) => to("failed", { error: errorMessage(error) }),
      },
      on: {
        settled: ({ data, event, to }) =>
          event.status === "select" ? to("selecting", { ...data, context: event.context }) : to("unavailable"),
        cancel: ({ to }) => to("cancelled"),
      },
    },
    selecting: {
      task: {
        run: async ({ data, signal }) =>
          startupReply(
            await runRequest(data.service, JSON.stringify({ step: "selecting", context: data.context }), signal),
          ),
        error: ({ error, to }) => to("failed", { error: errorMessage(error) }),
      },
      on: {
        settled: ({ data, event, to }) =>
          event.status === "check" ? to("checking", { ...data, context: event.context }) : to("unavailable"),
        cancel: ({ to }) => to("cancelled"),
      },
    },
    checking: {
      task: {
        run: async ({ data, signal }) =>
          startupReply(
            await runRequest(data.service, JSON.stringify({ step: "checking", context: data.context }), signal),
          ),
        error: ({ error, to }) => to("failed", { error: errorMessage(error) }),
      },
      on: {
        settled: ({ event, to }) =>
          event.status === "paired" ? to("connected") : event.status === "pairing" ? to("pairing") : to("unavailable"),
        cancel: ({ to }) => to("cancelled"),
      },
    },
    connected: { final: true },
    pairing: { final: true },
    unavailable: { final: true },
    failed: { final: true },
    cancelled: { final: true },
  },
});
