// herdsman-blocks as herdsman web uses it: build the whole view with h(),
// keyed lists with each(), then patch() it into the live DOM. This app is also
// the markup reference the other apps must match (see ../../README.md).

import { each, h, patch } from "../../../../web/src/dom.ts";
import { start } from "../../shared/driver.ts";
import {
  type Check,
  checkKey,
  type Event,
  eventKey,
  lanes,
  type Line,
  lineKey,
  type Member,
  type State,
  type Status,
  statusLabel,
} from "../../shared/state.ts";

function pill(status: Status): HTMLElement {
  return h("span", { class: `pill s-${status}` }, h("span", { class: "dot", "aria-hidden": "true" }), statusLabel[status]);
}

function card(m: Member, selected: boolean, onSelect: (pane: string) => void): HTMLElement {
  return h(
    "button",
    {
      class: `card r-${m.role} s-${m.status}${selected ? " selected" : ""}`,
      title: `Open ${m.title} (${m.pane})`,
      dataset: { pane: m.pane },
      onclick: () => onSelect(m.pane),
    },
    h("div", { class: "card-top" }, h("span", { class: "card-title" }, m.title), pill(m.status)),
    m.task !== null ? h("div", { class: "card-task" }, m.task) : null,
    h("div", { class: "card-sub" }, m.sub),
    h(
      "div",
      { class: "card-foot" },
      h("code", null, m.pane),
      h("span", { class: "muted" }, m.tab),
      h("span", { class: "muted clip", title: m.term }, m.term),
    ),
  );
}

function teamView(s: State, onSelect: (pane: string) => void): HTMLElement {
  return h(
    "div",
    { class: "team" },
    h(
      "div",
      { class: "kanban" },
      lanes(s.members).map((lane) =>
        h(
          "section",
          { class: `lane s-${lane.status}`, "aria-label": lane.title },
          h(
            "h2",
            null,
            h("span", { class: "lane-dot", "aria-hidden": "true" }),
            lane.title,
            h("span", { class: "count" }, String(lane.members.length)),
          ),
          lane.members.length
            ? each(
                lane.members,
                (m) => m.pane,
                (m) => card(m, s.selected === m.pane, onSelect),
              )
            : h("p", { class: "lane-empty" }, "None"),
        ),
      ),
    ),
  );
}

function eventItem(e: Event): HTMLElement {
  const who = e.who !== null ? h("strong", null, e.who) : null;
  const body =
    e.source === "web"
      ? [h("span", { class: "chip accent" }, "web"), h("span", null, e.text ?? "")]
      : e.status
        ? [who, pill(e.status)]
        : [h("span", { class: "chip" }, e.label), who];
  return h("li", { class: e.source }, h("time", { datetime: e.at }, e.clock), h("p", { class: "event" }, body));
}

function activityView(s: State): HTMLElement {
  return h("ol", { class: "timeline activity" }, each(s.events, eventKey, eventItem));
}

function checkItem(c: Check): HTMLElement {
  const state = c.done ? "Done" : c.skipped ? "Not applicable" : "Open";
  return h(
    "li",
    { class: c.done ? "done" : c.skipped ? "skipped" : "" },
    h("span", { class: "box", "aria-label": state }, c.done ? "✓" : c.skipped ? "–" : null),
    c.id ? h("code", null, c.id) : null,
    h("span", null, c.text),
  );
}

function checklistView(s: State): HTMLElement {
  return h("ul", { class: "checklist" }, each(s.checklist, checkKey, checkItem));
}

function lineItem(l: Line): HTMLElement {
  return h("li", { class: l.text.startsWith("web:") ? "web" : "" }, h("time", null, l.time), h("p", null, l.text));
}

function logView(s: State): HTMLElement {
  return h("ol", { class: "timeline" }, each(s.log, lineKey, lineItem));
}

start((root, onSelect) => ({
  render(s) {
    const body =
      s.tab === "team"
        ? teamView(s, onSelect)
        : s.tab === "activity"
          ? activityView(s)
          : s.tab === "checklist"
            ? checklistView(s)
            : logView(s);
    // Keyed by tab, as herdsman's renderMain, so a tab switch replaces the view.
    body.dataset["key"] = `tab-${s.tab}`;
    patch(root, body);
  },
}));
