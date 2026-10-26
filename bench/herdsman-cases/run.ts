// Runs the herdsman-cases benchmark in Chrome, the way js-framework-benchmark
// measures CPU tests (webdriver-ts/src/forkedBenchmarkRunnerPuppeteer.ts and
// puppeteerAccess.ts): a fresh page per sample, warmups, CPU throttling where
// set, a trace around a forced GC and the measured click, and the duration
// from trace.ts. Apps are interleaved within each iteration, rotating which
// goes first. Before timing, every app must render what herdsman-patch renders.
//
//   bun run.ts [--iterations 15] [--apps a,b] [--benchmarks x,y] [--force] [--vsync] [--traces]
//              [--timeout 60] [--max-load <cores/2>]
//
// Log lines carry the wall clock, the elapsed time, the load average and an
// ETA. A sample that takes longer than --timeout seconds fails, and later
// samples of that app and benchmark are skipped. Each iteration starts only
// once the 1-minute load average is below --max-load.
//
// Chrome runs headless. The official runner runs headed, where a click's
// commit follows its work at once; headless Chrome waits for its fixed frame
// clock instead, padding every update to the next tick (3 ms of work read as
// 13 ms). --disable-frame-rate-limit restores the headed behavior, so it is on
// unless --vsync is given.

import { existsSync, mkdirSync, readdirSync, readFileSync, unlinkSync, writeFileSync } from "node:fs";
import { cpus, loadavg } from "node:os";
import { join } from "node:path";
import puppeteer, { type Browser, type Page } from "puppeteer-core";
import type { Size } from "./build.ts";
import { distDir, serve } from "./serve.ts";
import { computeDuration, type Duration, NotOneClick } from "./trace.ts";

const CHROME = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const REFERENCE = "herdsman-patch";
const CONTROL = "herdsman-patch-control";
const resultsDir = join(import.meta.dir, "results");
const started = Date.now();

/** Every line carries the wall clock and the time since the run began. */
function log(msg: string): void {
  const s = Math.round((Date.now() - started) / 1000);
  const elapsed = `${Math.floor(s / 60)}m${String(s % 60).padStart(2, "0")}s`;
  console.log(`${new Date().toTimeString().slice(0, 8)} +${elapsed.padStart(7)} ${msg}`);
}

const minutes = (ms: number) => `${Math.round(ms / 60000)} min`;

/** Rejects after ms, so a hung page or trace cannot stall the run. */
function within<T>(ms: number, what: string, p: Promise<T>): Promise<T> {
  let timer: Timer;
  const limit = new Promise<never>((_, reject) => (timer = setTimeout(() => reject(new Error(`timeout after ${ms / 1000} s: ${what}`)), ms)));
  return Promise.race([p, limit]).finally(() => clearTimeout(timer));
}

/** Waits until the 1-minute load average drops below max, logging while it waits. */
async function quiet(max: number): Promise<void> {
  for (let waited = 0; ; waited += 10) {
    const load = loadavg()[0]!;
    if (load < max) return;
    if (waited % 60 === 0) log(`load ${load.toFixed(1)} >= ${max}; waiting for a quiet machine`);
    await wait(10000);
  }
}

interface Benchmark {
  id: string;
  /** Clicks before the warmups, each waited for. */
  setup: string[];
  warmup?: { op: string; count: number };
  /** The measured click. */
  measure: string;
  throttle?: number;
}

