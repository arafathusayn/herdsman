import { setup, types } from "@reactre/state";
import { reenter } from "@reactre/state/reenter";

import { delayRequest, runRequest } from "./request-service.ts";
const copy = setup({
  use: [reenter],
  input: types<{ service: string }>(),
  states: {
    idle: types<{ service: string }>(),
    copying: types<{ service: string; text: string }>(),
    shown: types<{ service: string; ok: boolean }>(),
    closed: types<{}>(),
  },
  events: { copy: types<{ text: string }>(), close: types<{}>() },
  internal: { copied: types<{ ok: boolean }>(), elapsed: types<{}>() },
});
export const copyMachine = copy.machine({
  start: ({ input, to }) => to("idle", { service: input.service }),
  states: {
    idle: {
      on: {
        copy: ({ data, event, to }) => to("copying", { ...data, text: event.text }),
        close: ({ to }) => to("closed"),
      },
    },
    copying: {
      task: {
        run: async ({ data, signal }) => ({
          type: "copied",
          ok: (await runRequest(data.service, data.text, signal)) === "true",
        }),
        error: ({ data, to }) => to("shown", { service: data.service, ok: false }),
      },
      on: {
        copied: ({ data, event, to }) => to("shown", { service: data.service, ok: event.ok }),
        copy: ({ data, event, reenter }) => reenter("copying", { ...data, text: event.text }),
        close: ({ to }) => to("closed"),
      },
    },
    shown: {
      task: {
        run: async ({ signal }) => {
          await delayRequest(1600, signal);
          return { type: "elapsed" };
        },
        error: ({ data, to }) => to("idle", { service: data.service }),
      },
      on: {
        elapsed: ({ data, to }) => to("idle", { service: data.service }),
        copy: ({ data, event, to }) => to("copying", { service: data.service, text: event.text }),
        close: ({ to }) => to("closed"),
      },
    },
    closed: { final: true },
  },
});
