import { For, Show, signal } from "@reactre/core";

import { explain, services } from "../../sdk/index.ts";
import { Async, fieldValue, load, PageFrame } from "../load.tsx";

export function ProjectsPage() {
  const state = load(() => services().projects.list({}));
  const [name, setName] = signal("");
  const [description, setDescription] = signal("");
  const [problem, setProblem] = signal("");

  const create = async (event: Event) => {
    event.preventDefault();
    setProblem("");
    try {
      await services().projects.create({ name: name().trim(), description: description().trim() });
      setName("");
      setDescription("");
      await state.reload();
    } catch (error) {
      setProblem(explain(error));
    }
  };

  return (
    <PageFrame title="Projects" lead="The repositories and folders herdsman groups work under.">
      <Async state={state} what="The project list">
        {(data) => (
          <>
            <form class="page-form" onSubmit={create}>
              <label class="field">
                Name
                <input required value={name()} onInput={(event) => setName(fieldValue(event))} />
              </label>
              <label class="field">
                Description
                <input value={description()} onInput={(event) => setDescription(fieldValue(event))} />
              </label>
              <button class="btn primary" type="submit" disabled={!name().trim()}>
                Create project
              </button>
              <Show when={problem()}>
                <p class="page-error" role="alert">
                  {problem()}
                </p>
              </Show>
            </form>
            <Show when={(data.projects ?? []).length > 0} fallback={<p class="page-empty">No projects yet.</p>}>
              <ul class="page-list">
                <For each={data.projects ?? []} key={(project) => project.id?.value ?? project.name ?? ""}>
                  {(project) => (
                    <li class="page-row">
                      <div class="page-row-main">
                        <strong>{project.name}</strong>
                        <span class="page-sub">{project.description}</span>
                        <span class="page-sub">{(project.repos ?? []).map((repo) => repo.name).join(", ")}</span>
                      </div>
                      <span class="badge">{`${project.openTasks ?? 0} open`}</span>
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
