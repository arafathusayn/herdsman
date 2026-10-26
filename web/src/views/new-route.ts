// The New route dialog: pick a folder, type the request, press Start. It
// starts an orchestrator (or an adjutant with its orchestrator) in new
// herdr tabs through the server, then shows each step and, for an adjutant,
// the channel setup checklist. The steps that belong to the human stay the
// human's: the app never approves a pair, writes the inbox or types into
// the adjutant's pane.
import { AuthError, catalog, createRoute, inboxState } from "../api.ts";
import { mountElement } from "../dom.ts";
import { html, on, text } from "../herdsman-blocks-view.ts";
import { store } from "../store.ts";
import { dismissOnBackdrop, toast } from "../ui.ts";
import { routeForm } from "./route-form.ts";

async function copy(text: string, what: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text);
    toast(`Copied the ${what}.`);
  } catch {
    toast("The browser blocked the clipboard.", "error");
  }
}

// The dialog and its form are elements the dialog owns: the route form
// renders into the form, and until it does, the form holds the loading or
// the failure parts, each made once.
const Dialog = html`<dialog
  class="dialog new-route"
  aria-label="New route"
  onclose=${on<"close", HTMLDialogElement>()}
></dialog>`;
const Body = html`<form method="dialog" novalidate></form>`;
const Heading = html`<h2>New route</h2>`;
const Loading = html`<p class="nr-loading">Reading the harness lists…</p>`;
const Failure = html`<p class="error">${text()}</p>`;
const CloseAction = html`<div class="dialog-actions"><button value="cancel" class="btn ghost">Close</button></div>`;

/** Opens the New route dialog. */
export function openNewRoute(): void {
  let stop = (): void => {};
  const dlg = mountElement("dialog", Dialog, [
    function () {
      stop();
      this.remove();
    },
  ]);
  const body = mountElement("form", Body, []);
  dlg.append(body);
  dismissOnBackdrop(dlg);
  document.body.append(dlg);
  body.replaceChildren(mountElement("h2", Heading, []), mountElement("p", Loading, []));
  dlg.showModal();
  catalog().then(
    (c) => {
      if (!dlg.open) return;
      const form = routeForm(body, c, store.app.server, {
        createRoute,
        inboxState,
        state: () => store.app.server,
        copy: (text, what) => void copy(text, what),
        openPane: (pane) => store.go({ pane }),
        close: () => dlg.close(),
        failed: (err) => {
          if (err instanceof AuthError) store.set({ fatal: err.message });
          toast(err instanceof Error ? err.message : String(err), "error");
        },
        every: (ms, fn) => {
          const t = setInterval(fn, ms);
          return () => clearInterval(t);
        },
        later: (ms, fn) => {
          const t = setTimeout(fn, ms);
          return () => clearTimeout(t);
        },
      });
      // The route form takes the body's place and focuses its first field.
      stop = form.stop;
    },
    (e: unknown) => {
      if (e instanceof AuthError) store.set({ fatal: e.message });
      body.replaceChildren(
        mountElement("h2", Heading, []),
        mountElement("p", Failure, [e instanceof Error ? e.message : String(e)]),
        mountElement("div", CloseAction, []),
      );
    },
  );
}
