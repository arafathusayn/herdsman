import { attach, memo, Show } from "@reactre/core";
import { useActor } from "@reactre/state/signals";

import { Tabs } from "../../shared/reactre/view.tsx";
import type { State } from "../../shared/state.ts";
import { viewMachine } from "./view.machine.ts";

export function createApp(root: HTMLElement, onSelect: (pane: string) => void) {
  let render!: (state: State) => void;
  const dispose = attach(() => {
    const [snapshot, actor] = useActor(viewMachine);
    render = (next) => {
      actor.send({ type: "render", next });
    };
    const state = memo(() => {
      const s = snapshot();
      return s.state === "showing" ? s.data.value : null;
    });
    // The actor only enters showing after its first render. Read that snapshot
    // directly under the mount guard instead of retaining another payload memo.
    return (
      <Show when={state()}>
        <Tabs state={state()!} onSelect={onSelect} />
      </Show>
    );
  }, root);
  return { render: (state: State) => render(state), dispose };
}
