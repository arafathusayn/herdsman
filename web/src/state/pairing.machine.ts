import { setup, types } from "@reactre/state";

import { runRequest } from "./request-service.ts";
import { decodeUnknown, errorMessage } from "./wire.ts";

const pairing = setup({
  input: types<{ service: string }>(),
  states: {
    editing: types<{ service: string; code: string; message: string; error: boolean }>(),
    checking: types<{ service: string; code: string }>(),
    paired: types<{ device: string }>(),
    ended: types<{ message: string }>(),
  },
  events: { edit: types<{ code: string }>(), submit: types<{}>(), cancel: types<{}>() },
  internal: { checked: types<{ status: string; device: string; message: string; left: number }>() },
});

export const pairingMachine = pairing.machine({
  start: ({ input, to }) => to("editing", { service: input.service, code: "", message: "", error: false }),
  states: {
    editing: {
      on: {
        edit: ({ data, event, to }) => to("editing", { ...data, code: event.code }),
        submit: ({ data, to }) =>
          /^\d{6}$/.test(data.code) ? to("checking", { service: data.service, code: data.code }) : null,
      },
    },
    checking: {
      task: {
        run: async ({ data, signal }) => {
          const status = decodeUnknown(await runRequest(data.service, data.code, signal));
          if (
            typeof status !== "object" ||
            status === null ||
            !("status" in status) ||
            typeof status.status !== "string"
          )
            throw new Error("Invalid pairing response");
          return {
            type: "checked",
            status: status.status,
            device: "device" in status && typeof status.device === "string" ? status.device : "",
            message: "error" in status && typeof status.error === "string" ? status.error : "",
            left: "left" in status && typeof status.left === "number" ? status.left : 0,
          };
        },
        error: ({ data, error, to }) => to("editing", { ...data, message: errorMessage(error), error: true }),
      },
      on: {
        cancel: ({ data, to }) => to("editing", { ...data, message: "", error: false }),
        checked: ({ data, event, to }) => {
          if (event.status === "paired" && event.device) return to("paired", { device: event.device });
          if (event.status === "none")
            return to("ended", { message: event.message === "" ? "This pairing request ended." : event.message });
          return to("editing", {
            service: data.service,
            code: "",
            error: true,
            message: event.left
              ? `${event.message === "" ? "That code is wrong." : event.message} ${event.left} ${event.left === 1 ? "try" : "tries"} left.`
              : event.message,
          });
        },
      },
    },
    paired: { final: true },
    ended: { final: true },
  },
});
