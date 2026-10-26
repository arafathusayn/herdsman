// Throughput of the herdsman-blocks build-time compiler (transform only, in
// process): over every module of web/src, and over a synthetic module of
// 10,000 view templates. Reports the median of repeated passes in MB/s.
//
//   bun compiler-throughput.ts

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

import { transform } from "../../web/src/herdsman-blocks-compiler.ts";

const RT = "/rt/herdsman-blocks-compiled.ts";
const src = join(import.meta.dir, "../../web/src");

function median(xs: number[]): number {
  const s = xs.slice().sort((a, b) => a - b);
  return s[s.length >> 1]!;
}

function measure(name: string, files: readonly string[], passes: number): void {
  const bytes = files.reduce((n, f) => n + Buffer.byteLength(f), 0);
  let views = 0;
  for (const f of files) views += transform(f, { runtime: RT, mistakes: "runtime" }).views; // warm up
  const times: number[] = [];
  for (let p = 0; p < passes; p++) {
    const t0 = performance.now();
    for (const f of files) transform(f, { runtime: RT, mistakes: "runtime" });
    times.push(performance.now() - t0);
  }
  const ms = median(times);
  console.log(
    `${name}: ${files.length} files, ${(bytes / 1e6).toFixed(3)} MB, ${views} views; ` +
      `median ${ms.toFixed(2)} ms per pass, ${(bytes / 1e6 / (ms / 1000)).toFixed(0)} MB/s`,
  );
}

const all = (readdirSync(src, { recursive: true }) as string[])
  .filter((f) => f.endsWith(".ts"))
  .map((f) => readFileSync(join(src, f), "utf8"));
measure("web/src, every module", all, 50);
measure(
  "web/src, modules that import the library",
  all.filter((f) => f.includes("herdsman-blocks")),
  50,
);
measure(
  "web/src, the rest (fast path)",
  all.filter((f) => !f.includes("herdsman-blocks")),
  50,
);

// 10,000 templates of every hole kind, each bound and called once.
let synth = `import { attr, child, cls, html, list, on, prop, render, svg, text } from "./herdsman-blocks-view.ts";\n`;
for (let i = 0; i < 10000; i++) {
  switch (i % 4) {
    case 0:
      synth += `const V${i} = html\`<tr class=\${cls()}><td class="col">\${text<number>()}</td><td><a onclick=\${on<"click">()}>\${text()}</a></td></tr>\`;\n`;
      break;
    case 1:
      synth += `const V${i} = html\`<section class="lane" aria-label=\${attr()}>\n  <h2>\${text()} <span class="count">\${text<number>()}</span></h2>\n  \${child()}\n</section>\`;\n`;
      break;
    case 2:
      synth += `const V${i} = html\`<label>\${text()}<input class=\${cls()} .value=\${prop<HTMLInputElement, "value">()} oninput=\${on<"input", HTMLInputElement>()} /></label>\`;\n`;
      break;
    default:
      synth += `const V${i} = svg\`<g class=\${attr()}><circle r="1"/><text>\${text()}</text>\${child()}</g>\`;\n`;
  }
  synth += `export const d${i} = V${i}([${i % 4 === 0 ? `"a", ${i}, () => 0, "b"` : i % 4 === 1 ? `"a", "b", 1, null` : i % 4 === 2 ? `"a", "b", "c", null` : `"a", "b", null`}]);\n`;
}
measure("synthetic module, 10,000 templates", [synth], 7);
