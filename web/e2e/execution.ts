import type { Browser, HTTPRequest, Page } from "puppeteer-core";

import type { Check } from "./app.ts";
import type { Mock } from "./mock.ts";

async function button(page: Page, label: string) {
  await page.evaluate((text) => {
    const button = [...document.querySelectorAll<HTMLButtonElement>("button")].find(
      (element) => element.textContent?.trim() === text,
    );
    if (!button) throw new Error(`Missing button: ${text}`);
    button.click();
  }, label);
}

/** Production bundle: confirmation, duplicate guards, cancellation and retry. */
export async function executionChecks(browser: Browser, mock: Mock, check: Check): Promise<void> {
  const page = await browser.newPage();
  const errors: string[] = [];
  const held: HTTPRequest[] = [];
  let mode: "hold" | "fail" | "success" = "hold";
  let submits = 0;
  page.on("pageerror", (error) => errors.push(String(error)));
  await page.setRequestInterception(true);
  page.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    const reply = (body: unknown, status = 200) =>
      request.respond({
        status,
        contentType: "application/json",
        body: JSON.stringify(body),
      });
    if (path.endsWith("RuntimeService/ListExecutions")) {
      void reply({
        executions: [
          {
            ref: { backend: "herdr-core", executionId: "browser-e1", generation: "1" },
            route: { value: "auth" },
            agent: "browser execution",
            actions: ["show", "submit"],
          },
        ],
      });
    } else if (path.endsWith("RuntimeService/Submit")) {
      submits++;
      if (mode === "hold") held.push(request);
      else
        void reply(
          mode === "fail"
            ? { code: "unavailable", message: "runtime unavailable" }
            : { submitId: "browser-s1", state: "typed" },
          mode === "fail" ? 503 : 200,
        );
    } else {
      void request.continue();
    }
  });
  try {
    await page.goto(mock.base + "/?page=route", { waitUntil: "domcontentloaded" });
    await page.waitForFunction(() => document.querySelector(".page")?.textContent?.includes("browser execution"));
    await button(page, "Type a line");
    check(
      "execution: confirmation precedes every submit",
      submits === 0 &&
        (await page.$eval(".page-form", (form) => form.textContent?.includes("nothing can recall it") === true)),
    );
    await page.type(".page-form input", "go on");
    await button(page, "I read this: type the line");
    await page.waitForFunction(() => document.querySelector(".page")?.textContent?.includes("Waiting for the runtime"));
    check(
      "execution: pending submit disables draft editing",
      await page.$eval(".page-form input", (input) => (input as HTMLInputElement).disabled),
    );
    await page.$eval(".page-form", (form) =>
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })),
    );
    await new Promise((resolve) => setTimeout(resolve, 50));
    check("execution: duplicate submit is declined", submits === 1, String(submits));
    await button(page, "Stop waiting");
    await page.waitForFunction(() =>
      document.querySelector(".page")?.textContent?.includes("may already have received"),
    );
    check(
      "execution: cancellation retains the draft and warns about accepted commands",
      await page.$eval(
        ".page-form input",
        (input) => !(input as HTMLInputElement).disabled && (input as HTMLInputElement).value === "go on",
      ),
    );
    for (const request of held.splice(0)) await request.abort().catch(() => {});
    mode = "fail";
    await button(page, "I read this: type the line");
    await page.waitForSelector(".page-error");
    check(
      "execution: failure keeps the confirmed draft for retry",
      await page.$eval(".page-form input", (input) => (input as HTMLInputElement).value === "go on"),
    );
    mode = "success";
    await button(page, "I read this: type the line");
    await page.waitForFunction(() =>
      document.querySelector(".page")?.textContent?.includes("Typed submit browser-s1: typed"),
    );
    check("execution: only explicit retries send another command", submits === 3, String(submits));
    await button(page, "Type a line");
    check(
      "execution: accepted submit clears its draft",
      await page.$eval(".page-form input", (input) => (input as HTMLInputElement).value === ""),
    );
  } catch (error) {
    check("execution: browser workflow completes", false, String(error));
  } finally {
    for (const request of held) await request.abort().catch(() => {});
    for (const error of errors) check("execution: page error", false, error);
    await page.close();
  }
}
