// The New route dialog: pick a folder, type the request, press Start. It
// starts an orchestrator (or an adjutant with its orchestrator) in new
// herdr tabs through the server, then shows each step and, for an adjutant,
// the channel setup checklist. The steps that belong to the human stay the
// human's: the app never approves a pair, writes the inbox or types into
// the adjutant's pane.
import { AuthError, catalog, createRoute, githubAccess, inboxState, savedChoices } from "../api.ts";
import { h, mount } from "../dom.ts";
import { store } from "../store.ts";
import { dismissOnBackdrop, toast } from "../ui.ts";
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

/** Opens the New route dialog. */
function failed(err: unknown): void {
  if (err instanceof AuthError) store.set({ fatal: err.message });
  toast(err instanceof Error ? err.message : String(err), "error");
}

function formDeps(): Deps {
  return {
    createRoute,
    inboxState,
    github: githubAccess,
    state: () => store.app.server,
    copy: (text, what) => void copy(text, what),
    openPane: (pane) => store.go({ pane }),
    close: () => document.querySelector<HTMLDialogElement>("dialog.new-route")?.close(),
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

export function openNewRoute(): void {
  const dlg = h("dialog", { class: "dialog new-route", "aria-label": "New route" });
  const body = h("form", { method: "dialog", novalidate: true });
  dlg.append(body);
  dismissOnBackdrop(dlg);
  document.body.append(dlg);
  dlg.showModal();
  const flow = newRouteFlow(
    body,
    { savedChoices, catalog },
    (c, previous, recommended) => {
      const f = routeForm(body, c, store.app.server, formDeps(), { usePrevious: previous, recommended });
      body.querySelector<HTMLElement>(f.form().folder ? "textarea" : "input[name=folder]")?.focus();
      return f;
    },
    (e) => {
      failed(e);
      if (body.querySelector(".nr-loading"))
        mount(
          body,
          h("h2", null, "New route"),
          h("p", { class: "error" }, e instanceof Error ? e.message : String(e)),
          h("div", { class: "dialog-actions" }, h("button", { value: "cancel", class: "btn ghost" }, "Close")),
        );
    },
  );
  dlg.addEventListener("close", () => {
    flow.stop();
    dlg.remove();
  });
}
