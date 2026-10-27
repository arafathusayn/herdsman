// The New route flow before the form (A32/A30): "Use the recommended
// setup?" first, then the previous-choice question, and the probe only for
// the roles that need it. It takes the server calls and the form as
// arguments, so tests drive it.
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
 * The recommended setup as the first question shows it: the checkpointer
 * depends on the harness and the skill's script, which are not known yet,
 * so its line says when it runs.
 */
export const FIRST_SUMMARY: [string, string][] = RECOMMENDED_SETUP.map(([k, v]) =>
  k === "Checkpointer"
    ? [
        k,
        "On when the orchestrator runs Claude Code or Codex and the skill's checkpointer script is found; off otherwise. The form shows which.",
      ]
    : [k, v],
);

/**
 * The New route flow (A32, A30): "Use the recommended setup?" first, with
 * the setup in one summary; then, when picks were saved, "Use your previous
 * choice?". Yes opens the form from the saved picks without a probe; the
 * harness lists are probed only when a role needs a fresh choice, and they
 * then update the form in place; each saved role keeps its own pick. No,
 * or no saved picks, probes the lists and opens the form.
 */
export function newRouteFlow(
  body: HTMLElement,
  api: FlowApi,
  open: (c: Catalog, previous: boolean, recommended: boolean, needLists?: () => void) => Form,
  failed: (e: unknown) => void,
): { stop(): void } {
  let form: Form | null = null;
  let stopped = false;
  let recommended = true;
  // The saved picks load while the first question shows; nothing is probed.
  const saved = api.savedChoices().catch((): ChoicePick[] => []);
  const probe = (): void => {
    mount(body, h("h2", null, "New route"), h("p", { class: "nr-loading" }, "Reading the harness lists…"));
    api.catalog(true).then((c) => {
      if (!stopped) form = open(c, false, recommended);
    }, failed);
  };
  const usePrevious = (): void => {
    api.catalog(false).then((c) => {
      if (stopped) return;
      // The harness lists are read when a role needs a fresh choice: a
      // role without a usable saved pick, or the user choosing again. One
      // read at a time; once they are read, never again; a failed read can
      // be tried again.
      let asked = false;
      const needLists = (): void => {
        if (asked) return;
        asked = true;
        api.catalog(true).then(
          (full) => {
            if (!stopped && !f.sent()) f.setCatalog({ ...full, previous: c.previous ?? [] });
          },
          (e: unknown) => {
            asked = false;
            failed(e);
          },
        );
      };
      const f = open(c, true, recommended, needLists);
      form = f;
      if (Object.keys(missingPicks(f.form(), c)).length) needLists();
    }, failed);
  };
  const askPrevious = (picks: ChoicePick[]): void => {
    const herdr = picks.filter((p) => p.runner === "herdr");
    if (!herdr.length) return probe();
    mount(
      body,
      h("h2", null, "New route"),
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
  };
  const answer = (on: boolean) => (): void => {
    recommended = on;
    void saved.then((p) => {
      if (!stopped) askPrevious(p);
    });
  };
  mount(
    body,
    h("h2", null, "New route"),
    h("p", { class: "muted" }, "Use the recommended setup?"),
    h(
      "dl",
      { class: "nr-summary", dataset: { field: "recommended-summary" } },
      FIRST_SUMMARY.flatMap(([k, v]) => [h("dt", null, k), h("dd", null, v)]),
    ),
    h(
      "div",
      { class: "dialog-actions" },
      h("button", { type: "button", class: "btn ghost", name: "custom-setup", onclick: answer(false) }, "Custom setup"),
      h(
        "button",
        { type: "button", class: "btn primary", name: "use-recommended", onclick: answer(true) },
        "Use the recommended setup",
      ),
    ),
  );
  return {
    stop: () => {
      stopped = true;
      form?.stop();
    },
  };
}
