// CPU profile of one benchmark operation on an entry, for finding hot spots:
// self time by function, from Chrome's sampling profiler. Builds the React
// app unminified for herdsman-react so the names are readable.
//
//   bun jfb/profile.ts [op] [repeat]     op: update | select | swap | remove | add | runlots | run | clear

import { join } from "node:path";
import puppeteer from "puppeteer-core";
import { findChrome } from "../chrome.ts";
import { herdsmanReactCompiler } from "../../../web/src/herdsman-react-compiler.ts";
import { reactAlias } from "../react-alias.ts";
import { keyed, root } from "./prepare.ts";

const op = process.argv[2] ?? "update";
const repeat = Number(process.argv[3] ?? 10);
const dir = join(keyed, "herdsman-react-profile");
const r = await Bun.build({
  entrypoints: [join(import.meta.dir, "app/main.jsx")],
  format: "iife",
  plugins: [herdsmanReactCompiler(), reactAlias("herdsman-react")],
  define: { "process.env.NODE_ENV": JSON.stringify("production") },
});
if (!r.success) throw new Error(r.logs.join("\n"));
await Bun.write(join(dir, "dist/main.js"), r.outputs[0]!);
await Bun.write(join(dir, "index.html"), (await Bun.file(join(import.meta.dir, "app/index.html")).text()).replace("NAME", "profile"));
const server = Bun.serve({
  port: 0,
  async fetch(req) {
    const f = Bun.file(join(root, decodeURIComponent(new URL(req.url).pathname)));
    return (await f.exists()) ? new Response(f) : new Response("", { status: 404 });
  },
});
const browser = await puppeteer.launch({ executablePath: findChrome(), headless: true, args: ["--no-sandbox"] });
const page = await browser.newPage();
await page.goto(`http://127.0.0.1:${server.port}/frameworks/keyed/herdsman-react-profile/index.html`);
await page.waitForSelector("#run");
const clickSel = async (sel: string) => page.evaluate((s) => (document.querySelector(s) as HTMLElement).click(), sel);
if (op !== "runlots" && op !== "run") await clickSel("#run");
const cdp = await page.createCDPSession();
await cdp.send("Profiler.enable");
await cdp.send("Profiler.setSamplingInterval", { interval: 50 });
await cdp.send("Profiler.start");
for (let i = 0; i < repeat; i++) {
  if (op === "select") await clickSel(`tbody>tr:nth-of-type(${(i % 50) + 2})>td:nth-of-type(2)>a`);
  else if (op === "remove") await clickSel(`tbody>tr:nth-of-type(${i + 2})>td:nth-of-type(3)>a>span`);
  else if (op === "run" || op === "runlots") await clickSel("#" + op);
  else if (op === "add") await clickSel("#add");
  else if (op === "clear") (await clickSel("#clear"), await clickSel("#run"));
  else await clickSel("#" + (op === "swap" ? "swaprows" : op));
  await page.evaluate(() => new Promise((r) => setTimeout(r, 0)));
}
const { profile } = (await cdp.send("Profiler.stop")) as { profile: { nodes: { id: number; callFrame: { functionName: string; lineNumber: number }; hitCount?: number }[]; samples: number[]; timeDeltas: number[] } };
const self = new Map<string, number>();
const byId = new Map(profile.nodes.map((n) => [n.id, n]));
profile.samples.forEach((id, i) => {
  const n = byId.get(id)!;
  const name = `${n.callFrame.functionName || "(anonymous)"}:${n.callFrame.lineNumber}`;
  self.set(name, (self.get(name) ?? 0) + (profile.timeDeltas[i] ?? 0) / 1000);
});
const total = [...self.values()].reduce((a, b) => a + b, 0);
console.log(`${op} x${repeat}: ${total.toFixed(1)} ms sampled`);
for (const [k, v] of [...self].sort((a, b) => b[1] - a[1]).slice(0, 25)) console.log(`${v.toFixed(1).padStart(8)} ms  ${k}`);
await browser.close();
server.stop();
