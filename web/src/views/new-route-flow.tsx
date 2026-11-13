import { attach, For, Match, Switch } from "@reactre/core";
import { createActor } from "@reactre/state";

import { missingPicks, pickSummary, RECOMMENDED_SETUP } from "../new-route.ts";
import { observeActor } from "../state/observe.ts";
import { registerRequest } from "../state/request-service.ts";
import { createRequest } from "../state/request.ts";
import { routeSetupMachine } from "../state/route-setup.machine.ts";
import { decodeValue, encodeValue } from "../state/wire.ts";
import type { Catalog, ChoicePick } from "../types.ts";
import type { routeForm } from "./route-form.tsx";

export interface FlowApi {
  savedChoices(signal?: AbortSignal): Promise<ChoicePick[]>;
  catalog(probe?: boolean, signal?: AbortSignal): Promise<Catalog>;
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
  let form: Form | null = null;
  let disposeQuestions = (): void => {};
  const savedRequest = createRequest(async (_: void, signal) => api.savedChoices(signal));
  const saved = savedRequest.run(undefined);
  const service = registerRequest(async (kind, signal) => {
    signal.throwIfAborted();
    if (kind === "saved") {
      const outcome = await saved;
      const picks = outcome.status === "done" ? outcome.value : [];
      return JSON.stringify([...new Set(picks.filter((pick) => pick.runner === "herdr").map(pickSummary))]);
    }
    return encodeValue(await api.catalog(kind === "probe", signal));
  });
  const actor = createActor(routeSetupMachine, { input: { service: service.id } });
  const observed = observeActor(actor);
  const fullLists = createRequest(async (_: void, signal) => api.catalog(true, signal));
  const state = (): FlowSnapshot => {
    const s = observed.read();
    return {
      stage:
        s.state === "questions.previous"
          ? "previous"
          : s.state === "failed"
            ? "error"
            : s.state === "loading"
              ? "loading"
              : "recommended",
      lines: s.state === "questions.previous" ? (JSON.parse(s.data.lines) as string[]) : [],
      waiting: s.state === "questions.saved" || s.state === "loading",
      error: s.state === "failed" ? s.data.error : "",
    };
  };
  const off = actor.subscribe((s) => {
    if (s.state === "failed") failed(new Error(s.data.error));
    if (s.state !== "opened") return;
    const catalog = decodeValue<Catalog>(s.data.catalog);
    const needLists = (): void => {
      if (fullLists.loading() || fullLists.snapshot().state === "ready") return;
      void fullLists.run(undefined).then((outcome) => {
        if (outcome.status === "done" && form && !form.sent())
          form.setCatalog({ ...outcome.value, previous: catalog.previous ?? [] });
        else if (outcome.status === "failed") failed(outcome.error);
      });
    };
    disposeQuestions();
    form = open(catalog, s.data.previous, s.data.recommended, s.data.previous ? needLists : undefined);
    if (s.data.previous && Object.keys(missingPicks(form.form(), catalog)).length) needLists();
  });
  const answer = (recommended: boolean): void => {
    actor.send({ type: "answer", recommended });
  };
  const probe = (): void => {
    actor.send({ type: "probe" });
  };
  const previous = (): void => {
    actor.send({ type: "previous" });
  };
  body.replaceChildren();
  disposeQuestions = attach(() => <Questions state={state} answer={answer} probe={probe} previous={previous} />, body);
  return {
    stop: () => {
      off();
      observed.stop();
      service.dispose();
      fullLists.stop();
      savedRequest.stop();
      form?.stop();
      disposeQuestions();
    },
  };
}
