// What the official duration measures for one click, per entry: the trace's
// duration and its commit count, largest gap between commits, and layouts
// (from ../trace.ts), median of several samples. For finding a runtime whose
// measured window holds more than its click's work (a second frame).
//
//   bun jfb/window.ts [entries] [benchmark id] [samples]

import { join } from "node:path";
import { computeDuration } from "../trace.ts";
import { root } from "./prepare.ts";
import { benches, startBrowser } from "./run.ts";

const entries = (process.argv[2] ?? "re,herdsman-react").split(",");
const id = process.argv[3] ?? "01_run1k";
const samples = Number(process.argv[4] ?? 5);
const b = benches.find((x) => x.id === id)!;
const server = Bun.serve({
  port: 0,
  async fetch(req) {
    const f = Bun.file(join(root, decodeURIComponent(new URL(req.url).pathname)));
    return (await f.exists()) ? new Response(f) : new Response("", { status: 404 });
  },
});
// The runner's own browser: same flags and viewport.
const browser = await startBrowser();
const cats = ["disabled-by-default-v8.cpu_profiler", "blink.user_timing", "devtools.timeline", "disabled-by-default-devtools.timeline", "v8", "disabled-by-default-v8.compile"];
for (const e of entries) {
  const rows: string[] = [];
  for (let i = 0; i < samples; i++) {
    const page = await browser.newPage();
    const pkg = Bun.file(join(root, "frameworks/keyed", e, "package.json"));
    const custom = (await pkg.exists()) ? (await pkg.json())["js-framework-benchmark"]?.customURL : undefined;
    await page.goto(`http://127.0.0.1:${server.port}/frameworks/keyed/${e}${(custom ?? "/").replace(/\/?$/, "/")}index.html`);
    await page.waitForSelector("#run");
    await b.init(page);
    if (b.throttle) await page.emulateCPUThrottling(b.throttle);
    await page.tracing.start({ categories: cats });
    await new Promise((r) => setTimeout(r, 50));
    await page.evaluate("window.gc({type:'major',execution:'sync',flavor:'last-resort'})");
    await b.run(page);
    await new Promise((r) => setTimeout(r, 100));
    const t = JSON.parse(new TextDecoder().decode((await page.tracing.stop())!));
    const d = computeDuration(t.traceEvents);
    // GC on the page's main thread inside the measured window.
    const ev = t.traceEvents as { name: string; ph: string; ts: number; dur?: number; pid: number }[];
    const gc = ev
      .filter((x) => x.ph === "X" && /GC|Scavenge|MarkCompact/i.test(x.name) && x.ts >= d.tsStart && x.ts <= d.tsEnd)
      .reduce((a, x) => a + (x.dur ?? 0) / 1000, 0);
    // Parsing and compiling inside the window: bytecode the forced GC discarded.
    const compile = ev
      .filter((x) => x.ph === "X" && /Compile|Parse/i.test(x.name) && x.ts >= d.tsStart && x.ts <= d.tsEnd)
      .reduce((a, x) => a + (x.dur ?? 0) / 1000, 0);
    rows.push(`${d.duration.toFixed(1)} ms (gc ${gc.toFixed(1)}, compile ${compile.toFixed(1)})`);
    if (process.argv.includes("--events")) {
      // Every complete event inside the window, summed by name (nested ones too).
      const by = new Map<string, number>();
      for (const x of ev) {
        if (x.ph !== "X" || x.ts < d.tsStart || x.ts > d.tsEnd) continue;
        by.set(x.name, (by.get(x.name) ?? 0) + (x.dur ?? 0) / 1000);
      }
      console.log(`  ${e} sample ${i + 1}: ` + [...by].sort((a, b) => b[1] - a[1]).slice(0, 14).map(([k, v]) => `${k} ${v.toFixed(1)}`).join(", "));
    }
    await page.close();
  }
  console.log(e.padEnd(15), rows.join(" | "));
}
await browser.close();
server.stop();
