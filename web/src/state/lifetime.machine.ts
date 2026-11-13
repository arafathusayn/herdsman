import { setup, types } from "@reactre/state";
const lifetime = setup({ states: { open: types<{}>(), closed: types<{}>() }, events: { close: types<{}>() } });
export const lifetimeMachine = lifetime.machine({
  start: ({ to }) => to("open"),
  states: { open: { on: { close: ({ to }) => to("closed") } }, closed: { final: true } },
});
