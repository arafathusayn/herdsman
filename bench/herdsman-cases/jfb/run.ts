// A replica of js-framework-benchmark's CPU tests (webdriver-ts, puppeteer
// runner, krausest/js-framework-benchmark at prepare.ts's JFB_COMMIT) for the
// entries prepare.ts builds: the official keyed top 5 and our React app on
// React 19 and on herdsman-react.
//
//   bun jfb/run.ts [--iterations 10] [--entries a,b] [--benchmarks 01_run1k,...]
//
// Per sample, as the official runner does: a fresh page, the benchmark's
// setup clicks and warmups (each waited for by its DOM check), CPU throttling
// where the benchmark sets it, a trace around a forced GC and the measured
// click, and the duration from the trace (../trace.ts, a port of the official
// computation). Entries are interleaved within each iteration. Each test's
// median is divided by the best median of that test in this run, and an
// entry's score is the geometric mean of those factors with the official
// weights, as the results table computes it (1.000 = best on every test).

import { join } from "node:path";
import puppeteer, { type Browser, type Page } from "puppeteer-core";
import { findChrome } from "../chrome.ts";
import { computeDuration, NotOneClick } from "../trace.ts";
import { OURS, root, TOP5 } from "./prepare.ts";

interface Bench {
  id: string;
  label: string;
  throttle?: number;
  weight: number;
  init(p: Page): Promise<void>;
  run(p: Page): Promise<void>;
}

const WARMUP = 5;
const row = (n: number, col: number) => `tbody>tr:nth-of-type(${n})>td:nth-of-type(${col})`;

async function click(p: Page, sel: string): Promise<void> {
  await p.waitForSelector(sel, { timeout: 20000 });
  await p.click(sel);
}
const exists = (p: Page, sel: string) => p.waitForSelector(sel, { timeout: 20000 }).then(() => {});
const gone = (p: Page, sel: string) => p.waitForFunction((s) => !document.querySelector(s), { timeout: 20000 }, sel).then(() => {});
const text = (p: Page, sel: string, t: string) =>
  p.waitForFunction((s, t) => document.querySelector(s)?.textContent?.includes(t) ?? false, { timeout: 20000 }, sel, t).then(() => {});
const hasClass = (p: Page, sel: string, c: string) =>
  p.waitForFunction((s, c) => document.querySelector(s)?.classList.contains(c) ?? false, { timeout: 20000 }, sel, c).then(() => {});

async function runClear(p: Page, n: number): Promise<void> {
  for (let i = 0; i < n; i++) {
    await click(p, "#run");
    await text(p, row(1000, 1), ((i + 1) * 1000).toFixed());
    await click(p, "#clear");
    await gone(p, row(1000, 1));
  }
}

