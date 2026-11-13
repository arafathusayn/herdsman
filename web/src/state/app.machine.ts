import { setup, types } from "@reactre/state";

import type { App } from "../app-state.ts";

// Required workflow fields wrap nested JSON contracts. Payloads are copied and
// frozen at ingress; transitions only share those immutable projections.
const app = setup({
  input: types<{ value: App }>(),
  states: { active: types<{ value: App }>() },
  events: { replace: types<{ value: App }>() },
});

export const appMachine = app.machine({
  start: ({ input, to }) => to("active", { value: input.value }),
  states: {
    active: { on: { replace: ({ event, to }) => to("active", { value: event.value }) } },
  },
});
