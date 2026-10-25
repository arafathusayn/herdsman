// The New route flow before the form (A30/A32): the recommended setup,
// then the previous-choice question, and the probe only when needed. It
// takes the server calls and the form as arguments, so tests drive it.
import { h, mount } from "../dom.ts";
import { missingPicks, pickSummary, RECOMMENDED_SETUP } from "../new-route.ts";
import type { Catalog, ChoicePick } from "../types.ts";
import type { routeForm } from "./route-form.ts";

/** What the flow asks the server; tests pass their own. */
export interface FlowApi {
  savedChoices(): Promise<ChoicePick[]>;
  catalog(probe?: boolean): Promise<Catalog>;
}

type Form = ReturnType<typeof routeForm>;

/**
 * The New route flow (A30/A32): the recommended setup first, then, when
 * picks were saved, "Use previous choice?". Yes opens the form from the
 * saved picks without a probe; the harness lists are probed only when a
 * role needs a fresh choice, and they then update the form in place. No,
 * or no saved picks, probes the lists and opens the form.
 */
export function newRouteFlow(
  body: HTMLElement,
  api: FlowApi,
  open: (c: Catalog, previous: boolean) => Form,
  failed: (e: unknown) => void,
): { stop(): void } {
  let form: Form | null = null;
  let stopped = false;
  const probe = (): void => {
    mount(body, h("h2", null, "New route"), h("p", { class: "nr-loading" }, "Reading the harness lists…"));
    api.catalog(true).then((c) => {
      if (!stopped) form = open(c, false);
    }, failed);
  };
  const usePrevious = (): void => {
    api.catalog(false).then((c) => {
      if (stopped) return;
      const f = open(c, true);
      form = f;
      // Only a role without a usable saved pick needs the harness lists.
      if (!Object.keys(missingPicks(f.form(), c)).length) return;
      api.catalog(true).then((full) => {
        if (!stopped && !f.sent()) f.setCatalog({ ...full, previous: c.previous ?? [] });
      }, failed);
    }, failed);
  };
  mount(body, h("h2", null, "New route"), h("p", { class: "nr-loading" }, "Reading your previous choice…"));
  api.savedChoices().then(
    (picks) => {
      if (stopped) return;
      const herdr = picks.filter((p) => p.runner === "herdr");
      if (!herdr.length) return probe();
      mount(
        body,
        h("h2", null, "New route"),
        h("p", { class: "muted" }, "Recommended setup:"),
        h(
          "dl",
          { class: "nr-summary", dataset: { field: "recommended-summary" } },
          RECOMMENDED_SETUP.flatMap(([k, v]) => [h("dt", null, k), h("dd", null, v)]),
        ),
        h("p", { class: "muted" }, "Use your previous choice?"),
        h(
          "ul",
          { class: "nr-previous-list" },
          herdr.map((p) => h("li", null, pickSummary(p))),
        ),
        h(
          "div",
          { class: "dialog-actions" },
          h("button", { type: "button", class: "btn ghost", name: "choose-again", onclick: probe }, "Choose again"),
          h(
            "button",
            { type: "button", class: "btn primary", name: "use-previous", onclick: usePrevious },
            "Use previous choice",
          ),
        ),
      );
    },
    () => {
      if (!stopped) probe();
    },
  );
  return {
    stop: () => {
      stopped = true;
      form?.stop();
    },
  };
}
