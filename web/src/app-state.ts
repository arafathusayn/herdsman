import { batch, memo } from "@reactre/core";

import { nextView } from "./model.ts";
import { valueState } from "./state/value.ts";
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
  server: State | null;
  entries: Entry[];
  connected: boolean;
  view: View;
  theme: Theme;
  fatal: string;
  channel: Channel | null;
}

/** Immutable application snapshots, owned by a Reactre state actor. */
export function createAppState(initial: App, navigate: (view: View, push: boolean) => void = () => {}) {
  // Independently changing domains must not serialize the server's complete
  // route/log history on every click, stream entry or connection heartbeat.
  const [server, setServer] = valueState(initial.server);
  const [entries, setEntries] = valueState(initial.entries);
  const [connected, setConnected] = valueState(initial.connected);
  const [view, setView] = valueState(initial.view);
  const [theme, setTheme] = valueState(initial.theme);
  const [fatal, setFatal] = valueState(initial.fatal);
  const [channel, setChannel] = valueState(initial.channel);
  const state = memo((): App =>
    Object.freeze({
      server: server(),
      entries: entries(),
      connected: connected(),
      view: view(),
      theme: theme(),
      fatal: fatal(),
      channel: channel(),
    }),
  );
  return {
    state,
    get app(): App {
      return state();
    },
    set(patch: Partial<App>): void {
      batch(() => {
        if ("server" in patch) setServer(patch.server!);
        if ("entries" in patch) setEntries(patch.entries!);
        if ("connected" in patch) setConnected(patch.connected!);
        if ("view" in patch) setView(patch.view!);
        if ("theme" in patch) setTheme(patch.theme!);
        if ("fatal" in patch) setFatal(patch.fatal!);
        if ("channel" in patch) setChannel(patch.channel!);
      });
    },
    go(patch: Partial<View>, push = true): void {
      const next = nextView(view(), patch);
      navigate(next, push);
      setView(next);
    },
    addEntries(entries: Entry[]): void {
      setEntries((previous) => [...previous, ...entries].slice(-400));
    },
  };
}
