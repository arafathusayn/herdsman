// A route's run-state log: the newest records the route view holds, then
// older ones a page at a time, down to the window's start (A24); past it
// the log says the history is in the route file.
import { routeRunState } from "../api.ts";
import { h } from "../dom.ts";
import type { LogLine, Route } from "../types.ts";
import { toast } from "../ui.ts";

/** Reads a page of older records: the lines before `before` and how many are older. */
export type PageReader = (route: string, before: number) => Promise<{ lines: LogLine[]; older: number }>;

export function logView(
  r: Route,
  page: PageReader = routeRunState,
  failed: (msg: string) => void = (msg) => toast(msg, "error"),
): HTMLElement {
  const lines = [...(r.run_state ?? [])].reverse();
  if (!lines.length) return h("p", { class: "muted pad" }, "No run-state lines yet.");
  const line = (l: LogLine): HTMLElement =>
    h("li", { class: l.text.startsWith("web:") ? "web" : "" }, h("time", null, l.time), h("p", null, l.text));
  const list = h("ol", { class: "timeline" }, lines.map(line));
  const from = r.run_state_from ?? 0;
  const note = h("p", { class: "muted pad" }, "Older history is in 00-route.md");
  let older = r.older_run_state ?? 0;
  if (older <= from) return from ? h("div", null, list, note) : list;
  const wrap = h("div", null, list);
  const label = (): string => `Show ${older - from} earlier lines`;
  const more: HTMLButtonElement = h(
    "button",
    {
      class: "btn ghost",
      type: "button",
      onclick: () => {
        more.disabled = true;
        page(r.id, older).then(
          (p) => {
            for (const l of [...p.lines].reverse()) list.appendChild(line(l));
            older = p.older;
            more.disabled = false;
            more.textContent = label();
            if (older <= from) {
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
