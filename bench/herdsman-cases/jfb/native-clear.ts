// The native cost of clearing an entry's rows, apart from its JavaScript:
// in each entry's own page, after the clear benchmark's warmups (five runs
// of create and clear, then create), the benchmark's 4x throttling and its
// last-resort GC, `tbody.textContent = ""` is timed directly. Entries whose
// rows carry JavaScript state on their elements (an expando, a WeakMap
// entry) show it here; see jfb/REPORT.md.
//
//   bun jfb/native-clear.ts [entries] [samples]     (run from bench/herdsman-cases)
//
// entries: comma-separated (default vanillajs,herdsman-blocks); samples per
// entry (default 40), taken in alternation, a new page each.

import type { Page } from "puppeteer-core";
import { serveRoot, startBrowser } from "./run.ts";

const entries = (process.argv[2] ?? "vanillajs,herdsman-blocks").split(",");
const samples = Number(process.argv[3] ?? 40);

const server = serveRoot();
const browser = await startBrowser();
const times: Record<string, number[]> = {};

async function rowsThere(p: Page, n: number): Promise<void> {
  await p.waitForSelector(`tbody>tr:nth-of-type(${n})`, { timeout: 20000 });
}

try {
  for (let r = 0; r < samples; r++) {
    for (const e of entries) {
      const page = await browser.newPage();
      try {
        await page.goto(`http://127.0.0.1:${server.port}/frameworks/keyed/${e}/index.html`, { waitUntil: "load" });
        await page.waitForSelector("#run");
        for (let i = 0; i < 5; i++) {
          await page.click("#run");
          await rowsThere(page, 1000);
          await page.click("#clear");
          await page.waitForFunction(() => !document.querySelector("tbody>tr"));
        }
        await page.click("#run");
        await rowsThere(page, 1000);
        await new Promise((ok) => setTimeout(ok, 200));
        await page.emulateCPUThrottling(4);
        const t = await page.evaluate(() => {
          (window as unknown as { gc(o: unknown): void }).gc({ type: "major", execution: "sync", flavor: "last-resort" });
          const tbody = document.querySelector("tbody")!;
          const t0 = performance.now();
          tbody.textContent = "";
          return performance.now() - t0;
        });
        (times[e] ??= []).push(t);
      } finally {
        await page.close();
      }
    }
  }
} finally {
  await browser.close();
  server.stop();
}

const median = (xs: number[]): number => {
  const s = [...xs].sort((a, b) => a - b);
  return s.length % 2 ? s[(s.length - 1) / 2]! : (s[s.length / 2 - 1]! + s[s.length / 2]!) / 2;
};
for (const e of entries) console.log(`${e.padEnd(18)} native clear of 1,000 rows at 4x: median ${median(times[e]!).toFixed(1)} ms (${samples} samples)`);
