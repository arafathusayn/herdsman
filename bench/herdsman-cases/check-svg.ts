// Runs check-svg.page.ts in headless Chrome and prints each check: SVG
// templates and views, class holes on SVG, and typed event and property
// holes against the real DOM. Exits non-zero when a check fails.
//
//   bun install && bun check-svg.ts

import { join } from "node:path";
import puppeteer from "puppeteer-core";

import { findChrome } from "./chrome.ts";

const CHROME = findChrome();

const built = await Bun.build({
  entrypoints: [join(import.meta.dir, "check-svg.page.ts")],
  target: "browser",
  minify: true,
});
if (!built.success) throw new Error(built.logs.join("\n"));
const code = await built.outputs[0]!.text();

const browser = await puppeteer.launch({
  headless: true,
  executablePath: CHROME,
  args: ["--no-first-run", "--disable-extensions"],
});
let failed = 0;
try {
  const page = await browser.newPage();
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.setContent(
    `<!doctype html><html><body><script type="module">${code}</script></body></html>`,
  );
  await page
    .waitForFunction(
      () => "__results" in window || document.title === "error",
      { timeout: 10_000 },
    )
    .catch(() => {});
  const results = (await page.evaluate(
    () => (window as unknown as { __results?: unknown }).__results,
  )) as { name: string; ok: boolean; detail: string }[] | undefined;
  console.log(
    `Chrome ${(await browser.version()).replace(/^.*\//, "")}, headless`,
  );
  for (const e of errors) {
    failed++;
    console.log(`FAIL page error: ${e}`);
  }
  if (!results) {
    failed++;
    console.log("FAIL the page did not report results");
  }
  for (const r of results ?? []) {
    if (!r.ok) failed++;
    console.log(
      `${r.ok ? "ok  " : "FAIL"} ${r.name}${r.ok ? "" : ` (${r.detail})`}`,
    );
  }
} finally {
  await browser.close();
}
process.exit(failed ? 1 : 0);