const benchmarks: Benchmark[] = [
  { id: "create", setup: [], measure: "board" },
  { id: "poll", setup: ["board"], warmup: { op: "poll", count: 5 }, measure: "poll", throttle: 4 },
  { id: "status", setup: ["board"], warmup: { op: "status", count: 4 }, measure: "status", throttle: 4 },
  { id: "select", setup: ["board"], warmup: { op: "select", count: 5 }, measure: "select", throttle: 4 },
  { id: "tab-activity", setup: ["board"], measure: "activity" },
  { id: "event", setup: ["board", "activity"], warmup: { op: "event", count: 5 }, measure: "event", throttle: 4 },
  { id: "burst", setup: ["board", "activity"], warmup: { op: "burst", count: 3 }, measure: "burst", throttle: 4 },
  { id: "tab-checklist", setup: ["board"], measure: "checklist" },
  { id: "toggle", setup: ["board", "checklist"], warmup: { op: "toggle", count: 4 }, measure: "toggle", throttle: 4 },
  { id: "tab-log", setup: ["board"], measure: "log" },
  { id: "logline", setup: ["board", "log"], warmup: { op: "logline", count: 5 }, measure: "logline", throttle: 4 },
  { id: "tab-team", setup: ["board", "activity"], measure: "team" },
];

/** The correctness gate's operations; "card" clicks the first .card. */
const gateSteps = ["board", "poll", "status", "select", "card", "activity", "event", "burst", "checklist", "toggle", "log", "logline", "team", "status", "poll"];

// As the official runner's categories.
const traceCategories = ["disabled-by-default-v8.cpu_profiler", "blink.user_timing", "devtools.timeline", "disabled-by-default-devtools.timeline"];

// --- options

interface Options {
  iterations: number;
  apps: string[];
  benchmarks: Benchmark[];
  force: boolean;
  vsync: boolean;
  traces: boolean;
  /** Seconds one sample may take, setup and trace included. */
  timeout: number;
  /** Each iteration waits until the 1-minute load average is below this. */
  maxLoad: number;
}

function parseArgs(argv: string[]): Options {
  const value = (flag: string) => {
    const i = argv.indexOf(flag);
    return i >= 0 ? argv[i + 1] : undefined;
  };
  const built = readdirSync(distDir).filter((n) => existsSync(join(distDir, n, "index.html")));
  const apps = value("--apps")?.split(",") ?? built.sort((a, b) => order(a) - order(b) || a.localeCompare(b));
  for (const a of apps) if (!built.includes(a)) throw new Error(`no dist/${a}; run bun build.ts first`);
  const ids = value("--benchmarks")?.split(",");
  const picked = ids ? ids.map((id) => benchmarks.find((b) => b.id === id) ?? fail(`no benchmark ${id}`)) : benchmarks;
  return {
    iterations: Number(value("--iterations") ?? 15),
    apps,
    benchmarks: picked,
    force: argv.includes("--force"),
    vsync: argv.includes("--vsync"),
    traces: argv.includes("--traces"),
    timeout: Number(value("--timeout") ?? 60),
    maxLoad: Number(value("--max-load") ?? cpus().length / 2),
  };
}

const order = (a: string) => (a === REFERENCE ? 0 : a === CONTROL ? 1 : 2);
function fail(msg: string): never {
  throw new Error(msg);
}

// --- browser, as puppeteerAccess.ts startBrowser

async function startBrowser(vsync: boolean): Promise<Browser> {
  const args = [
    "--window-size=1280,800",
    "--js-flags=--expose-gc",
    "--no-default-browser-check",
    "--disable-sync",
    "--no-first-run",
    "--ash-no-nudges",
    "--disable-extensions",
    "--disable-features=Translate,PrivacySandboxSettings4,IPH_SidePanelGenericMenuFeature",
    "--headless=new",
  ];
  if (!vsync) args.push("--disable-frame-rate-limit");
  return puppeteer.launch({ headless: false, executablePath: CHROME, args, dumpio: false, defaultViewport: { width: 1280, height: 800 } });
}

const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));

interface Opened {
  page: Page;
  errors: string[];
  close(): Promise<void>;
}

