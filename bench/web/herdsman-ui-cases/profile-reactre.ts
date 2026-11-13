// Diagnose mounting and cleanup without changing the official benchmark runner.
// Named, syntax-minified Reactre bundles use the production compiler/runtime.
// Each CPU profile covers both legs of 300 warmed tab switches; targetMs counts
// only the target click's synchronous work, without a layout/paint wait. These
// are diagnostic numbers, not the runner's commit-time ranking.
// Run after `bun build.ts`: `bun profile-reactre.ts <label>`.
import { join, resolve, sep } from "node:path";
import { statSync } from "node:fs";
import { plugin } from "bun";
import puppeteer from "puppeteer-core";
import {
  reactreSources,
  sourceCompiler,
  stateCompiler,
} from "../../../web/reactre.ts";
import { findChrome } from "./chrome.ts";
const root = import.meta.dir;
const label = process.argv[2] ?? new Date().toISOString().replace(/[:.]/g, "-");
if (!/^[a-zA-Z0-9_-]+$/.test(label))
  throw new Error(
    "Use a simple label containing letters, digits, underscores or hyphens",
  );
const output = join(root, "results", "profiles", label);
const apps = [
  "reactre-signals",
  "reactre-state",
  "vanillajs-lite",
  "sonnet",
  "doohtml-dp",
];
plugin(reactreSources);
for (const app of ["reactre-signals", "reactre-state"]) {
  const b = await Bun.build({
    entrypoints: [join(root, "apps", app, "index.html")],
    outdir: join(output, app),
    minify: { syntax: true, whitespace: false, identifiers: false },
    plugins: [
      reactreSources,
      stateCompiler(false),
      sourceCompiler(
        /\/(?:apps\/reactre-(?:signals|state)|shared)\/.*\.[jt]sx?$/,
      ),
    ],
  });
  if (!b.success) throw new AggregateError(b.logs);
}
const server = Bun.serve({
  hostname: "127.0.0.1",
  port: 0,
  fetch(req) {
    const [app, ...parts] = new URL(req.url).pathname.slice(1).split("/");
    if (!app || !apps.includes(app)) return new Response(null, { status: 404 });
    const directory = join(
      app.startsWith("reactre-") ? output : join(root, "dist"),
      app,
    );
    let file = resolve(directory, parts.join("/") || "index.html");
    if (!file.startsWith(directory + sep))
      return new Response(null, { status: 403 });
    try {
      if (statSync(file).isDirectory()) file = join(file, "index.html");
    } catch {
      return new Response(null, { status: 404 });
    }
    return new Response(Bun.file(file));
  },
});
let browser: Awaited<ReturnType<typeof puppeteer.launch>> | undefined;
const results = [];
try {
  browser = await puppeteer.launch({
    executablePath: findChrome(),
    headless: true,
    args: ["--js-flags=--expose-gc"],
  });
  for (const app of apps) {
    const page = await browser.newPage();
    await page.goto(server.url.origin + "/" + app + "/");
    await page.click("#board");
    const cdp = await page.createCDPSession();
    await cdp.send("Profiler.enable");
    await cdp.send("Profiler.setSamplingInterval", { interval: 100 });
    for (const [from, to] of [
      ["activity", "team"],
      ["team", "checklist"],
      ["team", "activity"],
    ]) {
      await page.evaluate(
        ({ from, to }) => {
          for (let i = 0; i < 30; i++) {
            document.getElementById(from)!.click();
            document.getElementById(to)!.click();
          }
        },
        { from, to },
      );
      await cdp.send("Profiler.start");
      const time = await page.evaluate(
        ({ from, to }) => {
          let ms = 0;
          for (let i = 0; i < 300; i++) {
            document.getElementById(from)!.click();
            const t = performance.now();
            document.getElementById(to)!.click();
            ms += performance.now() - t;
          }
          return {
            targetMs: ms,
            cycles: 300,
            nodes: document.querySelectorAll("#main *").length,
          };
        },
        { from, to },
      );
      const { profile } = await cdp.send("Profiler.stop");
      await Bun.write(
        join(output, `${label}-${app}-${to}.cpuprofile`),
        JSON.stringify(profile),
      );
      const weights = new Map<number, number>();
      for (let i = 0; i < (profile.samples?.length ?? 0); i++) {
        const id = profile.samples![i]!;
        weights.set(id, (weights.get(id) ?? 0) + profile.timeDeltas![i]!);
      }
      results.push({
        app,
        from,
        to,
        ...time,
        top: profile.nodes
          .map((n) => ({
            fn: n.callFrame.functionName,
            line: n.callFrame.lineNumber + 1,
            ms: (weights.get(n.id) ?? 0) / 1000,
          }))
          .sort((a, b) => b.ms - a.ms)
          .slice(0, 18),
      });
    }
    await page.close();
  }
  await Bun.write(
    join(output, `${label}.json`),
    JSON.stringify(results, null, 2),
  );
  console.log(JSON.stringify(results, null, 2));
} finally {
  try {
    await browser?.close();
  } finally {
    server.stop(true);
  }
}
