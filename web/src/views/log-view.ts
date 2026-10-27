// A route's run-state log: the newest records the route view holds, then
// older ones a page at a time, down to the window's start (A24); past it
// the log says the history is in the route file.
import { routeRunState } from "../api.ts";
import { attr, child, cls, html, list, type ListNode, on, text, type ViewNodeOf } from "../herdsman-blocks-view.ts";
import type { LogLine, Route } from "../types.ts";
import { toast } from "../ui.ts";
import { keyedOnce } from "./route.ts";

/** Reads a page of older records: the lines before `before`, how many are older, and the history they are from. */
export type PageReader = (
  route: string,
  before: number,
) => Promise<{ lines: LogLine[]; older: number; history?: string | undefined }>;

/**
 * The records a route's view has shown, oldest first, from index `lo`, of
 * one history (Route.history); kept while pages were loaded.
 */
interface Seen {
  lo: number;
  lines: LogLine[];
  history: string | undefined;
}

// By route: the app describes the view again on every update, and the pages
// already loaded stay (at most the window's records per route) while the
// route's history stays the same, for up to SEEN_ROUTES route histories
// with loaded pages, ordered by cache use; the least recently used goes first.
export const SEEN_ROUTES = 4;
const seen = new Map<string, Seen>();
// The routes whose "Show earlier lines" read is under way: their button is
// disabled until it answers.
const loading = new Set<string>();

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
  if (!prev || prev.history !== r.history || prev.lo >= at || end < at || end > at + newest.length) {
    seen.delete(r.id);
    return { lo: at, lines: newest, history: r.history };
  }
  for (let i = at; i < end; i++) {
    const a = prev.lines[i - prev.lo]!;
    const b = newest[i - at]!;
    if (a.time !== b.time || a.text !== b.text) {
      seen.delete(r.id);
      return { lo: at, lines: newest, history: r.history };
    }
  }
  const lo = Math.max(prev.lo, from);
  const s = { lo, lines: [...prev.lines.slice(lo - prev.lo, at - prev.lo), ...newest], history: r.history };
  remember(r.id, s);
  return s;
}

const Muted = html`<p class="muted pad">${text()}</p>`;
const Line = html`<li class=${cls()}>
  <time>${text()}</time>
  <p>${text()}</p>
</li>`;
const Log = html`<ol class="timeline">
  ${child<ListNode<ViewNodeOf<typeof Line>, string>>()}
</ol>`;
type LogListNode = ViewNodeOf<typeof Log>;
const Note = html`<p class="muted pad">Older history is in 00-route.md</p>`;
const More = html`<button class="btn ghost" type="button" disabled=${attr<boolean>()} onclick=${on<"click">()}>
  ${text()}
</button>`;
// The lines with what follows them: the button for earlier lines, or the
// note that the rest is in the route file.
// After the last page of a route whose history starts at its first record,
// nothing follows the lines, and they stay in their wrapper (as the h()
// version left them), so the list keeps its node.
const Paged = html`<div>
  ${child<LogListNode>()}${child<ViewNodeOf<typeof More> | ViewNodeOf<typeof Note> | null>()}
</div>`;

/** What `logView` returns. */
export type LogNode = LogListNode | ViewNodeOf<typeof Paged> | ViewNodeOf<typeof Muted>;

/**
 * The log of route r. A page of earlier lines is kept here, by route, and
 * `redraw` asks the app to describe the view again (the app calls this
 * again with its newest route), so the page shows.
 */
export function logView(
  r: Route,
  redraw: () => void,
  page: PageReader = routeRunState,
  failed: (msg: string) => void = (msg) => toast(msg, "error"),
): LogNode {
  const from = r.run_state_from ?? 0;
  const shown = joined(r, from);
  if (!shown.lines.length) return Muted(["No run-state lines yet."]);
  const lines = Log([
    list(
      keyedOnce([...shown.lines].reverse(), (l) => [l.time, l.text]),
      ([k]) => k,
      ([, l]) => Line([l.text.startsWith("web:") ? "web" : "", l.time, l.text]),
    ),
  ]);
  if (shown.lo <= from) return from ? Paged([lines, Note([])]) : seen.has(r.id) ? Paged([lines, null]) : lines;
  const more = (): void => {
    if (loading.has(r.id)) return;
    loading.add(r.id);
    redraw();
    const before = shown.lo;
    page(r.id, before).then(
      (p) => {
        loading.delete(r.id);
        // A page of another history does not join these lines; the
        // route's next update describes the view again.
        if (p.history !== r.history) {
          failed("Could not load earlier lines: the route file changed: reload the route");
          redraw();
          return;
        }
        remember(r.id, { lo: p.older, lines: [...p.lines, ...shown.lines], history: r.history });
        redraw();
      },
      (err: Error) => {
        loading.delete(r.id);
        failed(`Could not load earlier lines: ${err.message}`);
        redraw();
      },
    );
  };
  return Paged([lines, More([loading.has(r.id), more, `Show ${shown.lo - from} earlier lines`])]);
}
