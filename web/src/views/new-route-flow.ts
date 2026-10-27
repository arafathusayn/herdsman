// The New route flow before the form (A32/A30): "Use the recommended
// setup?" first, then the previous-choice question, and the probe only for
// the roles that need it. It takes the server calls and the form as
// arguments, so tests drive it.
import { attr, child, html, list, type ListNode, on, text, type ViewNodeOf } from "../herdsman-blocks-view.ts";
import { missingPicks, pickSummary, RECOMMENDED_SETUP } from "../new-route.ts";
import type { Catalog, ChoicePick } from "../types.ts";
import { element } from "./element-view.ts";
import type { routeForm } from "./route-form.ts";

/** What the flow asks the server; tests pass their own. */
export interface FlowApi {
  savedChoices(): Promise<ChoicePick[]>;
  catalog(probe?: boolean): Promise<Catalog>;
}

/** What the flow uses of the form it opens. */
type Form = Pick<ReturnType<typeof routeForm>, "stop" | "form" | "sent" | "setCatalog">;

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

// Each screen is a few parts of the dialog's form, each made once with
// element() and put in the form in place of the screen before.
const Heading = html`<h2>New route</h2>`;
const Question = html`<p class="muted">${text()}</p>`;
const Loading = html`<p class="nr-loading">Reading the harness lists…</p>`;
const Term = html`<dt>${text()}</dt>`;
const Item = html`<dd>${text()}</dd>`;
const Summary = html`<dl class="nr-summary" data-field="recommended-summary">
  ${child<ListNode<ViewNodeOf<typeof Term> | ViewNodeOf<typeof Item>, string>>()}
</dl>`;
const PickLine = html`<li>${text()}</li>`;
const Picks = html`<ul class="nr-previous-list">
  ${child<ListNode<ViewNodeOf<typeof PickLine>, string>>()}
</ul>`;
// Two answers: the ghost one first, the primary one last.
const Answers = html`<div class="dialog-actions">
  <button type="button" class="btn ghost" name=${attr()} onclick=${on<"click">()}>${text()}</button
  ><button type="button" class="btn primary" name=${attr()} onclick=${on<"click">()}>${text()}</button>
</div>`;

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
    body.replaceChildren(element("h2", Heading([])), element("p", Loading([])));
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
    // A pick is keyed by what it shows (its role first), so a repeated pick shows once.
    const lines = [...new Set(herdr.map(pickSummary))];
    body.replaceChildren(
      element("h2", Heading([])),
      element("p", Question(["Use your previous choice?"])),
      element(
        "ul",
        Picks([
          list(
            lines,
            (line) => line,
            (line) => PickLine([line]),
          ),
        ]),
      ),
      element(
        "div",
        Answers(["choose-again", probe, "Choose again", "use-previous", usePrevious, "Use previous choice"]),
      ),
    );
  };
  const answer = (on: boolean) => (): void => {
    recommended = on;
    void saved.then((p) => {
      if (!stopped) askPrevious(p);
    });
  };
  body.replaceChildren(
    element("h2", Heading([])),
    element("p", Question(["Use the recommended setup?"])),
    element(
      "dl",
      Summary([
        list(
          FIRST_SUMMARY.flatMap(([k, v]) => [
            { key: `dt|${k}`, text: k, term: true },
            { key: `dd|${k}`, text: v, term: false },
          ]),
          (x) => x.key,
          (x) => (x.term ? Term([x.text]) : Item([x.text])),
        ),
      ]),
    ),
    element(
      "div",
      Answers([
        "custom-setup",
        answer(false),
        "Custom setup",
        "use-recommended",
        answer(true),
        "Use the recommended setup",
      ]),
    ),
  );
  return {
    stop: () => {
      stopped = true;
      form?.stop();
    },
  };
}
