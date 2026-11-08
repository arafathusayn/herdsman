// SonnetJS, as the js-framework-benchmark "sonnet" entry uses it: components
// return their markup as strings from get(), the root is mounted with
// createApp().root(App).mount("#main"), and the component's script() runs once
// mounted and does the list work in plain DOM. Sonnet has no reactivity, no
// list primitive and no diffing (rerender replaces a whole subtree), so, like
// the official entry (rows cloned from a <template>, text set through
// nodeValue, select/swap/remove done by hand, clicks delegated to the tbody),
// rows here are cloned from <template>s in index.html, keyed by hand: kept
// rows get only their changed slots rewritten, and rows move, enter or leave
// in place. Card clicks are delegated to the root, as the entry delegates.

import { $component, createApp, SonnetComponent } from "@sonnetjs/core";
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
  statusLabel,
} from "../../shared/state.ts";

/** A row's slot values by field; key is its list key. */
type VM = { key: string } & Record<string, string>;

/** A row template: the element to clone and its {field} slots as child-index paths. */
interface Tpl {
  el: Element;
  slots: [field: string, path: number[], attr: string | null][];
}

type Row = Element & { key: string; vm: VM; tpl: Tpl; refs: Node[]; gen: number };

const SLOT = /^\{(\w+)\}$/;

/** Reads a <template>'s {field} slots once and blanks them; rows are clones of the result. */
function tpl(id: string): Tpl {
  const el = (document.getElementById(id) as HTMLTemplateElement).content.firstElementChild!;
  const slots: Tpl["slots"] = [];
  const walk = (n: Node, path: number[]) => {
    if (n.nodeType === 3) {
      const m = SLOT.exec(n.nodeValue!);
      if (m) {
        slots.push([m[1]!, path, null]);
        n.nodeValue = "";
      }
      return;
    }
    for (const a of [...(n as Element).attributes]) {
      const m = SLOT.exec(a.value);
      if (m) {
        slots.push([m[1]!, path, a.name]);
        a.value = "";
      }
    }
    n.childNodes.forEach((c, i) => walk(c, [...path, i]));
  };
  walk(el, []);
  return { el, slots };
}

const T = {
  lane: tpl("lane"),
  empty: tpl("lane-empty"),
  cardTask: tpl("card-task"),
  card: tpl("card"),
  evWeb: tpl("ev-web"),
  evStatus: tpl("ev-status"),
  evStatusAnon: tpl("ev-status-anon"),
  evLabel: tpl("ev-label"),
  evLabelAnon: tpl("ev-label-anon"),
  check: tpl("check"),
  checkNoId: tpl("check-noid"),
  line: tpl("line"),
};

let gen = 0;

function make(t: Tpl, vm: VM): Row {
  const r = t.el.cloneNode(true) as Row;
  const refs: Node[] = new Array(t.slots.length);
  for (let i = 0; i < t.slots.length; i++) {
    const [field, path, attr] = t.slots[i]!;
    let n: Node = r;
    for (const j of path) n = n.childNodes[j]!;
    refs[i] = n;
    if (attr) (n as Element).setAttribute(attr, vm[field]!);
    else n.nodeValue = vm[field]!;
  }
  r.key = vm.key;
  r.vm = vm;
  r.tpl = t;
  r.refs = refs;
  r.gen = gen;
  return r;
}

function patch(r: Row, vm: VM): void {
  const old = r.vm;
  const slots = r.tpl.slots;
  for (let i = 0; i < slots.length; i++) {
    const [field, , attr] = slots[i]!;
    const v = vm[field]!;
    if (v === old[field]) continue;
    if (attr) (r.refs[i] as Element).setAttribute(attr, v);
    else r.refs[i]!.nodeValue = v;
  }
  r.vm = vm;
  r.gen = gen;
}

/** The rows for items, keyed: kept rows are patched, rows whose template changed are rebuilt. */
function rows(map: Map<string, Row>, tpls: Tpl[], items: VM[]): Row[] {
  const out: Row[] = new Array(items.length);
  for (let i = 0; i < items.length; i++) {
    const vm = items[i]!;
    const t = tpls[i]!;
    let r = map.get(vm.key);
    if (r && r.tpl === t) patch(r, vm);
    else {
      r?.remove();
      r = make(t, vm);
      map.set(vm.key, r);
    }
    out[i] = r;
  }
  return out;
}

/** Puts rows, in order, after `after` (or first) in parent; moves only rows out of place. */
function place(parent: Node, after: Node | null, list: Row[]): void {
  let cur = after ? after.nextSibling : parent.firstChild;
  for (const r of list) {
    if (r === cur) cur = cur.nextSibling;
    else parent.insertBefore(r, cur);
  }
}