// The official tests and weights (webdriver-ts/src/benchmarksPuppeteer.ts,
// webdriver-ts-results/src/Common.ts).
const benches: Bench[] = [
  {
    id: "01_run1k",
    label: "create rows",
    weight: 0.64280248137063,
    init: (p) => runClear(p, WARMUP),
    run: async (p) => {
      await click(p, "#run");
      await text(p, row(1000, 1), ((WARMUP + 1) * 1000).toFixed());
    },
  },
  {
    id: "02_replace1k",
    label: "replace all rows",
    weight: 0.5607178150466176,
    init: async (p) => {
      for (let i = 0; i < WARMUP; i++) {
        await click(p, "#run");
        await text(p, row(1, 1), (i * 1000 + 1).toFixed());
      }
    },
    run: async (p) => {
      await click(p, "#run");
      await text(p, row(1, 1), `${WARMUP * 1000 + 1}`);
    },
  },
  {
    id: "03_update10th1k_x16",
    label: "partial update",
    throttle: 4,
    weight: 0.5643800750716564,
    init: async (p) => {
      await click(p, "#run");
      await exists(p, row(1000, 1));
      for (let i = 0; i < 3; i++) {
        await click(p, "#update");
        await text(p, row(991, 2), " !!!".repeat(i + 1));
      }
    },
    run: async (p) => {
      await click(p, "#update");
      await text(p, row(991, 2), " !!!".repeat(4));
    },
  },
  {
    id: "04_select1k",
    label: "select row",
    throttle: 4,
    weight: 0.1925635870170522,
    init: async (p) => {
      await click(p, "#run");
      await text(p, row(1000, 1), "1000");
      for (let i = 0; i <= WARMUP; i++) {
        await click(p, `tbody>tr:nth-of-type(${i + 5})>td:nth-of-type(2)>a`);
        await hasClass(p, `tbody>tr:nth-of-type(${i + 5})`, "danger");
      }
    },
    run: async (p) => {
      await click(p, "tbody>tr:nth-of-type(2)>td:nth-of-type(2)>a");
      await hasClass(p, "tbody>tr:nth-of-type(2)", "danger");
    },
  },
  {
    id: "05_swap1k",
    label: "swap rows",
    throttle: 4,
    weight: 0.13200612879341714,
    init: async (p) => {
      await click(p, "#run");
      await exists(p, row(1000, 1));
      for (let i = 0; i <= WARMUP; i++) {
        await click(p, "#swaprows");
        await text(p, row(999, 1), i % 2 === 0 ? "2" : "999");
      }
    },
    run: async (p) => {
      await click(p, "#swaprows");
      await text(p, row(999, 1), WARMUP % 2 === 0 ? "999" : "2");
      await text(p, row(2, 1), WARMUP % 2 === 0 ? "2" : "999");
    },
  },
  {
    id: "06_remove-one-1k",
    label: "remove row",
    throttle: 2,
    weight: 0.5277091212292658,
    init: async (p) => {
      const skip = 4;
      await click(p, "#run");
      await exists(p, row(1000, 1));
      for (let i = 0; i < WARMUP; i++) {
        const r = WARMUP - i + skip;
        await text(p, row(r, 1), `${r}`);
        await click(p, `${row(r, 3)}>a>span:nth-of-type(1)`);
        await text(p, row(r, 1), `${skip + WARMUP + 1}`);
      }
      await text(p, row(skip + 1, 1), `${skip + WARMUP + 1}`);
      await text(p, row(skip, 1), `${skip}`);
      await text(p, row(skip + 2, 1), `${skip + WARMUP + 2}`);
      await click(p, `${row(skip + 2, 3)}>a>span:nth-of-type(1)`);
      await text(p, row(skip + 2, 1), `${skip + WARMUP + 3}`);
    },
    run: async (p) => {
      await click(p, `${row(4, 3)}>a>span:nth-of-type(1)`);
      await text(p, row(4, 1), `${4 + WARMUP + 1}`);
    },
  },
  {
    id: "07_create10k",
    label: "create many rows",
    weight: 0.5644449600965534,
    init: (p) => runClear(p, WARMUP),
    run: async (p) => {
      await click(p, "#runlots");
      await exists(p, "tbody>tr:nth-of-type(10000)>td:nth-of-type(2)>a");
    },
  },
  {
    id: "08_create1k-after1k_x2",
    label: "append rows to large table",
    weight: 0.5508359820582848,
    init: async (p) => {
      await runClear(p, WARMUP);
      await click(p, "#run");
      await exists(p, row(1000, 1));
    },
    run: async (p) => {
      await click(p, "#add");
      await exists(p, row(2000, 1));
    },
  },
  {
    id: "09_clear1k_x8",
    label: "clear rows",
    throttle: 4,
    weight: 0.4225836631419211,
    init: async (p) => {
      await runClear(p, WARMUP);
      await click(p, "#run");
      await text(p, row(1000, 1), (WARMUP * 1000 + 1000).toFixed());
    },
    run: async (p) => {
      await click(p, "#clear");
      await gone(p, row(1000, 1));
    },
  },
];

const traceCategories = ["disabled-by-default-v8.cpu_profiler", "blink.user_timing", "devtools.timeline", "disabled-by-default-devtools.timeline"];
const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function startBrowser(): Promise<Browser> {
  const args = [
    "--window-size=1280,800",
    "--js-flags=--expose-gc",
    "--no-default-browser-check",
    "--disable-sync",
    "--no-first-run",
    "--disable-extensions",
    "--headless=new",
    "--disable-frame-rate-limit",
  ];
  return puppeteer.launch({ headless: false, executablePath: findChrome(), args, defaultViewport: { width: 1280, height: 800 } });
}

function serveRoot(): ReturnType<typeof Bun.serve> {
  return Bun.serve({
    port: 0,
    async fetch(req) {
      const path = decodeURIComponent(new URL(req.url).pathname);
      const file = Bun.file(join(root, path.endsWith("/") ? path + "index.html" : path));
      return (await file.exists()) ? new Response(file) : new Response("not found", { status: 404 });
    },
  });
}

