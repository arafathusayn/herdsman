// Call trees and flame graphs from Chrome CPU profiles (the .cpuprofile
// format the DevTools profiler records and loads), for jfb/profile.ts.
//
// The tree merges the profiles' stacks by function and call site; a sample
// counts for the time until the next sample, as DevTools counts it. (idle)
// is left out, so the root's total is the time the main thread was busy.

export type CpuProfile = {
  nodes: { id: number; callFrame: { functionName: string; url: string; lineNumber: number; columnNumber: number }; children?: number[] }[];
  samples: number[];
  timeDeltas: number[];
  startTime: number;
  endTime: number;
};

export type Frame = { name: string; native: boolean; self: number; total: number; kids: Map<string, Frame> };

const frame = (name: string, native: boolean): Frame => ({ name, native, self: 0, total: 0, kids: new Map() });

export function callTree(profiles: CpuProfile[]): Frame {
  const root = frame("(all)", true);
  for (const p of profiles) {
    const byId = new Map(p.nodes.map((n) => [n.id, n]));
    const parent = new Map<number, number>();
    for (const n of p.nodes) for (const c of n.children ?? []) parent.set(c, n.id);
    p.samples.forEach((id, i) => {
      const ms = (p.timeDeltas[i + 1] ?? 0) / 1000;
      const stack: number[] = [];
      for (let at: number | undefined = id; at !== undefined; at = parent.get(at)) stack.push(at);
      stack.pop(); // (root)
      if (stack.length === 1 && byId.get(stack[0]!)!.callFrame.functionName === "(idle)") return;
      let f = root;
      f.total += ms;
      for (let s = stack.length - 1; s >= 0; s--) {
        const { functionName, url, lineNumber } = byId.get(stack[s]!)!.callFrame;
        const name = functionName || "(anonymous)";
        const key = `${name}:${lineNumber}`;
        let k = f.kids.get(key);
        if (k === undefined) f.kids.set(key, (k = frame(lineNumber >= 0 && url ? `${name}:${lineNumber + 1}` : name, !url)));
        k.total += ms;
        f = k;
      }
      f.self += ms;
    });
  }
  return root;
}

/** Self and total time by function; a recursive function counts once per stack. */
export function byFunction(root: Frame): { name: string; self: number; total: number }[] {
  const out = new Map<string, { name: string; self: number; total: number }>();
  const walk = (f: Frame, open: Set<string>): void => {
    for (const k of f.kids.values()) {
      let r = out.get(k.name);
      if (r === undefined) out.set(k.name, (r = { name: k.name, self: 0, total: 0 }));
      r.self += k.self;
      const outer = open.has(k.name);
      if (!outer) r.total += k.total, open.add(k.name);
      walk(k, open);
      if (!outer) open.delete(k.name);
    }
  };
  walk(root, new Set());
  return [...out.values()];
}

const esc = (s: string): string => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");

function color(f: Frame): string {
  if (f.name.startsWith("(")) return "#b8b8b8"; // (program), (garbage collector)
  if (f.native) return "#7fa7d9"; // DOM and other native calls
  let h = 0;
  for (let i = 0; i < f.name.length; i++) h = (h * 31 + f.name.charCodeAt(i)) | 0;
  return `hsl(${20 + (Math.abs(h) % 35)}, 85%, ${58 + (Math.abs(h >> 8) % 14)}%)`;
}

