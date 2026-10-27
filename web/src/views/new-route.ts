// The New route dialog: pick a folder, type the request, press Start. It
// starts an orchestrator (or an adjutant with its orchestrator) in new
// herdr tabs through the server, then shows each step and, for an adjutant,
// the channel setup checklist. The steps that belong to the human stay the
// human's: the app never approves a pair, writes the inbox or types into
// the adjutant's pane.
import { AuthError, catalog, createRoute, githubAccess, inboxState, savedChoices } from "../api.ts";
import { html, on, text } from "../herdsman-blocks-view.ts";
import { store } from "../store.ts";
import { dismissOnBackdrop, toast } from "../ui.ts";
import { element } from "./element-view.ts";
import { newRouteFlow } from "./new-route-flow.ts";
import { type Deps, routeForm } from "./route-form.ts";

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

// The dialog and its form are elements the dialog owns: the flow's
// questions and then the route form render into the form, and a failure
// while the lists load replaces the loading parts, each made once.
const Dialog = html`<dialog
  class="dialog new-route"
  aria-label="New route"
  onclose=${on<"close", HTMLDialogElement>()}
></dialog>`;
const Body = html`<form method="dialog" novalidate></form>`;
const Heading = html`<h2>New route</h2>`;
const Failure = html`<p class="error">${text()}</p>`;
const CloseAction = html`<div class="dialog-actions"><button value="cancel" class="btn ghost">Close</button></div>`;

/** Opens the New route dialog. */
export function openNewRoute(): void {
  let stop = (): void => {};
  const dlg = element(
    "dialog",
    Dialog([
      function () {
        stop();
        this.remove();
      },
    ]),
  );
  const body = element("form", Body([]));
  dlg.append(body);
  dismissOnBackdrop(dlg);
  document.body.append(dlg);
  dlg.showModal();
  const flow = newRouteFlow(
    body,
    { savedChoices, catalog },
    // The route form takes the body's place and focuses its first field.
    (c, previous, recommended, needLists) =>
      routeForm(body, c, store.app.server, formDeps(dlg), { usePrevious: previous, recommended, needLists }),
    (e) => {
      failed(e);
      // A failure while the lists load takes the loading line's place; once
      // the route form has taken the body's place, the toast says it.
      if (body.parentNode && body.querySelector(".nr-loading"))
        body.replaceChildren(
          element("h2", Heading([])),
          element("p", Failure([e instanceof Error ? e.message : String(e)])),
          element("div", CloseAction([])),
        );
    },
  );
  stop = flow.stop;
}
