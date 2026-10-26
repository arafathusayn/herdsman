// A route's run-state log: the newest records the route view holds, then
// older ones a page at a time, down to the window's start (A24); past it
// the log says the history is in the route file.
import { routeRunState } from "../api.ts";
import { h } from "../dom.ts";
import type { LogLine, Route } from "../types.ts";
import { toast } from "../ui.ts";

/** Reads a page of older records: the lines before `before` and how many are older. */
export type PageReader = (route: string, before: number) => Promise<{ lines: LogLine[]; older: number }>;

/** The records a route's view has shown, oldest first, from index `lo`; kept while pages were loaded. */
interface Seen {
  lo: number;
  lines: LogLine[];
}

// By route: the app builds the view again on every update, and the pages
// already loaded stay (at most the window's records per route), for the
// SEEN_ROUTES routes viewed last; the oldest viewed is dropped first.
export const SEEN_ROUTES = 4;
const seen = new Map<string, Seen>();

function remember(id: string, s: Seen): void {
  seen.delete(id);
  seen.set(id, s);
  for (const old of seen.keys()) {
    if (seen.size <= SEEN_ROUTES) break;
    seen.delete(old);
  }
}

/**
 * The records to show: the new newest ones, after the loaded older ones
 * when those still join them (same records where they overlap, none
 * missing between); else the newest alone.
 */
function joined(r: Route, from: number): Seen {
  const newest = r.run_state ?? [];
  const at = r.older_run_state ?? 0;
  const prev = seen.get(r.id);
  const end = prev ? prev.lo + prev.lines.length : 0;
  if (!prev || prev.lo >= at || end < at || end > at + newest.length) {
    seen.delete(r.id);
    return { lo: at, lines: newest };
  }
  for (let i = at; i < end; i++) {
    const a = prev.lines[i - prev.lo]!;
    const b = newest[i - at]!;
    if (a.time !== b.time || a.text !== b.text) {
      seen.delete(r.id);
      return { lo: at, lines: newest };
    }
  }
  const lo = Math.max(prev.lo, from);
  const s = { lo, lines: [...prev.lines.slice(lo - prev.lo, at - prev.lo), ...newest] };
  remember(r.id, s);
  return s;
}

export function logView(
  r: Route,
  page: PageReader = routeRunState,
  failed: (msg: string) => void = (msg) => toast(msg, "error"),
): HTMLElement {
  const from = r.run_state_from ?? 0;
  let shown = joined(r, from);
  if (!shown.lines.length) return h("p", { class: "muted pad" }, "No run-state lines yet.");
  const line = (l: LogLine): HTMLElement =>
    h("li", { class: l.text.startsWith("web:") ? "web" : "" }, h("time", null, l.time), h("p", null, l.text));
  const list = h("ol", { class: "timeline" }, [...shown.lines].reverse().map(line));
  const note = h("p", { class: "muted pad" }, "Older history is in 00-route.md");
  if (shown.lo <= from) return from ? h("div", null, list, note) : list;
  const wrap = h("div", null, list);
  const label = (): string => `Show ${shown.lo - from} earlier lines`;
  const more: HTMLButtonElement = h(
    "button",
    {
      class: "btn ghost",
      type: "button",
      onclick: () => {
        more.disabled = true;
        const before = shown.lo;
        page(r.id, before).then(
          (p) => {
            more.disabled = false;
            if (shown.lo !== before) return;
            for (const l of [...p.lines].reverse()) list.appendChild(line(l));
            shown = { lo: p.older, lines: [...p.lines, ...shown.lines] };
            remember(r.id, shown);
            more.textContent = label();
            if (shown.lo <= from) {
              more.remove();
              if (from) wrap.append(note);
            }
          },
          (err: Error) => {
            more.disabled = false;
            failed(`Could not load earlier lines: ${err.message}`);
          },
        );
      },
    },
    label(),
  ) as HTMLButtonElement;
  wrap.append(more);
  return wrap;
}