/** A fresh browser context and page with the app loaded. */
async function open(browser: Browser, base: string, app: string): Promise<Opened> {
  const ctx = await browser.createBrowserContext();
  const page = await ctx.newPage();
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  page.on("console", (m) => void (m.type() === "error" && errors.push(m.text())));
  // Every file is local, so the page is ready at load; networkidle0 would add 500 ms per sample.
  await page.goto(`${base}/${app}/index.html`, { waitUntil: "load" });
  await page.waitForSelector("#board");
  return { page, errors, close: () => ctx.close() };
}

async function click(page: Page, op: string): Promise<void> {
  const sel = op === "card" ? "#main .card" : `#${op}`;
  const el = (await page.$(sel)) ?? fail(`no ${sel}`);
  await el.click();
  await el.dispose();
}

/** Waits for a click's work to land: two animation frames, then a task. Never while tracing. */
async function settle(page: Page): Promise<void> {
  await page.evaluate(
    () => new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(r, 0)))),
  );
}

async function clickSettled(page: Page, op: string): Promise<void> {
  await click(page, op);
  await settle(page);
}

async function prepare(page: Page, b: Benchmark): Promise<void> {
  for (const op of b.setup) await clickSettled(page, op);
  for (let i = 0; i < (b.warmup?.count ?? 0); i++) await clickSettled(page, b.warmup!.op);
}

// --- correctness gate and node identity

async function canons(browser: Browser, base: string, app: string): Promise<{ canon: string[]; errors: string[] }> {
  const o = await open(browser, base, app);
  try {
    const canon: string[] = [];
    for (const op of gateSteps) {
      await clickSettled(o.page, op);
      canon.push(await o.page.evaluate(() => window.__bench.canon()));
    }
    return { canon, errors: o.errors };
  } finally {
    await o.close();
  }
}

function firstDifference(want: string[], got: string[]): string | null {
  for (let s = 0; s < want.length; s++) {
    if (want[s] === got[s]) continue;
    const w = want[s]!.split("\n");
    const g = (got[s] ?? "").split("\n");
    let i = 0;
    while (i < w.length && w[i] === g[i]) i++;
    return `after ${gateSteps[s]} (step ${s + 1}), line ${i + 1}: expected ${JSON.stringify(w[i] ?? "<end>")}, got ${JSON.stringify(g[i] ?? "<end>")}`;
  }
  return null;
}

interface Kept {
  before: number;
  kept: number;
}

async function nodesKept(browser: Browser, base: string, app: string, b: Benchmark): Promise<Kept> {
  const o = await open(browser, base, app);
  try {
    await prepare(o.page, b);
    const before = await o.page.evaluate(() => window.__bench.tag());
    await clickSettled(o.page, b.measure);
    const kept = await o.page.evaluate(() => window.__bench.kept());
    return { before, kept };
  } finally {
    await o.close();
  }
}

// --- one timed sample, as forkedBenchmarkRunnerPuppeteer.ts runCPUBenchmark

interface Sample extends Duration {
  iteration: number;
}

async function sample(browser: Browser, base: string, app: string, b: Benchmark, tracePath: string | null): Promise<Duration> {
  for (let attempt = 1; ; attempt++) {
    const o = await open(browser, base, app);
    try {
      await prepare(o.page, b);
      if (b.throttle) await o.page.emulateCPUThrottling(b.throttle);
      await o.page.tracing.start({ screenshots: false, categories: traceCategories });
      await wait(50);
      await o.page.evaluate("window.gc({type:'major',execution:'sync',flavor:'last-resort'})");
      await click(o.page, b.measure);
      await wait(100);
      const buf = await o.page.tracing.stop();
      if (b.throttle) await o.page.emulateCPUThrottling(1);
      if (!buf) throw new Error("empty trace");
      if (tracePath) await Bun.write(tracePath, buf);
      const trace = JSON.parse(new TextDecoder().decode(buf)) as { traceEvents: Parameters<typeof computeDuration>[0] };
      if (o.errors.length) throw new Error(`page errors: ${o.errors.join("; ")}`);
      return computeDuration(trace.traceEvents);
    } catch (e) {
      // The official runner repeats a run whose trace has no single click.
      if (!(e instanceof NotOneClick) || attempt >= 3) throw e;
      log(`  repeat ${app} ${b.id}: ${e.message}`);
    } finally {
      await o.close();
    }
  }
}

