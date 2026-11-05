// Browser checks of herdsman web in headless Chrome:
//
// 1. app.ts: the built app (internal/web/dist, from `make web`) against a
//    mock API (mock.ts).
// 2. pages/*.page.ts: the forms and dialogs (Appearance, Hosts and the
//    connect screens, the route form), each bundled from web/src on its own
//    and run in a blank page.
//
// Prints one line per check and exits non-zero when any fails.
//
//   make web && cd web/e2e && bun install && bun run e2e     (or: make web-e2e)
//
// Chrome comes from $CHROME or the usual install places (chrome.ts).
import { existsSync } from "node:fs";
import { join } from "node:path";

import puppeteer from "puppeteer-core";

import { appChecks, type Check } from "./app.ts";
import { chromeArgs, findChrome } from "./chrome.ts";
import { startMock } from "./mock.ts";
import { pageChecks } from "./pages.ts";

const dist = join(import.meta.dir, "../../internal/web/dist");
if (!existsSync(join(dist, "index.html"))) {
  console.error(`No built app in ${dist}: run make web first.`);
  process.exit(2);
}

const results: { name: string; ok: boolean; detail: string }[] = [];
const check: Check = (name, ok, detail = "") => results.push({ name, ok, detail });

const browser = await puppeteer.launch({ headless: true, executablePath: findChrome(), args: chromeArgs() });
const mock = startMock(dist);
try {
  await appChecks(browser, mock, check);
  await pageChecks(browser, check);
} finally {
  await browser.close();
  mock.stop();
}

for (const r of results) console.log(`${r.ok ? "ok  " : "FAIL"} ${r.name}${r.ok ? "" : ` (${r.detail})`}`);
const failed = results.filter((r) => !r.ok).length;
console.log(`${results.length - failed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
