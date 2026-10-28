// Prepares the js-framework-benchmark replica in jfb/.cache (not committed):
//
//   1. A sparse clone of krausest/js-framework-benchmark at the commit of the
//      Chrome 156 results, with the shared stylesheet and the keyed entries
//      that ranked first to fifth there: re, thane, vanillajs-lite,
//      vanillajs and doohtml-dp.
//   2. Each entry built as its own build-prod script builds it, without
//      running any package install script: re is bundled by Bun from its
//      local re-core source, doohtml-dp copies its prebuilt module, thane runs
//      its own compiler, and the two vanillajs entries need no build.
//   3. jfb/app/main.jsx, one React app, bundled twice as entries of their
//      own: `react` with React 19 (from apps/react) and `herdsman-react`
//      with herdsman-react and its compiler (web/src/herdsman-react-compiler.ts),
//      through the same alias plugin as the other React apps.
//
//   bun jfb/prepare.ts [--app-only]      (run from bench/herdsman-cases)
//
// --app-only rebuilds only the two React entries (after a runtime change).

import { cpSync, existsSync, mkdirSync, rmSync, symlinkSync } from "node:fs";
import { join } from "node:path";
import { herdsmanReactCompiler } from "../../../web/src/herdsman-react-compiler.ts";
import { reactAlias, type Runtime } from "../react-alias.ts";

export const JFB_COMMIT = "ca8715d9ae84916fd24de3a79457d8c328f9b766";
export const cache = join(import.meta.dir, ".cache");
export const root = join(cache, "js-framework-benchmark");
export const keyed = join(root, "frameworks/keyed");
/** The official keyed top 5 of the Chrome 156 run, best first. */
export const TOP5 = ["re", "thane", "vanillajs-lite", "vanillajs", "doohtml-dp"];
/** Our app, built for each runtime: React 19, and herdsman-react with its compiler. */
export const OURS: Record<string, { runtime: Runtime; compiler: boolean }> = {
  react: { runtime: "react", compiler: false },
  "herdsman-react": { runtime: "herdsman-react", compiler: true },
};

function sh(cmd: string[], cwd: string): void {
  const p = Bun.spawnSync(cmd, { cwd, stdout: "inherit", stderr: "inherit" });
  if (p.exitCode !== 0) throw new Error(`${cmd.join(" ")} (in ${cwd}) exited ${p.exitCode}`);
}

function fetchRepo(): void {
  if (existsSync(join(root, ".git"))) return;
  mkdirSync(cache, { recursive: true });
  sh(["git", "clone", "--filter=blob:none", "--sparse", "--no-checkout", "https://github.com/krausest/js-framework-benchmark.git", root], cache);
  sh(["git", "sparse-checkout", "set", "css", ...TOP5.map((f) => "frameworks/keyed/" + f)], root);
  sh(["git", "checkout", JFB_COMMIT], root);
}

function buildTop5(): void {
  // re: esbuild src/main.ts --bundle --format=iife --minify, with @re/core from file:re-core.
  const re = join(keyed, "re");
  mkdirSync(join(re, "node_modules/@re"), { recursive: true });
  if (!existsSync(join(re, "node_modules/@re/core"))) symlinkSync("../../re-core", join(re, "node_modules/@re/core"));
  sh(["bun", "build", "src/main.ts", "--format=iife", "--minify", "--outfile=dist/bundle.js"], re);
  // doohtml-dp: copies doohtml's prebuilt module.
  const doo = join(keyed, "doohtml-dp");
  sh(["bun", "install", "--ignore-scripts"], doo);
  mkdirSync(join(doo, "dist"), { recursive: true });
  cpSync(join(doo, "node_modules/doohtml/dist/doohtml.mjs"), join(doo, "dist/doohtml.mjs"));
  // thane: its own compiler, with the entry's build-prod arguments.
  const thane = join(keyed, "thane");
  sh(["bun", "install", "--ignore-scripts"], thane);
  sh(
    ["bun", "node_modules/thane/dist/compiler/cli/thane.js", "build", "--prod", "--entry", "./main.ts", "--out", "./dist", "--assets", "./assets", "--html", "./index.html", "--base", "/frameworks/keyed/thane/dist/"],
    thane,
  );
}

export async function buildOurs(): Promise<void> {
  const html = await Bun.file(join(import.meta.dir, "app/index.html")).text();
    for (const [name, { runtime, compiler }] of Object.entries(OURS)) {
    const dir = join(keyed, name);
    rmSync(dir, { recursive: true, force: true });
    mkdirSync(join(dir, "dist"), { recursive: true });
    const r = await Bun.build({
      entrypoints: [join(import.meta.dir, "app/main.jsx")],
      format: "iife",
      minify: true,
            plugins: [...(compiler ? [herdsmanReactCompiler()] : []), reactAlias(runtime)],
      define: { "process.env.NODE_ENV": JSON.stringify("production") },
    });
    if (!r.success) throw new Error(r.logs.join("\n"));
    await Bun.write(join(dir, "dist/main.js"), r.outputs[0]!);
    await Bun.write(join(dir, "index.html"), html.replace("NAME", name));
  }
}

if (import.meta.main) {
  if (!process.argv.includes("--app-only")) {
    fetchRepo();
    buildTop5();
  }
  await buildOurs();
  console.log(`ready: ${[...TOP5, ...Object.keys(OURS)].join(", ")} in ${keyed}`);
}
