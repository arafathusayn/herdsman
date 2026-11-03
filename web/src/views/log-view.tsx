import { effect, For, memo, Show, signal, untrack } from "@reactre/core";
import { map } from "@reactre/core/features/lists";

import { routeRunState } from "../api.ts";
import type { LogLine, Route } from "../types.ts";
import { toast } from "../ui.ts";
import { keyedOnce } from "./route.tsx";

export type PageReader = (
  route: string,
  before: number,
) => Promise<{ lines: LogLine[]; older: number; history?: string | undefined }>;
interface Seen {
  lo: number;
  lines: LogLine[];
  history: string | undefined;
}
export const SEEN_ROUTES = 4;
const [seen, setSeen] = signal(new Map<string, Seen>());
const [loading, setLoading] = signal(new Set<string>());

function remember(id: string, value: Seen): void {
  setSeen((current) => {
    const next = new Map(current);
    next.delete(id);
    next.set(id, value);
    while (next.size > SEEN_ROUTES) next.delete(next.keys().next().value!);
    return next;
  });
}

function joined(route: Route, previous: Seen | undefined): Seen & { cached: boolean } {
  const newest = route.run_state ?? [];
  const at = route.older_run_state ?? 0;
  const from = route.run_state_from ?? 0;
  const end = previous ? previous.lo + previous.lines.length : 0;
  const fresh = { lo: at, lines: newest, history: route.history, cached: false };
  if (!previous || previous.history !== route.history || previous.lo >= at || end < at || end > at + newest.length)
    return fresh;
  for (let index = at; index < end; index++) {
    const old = previous.lines[index - previous.lo]!;
    const current = newest[index - at]!;
    if (old.time !== current.time || old.text !== current.text) return fresh;
  }
  const lo = Math.max(previous.lo, from);
  return {
    lo,
    lines: [...previous.lines.slice(lo - previous.lo, at - previous.lo), ...newest],
    history: route.history,
    cached: true,
  };
}

function LogLines(props: { lines: LogLine[] }) {
  const entries = memo(() => keyedOnce([...props.lines].reverse(), (line) => [line.time, line.text]));
  const rows = map(entries, (entry) => entry, { keyed: ([key]) => key });
  return (
    <ol class="timeline">
      <For each={rows()} key={(entry) => entry()[0]}>
        {(entry) => (
          <li class={entry()[1].text.startsWith("web:") ? "web" : ""}>
            <time>{entry()[1].time}</time>
            <p>{entry()[1].text}</p>
          </li>
        )}
      </For>
    </ol>
  );
}

export function LogView(props: { route: Route; page?: PageReader; failed?: (message: string) => void }) {
  effect(() => {
    const route = props.route;
    untrack(() => {
      const previous = seen().get(route.id);
      if (!previous) return;
      const merged = joined(route, previous);
      if (merged.cached) remember(route.id, merged);
      else
        setSeen((current) => {
          const next = new Map(current);
          next.delete(route.id);
          return next;
        });
    });
  });
  const shown = memo(() => joined(props.route, seen().get(props.route.id)));
  const from = memo(() => props.route.run_state_from ?? 0);
  const paged = memo(() => shown().lo > from() || from() > 0 || shown().cached);
  const failed = (message: string): void => (props.failed ?? ((text) => toast(text, "error")))(message);
  const more = async (): Promise<void> => {
    const route = props.route;
    if (loading().has(route.id)) return;
    const current = shown();
    setLoading((value) => new Set([...value, route.id]));
    // Track live appends even while the first page is still on its way.
    remember(route.id, current);
    try {
      const page = await (props.page ?? routeRunState)(route.id, current.lo);
      if (page.history !== route.history) {
        failed("Could not load earlier lines: the route file changed: reload the route");
        return;
      }
      const latest = seen().get(route.id);
      // An incompatible snapshot or cache eviction invalidates this request.
      if (!latest || latest.history !== route.history || latest.lo !== current.lo) return;
      remember(route.id, { lo: page.older, lines: [...page.lines, ...latest.lines], history: route.history });
    } catch (error) {
      failed(`Could not load earlier lines: ${error instanceof Error ? error.message : String(error)}`);
    } finally {
      setLoading((value) => {
        const next = new Set(value);
        next.delete(route.id);
        return next;
      });
    }
  };
  return (
    <Show when={shown().lines.length > 0} fallback={<p class="muted pad">No run-state lines yet.</p>}>
      <Show when={paged()} fallback={<LogLines lines={shown().lines} />}>
        <div>
          <LogLines lines={shown().lines} />
          <Show
            when={shown().lo > from()}
            fallback={
              <Show when={from() > 0}>
                <p class="muted pad">Older history is in 00-route.md</p>
              </Show>
            }
          >
            <button
              class="btn ghost"
              type="button"
              disabled={loading().has(props.route.id)}
              onClick={() => void more()}
            >{`Show ${shown().lo - from()} earlier lines`}</button>
          </Show>
        </div>
      </Show>
    </Show>
  );
}
