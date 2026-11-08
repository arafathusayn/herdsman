// Builds every app under apps/ into dist/<name>/ and records each app's
// JavaScript size in dist/sizes.json. An app with its own build.ts builds
// itself (contract: it writes dist/<name>/index.html plus assets); the rest
// bundle their index.html with Bun, minified, with HERDSMAN_DEV false.
// herdsman-patch is also built as herdsman-patch-control, the A/A control the runner reports.
//
//   bun build.ts [names...]

import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync, statSync } from "node:fs";
import { join } from "node:path";

const here = import.meta.dir;
const appsDir = join(here, "apps");
const distDir = join(here, "dist");

export const CONTROL = "herdsman-patch-control";

export interface Size {
  raw: number;
  gzip: number;
}

function appNames(): string[] {
  return readdirSync(appsDir)
    .filter((n) => existsSync(join(appsDir, n, "index.html")) || existsSync(join(appsDir, n, "build.ts")))
    .sort();
}

async function buildApp(name: string, out: string): Promise<void> {
  rmSync(join(distDir, out), { recursive: true, force: true });
  const own = join(appsDir, name, "build.ts");
  if (existsSync(own)) {
    const p = Bun.spawnSync(["bun", own], { cwd: here, stdout: "inherit", stderr: "inherit" });
    if (p.exitCode !== 0) throw new Error(`${name}: build.ts exited ${p.exitCode}`);
    if (out !== name) {
      // The app's own build writes dist/<name>; copy it under the control's name.
      Bun.spawnSync(["cp", "-R", join(distDir, name), join(distDir, out)]);
    }
  } else {
    const r = await Bun.build({
      entrypoints: [join(appsDir, name, "index.html")],
      outdir: join(distDir, out),
      minify: true,
      define: { HERDSMAN_DEV: "false" },
    });
    if (!r.success) throw new Error(`${name}: ${r.logs.join("\n")}`);
  }
  if (!existsSync(join(distDir, out, "index.html"))) throw new Error(`${name}: no dist/${out}/index.html`);
}

/** Every .js file under dir, raw and gzipped bytes, summed. */
function jsSize(dir: string): Size {
  const size: Size = { raw: 0, gzip: 0 };
  for (const f of readdirSync(dir, { recursive: true }) as string[]) {
    const p = join(dir, f);
    if (!f.endsWith(".js") || !statSync(p).isFile()) continue;
    const bytes = new Uint8Array(readFileSync(p));
    size.raw += bytes.length;
    size.gzip += Bun.gzipSync(bytes, { level: 9 }).length;
  }
  return size;
}

async function main(): Promise<void> {
  const all = appNames();
  const asked = process.argv.slice(2);
  for (const n of asked) if (!all.includes(n)) throw new Error(`no app ${n} in apps/ (have ${all.join(", ")})`);
  const names = asked.length ? asked : all;
  mkdirSync(distDir, { recursive: true });

  const sizesFile = join(distDir, "sizes.json");
  const sizes: Record<string, Size> = existsSync(sizesFile) ? await Bun.file(sizesFile).json() : {};
  const failed: string[] = [];
  for (const name of names) {
    const outs = name === "herdsman-patch" ? [name, CONTROL] : [name];
    for (const out of outs) {
      const t = performance.now();
      try {
        await buildApp(name, out);
      } catch (e) {
        // One broken app must not stop the others.
        failed.push(out);
        delete sizes[out];
        console.error(`${out.padEnd(16)} FAILED ${e instanceof AggregateError ? e.errors.map(String).join("\n") : (e as Error).message}`);
        continue;
      }
      sizes[out] = jsSize(join(distDir, out));
      const s = sizes[out]!;
      console.log(`${out.padEnd(16)} ${String(s.raw).padStart(7)} B  ${String(s.gzip).padStart(6)} B gzip  ${(performance.now() - t).toFixed(0)} ms`);
    }
  }
  await Bun.write(sizesFile, JSON.stringify(sizes, null, 2) + "\n");
  if (failed.length) {
    console.error(`failed: ${failed.join(", ")}`);
    process.exitCode = 1;
  }
}

if (import.meta.main) await main();
