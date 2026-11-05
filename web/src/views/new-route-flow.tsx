import { attach, For, Match, signal, Switch } from "@reactre/core";

import { missingPicks, pickSummary, RECOMMENDED_SETUP } from "../new-route.ts";
import type { Catalog, ChoicePick } from "../types.ts";
import type { routeForm } from "./route-form.tsx";

export interface FlowApi {
  savedChoices(): Promise<ChoicePick[]>;
  catalog(probe?: boolean): Promise<Catalog>;
}
type Form = Pick<ReturnType<typeof routeForm>, "stop" | "form" | "sent" | "setCatalog">;

export const FIRST_SUMMARY: [string, string][] = RECOMMENDED_SETUP.map(([k, v]) =>
  k === "Checkpointer"
    ? [
        k,
        "On when the orchestrator runs Claude Code or Codex and the skill's checkpointer script is found; off otherwise. The form shows which.",
      ]
    : [k, v],
);

interface FlowSnapshot {
  stage: "recommended" | "previous" | "loading" | "error";
  lines: string[];
  error: string;
  waiting: boolean;
}
function Answers(props: {
  negativeName: string;
  negative: string;
  no: () => void;
  positiveName: string;
  positive: string;
  yes: () => void;
  busy: boolean;
}) {
  return (
    <div class="dialog-actions">
      <button
        type="button"
        class="btn ghost"
        name={props.negativeName}
        disabled={props.busy}
        onClick={() => props.no()}
      >
        {props.negative}
      </button>
      <button
        type="button"
        class="btn primary"
        name={props.positiveName}
        disabled={props.busy}
        onClick={() => props.yes()}
      >
        {props.positive}
      </button>
    </div>
  );
}
function Questions(props: {
  state: () => FlowSnapshot;
  answer: (recommended: boolean) => void;
  probe: () => void;
  previous: () => void;
}) {
  return (
    <>
      <h2>New route</h2>
      <Switch fallback={<p class="nr-loading">Reading the harness lists…</p>}>
        <Match when={props.state().stage === "recommended"}>
          <p class="muted">Use the recommended setup?</p>
          <dl class="nr-summary" data-field="recommended-summary">
            <For each={FIRST_SUMMARY} key={([key]) => key}>
              {([key, value]) => (
                <>
                  <dt>{key}</dt>
                  <dd>{value}</dd>
                </>
              )}
            </For>
          </dl>
          <Answers
            negativeName="custom-setup"
            negative="Custom setup"
            no={() => props.answer(false)}
            positiveName="use-recommended"
            positive="Use the recommended setup"
            yes={() => props.answer(true)}
            busy={props.state().waiting}
          />
        </Match>
        <Match when={props.state().stage === "previous"}>
          <p class="muted">Use your previous choice?</p>
          <ul class="nr-previous-list">
            <For each={props.state().lines} key={(line) => line}>
              {(line) => <li>{line}</li>}
            </For>
          </ul>
          <Answers
            negativeName="choose-again"
            negative="Choose again"
            no={() => props.probe()}
            positiveName="use-previous"
            positive="Use previous choice"
            yes={() => props.previous()}
            busy={props.state().waiting}
          />
        </Match>
        <Match when={props.state().stage === "error"}>
          <p class="error">{props.state().error}</p>
          <div class="dialog-actions">
            <button value="cancel" class="btn ghost">
              Close
            </button>
          </div>
        </Match>
      </Switch>
    </>
  );
}

/** Questions are one owned view; the form that follows owns its controls and requests. */
export function newRouteFlow(
  body: HTMLElement,
  api: FlowApi,
  open: (catalog: Catalog, previous: boolean, recommended: boolean, needLists?: () => void) => Form,
  failed: (error: unknown) => void,
): { stop(): void } {
  const [state, setState] = signal<FlowSnapshot>({ stage: "recommended", lines: [], error: "", waiting: false });
  let form: Form | null = null,
    stopped = false,
    recommended = true,
    opening = false;
  let disposeQuestions = (): void => {};
  const saved = api.savedChoices().catch((): ChoicePick[] => []);
  const error = (error: unknown): void => {
    if (stopped) return;
    opening = false;
    if (!form)
      setState({
        ...state(),
        stage: "error",
        waiting: false,
        error: error instanceof Error ? error.message : String(error),
      });
    failed(error);
  };
  const showForm = (catalog: Catalog, previous: boolean, needLists?: () => void): Form => {
    disposeQuestions();
    return open(catalog, previous, recommended, needLists);
  };
  const probe = (): void => {
    if (stopped || opening) return;
    opening = true;
    setState({ ...state(), stage: "loading", waiting: true });
    void api.catalog(true).then((catalog) => {
      if (!stopped) form = showForm(catalog, false);
    }, error);
  };
  const previous = (): void => {
    if (stopped || opening) return;
    opening = true;
    setState({ ...state(), waiting: true });
    void api.catalog(false).then((catalog) => {
      if (stopped) return;
      let asked = false;
      const needLists = (): void => {
        if (asked || stopped) return;
        asked = true;
        void api.catalog(true).then(
          (full) => {
            if (!stopped && !current.sent()) current.setCatalog({ ...full, previous: catalog.previous ?? [] });
          },
          (failure: unknown) => {
            asked = false;
            error(failure);
          },
        );
      };
      const current = showForm(catalog, true, needLists);
      form = current;
      if (Object.keys(missingPicks(current.form(), catalog)).length) needLists();
    }, error);
  };
  const answer = (value: boolean): void => {
    if (stopped || state().waiting) return;
    recommended = value;
    setState({ ...state(), waiting: true });
    void saved.then((picks) => {
      if (stopped) return;
      const lines = [...new Set(picks.filter((pick) => pick.runner === "herdr").map(pickSummary))];
      if (!lines.length) {
        probe();
        return;
      }
      setState({ ...state(), stage: "previous", lines, waiting: false });
    });
  };
  body.replaceChildren();
  disposeQuestions = attach(() => <Questions state={state} answer={answer} probe={probe} previous={previous} />, body);
  return {
    stop: () => {
      if (stopped) return;
      stopped = true;
      form?.stop();
      disposeQuestions();
    },
  };
}
