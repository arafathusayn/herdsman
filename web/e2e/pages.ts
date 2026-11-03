// Runs each pages/*.page.ts in its own blank page: the page script is bundled
// for the browser together with the app modules it imports from web/src, and
// reports its checks on window.__results (pages/lib.ts).
import { join } from "node:path";

import { Glob, plugin } from "bun";
import type { Browser } from "puppeteer-core";

import { reactre, reactreSources } from "../reactre.ts";
import type { Check } from "./app.ts";

plugin(reactreSources);

export async function pageChecks(browser: Browser, check: Check): Promise<void> {
  const dir = join(import.meta.dir, "pages");
  const entries = [...new Glob("*.page.{ts,tsx}").scanSync(dir)].sort();
  for (const name of entries) {
    const built = await Bun.build({ entrypoints: [join(dir, name)], target: "browser", plugins: reactre() });
    if (!built.success) {
      check(`${name}: builds`, false, built.logs.join("\n"));
      continue;
    }
    const code = await built.outputs[0]!.text();
    const server = Bun.serve({
      port: 0,
      fetch: (request) =>
        new URL(request.url).pathname === "/fixture.js"
          ? new Response(code, { headers: { "content-type": "text/javascript; charset=utf-8" } })
          : new Response(
              '<!doctype html><html><head><meta charset="utf-8"></head><body><div id="app"></div><script type="module" src="/fixture.js"></script></body></html>',
              { headers: { "content-type": "text/html; charset=utf-8" } },
            ),
    });
    const page = await browser.newPage();
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    try {
      await page.goto(`http://127.0.0.1:${server.port}/`);
      await page.waitForFunction(() => "__results" in window, { timeout: 15000 }).catch(() => {});
      const results = await page.evaluate(
        () => (window as unknown as { __results?: { name: string; ok: boolean; detail: string }[] }).__results,
      );
      if (!results) check(`${name}: reports its checks`, false, "no results");
      for (const r of results ?? []) check(`${name.replace(/\.page\.tsx?$/, "")}: ${r.name}`, r.ok, r.detail);
      for (const e of errors) check(`${name}: page error`, false, e);
    } finally {
      await page.close();
      server.stop(true);
    }
  }
}
