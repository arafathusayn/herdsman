import type { Browser, Page } from "puppeteer-core";

import type { Check } from "./app.ts";
import { entry, startMock } from "./mock.ts";

export const stressLoad = { routes: 40, logLines: 400, terminalLines: 400 };

export async function typeWithOutput(page: Page) {
  return page.evaluate(async () => {
    const tail = document.querySelector("pre.tail")!;
    let mutations = 0;
    const observer = new MutationObserver((records) => {
      mutations += records.length;
    });
    observer.observe(tail, { subtree: true, childList: true, characterData: true });
    const field = document.querySelector<HTMLTextAreaElement>("textarea:not(.term-input)")!;
    const times: number[] = [];
    for (const char of "write a regression test") {
      const start = performance.now();
      field.value += char;
      field.dispatchEvent(new Event("input", { bubbles: true }));
      times.push(performance.now() - start);
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    }
    observer.disconnect();
    return { times, mutations, value: field.value };
  });
}

/** Work-count/identity regressions, not machine-dependent timing thresholds. */
export async function responsivenessChecks(browser: Browser, dist: string, check: Check): Promise<void> {
  const mock = startMock(dist, stressLoad);
  const page = await browser.newPage();
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  try {
    await page.setViewport({ width: 1300, height: 800 });
    await page.goto(mock.base + "/?r=auth", { waitUntil: "domcontentloaded" });
    await page.waitForSelector('.kanban .card[data-pane="p2"]', { timeout: 10000 });
    await page.click('.kanban .card[data-pane="p2"]');
    await page.waitForFunction(() => document.querySelector("pre.tail")?.textContent?.includes("green"), {
      timeout: 10000,
    });
    const firstRun = await page.$("pre.tail .ansi-output > span");
    const typed = await typeWithOutput(page);
    check(
      "stress: composer typing leaves 400 lines of terminal DOM untouched",
      typed.mutations === 0,
      String(typed.mutations),
    );
    check("stress: all composer input is retained", typed.value === "write a regression test", typed.value);
    mock.appendOutput("\nnew output marker");
    await page.waitForFunction(() => document.querySelector("pre.tail")?.textContent?.includes("new output marker"), {
      timeout: 10000,
    });
    check(
      "stress: polling new text keeps existing ANSI spans",
      await page.evaluate((run) => run === document.querySelector("pre.tail .ansi-output > span"), firstRun),
    );
    check(
      "stress: output refresh preserves the composer draft",
      await page.$eval(
        "textarea:not(.term-input)",
        (field) => (field as HTMLTextAreaElement).value === "write a regression test",
      ),
    );
    await page.keyboard.press("Escape");
    for (let i = 0; i < 30; i++) mock.push(entry(500 + i, `burst ${i}`));
    await page.evaluate(() => {
      [...document.querySelectorAll<HTMLButtonElement>(".tabs .tab")]
        .find((el) => el.textContent?.includes("Activity"))!
        .click();
    });
    await page.waitForFunction(() => document.querySelector(".timeline.activity")?.textContent?.includes("burst 29"), {
      timeout: 5000,
    });
    check("stress: activity burst reaches the visible feed with large archived history", true);
    await page.click(".tabs .tab:nth-child(2)");
    await page.waitForSelector(".checklist", { timeout: 5000 });
    check("stress: navigation responds after the burst", true);
  } catch (error) {
    check("stress: browser workflow completes", false, String(error));
  } finally {
    for (const error of errors) check("stress: page error", false, error);
    await page.close();
    mock.stop();
  }
}
