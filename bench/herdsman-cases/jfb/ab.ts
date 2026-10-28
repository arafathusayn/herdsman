// A/B proof for one runtime or compiler change: the jfb app built on a base
// git revision of web/src and on the working tree, measured as run.ts
// measures (the official click window, warmups, throttling, forced GC), in
// alternating batches, each in a fresh browser, so drift in the machine's
// speed lands on both. Per test: each side's median, the difference of
// medians and its 95% bootstrap interval. The change is proven faster when
// the whole interval is below zero, proven slower when above, and
// unproven otherwise.
//
//   bun jfb/ab.ts --base <git ref> [--benchmarks 04_select1k,...] [--rounds 6] [--batch 5] [--phases]

import { mkdirSync, rmSync } from "node:fs";
import { join } from "node:path";
import { reactAlias } from "../react-alias.ts";
import { keyed } from "./prepare.ts";
import { benches, measure, serveRoot, startBrowser } from "./run.ts";

const argv = process.argv.slice(2);
const value = (k: string) => (argv.includes(k) ? argv[argv.indexOf(k) + 1] : undefined);
const ref = value("--base") ?? "HEAD";
const ids = value("--benchmarks")?.split(",") ?? ["04_select1k", "03_update10th1k_x16", "05_swap1k", "02_replace1k", "01_run1k", "09_clear1k_x8"];
const rounds = Number(value("--rounds") ?? 6);
const batch = Number(value("--batch") ?? 5);
// The window's duration and script time, and with --phases its style, layout and paint too.
const metrics = argv.includes("--phases") ? ["duration", "script", "style", "layout", "paint"] : ["duration", "script"];
const repo = join(import.meta.dir, "../../..");
const scratch = join(import.meta.dir, ".cache/ab");

// The base revision's sources, as files next to the working tree's.
rmSync(scratch, { recursive: true, force: true });
mkdirSync(scratch, { recursive: true });
const tar = Bun.spawnSync(["sh", "-c", `git -C '${repo}' archive '${ref}' web/src | tar -x -C '${scratch}'`]);
if (tar.exitCode !== 0) throw new Error(`git archive ${ref}: ${tar.stderr}`);

async function build(name: string, src: string): Promise<void> {
  const { herdsmanReactCompiler } = (await import(join(src, "herdsman-react-compiler.ts"))) as typeof import("../../../web/src/herdsman-react-compiler.ts");
  const r = await Bun.build({
    entrypoints: [join(import.meta.dir, "app/main.jsx")],
    format: "iife",
    minify: true,
    plugins: [herdsmanReactCompiler(), reactAlias("herdsman-react", { src })],
    define: { "process.env.NODE_ENV": JSON.stringify("production") },
  });
  if (!r.success) throw new Error(r.logs.join("\n"));
  const dir = join(keyed, name);
  rmSync(dir, { recursive: true, force: true });
  await Bun.write(join(dir, "dist/main.js"), r.outputs[0]!);
  await Bun.write(join(dir, "index.html"), (await Bun.file(join(import.meta.dir, "app/index.html")).text()).replace("NAME", name));
}
await build("ab-base", join(scratch, "web/src"));
await build("ab-head", join(repo, "web/src"));

const median = (xs: number[]): number => {
  const s = [...xs].sort((a, b) => a - b);
  return s.length % 2 ? s[(s.length - 1) / 2]! : (s[s.length / 2 - 1]! + s[s.length / 2]!) / 2;
};
// 95% bootstrap interval of median(b) - median(a).
function interval(a: number[], b: number[]): [number, number] {
  const pick = (xs: number[]) => Array.from(xs, () => xs[Math.floor(Math.random() * xs.length)]!);
  const d: number[] = [];
  for (let i = 0; i < 4000; i++) d.push(median(pick(b)) - median(pick(a)));
  d.sort((x, y) => x - y);
  return [d[Math.floor(d.length * 0.025)]!, d[Math.floor(d.length * 0.975)]!];
}

const server = serveRoot();
const base = `http://127.0.0.1:${server.port}`;
console.log(`A/B: base ${ref} vs working tree, ${rounds} rounds of ${batch} samples per side\n`);
try {
  for (const b of benches.filter((x) => ids.includes(x.id))) {
    const times: Record<string, Record<string, number>[]> = { "ab-base": [], "ab-head": [] };
    for (let r = 0; r < rounds; r++) {
      for (const side of r % 2 ? ["ab-head", "ab-base"] : ["ab-base", "ab-head"]) {
        const browser = await startBrowser();
        try {
          for (let i = 0; i < batch; i++) times[side]!.push(await measure(browser, base, side, b));
        } finally {
          await browser.close();
        }
      }
    }
    for (const metric of metrics) {
      const a = times["ab-base"]!.map((x) => x[metric]!),
        h = times["ab-head"]!.map((x) => x[metric]!);
      const [lo, hi] = interval(a, h);
      const verdict = hi < 0 ? "FASTER (proven)" : lo > 0 ? "SLOWER (proven)" : "no proven difference";
      console.log(
        `${b.id.padEnd(22)} ${metric.padEnd(8)} base ${median(a).toFixed(1).padStart(7)}  head ${median(h).toFixed(1).padStart(7)}  ` +
          `diff ${(median(h) - median(a)).toFixed(1).padStart(6)} ms  95% [${lo.toFixed(1)}, ${hi.toFixed(1)}]  ${verdict}`,
      );
    }
  }
} finally {
  server.stop();
}