/** A top-down flame graph (root on top, as in DevTools), children by total time. */
export function flameSvg(root: Frame, title: string, width = 1400): string {
  const row = 17;
  const rects: string[] = [];
  let depth = 0;
  const scale = (width - 20) / (root.total || 1);
  const draw = (f: Frame, x: number, d: number): void => {
    const w = f.total * scale;
    if (w < 0.4) return;
    depth = Math.max(depth, d);
    const y = 40 + d * row;
    const pct = ((f.total / (root.total || 1)) * 100).toFixed(1);
    const label = w > 28 ? esc(f.name.slice(0, Math.floor((w - 6) / 6.6))) : "";
    rects.push(
      `<g><title>${esc(f.name)}: ${f.total.toFixed(2)} ms total (${pct}%), ${f.self.toFixed(2)} ms self</title>` +
        `<rect x="${(10 + x).toFixed(1)}" y="${y}" width="${Math.max(w - 0.5, 0.3).toFixed(1)}" height="${row - 1}" fill="${color(f)}" rx="1.5"/>` +
        (label ? `<text x="${(13 + x).toFixed(1)}" y="${y + 12}">${label}</text>` : "") +
        `</g>`,
    );
    let cx = x;
    for (const k of [...f.kids.values()].sort((a, b) => b.total - a.total)) (draw(k, cx, d + 1), (cx += k.total * scale));
  };
  draw(root, 0, 0);
  const height = 60 + (depth + 1) * row;
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}" font-family="ui-monospace,monospace" font-size="11">` +
    `<rect width="100%" height="100%" fill="#fff"/><text x="10" y="22" font-size="14" font-weight="bold">${esc(title)}</text>` +
    `<text x="10" y="${height - 8}" fill="#666">${root.total.toFixed(1)} ms busy · orange: JS · blue: native and DOM calls · gray: (program) and GC · hover for times</text>` +
    rects.join("") +
    `</svg>`
  );
}

/** Samples by source line inside the named functions (DevTools' line-level ticks). */
export function hotLines(profiles: CpuProfile[], names: string[]): { fn: string; line: number; ticks: number }[] {
  const out = new Map<string, { fn: string; line: number; ticks: number }>();
  for (const p of profiles)
    for (const n of p.nodes as (CpuProfile["nodes"][number] & { positionTicks?: { line: number; ticks: number }[] })[]) {
      const fn = n.callFrame.functionName || "(anonymous)";
      if (!names.includes(fn)) continue;
      for (const t of n.positionTicks ?? []) {
        const k = `${fn}:${t.line}`;
        const r = out.get(k);
        if (r) r.ticks += t.ticks;
        else out.set(k, { fn, line: t.line, ticks: t.ticks });
      }
    }
  return [...out.values()].sort((a, b) => b.ticks - a.ticks);
}

type TraceEvent = { name: string; ph: string; ts: number; pid: number; tid: number; id?: string; args?: Any };
// deno-lint-ignore no-explicit-any
type Any = any;

/**
 * The CPU profile a trace recorded (its disabled-by-default-v8.cpu_profiler
 * Profile and ProfileChunk events, which the Performance panel draws as the
 * JS flame chart), cut to the samples between from and to (trace µs).
 */
export function fromTrace(events: TraceEvent[], from: number, to: number): CpuProfile[] {
  const out: CpuProfile[] = [];
  for (const start of events.filter((e) => e.name === "Profile")) {
    const nodes = new Map<number, CpuProfile["nodes"][number] & { parent?: number }>();
    const samples: number[] = [];
    const deltas: number[] = [];
    let t = start.args.data.startTime as number;
    for (const c of events) {
      if (c.name !== "ProfileChunk" || c.id !== start.id || c.pid !== start.pid) continue;
      const d = c.args.data;
      for (const n of d.cpuProfile?.nodes ?? []) nodes.set(n.id, { ...n, children: [] });
      const s: number[] = d.cpuProfile?.samples ?? [];
      const td: number[] = d.timeDeltas ?? [];
      for (let i = 0; i < s.length; i++) {
        t += td[i] ?? 0;
        if (t < from || t > to) continue;
        samples.push(s[i]!);
        deltas.push(td[i] ?? 0);
      }
    }
    for (const n of nodes.values()) if (n.parent !== undefined) nodes.get(n.parent)?.children!.push(n.id);
    if (samples.length === 0) continue;
    // callTree counts a sample until the next one: shift the deltas by one.
    out.push({ nodes: [...nodes.values()], samples, timeDeltas: [0, ...deltas.slice(1), 0], startTime: from, endTime: to });
  }
  return out;
}
