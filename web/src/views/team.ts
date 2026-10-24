// The Team tab: the route's agents as a kanban board, in columns by role or
// by status, and the agent card the session view shares.
import { h } from "../dom.ts";
import { type BoardBy, lanes, type Member, roleName } from "../model.ts";
import { type App, store } from "../store.ts";
import { pill } from "../ui.ts";

const byKey = "herdsman.team-board";

function loadBy(): BoardBy {
  try {
    return localStorage.getItem(byKey) === "status" ? "status" : "role";
  } catch {
    return "role";
  }
}

let boardBy = loadBy();

/** An agent's card; with role, it also says what the agent does in the route. */
export function card(m: Member, app: App, role = false): HTMLElement {
  const sub = [role ? roleName[m.role] : "", m.agent?.display_agent ?? m.agent?.agent, m.task?.branch]
    .filter(Boolean)
    .join(" · ");
  const title = m.agent?.terminal_title_stripped ?? m.pane?.terminal_title_stripped ?? "";
  return h(
    "button",
    {
      class: `card r-${m.role} s-${m.status}${app.view.pane === m.pane_id ? " selected" : ""}`,
      title: `Open ${m.title} (${m.pane_id}): its output, keys and actions`,
      dataset: { pane: m.pane_id },
      onclick: () => store.go({ pane: m.pane_id }),
    },
    h("div", { class: "card-top" }, h("span", { class: "card-title" }, m.title), pill(m.status)),
    m.task?.task && m.task.task !== m.title ? h("div", { class: "card-task" }, m.task.task) : null,
    sub ? h("div", { class: "card-sub" }, sub) : null,
    h(
      "div",
      { class: "card-foot" },
      h("code", null, m.pane_id),
      m.tab ? h("span", { class: "muted" }, m.tab.label) : null,
      title ? h("span", { class: "muted clip", title }, title) : null,
    ),
  );
}

export function teamView(members: Member[], app: App): HTMLElement {
  const switcher = h(
    "div",
    { class: "view-switch board-switch", role: "group", "aria-label": "Columns" },
    (
      [
        ["role", "Role", "A column for each role in the route"],
        ["status", "Status", "A column for each status; cards move as agents work"],
      ] as const
    ).map(([by, label, tip]) =>
      h(
        "button",
        {
          type: "button",
          class: by === boardBy ? "active" : "",
          "aria-pressed": String(by === boardBy),
          title: tip,
          onclick: () => {
            boardBy = by;
            try {
              localStorage.setItem(byKey, by);
            } catch {
              // The choice lasts for this page only.
            }
            store.emit();
          },
        },
        label,
      ),
    ),
  );
  const cols = lanes(members, boardBy);
  return h(
    "div",
    { class: "team" },
    h("div", { class: "team-bar" }, switcher),
    members.length
      ? h(
          "div",
          { class: "kanban" },
          cols.map((lane) =>
            h(
              "section",
              { class: lane.status ? `lane s-${lane.status}` : "lane", "aria-label": lane.title },
              h(
                "h2",
                null,
                lane.status ? h("span", { class: "lane-dot", "aria-hidden": "true" }) : null,
                lane.title,
                h("span", { class: "count" }, String(lane.members.length)),
              ),
              lane.members.length
                ? lane.members.map((m) => card(m, app, boardBy === "status"))
                : h("p", { class: "lane-empty" }, "None"),
            ),
          ),
        )
      : h("p", { class: "muted" }, "This route's task table names no panes."),
  );
}
