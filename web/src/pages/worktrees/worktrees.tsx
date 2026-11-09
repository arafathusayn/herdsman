import type { ResourcesResponse } from "@herdsman/sdk/herdsman/daemon/v1/ledger";
import { For, Show, signal } from "@reactre/core";

import { services } from "../../sdk/index.ts";
import { Failure, fieldValue, NotServed, PageFrame } from "../load.tsx";

export function WorktreesPage() {
  const [commonDir, setCommonDir] = signal("");
  const [task, setTask] = signal("");
  const [reply, setReply] = signal<ResourcesResponse | null>(null);
  const [error, setError] = signal<unknown>(undefined);

  const look = async (event: Event) => {
    event.preventDefault();
    setError(undefined);
    setReply(null);
    try {
      const found = await services().ledger.resources({ commonDir: commonDir().trim(), task: task().trim() });
      setReply(() => found);
    } catch (failure) {
      setError(() => failure);
    }
  };

  return (
    <PageFrame title="Worktrees" lead="What each task holds, and what taking it down would do.">
      <form class="page-form" onSubmit={look}>
        <label class="field">
          Repository git folder
          <input
            required
            placeholder="/path/to/repo/.git"
            value={commonDir()}
            onInput={(event) => setCommonDir(fieldValue(event))}
          />
        </label>
        <label class="field">
          Task
          <input required value={task()} onInput={(event) => setTask(fieldValue(event))} />
        </label>
        <button class="btn primary" type="submit" disabled={!commonDir().trim() || !task().trim()}>
          Show resources
        </button>
      </form>
      <Show when={error() !== undefined}>
        <Failure error={error()} what="The resource ledger" />
      </Show>
      <Show when={reply()}>
        {(found) => (
          <>
            <h2 class="page-h2">Resources</h2>
            <Show when={(found().records ?? []).length > 0} fallback={<p class="page-empty">Nothing recorded.</p>}>
              <ul class="page-list">
                <For each={found().records ?? []} key={(record) => `${record.kind}:${record.id}`}>
                  {(record) => (
                    <li class="page-row">
                      <div class="page-row-main">
                        <strong>{`${record.kind ?? ""} ${record.id ?? ""}`}</strong>
                        <span class="page-sub">{record.command}</span>
                      </div>
                      <Show when={record.adopted}>
                        <span class="badge">adopted</span>
                      </Show>
                    </li>
                  )}
                </For>
              </ul>
            </Show>
            <h2 class="page-h2">Teardown plan</h2>
            <Show when={(found().plan?.lines ?? []).length > 0} fallback={<p class="page-empty">Nothing to do.</p>}>
              <ul class="page-list">
                <For each={found().plan?.lines ?? []} key={(line) => `${line.kind}:${line.id}`}>
                  {(line) => (
                    <li class="page-row">
                      <div class="page-row-main">
                        <strong>{`${line.kind ?? ""} ${line.id ?? ""}`}</strong>
                        <span class="page-sub">{line.class}</span>
                      </div>
                      <span class="badge">{line.action}</span>
                    </li>
                  )}
                </For>
              </ul>
            </Show>
          </>
        )}
      </Show>
      <NotServed what="Queueing teardown, clean, down and undo" terminal="herdsman teardown <task>" />
    </PageFrame>
  );
}
