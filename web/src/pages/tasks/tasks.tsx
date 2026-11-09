import { For, Show, signal } from "@reactre/core";

import { explain, services } from "../../sdk/index.ts";
import { Async, fieldValue, load, PageFrame } from "../load.tsx";

const statuses = ["todo", "doing", "review", "done", "archived"];

export function TasksPage() {
  const projects = load(() => services().projects.list({}));
  const [project, setProject] = signal("");
  const scope = () => (project() ? { scope: { project: project() } } : { all: true });
  const state = load(() => services().tasks.list(scope()));
  const [title, setTitle] = signal("");
  const [problem, setProblem] = signal("");

  const attempt = async (work: () => Promise<unknown>) => {
    setProblem("");
    try {
      await work();
      await state.reload();
    } catch (error) {
      setProblem(explain(error));
    }
  };

  const create = (event: Event) => {
    event.preventDefault();
    void attempt(async () => {
      await services().tasks.create({ scope: { project: project() }, title: title().trim() });
      setTitle("");
    });
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
                disabled={!title().trim() || !project()}
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
                        onChange={(event) => {
                          // A list row's events are delegated, so currentTarget is the list.
                          const status = (event.target as HTMLSelectElement).value;
                          void attempt(() =>
                            services().tasks.update({
                              scope: { project: task.project?.value ?? "" },
                              task: task.slug ?? "",
                              status,
                            }),
                          );
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
