import type { Execution } from "@herdsman/sdk/herdsman/daemon/v1/runtime";
import { For, Show } from "@reactre/core";

import { explain, services } from "../../sdk/index.ts";
import { createPoll } from "../../state/poll.ts";
import { valueState } from "../../state/value.ts";
import { Async, Failure, fieldValue, load, NotServed, PageFrame } from "../load.tsx";

const refreshMs = 5000;

/** What a person reads before a typed submit; the daemon refuses one without it. */
export const submitWarning =
  "This types one line into the agent's terminal and presses Enter. herdr-core checks the program's state once, before the paste; " +
  "the Enter follows that check by about 200 ms with no second check, so a dialog that opens in between is confirmed by it. " +
  "Once core holds the line, nothing can recall it.";

/**
 * What a person is doing with one execution's controls. It lives with the page,
 * not the row, so a refresh of the list keeps a half-typed line and the last
 * answer.
 */
interface RowState {
  line: string;
  asking: boolean;
  problem: string;
  done: string;
}

const blankRow: RowState = { line: "", asking: false, problem: "", done: "" };

interface Rows {
  get: (id: string) => RowState;
  set: (id: string, change: Partial<RowState>) => void;
}

function rowStore(): Rows {
  const [rows, setRows] = valueState<Record<string, RowState>>({});
  return {
    get: (id) => rows()[id] ?? blankRow,
    set: (id, change) => setRows((all) => ({ ...all, [id]: { ...(all[id] ?? blankRow), ...change } })),
  };
}

export function RoutesPage() {
  const state = load((signal) => services().routes.list({}, { signal }));
  const executions = load((signal) => services().runtime.listExecutions({}, { signal }));
  const rows = rowStore();
  const poll = createPoll(async () => {
    await Promise.all([
      state.loading() ? undefined : state.reload(),
      executions.loading() ? undefined : executions.reload(),
    ]);
    return refreshMs;
  }, refreshMs);
  poll.start();
  return (
    <PageFrame
      title="Route starts"
      lead="Every route the daemon has started, how far each got, and where its agents run."
    >
      <Async state={state} what="The route list">
        {(data) => (
          <Show when={(data.starts ?? []).length > 0} fallback={<p class="page-empty">No routes started yet.</p>}>
            <ul class="page-list">
              <For each={data.starts ?? []} key={(start) => start.operation ?? start.route?.value ?? ""}>
                {(start) => (
                  <li class="page-row page-stack">
                    <div class="page-row-head">
                      <strong>{start.route?.value}</strong>
                      <span class="badge">{start.state}</span>
                      <Show when={start.backend}>
                        <span class="badge">{start.backend}</span>
                      </Show>
                    </div>
                    <span class="page-sub">{start.reason}</span>
                    <ol class="page-steps">
                      <For each={start.steps ?? []} key={(step) => step.ordinal ?? 0}>
                        {(step) => (
                          <li>
                            <span>{step.name}</span>
                            <span class="page-sub">{step.state}</span>
                          </li>
                        )}
                      </For>
                    </ol>
                    <Show
                      when={executions.error() === undefined}
                      fallback={<Failure error={executions.error()} what="The execution list" />}
                    >
                      <Executions
                        list={(executions.data()?.executions ?? []).filter(
                          (e) => e.route?.value === start.route?.value,
                        )}
                        changed={() => void executions.reload()}
                        rows={rows}
                      />
                    </Show>
                  </li>
                )}
              </For>
            </ul>
          </Show>
        )}
      </Async>
      <NotServed what="Pausing, resuming, stopping and deleting a route" />
    </PageFrame>
  );
}

/** Each runtime field of an execution on its own line, as RuntimeService reports it. */
function Executions(props: { list: Execution[]; changed: () => void; rows: Rows }) {
  return (
    <Show when={props.list.length > 0} fallback={<p class="page-sub">No executions recorded.</p>}>
      <ul class="page-list">
        <For each={props.list} key={(e) => e.ref?.executionId || `${e.agent}:${e.source}`}>
          {(e) => <ExecutionRow execution={e} changed={props.changed} rows={props.rows} />}
        </For>
      </ul>
    </Show>
  );
}

function field(label: string, value: string | undefined) {
  return (
    <Show when={value}>
      <span class="page-sub">{`${label}: ${value}`}</span>
    </Show>
  );
}

