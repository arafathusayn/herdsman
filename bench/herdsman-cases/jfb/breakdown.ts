// Where one measured click's time goes, per entry: the click's main-thread
// events in the trace summed by kind (script, style, layout, paint and
// commit, other). For telling a runtime's own cost from the browser's work.
//
//   bun jfb/breakdown.ts [entries] [op] [throttle] [--names]
//
// op: run (create 1,000 rows) | runlots | update | select | swaprows | clear.
// --names lists the top-level events by name instead of by kind.

import { join } from "node:path";
import puppeteer from "puppeteer-core";
import { findChrome } from "../chrome.ts";
import { root } from "./prepare.ts";

const entries = (process.argv[2] ?? "vanillajs,re,herdsman-react").split(",");
const op = process.argv[3] ?? "run";
const throttle = Number(process.argv[4] ?? 1);
const byName = process.argv.includes("--names");
const server = Bun.serve({
  port: 0,
  async fetch(req) {
    const f = Bun.file(join(root, decodeURIComponent(new URL(req.url).pathname)));
    return (await f.exists()) ? new Response(f) : new Response("", { status: 404 });
  },
});
const browser = await puppeteer.launch({ executablePath: findChrome(), headless: true, args: ["--no-sandbox", "--js-flags=--expose-gc"] });
const KINDS: Record<string, string> = {
  FunctionCall: "script", EventDispatch: "script", EvaluateScript: "script", "v8.callFunction": "script", RunMicrotasks: "script", TimerFire: "script",
  UpdateLayoutTree: "style", RecalculateStyles: "style", ParseAuthorStyleSheet: "style",
  Layout: "layout", PrePaint: "paint", Paint: "paint", PaintImage: "paint", CompositeLayers: "paint", Layerize: "paint", Commit: "paint",
  MajorGC: "gc", MinorGC: "gc", "V8.GCScavenger": "gc", "BlinkGC.AtomicPhase": "gc",
};
for (const e of entries) {
  const totals: Record<string, number> = {};
  for (let rep = 0; rep < 5; rep++) {
    const page = await browser.newPage();
    const pkg = Bun.file(join(root, "frameworks/keyed", e, "package.json"));
    const custom = (await pkg.exists()) ? (await pkg.json())["js-framework-benchmark"]?.customURL : undefined;
    await page.goto(`http://127.0.0.1:${server.port}/frameworks/keyed/${e}${(custom ?? "/").replace(/\/?$/, "/")}index.html`);
    await page.waitForSelector("#run");
    for (let i = 0; i < 5; i++) {
      await page.click("#run");
      await page.waitForSelector("tbody>tr:nth-of-type(1000)");
      await page.click("#clear");
      await page.waitForFunction(() => !document.querySelector("tbody>tr"));
    }
    if (op !== "run" && op !== "runlots") {
      await page.click("#run");
      await page.waitForSelector("tbody>tr:nth-of-type(1000)");
    }
    if (throttle > 1) await page.emulateCPUThrottling(throttle);
    await page.tracing.start({ categories: ["devtools.timeline", "disabled-by-default-devtools.timeline", "v8"] });
    await page.evaluate("window.gc()");
    const sel = op === "select" ? "tbody>tr:nth-of-type(2)>td:nth-of-type(2)>a" : "#" + op;
    await page.click(sel);
    await new Promise((r) => setTimeout(r, 300));
    const trace = JSON.parse(new TextDecoder().decode((await page.tracing.stop())!)) as { traceEvents: { name: string; ph: string; dur?: number; tid: number; pid: number; ts: number }[] };
    const click = trace.traceEvents.find((x) => x.name === "EventDispatch" && x.ph === "X");
    const main = click ? trace.traceEvents.filter((x) => x.pid === click.pid && x.tid === click.tid && x.ph === "X" && x.ts >= click.ts) : [];
    // Top-level events only: skip any event inside another on the same thread.
    let end = 0;
    for (const x of main.sort((a, b) => a.ts - b.ts)) {
      if (x.ts < end) continue;
      const k = byName ? x.name : KINDS[x.name];
      if (!k) continue;
      totals[k] = (totals[k] ?? 0) + (x.dur ?? 0) / 1000 / 5;
      end = x.ts + (x.dur ?? 0);
    }
    await page.close();
  }
  console.log(e.padEnd(16), Object.entries(totals).sort((a, b) => b[1] - a[1]).slice(0, 10).map(([k, v]) => `${k} ${v.toFixed(1)}`).join("  "));
}
await browser.close();
server.stop();
