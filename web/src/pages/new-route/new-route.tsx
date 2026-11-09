import type { RouteStart, UpRequest } from "@herdsman/sdk/herdsman/daemon/v1/route";
import { Show, signal } from "@reactre/core";

import { explain, services } from "../../sdk/index.ts";
import { fieldValue, PageFrame } from "../load.tsx";

export interface LaunchFields {
  route: string;
  repo: string;
  prompt: string;
  adjutant: string;
  adjutantKind: string;
  orchestrator: string;
  orchestratorKind: string;
  noAdjutant: boolean;
}

function shellWord(word: string): string {
  return /^[\w@%+=:,./-]+$/.test(word) ? word : `'${word.replaceAll("'", "'\\''")}'`;
}

/** The terminal command that starts what the form holds. */
export function launchCommand(fields: LaunchFields): string {
  const adjutant = fields.noAdjutant
    ? "--no-adjutant"
    : `-a ${shellWord(fields.adjutant.trim())} --adjutant-kind ${shellWord(fields.adjutantKind.trim())}`;
  const orchestrator = `-o ${shellWord(fields.orchestrator.trim())} --orchestrator-kind ${shellWord(fields.orchestratorKind.trim())}`;
  const up = `herdsman up ${shellWord(fields.route.trim())} -p ${shellWord(fields.prompt)}`;
  return `cd ${shellWord(fields.repo.trim())} && ${up} ${adjutant} ${orchestrator}`;
}

/** The start request for what the form holds. */
export function launchRequest(fields: LaunchFields): UpRequest {
  return {
    route: { value: fields.route.trim() },
    repo: fields.repo.trim(),
    prompt: fields.prompt,
    orchestratorCommand: fields.orchestrator.trim(),
    orchestratorKind: fields.orchestratorKind.trim(),
    ...(fields.noAdjutant
      ? { noAdjutant: true }
      : { adjutantCommand: fields.adjutant.trim(), adjutantKind: fields.adjutantKind.trim() }),
  };
}

export function NewRoutePage() {
  const [route, setRoute] = signal("");
  const [repo, setRepo] = signal("");
  const [prompt, setPrompt] = signal("");
  const [adjutant, setAdjutant] = signal("");
  const [adjutantKind, setAdjutantKind] = signal("");
  const [orchestrator, setOrchestrator] = signal("");
  const [orchestratorKind, setOrchestratorKind] = signal("");
  const [noAdjutant, setNoAdjutant] = signal(false);
  const [plan, setPlan] = signal<RouteStart | null>(null);
  const [problem, setProblem] = signal("");
  const [busy, setBusy] = signal(false);

  // Each edit or preview makes the replies still on their way out of date.
  let revision = 0;
  const invalidate = () => {
    revision++;
    setPlan(null);
    setProblem("");
    setBusy(false);
  };
  const edit = (set: (value: string) => void) => (event: Event) => {
    set(fieldValue(event));
    invalidate();
  };
  const fields = (): LaunchFields => ({
    route: route(),
    repo: repo(),
    prompt: prompt(),
    adjutant: adjutant(),
    adjutantKind: adjutantKind(),
    orchestrator: orchestrator(),
    orchestratorKind: orchestratorKind(),
    noAdjutant: noAdjutant(),
  });
  const ready = () =>
    !!route().trim() &&
    !!repo().trim() &&
    !!orchestrator().trim() &&
    !!orchestratorKind().trim() &&
    (noAdjutant() || (!!adjutant().trim() && !!adjutantKind().trim()));

  const preview = async () => {
    invalidate();
    const mine = revision;
    setBusy(true);
    try {
      const reply = await services().routes.up({ ...launchRequest(fields()), dryRun: true });
      if (mine !== revision) return;
      setPlan(() => reply.start ?? null);
    } catch (error) {
      if (mine !== revision) return;
      setProblem(explain(error));
    }
    setBusy(false);
  };

  return (
    <PageFrame title="New route" lead="Preview what a route will do, then start it from a terminal.">
      <form
        class="page-form"
        onSubmit={(event) => {
          event.preventDefault();
          void preview();
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
        <label class="field">
          Orchestrator kind
          <input required placeholder="codex" value={orchestratorKind()} onInput={edit(setOrchestratorKind)} />
        </label>
        <label class="page-check page-wide">
          <input
            type="checkbox"
            checked={noAdjutant()}
            onChange={(event) => {
              setNoAdjutant((event.currentTarget as HTMLInputElement).checked);
              invalidate();
            }}
          />
          Run without an adjutant
        </label>
        <Show when={!noAdjutant()}>
          <label class="field page-wide">
            Adjutant launch command
            <input required value={adjutant()} onInput={edit(setAdjutant)} />
          </label>
          <label class="field">
            Adjutant kind
            <input required placeholder="claude" value={adjutantKind()} onInput={edit(setAdjutantKind)} />
          </label>
        </Show>
        <label class="field page-wide">
          First prompt
          <textarea rows={5} defaultValue={prompt()} onInput={edit(setPrompt)} />
        </label>
        <button class="btn" type="submit" disabled={!ready() || busy()}>
          Preview
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
            <p class="page-note" role="status">
              <strong>A browser cannot start a route: the daemon confirms who asks from the calling process.</strong>
              <span>
                {"Run it in a terminal: "}
                <code>{launchCommand(fields())}</code>
              </span>
            </p>
          </div>
        )}
      </Show>
    </PageFrame>
  );
}