// --- statistics and report

function median(xs: number[]): number {
  const s = [...xs].sort((a, b) => a - b);
  const m = s.length >> 1;
  return s.length % 2 ? s[m]! : (s[m - 1]! + s[m]!) / 2;
}

const geomean = (xs: number[]) => Math.exp(xs.reduce((a, x) => a + Math.log(x), 0) / xs.length);

interface Results {
  date: string;
  chrome: string;
  mode: string;
  puppeteer: string;
  settings: { iterations: number; apps: string[]; benchmarks: string[]; force: boolean; cpu: string };
  invalid: Record<string, string>;
  sizes: Record<string, Size>;
  kept: Record<string, Record<string, Kept>>;
  samples: Record<string, Record<string, Sample[]>>;
  errors: Record<string, Record<string, string>>;
  medians: Record<string, Record<string, number>>;
  factors: Record<string, Record<string, number>>;
  scores: Record<string, number>;
  aaGap: number | null;
}

function summarize(r: Results, timed: string[]): void {
  for (const b of r.settings.benchmarks) {
    r.medians[b] = {};
    r.factors[b] = {};
    for (const app of timed) {
      const xs = r.samples[app]?.[b]?.map((s) => s.duration) ?? [];
      if (xs.length) r.medians[b]![app] = median(xs);
    }
    const best = Math.min(...Object.values(r.medians[b]!));
    for (const [app, m] of Object.entries(r.medians[b]!)) r.factors[b]![app] = m / best;
  }
  for (const app of timed) {
    const fs = r.settings.benchmarks.map((b) => r.factors[b]?.[app]).filter((f): f is number => f !== undefined);
    if (fs.length === r.settings.benchmarks.length) r.scores[app] = geomean(fs);
  }
  const a = r.scores[REFERENCE];
  const c = r.scores[CONTROL];
  r.aaGap = a && c ? Math.abs(a - c) / Math.min(a, c) : null;
}

function table(head: string[], rows: string[][]): string {
  const w = head.map((h, i) => Math.max(h.length, ...rows.map((r) => r[i]!.length)));
  const line = (cells: string[]) => `| ${cells.map((c, i) => (i ? c.padStart(w[i]!) : c.padEnd(w[i]!))).join(" | ")} |`;
  return [line(head), `| ${w.map((n, i) => (i ? "-".repeat(n - 1) + ":" : "-".repeat(n))).join(" | ")} |`, ...rows.map(line)].join("\n");
}

