import { setup, types } from "@reactre/state";

import { runRequest } from "./request-service.ts";
import { encodeValue } from "./wire.ts";

interface Data {
  service: string;
  line: string;
  done: string;
  error: string;
}

const execution = setup({
  input: types<{ service: string }>(),
  states: {
    idle: types<Data>(),
    confirming: types<Data>(),
    submitting: types<Data>(),
    showing: types<Data & { confirming: boolean }>(),
  },
  events: {
    edit: types<{ line: string }>(),
    ask: types<{}>(),
    cancel: types<{}>(),
    submit: types<{}>(),
    show: types<{}>(),
  },
  internal: { submitted: types<{ message: string }>(), shown: types<{ message: string }>() },
});

/** A page-owned control survives refreshed execution rows. Only the explicit
 * confirmation state accepts submit; pending commands decline duplicates. */
export const executionMachine = execution.machine({
  start: ({ input, to }) => to("idle", { service: input.service, line: "", done: "", error: "" }),
  states: {
    idle: {
      on: {
        edit: ({ data, event, to }) => to("idle", { ...data, line: event.line }),
        ask: ({ data, to }) => to("confirming", { ...data }),
        show: ({ data, to }) => to("showing", { ...data, confirming: false, error: "" }),
      },
    },
    confirming: {
      on: {
        edit: ({ data, event, to }) => to("confirming", { ...data, line: event.line }),
        cancel: ({ data, to }) => to("idle", { ...data }),
        submit: ({ data, to }) => (data.line.trim() ? to("submitting", { ...data, error: "" }) : null),
        show: ({ data, to }) => to("showing", { ...data, confirming: true, error: "" }),
      },
    },
    submitting: {
      task: {
        run: async ({ data, signal }) => ({
          type: "submitted",
          message: await runRequest(data.service, JSON.stringify({ kind: "submit", line: data.line.trim() }), signal),
        }),
        error: ({ data, error, to }) => to("confirming", { ...data, error: encodeValue(error) }),
      },
      on: {
        submitted: ({ data, event, to }) =>
          to("idle", { service: data.service, line: "", done: event.message, error: "" }),
        cancel: ({ data, to }) =>
          to("confirming", {
            ...data,
            done: "Stopped waiting; the runtime may already have received the line.",
          }),
      },
    },
    showing: {
      task: {
        run: async ({ data, signal }) => ({
          type: "shown",
          message: await runRequest(data.service, JSON.stringify({ kind: "show", line: "" }), signal),
        }),
        error: ({ data, error, to }) =>
          data.confirming
            ? to("confirming", { service: data.service, line: data.line, done: data.done, error: encodeValue(error) })
            : to("idle", { service: data.service, line: data.line, done: data.done, error: encodeValue(error) }),
      },
      on: {
        cancel: ({ data, to }) =>
          data.confirming
            ? to("confirming", {
                service: data.service,
                line: data.line,
                done: "Stopped waiting for the runtime.",
                error: "",
              })
            : to("idle", {
                service: data.service,
                line: data.line,
                done: "Stopped waiting for the runtime.",
                error: "",
              }),
        shown: ({ data, event, to }) =>
          data.confirming
            ? to("confirming", { service: data.service, line: data.line, done: event.message, error: "" })
            : to("idle", { service: data.service, line: data.line, done: event.message, error: "" }),
      },
    },
  },
});
