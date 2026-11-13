import { setup, types } from "@reactre/state";
import { nested } from "@reactre/state/nested";

import { runRequest } from "./request-service.ts";
import { errorMessage } from "./wire.ts";

const setupFlow = setup({
  use: [nested],
  input: types<{ service: string }>(),
  states: {
    "questions.recommended": types<{ service: string }>(),
    "questions.saved": types<{ service: string; recommended: boolean }>(),
    "questions.previous": types<{ service: string; recommended: boolean; lines: string }>(),
    loading: types<{ service: string; recommended: boolean; previous: boolean }>(),
    failed: types<{ service: string; recommended: boolean; error: string }>(),
    opened: types<{ recommended: boolean; previous: boolean; catalog: string }>(),
    cancelled: types<{}>(),
  },
  events: { answer: types<{ recommended: boolean }>(), previous: types<{}>(), probe: types<{}>(), cancel: types<{}>() },
  internal: { saved: types<{ lines: string }>(), loaded: types<{ catalog: string }>() },
});
export const routeSetupMachine = setupFlow.machine({
  start: ({ input, to }) => to("questions.recommended", { service: input.service }),
  states: {
    questions: { on: { cancel: ({ to }) => to("cancelled") } },
    "questions.recommended": {
      on: { answer: ({ data, event, to }) => to("questions.saved", { ...data, recommended: event.recommended }) },
    },
    "questions.saved": {
      task: {
        run: async ({ data, signal }) => ({ type: "saved", lines: await runRequest(data.service, "saved", signal) }),
        error: ({ data, to }) => to("loading", { ...data, previous: false }),
      },
      on: {
        saved: ({ data, event, to }) =>
          event.lines === "[]"
            ? to("loading", { ...data, previous: false })
            : to("questions.previous", { ...data, lines: event.lines }),
      },
    },
    "questions.previous": {
      on: {
        previous: ({ data, to }) =>
          to("loading", { service: data.service, recommended: data.recommended, previous: true }),
        probe: ({ data, to }) =>
          to("loading", { service: data.service, recommended: data.recommended, previous: false }),
      },
    },
    loading: {
      task: {
        run: async ({ data, signal }) => ({
          type: "loaded",
          catalog: await runRequest(data.service, data.previous ? "previous" : "probe", signal),
        }),
        error: ({ data, error, to }) =>
          to("failed", {
            service: data.service,
            recommended: data.recommended,
            error: errorMessage(error),
          }),
      },
      on: {
        loaded: ({ data, event, to }) =>
          to("opened", { recommended: data.recommended, previous: data.previous, catalog: event.catalog }),
        cancel: ({ to }) => to("cancelled"),
      },
    },
    failed: {
      on: {
        probe: ({ data, to }) =>
          to("loading", { service: data.service, recommended: data.recommended, previous: false }),
        previous: ({ data, to }) =>
          to("loading", { service: data.service, recommended: data.recommended, previous: true }),
        cancel: ({ to }) => to("cancelled"),
      },
    },
    opened: { final: true },
    cancelled: { final: true },
  },
});