// An entry's page: index.html, or under its package.json's
// js-framework-benchmark.customURL (thane serves from /dist/).
async function pagePath(entry: string): Promise<string> {
  const pkg = Bun.file(join(root, "frameworks/keyed", entry, "package.json"));
  const custom = (await pkg.exists()) ? (await pkg.json())["js-framework-benchmark"]?.customURL : undefined;
  return (custom ?? "/").replace(/\/?$/, "/") + "index.html";
}

async function sample(browser: Browser, base: string, entry: string, b: Bench): Promise<number> {
  for (let attempt = 1; ; attempt++) {
    const page = await browser.newPage();
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    try {
            await page.goto(`${base}/frameworks/keyed/${entry}${await pagePath(entry)}`, { waitUntil: "load" });
      await exists(page, "#run");
      await b.init(page);
      if (b.throttle) await page.emulateCPUThrottling(b.throttle);
      await page.tracing.start({ screenshots: false, categories: traceCategories });
      await wait(50);
      await page.evaluate("window.gc({type:'major',execution:'sync',flavor:'last-resort'})");
      await b.run(page);
      await wait(100);
      const buf = await page.tracing.stop();
      if (b.throttle) await page.emulateCPUThrottling(1);
      if (errors.length) throw new Error(`${entry}: page errors: ${errors.join("; ")}`);
      const trace = JSON.parse(new TextDecoder().decode(buf!)) as { traceEvents: Parameters<typeof computeDuration>[0] };
      return computeDuration(trace.traceEvents).duration;
    } catch (e) {
      if (!(e instanceof NotOneClick) || attempt >= 3) throw e;
    } finally {
      await page.close();
    }
  }
}

const median = (xs: number[]): number => {
  const s = [...xs].sort((a, b) => a - b);
  return s.length % 2 ? s[(s.length - 1) / 2]! : (s[s.length / 2 - 1]! + s[s.length / 2]!) / 2;
};

async function main(): Promise<void> {
  const argv = process.argv.slice(2);
  const value = (k: string) => (argv.includes(k) ? argv[argv.indexOf(k) + 1] : undefined);
  const entries = value("--entries")?.split(",") ?? [...TOP5, ...Object.keys(OURS)];
  const ids = value("--benchmarks")?.split(",");
  const iterations = Number(value("--iterations") ?? 10);
  const tests = ids ? benches.filter((b) => ids.includes(b.id)) : benches;
  const server = serveRoot();
  const base = `http://127.0.0.1:${server.port}`;
  const browser = await startBrowser();
  const times = new Map<string, number[]>();
  try {
    for (let it = 0; it < iterations; it++) {
      for (const b of tests) {
        const order = entries.map((_, i) => entries[(i + it) % entries.length]!);
        const line: string[] = [];
        for (const e of order) {
          const t = await sample(browser, base, e, b);
          const k = e + "|" + b.id;
          (times.get(k) ?? times.set(k, []).get(k)!).push(t);
          line.push(`${e} ${t.toFixed(1)}`);
        }
        console.log(`[${it + 1}/${iterations}] ${b.id.padEnd(22)} ${line.join(", ")}`);
      }
    }
  } finally {
    await browser.close();
    server.stop();
  }
  // Report: medians, factors to the best median, weighted geometric mean.
  const best = tests.map((b) => Math.min(...entries.map((e) => median(times.get(e + "|" + b.id)!))));
  const score = (e: string) => {
    let g = 0,
      w = 0;
    tests.forEach((b, i) => {
      g += b.weight * Math.log(median(times.get(e + "|" + b.id)!) / best[i]!);
      w += b.weight;
    });
    return Math.exp(g / w);
  };
  const ranked = [...entries].sort((a, b) => score(a) - score(b));
  const head = ["test", ...ranked];
  const rows = tests.map((b, i) => [
    `${b.label}${b.throttle ? ` (${b.throttle}x)` : ""}`,
    ...ranked.map((e) => {
      const m = median(times.get(e + "|" + b.id)!);
      return `${m.toFixed(1)} (${(m / best[i]!).toFixed(2)})`;
    }),
  ]);
  rows.push(["**weighted score**", ...ranked.map((e) => `**${score(e).toFixed(3)}**`)]);
  const md = [head, head.map(() => "---"), ...rows].map((r) => "| " + r.join(" | ") + " |").join("\n");
  console.log(`\njs-framework-benchmark replica, ${iterations} iterations, median ms (factor to the best median)\n\n${md}`);
}

await main();
