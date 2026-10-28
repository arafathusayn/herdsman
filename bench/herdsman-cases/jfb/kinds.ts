// The official click window's main-thread time by kind, from its trace.

// Main-thread time in [from, to] by kind: the union of each kind's event spans.
export function byKind(events: { name: string; ph: string; ts: number; dur?: number; tid: number }[], from: number, to: number): Record<string, number> {
  const KIND: Record<string, string> = {
    FunctionCall: "script", EventDispatch: "script", "v8.callFunction": "script", RunMicrotasks: "script", TimerFire: "script", FireAnimationFrame: "script",
    UpdateLayoutTree: "style", Layout: "layout",
    PrePaint: "paint", Paint: "paint", Layerize: "paint", Commit: "paint",
    MajorGC: "gc", MinorGC: "gc", "V8.GC_SCAVENGER": "gc", "BlinkGC.AtomicPhase": "gc",
  };
  const main = events.find((e) => e.name === "EventDispatch")?.tid;
  const spans: Record<string, [number, number][]> = {};
  for (const e of events) {
    const k = KIND[e.name];
    if (k === undefined || e.ph !== "X" || e.tid !== main) continue;
    const a = Math.max(e.ts, from), b = Math.min(e.ts + (e.dur ?? 0), to);
    if (b > a) (spans[k] ??= []).push([a, b]);
  }
  const out: Record<string, number> = { script: 0, style: 0, layout: 0, paint: 0, gc: 0 };
  for (const [k, xs] of Object.entries(spans)) {
    xs.sort((x, y) => x[0] - y[0]);
    let end = -Infinity;
    for (const [a, b] of xs) {
      if (b <= end) continue;
      out[k]! += (b - Math.max(a, end)) / 1000;
      end = b;
    }
  }
  return out;
}
