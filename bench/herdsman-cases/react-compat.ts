// React compatibility check: runs the scenarios in react-compat/cases.tsx in
// headless Chrome against React 19 and against each runtime that stands in
// for it, and compares every transcript (logged values and the HTML after
// each step) with React's, line for line.
//
//   bun react-compat.ts [--runtimes herdsman-react,herdsman-react+classes,redact] [--verbose]
//
// Runtimes: `herdsman-react` is web/src/herdsman-react.ts as apps get it by
// default, without class components; `herdsman-react+classes` is the build with them
// (herdsman-react-classes.ts); `redact` is @tanstack/redact from
// apps/redact (run `bun install` there first). React comes from apps/react,
// the libraries from react-compat/package.json. The build aliases `react`,
// `react-dom` and their subpaths to the runtime, as a bundler plugin for a
// real app would. Exits non-zero when a `herdsman-react+classes` scenario differs
// from React, or a `herdsman-react` scenario differs for any reason but rendering a
// class (those are listed as needing classes).

import { join } from "node:path";
import puppeteer from "puppeteer-core";
import { findChrome } from "./chrome.ts";
import { herdsmanReactCompiler } from "../../web/src/herdsman-react-compiler.ts";
import { reactAlias, type Runtime } from "./react-alias.ts";

const here = import.meta.dir;
const dir = join(here, "react-compat");

// "herdsman-react+classes" is the herdsman-react runtime with class support,
// "+compiler" adds herdsman-react-compiler (the scenarios' JSX compiled to
// templates).
async function bundle(entry: string, spec: string): Promise<string> {
  const [runtime, ...extras] = spec.split("+") as [Runtime, ...string[]];
  const r = await Bun.build({
    entrypoints: [join(dir, entry)],
    format: "iife",
    minify: true,
            plugins: [
      ...(extras.includes("compiler") ? [herdsmanReactCompiler()] : []),
      reactAlias(runtime, { classes: extras.includes("classes") }),
    ],
    define: { "process.env.NODE_ENV": JSON.stringify("production") },
  });
  if (!r.success) throw new Error(r.logs.join("\n"));
  return await r.outputs[0]!.text();
}

const args = process.argv.slice(2);
const opt = (name: string): string | undefined => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
};
const runtimes = (opt("--runtimes") ?? "herdsman-react,herdsman-react+classes,redact").split(",");
const ours = (rt: string) => rt.startsWith("herdsman-react") || rt.startsWith("reactre-compat");
// What the default build throws when a class component renders.
const NO_CLASSES = "this build leaves class support out";
const verbose = args.includes("--verbose");

const browser = await puppeteer.launch({ executablePath: findChrome(), headless: true, args: ["--no-sandbox"] });

async function run(code: string, ssr: Record<string, string>): Promise<Record<string, string[]>> {
  const page = await browser.newPage();
  if (verbose) {
    page.on("console", (m) => console.log("  page:", m.text()));
    page.on("pageerror", (e) => console.log("  page error:", e.message));
  }
  try {
    await page.setContent("<!doctype html><html><head></head><body></body></html>");
    await page.evaluate((s) => ((window as unknown as { __ssr: unknown }).__ssr = s), ssr);
    await page.addScriptTag({ content: code });
    await page.waitForFunction(() => (window as unknown as { __done?: boolean }).__done === true, { timeout: 60000 });
    return await page.evaluate(() => (window as unknown as { __results: Record<string, string[]> }).__results);
  } finally {
    await page.close();
  }
}

let failed = false;
try {
  const ssrPage = await browser.newPage();
  await ssrPage.setContent("<!doctype html><html><body></body></html>");
  await ssrPage.addScriptTag({ content: await bundle("ssr.tsx", "react") });
  const ssr = await ssrPage.evaluate(() => (window as unknown as { __ssr: Record<string, string> }).__ssr);
  await ssrPage.close();

  const want = await run(await bundle("page.tsx", "react"), ssr);
  const names = Object.keys(want);
  const summary: string[] = [];
  for (const rt of runtimes) {
    let got: Record<string, string[]>;
    try {
      got = await run(await bundle("page.tsx", rt), ssr);
    } catch (e) {
      summary.push(`${rt}: did not run: ${e instanceof Error ? e.message.split("\n")[0] : e}`);
            if (ours(rt)) failed = true;
      continue;
    }
    let pass = 0;
    const bad: string[] = [],
      classes: string[] = [];
    for (const name of names) {
      const a = want[name]!,
        b = got[name] ?? ["(missing)"];
      const n = Math.max(a.length, b.length);
      let diff = -1;
      for (let i = 0; i < n; i++) if (a[i] !== b[i]) ((diff = i), (i = n));
      if (diff < 0) {
        pass++;
        continue;
      }
            if (ours(rt) && !rt.includes("+classes") && b.some((line) => line.includes(NO_CLASSES))) {
        classes.push(name);
        continue;
      }
      bad.push(name);
      console.log(`\n${rt} ✗ ${name}\n  react: ${a[diff] ?? "(nothing)"}\n  ${rt.padStart(5)}: ${b[diff] ?? "(nothing)"}`);
      if (verbose) {
        console.log("  --- react ---\n  " + a.join("\n  "));
        console.log(`  --- ${rt} ---\n  ` + b.join("\n  "));
      }
    }
        summary.push(
      `${rt}: ${pass}/${names.length} scenarios match React` +
        (classes.length ? `; need classes: ${classes.join("; ")}` : "") +
        (bad.length ? `; differ: ${bad.join("; ")}` : ""),
    );
    if (ours(rt) && bad.length) failed = true;
  }
  console.log("\n" + summary.join("\n"));
} finally {
  await browser.close();
}
process.exit(failed ? 1 : 0);
