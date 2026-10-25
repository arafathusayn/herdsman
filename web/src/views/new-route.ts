// The New route dialog: pick a folder, type the request, press Start. It
// starts an orchestrator (or an adjutant with its orchestrator) in new
// herdr tabs through the server, then shows each step and, for an adjutant,
// the channel setup checklist. The steps that belong to the human stay the
// human's: the app never approves a pair, writes the inbox or types into
// the adjutant's pane.
import { AuthError, catalog, createRoute, githubAccess, inboxState, savedChoices } from "../api.ts";
import { h, mount } from "../dom.ts";
import { pickSummary } from "../new-route.ts";
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

/** Opens the New route dialog. */
export function openNewRoute(): void {
  const dlg = h("dialog", { class: "dialog new-route", "aria-label": "New route" });
  const body = h("form", { method: "dialog", novalidate: true });
  let stop = (): void => {};
  dlg.append(body);
  dlg.addEventListener("close", () => {
    stop();
    dlg.remove();
  });
  dismissOnBackdrop(dlg);
  document.body.append(dlg);
  mount(body, h("h2", null, "New route"), h("p", { class: "nr-loading" }, "Reading the harness lists…"));
  dlg.showModal();
  // The saved picks need no probe: they show while the harness lists load (A30).
  savedChoices().then(
    (picks) => {
      if (!dlg.open || !picks.length || !body.querySelector(".nr-loading")) return;
      body.append(
        h("p", { class: "muted" }, "Your previous choice:"),
        h(
          "ul",
          { class: "nr-previous-list" },
          picks.map((p) => h("li", null, pickSummary(p))),
        ),
      );
    },
    () => {},
  );
  catalog().then(
    (c) => {
      if (!dlg.open) return;
      const form = routeForm(body, c, store.app.server, {
        createRoute,
        inboxState,
        github: githubAccess,
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
      stop = form.stop;
      body.querySelector<HTMLElement>(form.form().folder ? "textarea" : "input[name=folder]")?.focus();
    },
    (e: unknown) => {
      if (e instanceof AuthError) store.set({ fatal: e.message });
      mount(
        body,
        h("h2", null, "New route"),
        h("p", { class: "error" }, e instanceof Error ? e.message : String(e)),
        h("div", { class: "dialog-actions" }, h("button", { value: "cancel", class: "btn ghost" }, "Close")),
      );
    },
  );
}
