// The New route flow before the form (A32/A30): "Use the recommended
// setup?" first, then the previous-choice question, and the probe only for
// the roles that need it. It takes the server calls and the form as
// arguments, so tests drive it.
import { h, mount } from "../dom.ts";
import { missingPicks, pickSummary, RECOMMENDED_SETUP } from "../new-route.ts";
import type { Catalog, CatalogKind, ChoicePick } from "../types.ts";
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
 * The probed lists with the saved picks' kinds merged in: a role that keeps
 * its saved pick still finds its exact model and effort, while the roles
 * that need a fresh pick choose from the probed lists (A30, per role).
 */
export function mergeSaved(probed: Catalog, saved: Catalog): Catalog {
  const kinds: CatalogKind[] = probed.kinds.map((k) => ({ ...k, models: k.models.map((m) => ({ ...m })) }));
  for (const s of saved.kinds) {
    const k = kinds.find((x) => x.kind === s.kind);
    if (!k) {
      kinds.push(s);
      continue;
    }
    k.saved = true;
    if (s.effort_flag && !k.effort_flag) k.effort_flag = s.effort_flag;
    for (const sm of s.models) {
      const m = k.models.find((x) => x.id === sm.id);
      if (!m) k.models.push({ ...sm });
      else m.efforts = [...m.efforts, ...sm.efforts.filter((e) => !m.efforts.includes(e))];
    }
  }
  return { ...probed, kinds, previous: saved.previous ?? [] };
}

/**
 * The New route flow (A32, A30): "Use the recommended setup?" first, with
 * the setup in one summary; then, when picks were saved, "Use your previous
 * choice?". Yes opens the form from the saved picks without a probe; the
 * harness lists are probed only when a role needs a fresh choice, and they
 * then update the form in place, the saved roles keeping their picks. No,
 * or no saved picks, probes the lists and opens the form.
 */
export function newRouteFlow(
  body: HTMLElement,
  api: FlowApi,
  open: (c: Catalog, previous: boolean, recommended: boolean) => Form,
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
      const f = open(c, true, recommended);
      form = f;
      // Only a role without a usable saved pick needs the harness lists.
      if (!Object.keys(missingPicks(f.form(), c)).length) return;
      api.catalog(true).then((full) => {
        if (!stopped && !f.sent()) f.setCatalog(mergeSaved(full, c));
      }, failed);
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