function sweep(map: Map<string, Row>): void {
  for (const [k, r] of map) {
    if (r.gen !== gen) {
      r.remove();
      map.delete(k);
    }
  }
}

// --- view models

const pillCls = (s: string) => `pill s-${s}`;

function cardVM(m: Member, selected: string | null): VM {
  return {
    key: m.pane,
    cls: `card r-${m.role} s-${m.status}${selected === m.pane ? " selected" : ""}`,
    hover: `Open ${m.title} (${m.pane})`,
    pane: m.pane,
    title: m.title,
    pillCls: pillCls(m.status),
    statusLabel: statusLabel[m.status],
    task: m.task ?? "",
    sub: m.sub,
    tab: m.tab,
    term: m.term,
  };
}

function eventTpl(e: Event): Tpl {
  if (e.source === "web") return T.evWeb;
  if (e.status) return e.who !== null ? T.evStatus : T.evStatusAnon;
  return e.who !== null ? T.evLabel : T.evLabelAnon;
}

function eventVM(e: Event): VM {
  return {
    key: eventKey(e),
    source: e.source,
    at: e.at,
    clock: e.clock,
    text: e.text ?? "",
    who: e.who ?? "",
    label: e.label,
    pillCls: e.status ? pillCls(e.status) : "",
    statusLabel: e.status ? statusLabel[e.status] : "",
  };
}

function checkVM(c: Check): VM {
  return {
    key: checkKey(c),
    cls: c.done ? "done" : c.skipped ? "skipped" : "",
    state: c.done ? "Done" : c.skipped ? "Not applicable" : "Open",
    mark: c.done ? "✓" : c.skipped ? "–" : "",
    id: c.id,
    text: c.text,
  };
}

function lineVM(l: Line): VM {
  return { key: lineKey(l), cls: l.text.startsWith("web:") ? "web" : "", time: l.time, text: l.text };
}

// --- components

class TeamView extends SonnetComponent {
  get() {
    return `<div class="team"><div class="kanban"></div></div>`;
  }
}
class ActivityView extends SonnetComponent {
  get() {
    return `<ol class="timeline activity"></ol>`;
  }
}
class ChecklistView extends SonnetComponent {
  get() {
    return `<ul class="checklist"></ul>`;
  }
}
class LogView extends SonnetComponent {
  get() {
    return `<ol class="timeline"></ol>`;
  }
}

const views: Record<State["tab"], SonnetComponent> = {
  team: $component(TeamView)(),
  activity: $component(ActivityView)(),
  checklist: $component(ChecklistView)(),
  log: $component(LogView)(),
};

class App extends SonnetComponent {
  get() {
    return "";
  }

  script() {
    start((root, onSelect) => {
      let tab: State["tab"] | null = null;
      let list: Element;
      const laneRows = new Map<string, Row>();
      const cardRows = new Map<string, Row>();
      const itemRows = new Map<string, Row>();

      root.onclick = (e) => {
        const card = (e.target as Element).closest(".card") as Row | null;
        if (card) onSelect(card.key);
      };

      const flat = <X>(items: X[], t: (x: X) => Tpl, vm: (x: X) => VM) => {
        place(list, null, rows(itemRows, items.map(t), items.map(vm)));
        sweep(itemRows);
      };

      return {
        render(s) {
          gen++;
          if (s.tab !== tab) {
            // A tab switch replaces the view with the tab component's markup, as mount does.
            tab = s.tab;
            laneRows.clear();
            cardRows.clear();
            itemRows.clear();
            root.innerHTML = views[tab].get() as string;
            list = tab === "team" ? root.firstElementChild!.firstElementChild! : root.firstElementChild!;
          }
          if (s.tab === "team") {
            const ls = lanes(s.members);
            const laneEls = rows(
              laneRows,
              ls.map(() => T.lane),
              ls.map((l) => ({ key: l.status, cls: `lane s-${l.status}`, title: l.title, count: String(l.members.length) })),
            );
            place(list, null, laneEls);
            for (let i = 0; i < ls.length; i++) {
              const lane = ls[i]!;
              const el = laneEls[i]!;
              const ms = lane.members;
              const cards = ms.length
                ? rows(
                    cardRows,
                    ms.map((m) => (m.task !== null ? T.cardTask : T.card)),
                    ms.map((m) => cardVM(m, s.selected)),
                  )
                : rows(cardRows, [T.empty], [{ key: `empty:${lane.status}` }]);
              place(el, el.firstChild, cards);
            }
            sweep(cardRows);
            sweep(laneRows);
          } else if (s.tab === "activity") flat(s.events, eventTpl, eventVM);
          else if (s.tab === "checklist") flat(s.checklist, (c) => (c.id ? T.check : T.checkNoId), checkVM);
          else flat(s.log, () => T.line, lineVM);
        },
      };
    });
  }
}

createApp().root($component(App)).mount("#main");
