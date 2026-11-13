// Reproducible production-app responsiveness probe; run after `make web`:
//   cd web/e2e && bun performance.ts > /tmp/herdsman-performance.json
// CPU=1 disables the default fourfold Chrome CPU slowdown. Timing is reported,
// not a flaky CI gate; structural work-count regressions live in unit tests.
import { join } from "node:path";

import puppeteer from "puppeteer-core";

import { chromeArgs, findChrome } from "./chrome.ts";
import { entry, startMock } from "./mock.ts";
import { stressLoad, typeWithOutput } from "./responsiveness.ts";

const load = stressLoad;
const browser = await puppeteer.launch({ headless: true, executablePath: findChrome(), args: chromeArgs() });
const mock = startMock(join(import.meta.dir, "../../internal/web/dist"), load);
const page = await browser.newPage();
const errors: string[] = [];
page.on("pageerror", (e) => errors.push(String(e)));
try {
  await page.setViewport({ width: 1300, height: 800 });
  const cdp = await page.createCDPSession();
  const cpu = Number(process.env["CPU"] ?? 4);
  await cdp.send("Emulation.setCPUThrottlingRate", { rate: cpu });
  await page.evaluateOnNewDocument(() => {
    const durations: number[] = [];
    Object.assign(window, { __longTasks: durations });
    new PerformanceObserver((list) => {
      for (const item of list.getEntries()) durations.push(item.duration);
    }).observe({ type: "longtask", buffered: true });
  });
  const started = performance.now();
  await page.goto(mock.base + "/?r=auth", { waitUntil: "domcontentloaded" });
  await page.waitForSelector(".kanban .card", { timeout: 30000 });
  const loadMs = performance.now() - started;
  await cdp.send("Profiler.enable");
  await cdp.send("Profiler.start");
  const click = async (selector: string) =>
    page.evaluate(async (sel) => {
      const control = document.querySelector<HTMLElement>(sel);
      if (!control) throw new Error(`Missing control ${sel}`);
      const start = performance.now();
      control.click();
      const handler = performance.now() - start;
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())));
      return { handler, frame: performance.now() - start };
    }, selector);
  const navigation = [];
  const theme = [];
  for (let i = 0; i < 12; i++) {
    navigation.push(await click(`.tabs .tab:nth-child(${i % 2 ? 1 : 2})`));
    theme.push(await click(`.sidebar .theme-switch button:nth-child(${i % 2 ? 1 : 3})`));
  }
  await click(".tabs .tab:first-child");
  await click('.kanban .card[data-pane="p2"]');
  await page.waitForFunction(() => document.querySelector("pre.tail")?.textContent?.includes("green"), {
    timeout: 10000,
  });
  const typing = await typeWithOutput(page);
  await page.keyboard.press("Escape");
  // A burst of independent activity events must not repeatedly process the
  // unrelated archived route history or prevent a subsequent navigation.
  const burstStart = performance.now();
  for (let i = 0; i < 30; i++) mock.push(entry(500 + i, `burst ${i}`));
  await page.evaluate(() => {
    const tab = [...document.querySelectorAll<HTMLButtonElement>(".tabs .tab")].find((el) =>
      el.textContent?.includes("Activity"),
    );
    if (!tab) throw new Error("Missing activity tab");
    tab.click();
  });
  await page.waitForFunction(() => document.querySelector(".timeline.activity")?.textContent?.includes("burst 29"));
  await click(".tabs .tab:nth-child(2)");
  const burstMs = performance.now() - burstStart;
  const { profile } = await cdp.send("Profiler.stop");
  const weights = new Map<number, number>();
  for (let i = 0; i < (profile.samples?.length ?? 0); i++) {
    const id = profile.samples![i]!;
    weights.set(id, (weights.get(id) ?? 0) + (profile.timeDeltas?.[i] ?? 0));
  }
  const hot = profile.nodes
    .map((n) => ({
      function: n.callFrame.functionName || "(anonymous)",
      selfMs: (weights.get(n.id) ?? 0) / 1000,
    }))
    .sort((a, b) => b.selfMs - a.selfMs)
    .slice(0, 12);
  const longTasks = await page.evaluate(() => (window as unknown as { __longTasks: number[] }).__longTasks);
  console.log(
    JSON.stringify({ cpu, load, loadMs, navigation, theme, typing, burstMs, longTasks, hot, errors }, null, 2),
  );
  if (errors.length) process.exitCode = 1;
} finally {
  await browser.close();
  mock.stop();
}
