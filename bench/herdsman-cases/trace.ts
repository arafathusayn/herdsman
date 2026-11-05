// The duration of one measured click, from a Chrome trace. A port of
// computeResultsCPU and extractRelevantEvents in js-framework-benchmark's
// webdriver-ts/src/timeline.ts (krausest/js-framework-benchmark, ca8715d):
// from the start of the click's event dispatch to the end of the first
// compositor commit after the last script, timer, animation frame or layout
// event that follows the click, all on the click's process. Ramda is replaced
// by array methods; the logic, filters and the rAF delay correction are kept.

type Kind =
  | "startLogicEvent"
  | "click"
  | "mousedown"
  | "pointerup"
  | "layout"
  | "functioncall"
  | "hittest"
  | "commit"
  | "paint"
  | "fireAnimationFrame"
  | "timerFire"
  | "requestAnimationFrame";

interface Timing {
  type: Kind;
  ts: number;
  dur: number;
  end: number;
  pid: number;
}

interface TraceEvent {
  name: string;
  ph: string;
  ts: number;
  dur?: number;
  pid: number;
  args?: { data?: { type?: string } };
}

export interface Duration {
  /** Milliseconds, click dispatch start to the commit's end, less any rAF delay correction. */
  duration: number;
  tsStart: number;
  tsEnd: number;
  layouts: number;
  numberCommits: number;
  maxDeltaBetweenCommits: number;
  rafLongDelay: number;
  droppedNonMainProcessCommitEvents: boolean;
  droppedNonMainProcessOtherEvents: boolean;
}

/** Thrown when the trace holds no single click; the official runner repeats the run. */
export class NotOneClick extends Error {}

const durationOf = (e: TraceEvent) => +(e.dur ?? 0);

function relevantEvents(entries: TraceEvent[], startLogicEvent: string): Timing[] {
  const out: Timing[] = [];
  const push = (type: Kind, e: TraceEvent, dur = durationOf(e)) => out.push({ type, ts: +e.ts, dur, end: +e.ts + dur, pid: e.pid });
  for (const e of entries) {
    if (e.name === "EventDispatch") {
      const t = e.args?.data?.type;
      if (t === startLogicEvent) push("startLogicEvent", e);
      if (t === "click") push("click", e);
      else if (t === "mousedown") push("mousedown", e);
      else if (t === "pointerup") push("pointerup", e);
    } else if (e.name === "Layout" && e.ph === "X") push("layout", e);
    else if (e.name === "FunctionCall" && e.ph === "X") push("functioncall", e);
    else if (e.name === "HitTest" && e.ph === "X") push("hittest", e);
    else if (e.name === "Commit" && e.ph === "X") push("commit", e);
    else if (e.name === "Paint" && e.ph === "X") push("paint", e);
    else if (e.name === "FireAnimationFrame" && e.ph === "X") push("fireAnimationFrame", e);
    else if (e.name === "TimerFire" && e.ph === "X") push("timerFire", e, 0);
    else if (e.name === "RequestAnimationFrame") push("requestAnimationFrame", e, 0);
  }
  return out;
}

const ofType =
  (...types: string[]) =>
  (e: Timing) =>
    types.includes(e.type);

export function computeDuration(entries: TraceEvent[], startLogicEventName = "click"): Duration {
  // Stable sort by end, as R.sortBy.
  const events = relevantEvents(entries, startLogicEventName).sort((a, b) => a.end - b.end);

  const mousedowns = events.filter(ofType("mousedown"));
  if (mousedowns.length > 1) throw new Error("at most one mousedown event is expected");

  const clicks = events.filter(ofType("startLogicEvent"));
  if (clicks.length !== 1) throw new NotOneClick(`exactly one click event is expected, got ${clicks.length}`);
  const click = clicks[0]!;

  // Only events after the click's dispatch, on the click's process.
  const pid = click.pid;
  const during = events.filter((e) => e.ts > click.end || e.type === "click");
  const onMain = during.filter((e) => e.pid === pid);
  const dropped = events.filter((e) => e.pid !== pid);
  const droppedNonMainProcessCommitEvents = onMain.length !== during.length && dropped.some((e) => e.type === "commit");
  const droppedNonMainProcessOtherEvents = onMain.length !== during.length && dropped.some((e) => e.type !== "commit");

  // The commit after the last script, timer, rAF or layout event.
  const startFromEvent = onMain.filter(ofType(startLogicEventName, "fireAnimationFrame", "timerFire", "layout", "functioncall")).at(-1);
  if (!startFromEvent) throw new Error("no events after the click");
  const commits = onMain.filter(ofType("commit"));
  let commit = commits.find((e) => e.ts > startFromEvent.end);
  if (!commit) {
    if (commits.length === 0) throw new Error("no commit event after the click");
    commit = commits.at(-1)!;
  }
  const maxDeltaBetweenCommits = (commits.at(-1)!.ts - commits[0]!.ts) / 1000;
  let duration = (commit.end - click.ts) / 1000;
  const layouts = onMain.filter(ofType("layout"));

  // The official correction for a long wait between a requested and a fired animation frame.
  const rafsWithinClick = events.filter((e) => e.type === "requestAnimationFrame" && e.ts >= click.ts && e.ts <= click.end);
  const fafs = events.filter((e) => e.type === "fireAnimationFrame" && e.ts >= click.ts && e.ts < commit.ts);
  let rafLongDelay = 0;
  if (rafsWithinClick.length > 0 && fafs.length > 0) {
    const waitDelay = (fafs[0]!.ts - click.end) / 1000;
    if (rafsWithinClick.length === 1 && fafs.length === 1) {
      if (waitDelay > 16 && !layouts.some((e) => e.ts < fafs[0]!.ts)) {
        rafLongDelay = waitDelay - 16;
        duration -= rafLongDelay;
      }
    } else if (fafs.length === 1) {
      throw new Error("one fired animation frame, but inconsistent requested animation frames");
    }
  }

  return {
    duration,
    tsStart: click.ts,
    tsEnd: commit.end,
    layouts: layouts.length,
    numberCommits: commits.length,
    maxDeltaBetweenCommits,
    rafLongDelay,
    droppedNonMainProcessCommitEvents,
    droppedNonMainProcessOtherEvents,
  };
}
