import { createActor } from "@reactre/state";

// Sends actions, asks before the risky ones, and reports the outcome.
import { act, AuthError } from "./api.ts";
import { actionMachine } from "./state/action.machine.ts";
import { registerRequest } from "./state/request-service.ts";
import { decodeValue, encodeValue } from "./state/wire.ts";
import { store } from "./store.ts";
import type { Action } from "./types.ts";
import { confirmDialog, toast } from "./ui.ts";

/** What the pane itself shows: sending it toasts only on failure. */
const showsOnPane = new Set<Action["type"]>(["keys", "prompt", "text"]);

/**
 * Runs an action and toasts the outcome; keys and messages toast only on
 * failure. A needs_force answer asks the user, then sends it with force.
 */
export async function run(a: Action, signal?: AbortSignal): Promise<boolean> {
  if (signal?.aborted) return false;
  const service = registerRequest(async (json, taskSignal) => {
    const command = JSON.parse(json) as { kind: string; action: string; reason: string };
    const action = decodeValue<Action>(command.action);
    if (command.kind === "confirm")
      return String(await confirmDialog("Delete this pane?", command.reason, "Delete pane", true, taskSignal));
    try {
      const result = await act(
        command.kind === "force" && action.type === "close" ? { ...action, force: true } : action,
        taskSignal,
      );
      taskSignal.throwIfAborted();
      return JSON.stringify({
        status: result.code === "needs_force" && action.type === "close" ? "confirm" : result.ok ? "ok" : "failed",
        message: result.ok ? (result.summary ?? "") : (result.error ?? "The action failed."),
      });
    } catch (error) {
      if (!taskSignal.aborted && error instanceof AuthError) store.set({ fatal: error.message });
      throw error;
    }
  });
  const actor = createActor(actionMachine, { input: { service: service.id, action: encodeValue(a) } });
  return new Promise((resolve) => {
    const cancel = () => {
      actor.send({ type: "cancel" });
    };
    const off = actor.subscribe((snapshot) => {
      if (snapshot.state !== "succeeded" && snapshot.state !== "failed" && snapshot.state !== "cancelled") return;
      off();
      signal?.removeEventListener("abort", cancel);
      service.dispose();
      actor.stop();
      if (snapshot.state === "failed") toast(snapshot.data.error, "error");
      if (snapshot.state === "succeeded" && !showsOnPane.has(a.type))
        toast(snapshot.data.summary ? capitalize(snapshot.data.summary) : "Done.");
      resolve(snapshot.state === "succeeded");
    });
    signal?.addEventListener("abort", cancel, { once: true });
    if (signal?.aborted) cancel();
  });
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}
