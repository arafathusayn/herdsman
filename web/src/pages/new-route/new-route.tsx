import type { RouteStart, UpRequest } from "@herdsman/sdk/herdsman/daemon/v1/route";
import { Show, signal } from "@reactre/core";

import { explain, services } from "../../sdk/index.ts";
import { fieldValue, PageFrame } from "../load.tsx";

/** The start request for what the form holds. */
export function launchRequest(fields: {
  route: string;
  repo: string;
  prompt: string;
  adjutant: string;
  orchestrator: string;
  noAdjutant: boolean;
}): UpRequest {
  return {
    route: { value: fields.route.trim() },
    repo: fields.repo.trim(),
    prompt: fields.prompt,
    orchestratorCommand: fields.orchestrator.trim(),
    ...(fields.noAdjutant ? { noAdjutant: true } : { adjutantCommand: fields.adjutant.trim() }),
  };
}

export function NewRoutePage() {
  const [route, setRoute] = signal("");
  const [repo, setRepo] = signal("");
  const [prompt, setPrompt] = signal("");
  const [adjutant, setAdjutant] = signal("");
  const [orchestrator, setOrchestrator] = signal("");
  const [noAdjutant, setNoAdjutant] = signal(false);
  const [plan, setPlan] = signal<RouteStart | null>(null);
  const [started, setStarted] = signal<RouteStart | null>(null);
  const [problem, setProblem] = signal("");
  const [busy, setBusy] = signal(false);

  // A plan answers for the fields as they were: any edit asks for a new one.
  const edit = (set: (value: string) => void) => (event: Event) => {
    set(fieldValue(event));
    setPlan(null);
    setStarted(null);
  };
  const request = () =>
    launchRequest({
      route: route(),
      repo: repo(),
      prompt: prompt(),
      adjutant: adjutant(),
      orchestrator: orchestrator(),
      noAdjutant: noAdjutant(),
    });
  const ready = () =>
    !!route().trim() && !!repo().trim() && !!orchestrator().trim() && (noAdjutant() || !!adjutant().trim());

  const send = async (dryRun: boolean) => {
    setProblem("");
    setBusy(true);
    try {
      const reply = await services().routes.up({ ...request(), dryRun });
      if (dryRun) setPlan(() => reply.start ?? null);
      else setStarted(() => reply.start ?? null);
    } catch (error) {
      setProblem(explain(error));
    }
    setBusy(false);
  };

  return (
    <PageFrame title="New route" lead="Preview what a route will do, then start it.">
      <form
        class="page-form"
        onSubmit={(event) => {
          event.preventDefault();
          void send(true);
        }}
      >
        <label class="field">
          Route name
          <input required value={route()} onInput={edit(setRoute)} />
        </label>
        <label class="field">
          Repository
          <input required value={repo()} onInput={edit(setRepo)} />
        </label>
        <label class="field page-wide">
          Orchestrator launch command
          <input required value={orchestrator()} onInput={edit(setOrchestrator)} />
        </label>
        <label class="page-check page-wide">
          <input
            type="checkbox"
            checked={noAdjutant()}
            onChange={(event) => {
              setNoAdjutant((event.currentTarget as HTMLInputElement).checked);
              setPlan(null);
              setStarted(null);
            }}
          />
          Run without an adjutant
        </label>
        <Show when={!noAdjutant()}>
          <label class="field page-wide">
            Adjutant launch command
            <input required value={adjutant()} onInput={edit(setAdjutant)} />
          </label>
        </Show>
        <label class="field page-wide">
          First prompt
          <textarea rows={5} defaultValue={prompt()} onInput={edit(setPrompt)} />
        </label>
        <button class="btn" type="submit" disabled={!ready() || busy()}>
          Preview
        </button>
        <button class="btn primary" type="button" disabled={!plan() || busy()} onClick={() => void send(false)}>
          Start route
        </button>
        <Show when={problem()}>
          <p class="page-error" role="alert">
            {problem()}
          </p>
        </Show>
      </form>
      <Show when={plan()}>
        {(current) => (
          <div class="page-detail">
            <h2 class="page-h2">Plan</h2>
            <p class="page-sub">{current().folder}</p>
            <pre class="page-pre">{current().plan}</pre>
          </div>
        )}
      </Show>
      <Show when={started()}>
        {(current) => (
          <p class="page-note" role="status">
            <strong>{`${current().route?.value ?? ""}: ${current().state ?? ""}`}</strong>
            <span>{current().reason}</span>
          </p>
        )}
      </Show>
    </PageFrame>
  );
}