function ExecutionRow(props: { execution: Execution; changed: () => void; rows: Rows }) {
  const e = props.execution;
  const obs = e.observation ?? {};
  const actions = e.actions ?? [];
  const id = e.ref?.executionId ?? "";
  const row = () => props.rows.get(id);
  const line = () => row().line;
  const asking = () => row().asking;
  const problem = () => row().problem;
  const done = () => row().done;
  const setLine = (line: string) => props.rows.set(id, { line });
  const setAsking = (asking: boolean) => props.rows.set(id, { asking });
  const setProblem = (problem: string) => props.rows.set(id, { problem });
  const setDone = (done: string) => props.rows.set(id, { done });

  const show = async () => {
    setProblem("");
    try {
      await services().runtime.show({ executionId: id });
      setDone("Shown in the herdr app.");
    } catch (error) {
      setProblem(explain(error));
    }
  };
  // The confirmation is the person's: the warning is on screen and the line
  // is typed only from the second button.
  const submit = async (event: Event) => {
    event.preventDefault();
    setProblem("");
    try {
      const res = await services().runtime.submit({ executionId: id, text: line().trim(), confirmed: true });
      setDone(`Typed submit ${res.submitId ?? ""}: ${res.state ?? ""}`);
      setLine("");
      setAsking(false);
      props.changed();
    } catch (error) {
      setProblem(explain(error));
    }
  };

  const visibility = obs.visibility ? `${obs.visibility}${obs.app ? `, app ${obs.app}` : ""}` : undefined;
  const program =
    obs.rawProgramStatus && obs.rawProgramStatus !== obs.program
      ? `${obs.program} (reported ${obs.rawProgramStatus})`
      : obs.program;
  const notices = e.notices
    ? `issued ${e.notices.issued ?? 0}, claimed ${e.notices.claimed ?? 0}, handled ${e.notices.handled ?? 0}, returned ${e.notices.returned ?? 0}` +
      (e.notices.openClaim ? `, open claim ${e.notices.openClaim}` : "") +
      (e.notices.heldGate ? `, held by ${e.notices.heldGate}` : "")
    : undefined;

  return (
    <li class="page-row page-stack">
      <div class="page-row-head">
        <strong>{e.agent}</strong>
        <span class="badge">{e.role || "agent"}</span>
        <span class="badge">{e.ref?.backend}</span>
      </div>
      {field("Execution", id ? `${id} (generation ${e.ref?.generation ?? "0"})` : e.source)}
      {field("Address", e.ref?.terminalAddress || e.ref?.pane)}
      {field("Process", obs.process)}
      {field("Readiness", obs.readiness)}
      {field("Program", program)}
      {field("Visibility", visibility)}
      {field("Orchestration", obs.orchestration)}
      {field("Notices", notices)}
      <For each={e.attention ?? []} key={(a) => `${a.reason}:${a.noticeId ?? ""}`}>
        {(a) => (
          <p class="page-note" role="status">
            <strong>{`Needs a person: ${a.reason}${a.gate ? ` (${a.gate})` : ""}`}</strong>
            <span>{a.detail}</span>
            <Show when={a.line}>
              <code>{a.line}</code>
            </Show>
          </p>
        )}
      </For>
      <For each={e.unresolved ?? []} key={(u) => u.id ?? ""}>
        {(u) => (
          <p class="page-note danger" role="alert">
            <strong>{`Unresolved ${u.kind} ${u.id}: ${u.state}`}</strong>
            <span>{u.detail}</span>
            <span>{`Choices: ${(u.choices ?? []).join(", ")}`}</span>
          </p>
        )}
      </For>
      <Show when={id && actions.length > 0}>
        <div class="page-row-head">
          <Show when={actions.includes("show")}>
            <button class="btn" type="button" onClick={() => void show()}>
              Show in the app
            </button>
          </Show>
          <Show when={actions.includes("submit")}>
            <button class="btn" type="button" onClick={() => setAsking(!asking())}>
              Type a line
            </button>
          </Show>
        </div>
      </Show>
      <Show when={asking()}>
        <form class="page-form" onSubmit={submit}>
          <p class="page-note danger" role="alert">
            {submitWarning}
          </p>
          <label class="field">
            Line
            <input required value={line()} onInput={(event) => setLine(fieldValue(event))} />
          </label>
          <button class="btn primary" type="submit" disabled={!line().trim()}>
            I read this: type the line
          </button>
        </form>
      </Show>
      <Show when={done()}>
        <span class="page-sub" role="status">
          {done()}
        </span>
      </Show>
      <Show when={problem()}>
        <p class="page-error" role="alert">
          {problem()}
        </p>
      </Show>
    </li>
  );
}
