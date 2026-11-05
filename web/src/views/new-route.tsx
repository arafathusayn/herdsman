import { attach, onCleanup } from "@reactre/core";

// The New route dialog: pick a folder, type the request, press Start. It
// starts an orchestrator (or an adjutant with its orchestrator) in new
// herdr tabs through the server, then shows each step and, for an adjutant,
// the channel setup checklist. The steps that belong to the human stay the
// human's: the app never approves a pair, writes the inbox or types into
// the adjutant's pane.
import { AuthError, catalog, createRoute, githubAccess, inboxState, savedChoices } from "../api.ts";
import { store } from "../store.ts";
import { dismissOnBackdrop, toast } from "../ui.ts";
import { newRouteFlow } from "./new-route-flow.tsx";
import { type Deps, routeForm } from "./route-form.tsx";

async function copy(text: string, what: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text);
    toast(`Copied the ${what}.`);
  } catch {
    toast("The browser blocked the clipboard.", "error");
  }
}

function failed(err: unknown): void {
  if (err instanceof AuthError) store.set({ fatal: err.message });
  toast(err instanceof Error ? err.message : String(err), "error");
}

function formDeps(dlg: HTMLDialogElement): Deps {
  return {
    createRoute,
    inboxState,
    github: githubAccess,
    state: () => store.app.server,
    copy: (text, what) => void copy(text, what),
    openPane: (pane) => store.go({ pane }),
    close: () => dlg.close(),
    failed,
    every: (ms, fn) => {
      const t = setInterval(fn, ms);
      return () => clearInterval(t);
    },
    later: (ms, fn) => {
      const t = setTimeout(fn, ms);
      return () => clearTimeout(t);
    },
  };
}

/** Opens one owned dialog; closing stops the flow and its form's requests. */
export function openNewRoute(): void {
  let dialog!: HTMLDialogElement, body!: HTMLFormElement;
  let stop = (): void => {},
    dispose = (): void => {};
  dispose = attach(() => {
    onCleanup(() => stop());
    return (
      <dialog
        class="dialog new-route"
        aria-label="New route"
        ref={(element) => {
          dialog = element;
        }}
        onClose={() => dispose()}
      >
        <form
          method="dialog"
          novalidate
          ref={(element) => {
            body = element;
          }}
        />
      </dialog>
    );
  }, document.body);
  dismissOnBackdrop(dialog);
  dialog.showModal();
  stop = newRouteFlow(
    body,
    { savedChoices, catalog },
    (catalog, previous, recommended, needLists) =>
      routeForm(body, catalog, store.app.server, formDeps(dialog), { usePrevious: previous, recommended, needLists }),
    failed,
  ).stop;
}
