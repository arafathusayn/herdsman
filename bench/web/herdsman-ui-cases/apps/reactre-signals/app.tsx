import { attach, Show, signal } from "@reactre/core";

import { Tabs } from "../../shared/reactre/view.tsx";
import type { State } from "../../shared/state.ts";

/** The built-in signal owns each fresh snapshot. Rendering is shared with
 * the actor entry so the comparison changes only the state owner. */
export function createApp(root: HTMLElement, onSelect: (pane: string) => void) {
  let render!: (state: State) => void;
  const dispose = attach(() => {
    const [state, write] = signal<State | null>(null);
    render = (next) => {
      write(next);
    };
    return (
      <Show when={state()}>
        {(s) => <Tabs state={s()} onSelect={onSelect} />}
      </Show>
    );
  }, root);
  return { render: (state: State) => render(state), dispose };
}