function report(r: Results, timed: string[]): string {
  const out: string[] = [];
  out.push(`# herdsman-cases, ${r.date}`, "");
  out.push(
    `Chrome ${r.chrome} (${r.mode}), puppeteer-core ${r.puppeteer}, ${r.settings.iterations} iterations, ${r.settings.cpu}.`,
    "Duration: click dispatch to the end of the first commit after the click's last script, timer, rAF or layout work (js-framework-benchmark's method). Median ms, factor to the best median in parentheses.",
    "",
  );
  for (const [app, why] of Object.entries(r.invalid)) out.push(`**${app} INVALID**${r.settings.force ? " (timed anyway, --force)" : " (not timed)"}: ${why}`, "");

  const rows = r.settings.benchmarks.map((b) => {
    const throttle = benchmarks.find((x) => x.id === b)!.throttle;
    return [
      `${b}${throttle ? ` (${throttle}x)` : ""}`,
      ...timed.map((app) => {
        const m = r.medians[b]?.[app];
        if (m === undefined) return r.errors[app]?.[b] ? "error" : "-";
        return `${m.toFixed(2)} (${r.factors[b]![app]!.toFixed(2)})`;
      }),
    ];
  });
  rows.push(["geometric mean", ...timed.map((app) => (r.scores[app] ? r.scores[app]!.toFixed(3) : "-"))]);
  out.push(table(["benchmark", ...timed], rows), "");
  if (r.aaGap !== null)
    out.push(`Noise floor: ${REFERENCE} against ${CONTROL}, the same build twice, differ by ${(r.aaGap * 100).toFixed(1)}% in score. Gaps below that are not results.`, "");

  const keptApps = Object.keys(r.kept);
  if (keptApps.length) {
    out.push("## Nodes kept", "", "Elements under #main that survive the measured click, of those before it.", "");
    out.push(
      table(
        ["benchmark", ...keptApps],
        r.settings.benchmarks.map((b) => [b, ...keptApps.map((app) => (r.kept[app]?.[b] ? `${r.kept[app]![b]!.kept}/${r.kept[app]![b]!.before}` : "-"))]),
      ),
      "",
    );
  }

  const sized = r.settings.apps.filter((a) => r.sizes[a]);
  if (sized.length) {
    out.push("## Size", "", "All JavaScript of the built app, bytes.", "");
    out.push(table(["app", "raw", "gzip"], sized.map((a) => [a, String(r.sizes[a]!.raw), String(r.sizes[a]!.gzip)])), "");
  }

  const errs = Object.entries(r.errors).flatMap(([app, bs]) => Object.entries(bs).map(([b, e]) => `- ${app} ${b}: ${e}`));
  if (errs.length) out.push("## Errors", "", ...errs, "");
  return out.join("\n");
}

// --- main

/** One run at a time: two runs on one machine skew each other's timings. */
function lock(): void {
  mkdirSync(resultsDir, { recursive: true });
  const file = join(resultsDir, ".lock");
  if (existsSync(file)) {
    const pid = Number(readFileSync(file, "utf8"));
    let alive = false;
    try {
      process.kill(pid, 0);
      alive = true;
    } catch {
      // A stale lock from a run that died.
    }
    if (alive) fail(`another run (pid ${pid}) is running; stop it or wait (lock: ${file})`);
  }
  writeFileSync(file, String(process.pid));
  const release = () => {
    try {
      if (readFileSync(file, "utf8") === String(process.pid)) unlinkSync(file);
    } catch {
      // Already gone.
    }
  };
  process.on("exit", release);
  for (const sig of ["SIGINT", "SIGTERM", "SIGHUP"] as const)
    process.on(sig, () => {
      release();
      process.exit(130);
    });
}

