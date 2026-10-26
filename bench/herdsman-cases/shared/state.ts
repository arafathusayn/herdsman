// The state every app renders, its deterministic data, and the operations the
// runner measures. Apps never see an operation's name: each click hands them a
// fresh snapshot (as a poll parses fresh JSON), and they render from it alone.

export type Status = "working" | "blocked" | "idle" | "done" | "unknown" | "gone";
export type Role = "implementer" | "reviewer" | "integrator" | "checkpointer";
export type Tab = "team" | "activity" | "checklist" | "log";

export interface Member {
  pane: string;
  title: string;
  role: Role;
  status: Status;
  /** Shown only when not null. */
  task: string | null;
  sub: string;
  tab: string;
  term: string;
}

export interface Event {
  /** ISO time; with kind, pane, status and text it identifies the event. */
  at: string;
  /** The clock label shown, already formatted. */
  clock: string;
  source: "herdr" | "web";
  kind: string;
  /** The display name of the event's kind, already formatted. */
  label: string;
  pane: string | null;
  /** The pane's member title, or the pane id; null when pane is null. */
  who: string | null;
  status: Status | null;
  text: string | null;
}

export interface Check {
  id: string;
  text: string;
  done: boolean;
  skipped: boolean;
}

export interface Line {
  time: string;
  text: string;
}

export interface State {
  tab: Tab;
  selected: string | null;
  members: Member[];
  /** Newest first, at most 200. */
  events: Event[];
  checklist: Check[];
  /** Newest first. */
  log: Line[];
}

export const statusLabel: Record<Status, string> = {
  working: "Working",
  idle: "Idle",
  blocked: "Needs input",
  done: "Done",
  unknown: "Unknown",
  gone: "Pane gone",
};

export const statusOrder: Status[] = ["working", "blocked", "idle", "done", "unknown", "gone"];
const steadyLanes = new Set<Status>(["working", "blocked", "idle", "done"]);

export interface Lane {
  status: Status;
  title: string;
  members: Member[];
}

/** The Team board's columns, as herdsman's lanes(members, "status"). */
export function lanes(members: Member[]): Lane[] {
  return statusOrder
    .map((s) => ({ status: s, title: statusLabel[s], members: members.filter((m) => m.status === s) }))
    .filter((l) => l.members.length > 0 || steadyLanes.has(l.status));
}

export function eventKey(e: Event): string {
  return `${e.at}|${e.kind}|${e.pane ?? ""}|${e.status ?? ""}|${e.text ?? ""}`;
}

export function checkKey(c: Check): string {
  return c.id || `text:${c.text}`;
}

export function lineKey(l: Line): string {
  return `${l.time}|${l.text}`;
}

export const MEMBERS = 32;
export const EVENTS = 200;
export const CHECKS = 80;
export const LINES = 300;

const roles: Role[] = ["implementer", "implementer", "implementer", "reviewer", "integrator", "checkpointer"];
const statuses: Status[] = ["working", "working", "idle", "blocked", "working", "done", "idle", "unknown"];
const agents = ["claude", "codex", "opencode", "muse"];
const words = ["parser", "journal", "socket", "daemon", "web", "lexer", "cache", "router", "store", "inbox"];

const pad = (n: number, w = 2) => String(n).padStart(w, "0");
const t0 = Date.UTC(2026, 9, 26, 9, 0, 0);
const iso = (sec: number) => new Date(t0 + sec * 1000).toISOString();
const clockOf = (sec: number) => {
  const d = new Date(t0 + sec * 1000);
  return `${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}:${pad(d.getUTCSeconds())}`;
};

function member(i: number): Member {
  const role = roles[i % roles.length]!;
  const w = words[i % words.length]!;
  return {
    pane: `w${1 + (i >> 3)}:p${i}`,
    title: `${role.slice(0, 2)}-${w}-${i}`,
    role,
    status: statuses[i % statuses.length]!,
    task: i % 3 === 0 ? null : `Port the ${w} unit and its tests (slice ${1 + (i % 5)})`,
    sub: [agents[i % agents.length], `feat/${w}-${i}`].join(" · "),
    tab: `${w}-${role}`,
    term: `✳ ${w}: running go test ./internal/${w}/... (${i})`,
  };
}

/** Event number n; event numbers grow with time. */
function event(n: number, members: Member[]): Event {
  const m = members[(n * 7) % members.length]!;
  const at = iso(n * 3);
  const clock = clockOf(n * 3);
  if (n % 11 === 0)
    return { at, clock, source: "web", kind: "web", label: "web", pane: null, who: null, status: null, text: `sent keys to ${m.pane}` };
  if (n % 3 === 0)
    return { at, clock, source: "herdr", kind: "pane_focused", label: "focused", pane: m.pane, who: m.title, status: null, text: null };
  const status = statuses[n % statuses.length]!;
  return { at, clock, source: "herdr", kind: "pane.agent_status_changed", label: "agent status changed", pane: m.pane, who: m.title, status, text: null };
}

function check(i: number): Check {
  return {
    id: i % 9 === 4 ? "" : `C${1 + (i >> 3)}.${i % 8}`,
    text: `${words[i % words.length]} contract ${i}: the ${words[(i + 3) % words.length]} keeps its order`,
    done: i % 3 !== 2,
    skipped: i % 13 === 5,
  };
}

function line(n: number): Line {
  return { time: iso(n * 17).slice(11, 19), text: `${n % 5 === 0 ? "web: " : ""}step ${n}: ${words[n % words.length]} ${n % 2 ? "accepted" : "sent to review"}` };
}

/** The driver's evolving source of truth; snapshots are cut from it. */
export class World {
  s: State;
  private nextEvent = EVENTS;
  private nextLine = LINES;
  private statusFlip = false;
  private selectAt = -1;

  constructor() {
    const members = Array.from({ length: MEMBERS }, (_, i) => member(i));
    this.s = {
      tab: "team",
      selected: null,
      members,
      events: Array.from({ length: EVENTS }, (_, i) => event(i, members)).reverse(),
      checklist: Array.from({ length: CHECKS }, (_, i) => check(i)),
      log: Array.from({ length: LINES }, (_, i) => line(i)).reverse(),
    };
  }

  /** A fresh copy, as JSON from a poll would be: equal values, new objects. */
  snapshot(): State {
    return JSON.parse(JSON.stringify(this.s)) as State;
  }

  /** The operations behind the toolbar buttons. */
  readonly ops: Record<string, () => void> = {
    poll: () => {},
    status: () => {
      // Member 1 moves between Working and Idle: a lane change.
      this.statusFlip = !this.statusFlip;
      this.s.members[1]!.status = this.statusFlip ? "idle" : "working";
    },
    select: () => {
      this.selectAt = (this.selectAt + 1) % this.s.members.length;
      this.s.selected = this.s.members[this.selectAt]!.pane;
    },
    team: () => void (this.s.tab = "team"),
    activity: () => void (this.s.tab = "activity"),
    checklist: () => void (this.s.tab = "checklist"),
    log: () => void (this.s.tab = "log"),
    event: () => this.addEvents(1),
    burst: () => this.addEvents(20),
    toggle: () => {
      const c = this.s.checklist[40]!;
      c.done = !c.done;
    },
    logline: () => {
      this.s.log.unshift(line(this.nextLine++));
    },
  };

  private addEvents(n: number): void {
    const fresh: Event[] = [];
    for (let i = 0; i < n; i++) fresh.unshift(event(this.nextEvent++, this.s.members));
    this.s.events = fresh.concat(this.s.events).slice(0, EVENTS);
  }
}
