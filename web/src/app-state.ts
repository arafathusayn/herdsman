import { signal } from "@reactre/core";

import { nextView } from "./model.ts";
import type { Channel, Entry, State } from "./types.ts";
import type { Theme } from "./ui.ts";

export type RouteTab = "team" | "checklist" | "log" | "docs" | "changes" | "artifacts" | "messages" | "activity";

export interface View {
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
  server: State | null;
  entries: Entry[];
  connected: boolean;
  view: View;
  theme: Theme;
  fatal: string;
  channel: Channel | null;
}

/** Immutable application snapshots, read and updated through a Reactre signal. */
export function createAppState(initial: App, navigate: (view: View, push: boolean) => void = () => {}) {
  const [state, setState] = signal(initial);
  return {
    state,
    get app(): App {
      return state();
    },
    set(patch: Partial<App>): void {
      setState((app) => ({ ...app, ...patch }));
    },
    go(patch: Partial<View>, push = true): void {
      const view = nextView(state().view, patch);
      navigate(view, push);
      setState((app) => ({ ...app, view }));
    },
    addEntries(entries: Entry[]): void {
      setState((app) => ({ ...app, entries: [...app.entries, ...entries].slice(-400) }));
    },
  };
}
