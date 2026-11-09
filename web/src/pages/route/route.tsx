import { For, Show } from "@reactre/core";

import { services } from "../../sdk/index.ts";
import { Async, every, load, NotServed, PageFrame } from "../load.tsx";

const refreshMs = 5000;

export function RoutesPage() {
  const state = load(() => services().routes.list({}));
  every(refreshMs, () => void state.reload());
  return (
    <PageFrame title="Route starts" lead="Every route the daemon has started, and how far each got.">
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
