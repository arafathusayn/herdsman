import { setup, types } from "@reactre/state";

import { delayRequest } from "./request-service.ts";
const notice = setup({
  input: types<{ duration: number }>(),
  states: { showing: types<{ duration: number }>(), leaving: types<{}>(), removed: types<{}>() },
  events: { dismiss: types<{}>() },
  internal: { elapsed: types<{}>() },
});
export const noticeMachine = notice.machine({
  start: ({ input, to }) => to("showing", { duration: input.duration }),
  states: {
    showing: {
      task: {
        run: async ({ data, signal }) => {
          await delayRequest(data.duration, signal);
          return { type: "elapsed" };
        },
        error: ({ to }) => to("removed"),
      },
      on: { elapsed: ({ to }) => to("leaving"), dismiss: ({ to }) => to("removed") },
    },
    leaving: {
      task: {
        run: async ({ signal }) => {
          await delayRequest(400, signal);
          return { type: "elapsed" };
        },
        error: ({ to }) => to("removed"),
      },
      on: { elapsed: ({ to }) => to("removed"), dismiss: ({ to }) => to("removed") },
    },
    removed: { final: true },
  },
});