async function main(): Promise<void> {
  const opts = parseArgs(process.argv.slice(2));
  lock();
  if (!existsSync(join(distDir, REFERENCE, "index.html"))) throw new Error(`the gate needs dist/${REFERENCE}; run bun build.ts ${REFERENCE}`);
  const server = serve();
  const base = server.url.origin;
  const sizesFile = join(distDir, "sizes.json");
  const date = new Date().toISOString();
  const r: Results = {
    date,
    chrome: "",
    mode: opts.vsync ? "headless=new" : "headless=new, --disable-frame-rate-limit",
    puppeteer: (await Bun.file(join(import.meta.dir, "node_modules/puppeteer-core/package.json")).json()).version,
    settings: {
      iterations: opts.iterations,
      apps: opts.apps,
      benchmarks: opts.benchmarks.map((b) => b.id),
      force: opts.force,
      cpu: "4x CPU throttling where marked",
    },
    invalid: {},
    sizes: existsSync(sizesFile) ? await Bun.file(sizesFile).json() : {},
    kept: {},
    samples: {},
    errors: {},
    medians: {},
    factors: {},
    scores: {},
    aaGap: null,
  };

  // Gate and node identity, outside timing.
  let browser = await startBrowser(opts.vsync);
  r.chrome = (await browser.version()).replace(/^.*\//, "");
  log(`Chrome ${r.chrome} (${r.mode}), serving dist/ at ${base}`);
  log(`${opts.apps.length} apps, ${opts.benchmarks.length} benchmarks, ${opts.iterations} iterations; sample timeout ${opts.timeout} s, max load ${opts.maxLoad}`);
  const reference = await canons(browser, base, REFERENCE);
  if (reference.errors.length) throw new Error(`${REFERENCE} logged errors: ${reference.errors.join("; ")}`);
  for (const app of opts.apps) {
    let why: string | null;
    try {
      const got = await within(opts.timeout * 1000, `gate ${app}`, canons(browser, base, app));
      why = got.errors.length ? `page errors: ${got.errors.join("; ")}` : firstDifference(reference.canon, got.canon);
    } catch (e) {
      why = `gate failed: ${(e as Error).message}`;
    }
    if (why) r.invalid[app] = why;
    log(`gate ${app.padEnd(16)} ${why ? `INVALID ${why}` : "ok"}`);
  }
  const timed = opts.apps.filter((a) => opts.force || !r.invalid[a]);
  for (const app of timed) {
    r.kept[app] = {};
    for (const b of opts.benchmarks) r.kept[app]![b.id] = await within(opts.timeout * 1000, `nodes kept ${app} ${b.id}`, nodesKept(browser, base, app, b));
  }
  await browser.close();
  log(`nodes kept measured; timing ${timed.length} apps`);

  // Timing: per iteration and benchmark, a fresh browser; apps rotate.
  const stamp = date.replace(/[:.]/g, "-");
  const traceDir = join(resultsDir, "traces", stamp);
  if (opts.traces) mkdirSync(traceDir, { recursive: true });
  const rows = opts.iterations * opts.benchmarks.length;
  const timingStart = Date.now();
  let done = 0;
  for (let it = 0; it < opts.iterations; it++) {
    await quiet(opts.maxLoad);
    for (const [bi, b] of opts.benchmarks.entries()) {
      browser = await startBrowser(opts.vsync);
      const shift = (it + bi) % timed.length;
      const round = [...timed.slice(shift), ...timed.slice(0, shift)];
      const line: string[] = [];
      for (const app of round) {
        if (r.errors[app]?.[b.id]) continue;
        try {
          const d = await within(
            opts.timeout * 1000,
            `${app} ${b.id}`,
            sample(browser, base, app, b, opts.traces ? join(traceDir, `${app}_${b.id}_${it}.json`) : null),
          );
          ((r.samples[app] ??= {})[b.id] ??= []).push({ iteration: it, ...d });
          line.push(`${app} ${d.duration.toFixed(2)}`);
        } catch (e) {
          (r.errors[app] ??= {})[b.id] = (e as Error).message;
          line.push(`${app} error`);
          log(`  ${app} ${b.id} failed: ${(e as Error).message}; later samples of it are skipped`);
          // A timed-out page may hold the browser; start the rest of the row on a fresh one.
          await browser.close().catch(() => {});
          browser = await startBrowser(opts.vsync);
        }
      }
      await browser.close().catch(() => {});
      done++;
      const eta = ((Date.now() - timingStart) / done) * (rows - done);
      log(`[${it + 1}/${opts.iterations}] ${b.id.padEnd(13)} ${line.join(", ")}  (${done}/${rows}, load ${loadavg()[0]!.toFixed(1)}, ETA ${minutes(eta)})`);
    }
  }
  server.stop(true);

  summarize(r, timed);
  mkdirSync(resultsDir, { recursive: true });
  const md = report(r, timed);
  await Bun.write(join(resultsDir, `${stamp}.json`), JSON.stringify(r, null, 2) + "\n");
  await Bun.write(join(resultsDir, `${stamp}.md`), md);
  console.log("\n" + md);
  log(`results/${stamp}.json, results/${stamp}.md`);
}

await main();
