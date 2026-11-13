import { For, Show } from "@reactre/core";

import { explain, services } from "../../sdk/index.ts";
import { createRequest } from "../../state/request.ts";
import { valueState } from "../../state/value.ts";
import { Async, fieldValue, load, PageFrame } from "../load.tsx";

const statuses = ["todo", "doing", "review", "done", "archived"];

export function TasksPage() {
  const projects = load((signal) => services().projects.list({}, { signal }));
  const [project, setProject] = valueState("");
  const scope = () => (project() ? { scope: { project: project() } } : { all: true });
  const state = load((signal) => services().tasks.list(scope(), { signal }));
  const [title, setTitle] = valueState("");
  const mutation = createRequest(
    async (
      input: { kind: "create" | "update"; project: string; task: string; title: string; status: string },
      signal,
    ) => {
      if (input.kind === "create")
        return services().tasks.create({ scope: { project: input.project }, title: input.title }, { signal });
      return services().tasks.update(
        { scope: { project: input.project }, task: input.task, status: input.status },
        { signal },
      );
    },
  );
  const problem = () => (mutation.error() === undefined ? "" : explain(mutation.error()));
  const update = async (project: string, task: string, status: string) => {
    if (mutation.loading()) return;
    const outcome = await mutation.run({ kind: "update", project, task, status, title: "" });
    if (outcome.status === "done") await state.reload();
  };
  const create = async (event: Event) => {
    event.preventDefault();
    if (mutation.loading() || !project() || !title().trim()) return;
    const sent = title().trim();
    const outcome = await mutation.run({ kind: "create", project: project(), task: "", title: sent, status: "" });
    if (outcome.status !== "done") return;
    if (title().trim() === sent) setTitle("");
    await state.reload();
  };

  return (
    <PageFrame title="Tasks" lead="Work tracked in each project, with its status.">
      <Async state={state} what="The task list">
        {(data) => (
          <>
            <form class="page-form" onSubmit={create}>
              <label class="field">
                Project
                <select
                  onChange={(event) => {
                    setProject(fieldValue(event));
                    void state.reload();
                  }}
                >
                  <option value="" selected={!project()}>
                    All projects
                  </option>
                  <For each={projects.data()?.projects ?? []} key={(item) => item.id?.value ?? item.name ?? ""}>
                    {(item) => (
                      <option value={item.name ?? ""} selected={item.name === project()}>
                        {item.name}
                      </option>
                    )}
                  </For>
                </select>
              </label>
              <label class="field">
                New task
                <input value={title()} onInput={(event) => setTitle(fieldValue(event))} />
              </label>
              <button
                class="btn primary"
                type="submit"
                disabled={!title().trim() || !project() || mutation.loading()}
                title={project() ? "" : "Pick a project first"}
              >
                Add task
              </button>
              <Show when={problem()}>
                <p class="page-error" role="alert">
                  {problem()}
                </p>
              </Show>
            </form>
            <Show when={(data.tasks ?? []).length > 0} fallback={<p class="page-empty">No tasks here.</p>}>
              <ul class="page-list">
                <For each={data.tasks ?? []} key={(task) => task.id?.value ?? task.slug ?? ""}>
                  {(task) => (
                    <li class="page-row">
                      <div class="page-row-main">
                        <strong>{task.title}</strong>
                        <span class="page-sub">{[task.project?.value, task.slug].filter(Boolean).join(" · ")}</span>
                      </div>
                      <select
                        aria-label={`Status of ${task.title}`}
                        disabled={mutation.loading()}
                        onChange={(event) => {
                          // A list row's events are delegated, so currentTarget is the list.
                          const status = (event.target as HTMLSelectElement).value;
                          void update(task.project?.value ?? "", task.slug ?? "", status);
                        }}
                      >
                        <For each={statuses} key={(status) => status}>
                          {(status) => (
                            <option value={status} selected={status === task.status}>
                              {status}
                            </option>
                          )}
                        </For>
                      </select>
                    </li>
                  )}
                </For>
              </ul>
            </Show>
          </>
        )}
      </Async>
    </PageFrame>
  );
}
