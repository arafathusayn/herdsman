import { setup, types } from "@reactre/state";
import { reenter } from "@reactre/state/reenter";

import { delayRequest } from "./request-service.ts";

const tooltip = setup({
  use: [reenter],
  states: {
    idle: types<{ warmUntil: number }>(),
    waiting: types<{ warmUntil: number; delay: number }>(),
    placing: types<{ warmUntil: number }>(),
    showing: types<{}>(),
    closed: types<{}>(),
  },
  events: {
    arm: types<{ now: number; delay: number }>(),
    placed: types<{}>(),
    hide: types<{ now: number }>(),
    close: types<{}>(),
  },
  internal: { elapsed: types<{}>() },
});

/** DOM targets are installer resources. The actor owns the delay, visibility
 * and warm reuse window; leaving waiting aborts its timer. */
export const tooltipMachine = tooltip.machine({
  start: ({ to }) => to("idle", { warmUntil: 0 }),
  states: {
    idle: {
      on: {
        arm: ({ data, event, to }) => to("waiting", { ...data, delay: event.now < data.warmUntil ? 0 : event.delay }),
        hide: () => null,
        close: ({ to }) => to("closed"),
      },
    },
    waiting: {
      task: {
        run: async ({ data, signal }) => {
          await delayRequest(data.delay, signal);
          return { type: "elapsed" };
        },
        error: ({ data, to }) => to("idle", { warmUntil: data.warmUntil }),
      },
      on: {
        elapsed: ({ data, to }) => to("placing", { warmUntil: data.warmUntil }),
        arm: ({ data, event, reenter }) =>
          reenter("waiting", { warmUntil: data.warmUntil, delay: event.now < data.warmUntil ? 0 : event.delay }),
        hide: ({ data, to }) => to("idle", { warmUntil: data.warmUntil }),
        close: ({ to }) => to("closed"),
      },
    },
    placing: {
      on: {
        placed: ({ to }) => to("showing"),
        hide: ({ data, to }) => to("idle", { warmUntil: data.warmUntil }),
        close: ({ to }) => to("closed"),
      },
    },
    showing: {
      on: {
        hide: ({ event, to }) => to("idle", { warmUntil: event.now + 600 }),
        close: ({ to }) => to("closed"),
      },
    },
    closed: { final: true },
  },
});
