// DooHTML, as the js-framework-benchmark "doohtml-dp" entry uses it: rows come
// from <template> markup with {{field}} slots (index.html), compiled once by
// createTemplate and stamped out by addWithProvider (clone the processed row,
// fill its slots, set row.key). DooHTML has no diffing or reactivity, so, like
// the official entry (update, select, swap and delete all edit the rows by
// hand), keyed updates are done here: rows are found by their DooHTML key,
// their changed slots rewritten through the slot paths DooHTML compiled, and
// rows moved, inserted or removed in place. Clicks are delegated to the root,
// as the official entry delegates to its tbody and reads row.key.

// @ts-expect-error doohtml ships no types
import { addWithProvider, createTemplate } from "doohtml";
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

/** A row's slot values, by template field; key is the DooHTML data-key. */
type VM = { key: string } & Record<string, string>;

/** A compiled DooHTML template: its processed row and slot paths, with a fragment to stamp rows into. */
interface Tpl {
  stage: DocumentFragment;
  slots: [field: string, path: number[], type: string][];
}

type Row = Element & { key: string; vm: VM; tpl: Tpl; gen: number };

const ids = [
  "lane-working",
  "lane-blocked",
  "lane-idle",
  "lane-done",
  "lane-unknown",
  "lane-gone",
  "lane-empty",
  "card-task",
  "card",
  "ev-web",
  "ev-status",
  "ev-status-anon",
  "ev-label",
  "ev-label-anon",
  "check",
  "check-noid",
  "line",
] as const;
type TplId = (typeof ids)[number];

/** Compiles every row template with createTemplate, through hidden subscribers outside #main. */
async function compile(): Promise<Record<TplId, Tpl>> {
  const holder = document.createElement("div");
  holder.hidden = true;
  holder.innerHTML = ids.map((id) => `<div data-template="${id}" data-key="key"></div>`).join("");
  document.body.append(holder);
  const out = {} as Record<TplId, Tpl>;
  for (const id of ids) {
    const target = await createTemplate(id);
    const stage = Object.assign(document.createDocumentFragment(), {
      processNode: target.processNode,
      dataSlots: target.dataSlots,
      key: target.key,
    });
    out[id] = { stage, slots: target.dataSlots };
  }
  holder.remove();
  return out;
}

const T = await compile();
let gen = 0;

/** Stamps rows items[start..end) (all of one template) with DooHTML; returns them in order. */
function stamp(tpl: Tpl, items: VM[], startAt: number, end: number): Row[] {
  // Rows stamped earlier in this render may still sit in the stage until placed.
  const kids = tpl.stage.childNodes;
  const from = kids.length;
  addWithProvider(tpl.stage, items, startAt, end - startAt);
  const rows = Array.prototype.slice.call(kids, from) as Row[];
  for (let i = 0; i < rows.length; i++) {
    const r = rows[i]!;
    r.vm = items[startAt + i]!;
    r.tpl = tpl;
    r.gen = gen;
  }
  return rows;
}

/** Rewrites the slots whose values changed, by the paths DooHTML compiled for the template. */
function patch(r: Row, vm: VM): void {
  const old = r.vm;
  for (const [field, path, type] of r.tpl.slots) {
    const v = vm[field]!;
    if (v === old[field]) continue;
    let n: Node = r;
    for (const i of path) n = n.childNodes[i]!;
    if (type === "textContent") n.nodeValue = v;
    else (n as Element).setAttribute(type, v);
  }
  r.vm = vm;
}

/** The rows for items, keyed: kept rows are patched, rows whose template changed are re-stamped, new ones stamped. */
function rows(map: Map<string, Row>, tpls: Tpl[], items: VM[]): Row[] {
  const out: Row[] = new Array(items.length);
  let i = 0;
  while (i < items.length) {
    const vm = items[i]!;
    const tpl = tpls[i]!;
    const r = map.get(vm.key);
    if (r && r.tpl === tpl) {
      patch(r, vm);
      r.gen = gen;
      out[i++] = r;
      continue;
    }
    // A run of new rows with the same template is stamped in one call.
    let j = i + 1;
    while (j < items.length && tpls[j] === tpl && !map.has(items[j]!.key)) j++;
    for (const n of stamp(tpl, items, i, j)) {
      map.get(n.key)?.remove();
      map.set(n.key, n);
      out[i++] = n;
    }
  }
  return out;
}

/** Puts rows, in order, after the node `after` (or first) in parent; moves only rows out of place. */
function place(parent: Element, after: Node | null, list: Row[]): void {
  let cur = after ? after.nextSibling : parent.firstChild;
  for (const r of list) {
    if (r === cur) cur = cur.nextSibling;
    else parent.insertBefore(r, cur);
  }
}

/** Removes the rows this render did not ask for. */
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
  if (e.source === "web") return T["ev-web"];
  if (e.status) return e.who !== null ? T["ev-status"] : T["ev-status-anon"];
  return e.who !== null ? T["ev-label"] : T["ev-label-anon"];
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

// --- views

const views = {
  team: document.getElementById("view-team") as HTMLTemplateElement,
  activity: document.getElementById("view-activity") as HTMLTemplateElement,
  checklist: document.getElementById("view-checklist") as HTMLTemplateElement,
  log: document.getElementById("view-log") as HTMLTemplateElement,
};

start((root, onSelect) => {
  let tab: State["tab"] | null = null;
  let list: Element;
  const laneRows = new Map<string, Row>();
  const cardRows = new Map<string, Row>();
  const itemRows = new Map<string, Row>();

  root.addEventListener("click", (e) => {
    const card = (e.target as Element).closest(".card") as Row | null;
    if (card) onSelect(card.key);
  });

  const flat = <T>(items: T[], tpl: (x: T) => Tpl, vm: (x: T) => VM) => {
    place(list, null, rows(itemRows, items.map(tpl), items.map(vm)));
    sweep(itemRows);
  };

  return {
    render(s) {
      gen++;
      if (s.tab !== tab) {
        // A tab switch replaces the view: a fresh shell, every row stamped anew.
        tab = s.tab;
        laneRows.clear();
        cardRows.clear();
        itemRows.clear();
        const view = views[tab].content.cloneNode(true) as DocumentFragment;
        list = tab === "team" ? view.firstElementChild!.firstElementChild! : view.firstElementChild!;
        root.replaceChildren(view);
      }
      if (s.tab === "team") {
        const ls = lanes(s.members);
        const laneEls = rows(
          laneRows,
          ls.map((l) => T[`lane-${l.status}`]),
          ls.map((l) => ({ key: l.status, count: String(l.members.length) })),
        );
        place(list, null, laneEls);
        for (let i = 0; i < ls.length; i++) {
          const lane = ls[i]!;
          const el = laneEls[i]!;
          const ms = lane.members;
          const cards = ms.length
            ? rows(
                cardRows,
                ms.map((m) => (m.task !== null ? T["card-task"] : T.card)),
                ms.map((m) => cardVM(m, s.selected)),
              )
            : rows(cardRows, [T["lane-empty"]], [{ key: `empty:${lane.status}` }]);
          place(el, el.firstChild, cards);
        }
        sweep(cardRows);
        sweep(laneRows);
      } else if (s.tab === "activity") flat(s.events, eventTpl, eventVM);
      else if (s.tab === "checklist") flat(s.checklist, (c) => (c.id ? T.check : T["check-noid"]), checkVM);
      else flat(s.log, () => T.line, lineVM);
    },
  };
});
