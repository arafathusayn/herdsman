import { memo, untrack } from "@reactre/core";
import { createActor, type DeepReadonly } from "@reactre/state";
import { immutableData } from "@reactre/state/data";

import { nextView } from "./model.ts";
import { appMachine } from "./state/app.machine.ts";
import { equalData } from "./state/equal-data.ts";
import { observeActor } from "./state/observe.ts";
import type { Channel, Entry, State } from "./types.ts";
import type { Theme } from "./ui.ts";

export type RouteTab = "team" | "checklist" | "log" | "docs" | "changes" | "artifacts" | "messages" | "activity";

export interface View {
  /** A page of `pages/index.tsx`; it stands in front of the route views. */
  page: string;
  route: string;
  tab: RouteTab;
  file: string;
  source: string;
  diffFile: string;
  artifact: string;
  pane: string;
  ws: string;
}

export interface App {
  server: DeepReadonly<State> | null;
  entries: readonly Entry[];
  connected: boolean;
  view: View;
  theme: Theme;
  fatal: string;
  channel: DeepReadonly<Channel> | null;
}

/** JSON data enters once, outside transitions. The actor shares unchanged
 * domains so navigation never copies the server's route/log history. */
export function createAppState(initial: App, navigate: (view: View, push: boolean) => void = () => {}) {
  const actor = createActor(appMachine, { input: { value: immutableData(initial) } });
  const observed = observeActor(actor);
  const state = memo(() => observed.read().data.value);
  const retain = <T>(previous: T, next: T): T => (equalData(previous, next) ? previous : next);
  const set = (patch: Partial<App>): void => {
    // Validate representability before comparison, even for equal updates.
    // Unknown transport data needs schema validation before this boundary.
    const incoming = immutableData(patch);
    const previous = untrack(state);
    const value = {
      server: "server" in incoming ? retain(previous.server, incoming.server) : previous.server,
      entries: "entries" in incoming ? retain(previous.entries, incoming.entries) : previous.entries,
      connected: "connected" in incoming ? incoming.connected : previous.connected,
      view: "view" in incoming ? retain(previous.view, incoming.view) : previous.view,
      theme: "theme" in incoming ? incoming.theme : previous.theme,
      fatal: "fatal" in incoming ? incoming.fatal : previous.fatal,
      channel: "channel" in incoming ? retain(previous.channel, incoming.channel) : previous.channel,
    };
    if (
      value.server === previous.server &&
      value.entries === previous.entries &&
      value.connected === previous.connected &&
      value.view === previous.view &&
      value.theme === previous.theme &&
      value.fatal === previous.fatal &&
      value.channel === previous.channel
    )
      return;
    actor.send({ type: "replace", value: Object.freeze(value) });
  };
  return {
    state,
    get app() {
      return state();
    },
    set,
    go(patch: Partial<View>, push = true): void {
      const next = nextView(untrack(state).view, patch);
      navigate(next, push);
      set({ view: next });
    },
    addEntries(entries: readonly Entry[]): void {
      set({ entries: [...untrack(state).entries, ...entries].slice(-400) });
    },
  };
}
