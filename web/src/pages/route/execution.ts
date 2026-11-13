import { onCleanup } from "@reactre/core";
import { createActor } from "@reactre/state";

import { explain, services } from "../../sdk/index.ts";
import { executionMachine } from "../../state/execution.machine.ts";
import { observeActor } from "../../state/observe.ts";
import { registerRequest } from "../../state/request-service.ts";
import { controllerScope } from "../../state/scope.ts";
import { decodeValue } from "../../state/wire.ts";

function control(id: string, changed: () => void) {
  const service = registerRequest(async (json, signal) => {
    const command = JSON.parse(json) as { kind: string; line: string };
    if (command.kind === "show") {
      await services().runtime.show({ executionId: id }, { signal });
      signal.throwIfAborted();
      return "Shown in the herdr app.";
    }
    const result = await services().runtime.submit(
      { executionId: id, text: command.line, confirmed: true },
      { signal },
    );
    signal.throwIfAborted();
    changed();
    return `Typed submit ${result.submitId ?? ""}: ${result.state ?? ""}`;
  });
  onCleanup(service.dispose);
  const actor = createActor(executionMachine, { input: { service: service.id } });
  const state = observeActor(actor);
  return {
    line: () => state.read().data.line,
    done: () => state.read().data.done,
    problem: () => (state.read().data.error ? explain(decodeValue(state.read().data.error)) : ""),
    asking: () => {
      const s = state.read();
      return s.state === "confirming" || s.state === "submitting" || (s.state === "showing" && s.data.confirming);
    },
    busy: () => state.read().state === "submitting" || state.read().state === "showing",
    edit: (line: string) => {
      actor.send({ type: "edit", line });
    },
    toggle: () => {
      actor.send({ type: state.read().state === "confirming" ? "cancel" : "ask" });
    },
    show: () => {
      actor.send({ type: "show" });
    },
    cancel: () => {
      actor.send({ type: "cancel" });
    },
    submit: (event: Event) => {
      event.preventDefault();
      actor.send({ type: "submit" });
    },
  };
}

export type ExecutionControls = ReturnType<typeof executionControls>;

/** The map holds controller resources; their actors own drafts and phases.
 * Row refreshes keep them, and leaving the page stops every pending task. */
export function executionControls(changed: () => void) {
  const controls = new Map<string, ReturnType<typeof control> & { dispose(): void }>();
  onCleanup(() => {
    for (const current of controls.values()) current.dispose();
    controls.clear();
  });
  return {
    get(id: string) {
      let current = controls.get(id);
      if (!current) {
        current = controllerScope(() => control(id, changed));
        controls.set(id, current);
      }
      return current;
    },
  };
}
