// Sends actions, asks before the risky ones, and reports the outcome.
import { act, AuthError } from "./api.ts";
import { store } from "./store.ts";
import type { Action } from "./types.ts";
import { confirmDialog, toast } from "./ui.ts";

/** What the pane itself shows: sending it toasts only on failure. */
const showsOnPane = new Set<Action["type"]>(["keys", "prompt", "text"]);

/**
 * Runs an action and toasts the outcome; keys and messages toast only on
 * failure. A needs_force answer asks the user, then sends it with force.
 */
export async function run(a: Action): Promise<boolean> {
  try {
    let res = await act(a);
    if (res.code === "needs_force" && a.type === "close") {
      const ok = await confirmDialog("Delete this pane?", res.error ?? "", "Delete pane", true);
      if (!ok) return false;
      res = await act({ ...a, force: true });
    }
    if (!res.ok) {
      toast(res.error ?? "The action failed.", "error");
      return false;
    }
    if (!showsOnPane.has(a.type)) toast(res.summary ? capitalize(res.summary) : "Done.");
    return true;
  } catch (e) {
    if (e instanceof AuthError) store.set({ fatal: e.message });
    else toast(e instanceof Error ? e.message : String(e), "error");
    return false;
  }
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}
