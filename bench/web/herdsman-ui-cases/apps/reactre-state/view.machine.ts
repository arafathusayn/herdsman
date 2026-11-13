import { setup, types } from "@reactre/state";

import type { State } from "../../shared/state.ts";

const view = setup({
  states: { empty: types<{}>(), showing: types<{ value: State }>() },
  events: { render: types<{ next: State }>() },
});

// Every fresh driver snapshot enters the actor. No operation names, snapshot
// equality shortcuts or retained mutable model bypass this transition.
export const viewMachine = view.machine({
  start: ({ to }) => to("empty"),
  states: {
    empty: {
      on: { render: ({ event, to }) => to("showing", { value: event.next }) },
    },
    showing: {
      on: { render: ({ event, to }) => to("showing", { value: event.next }) },
    },
  },
});
