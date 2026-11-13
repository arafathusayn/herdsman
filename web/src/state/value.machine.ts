import { setup, types } from "@reactre/state";

// SDK replies have optional fields and collection-valued view preferences.
// Their lossless JSON representation crosses this contract; live resources
// (DOM nodes, callbacks, actors) never enter it.
const value = setup({
  input: types<{ json: string }>(),
  states: { active: types<{ json: string }>() },
  events: { replace: types<{ json: string }>() },
});

export const valueMachine = value.machine({
  start: ({ input, to }) => to("active", { json: input.json }),
  states: {
    active: {
      on: { replace: ({ event, to }) => to("active", { json: event.json }) },
    },
  },
});
