// Makes a js-framework-benchmark entry from this repository's herdsman-blocks
// sources: the main.ts of the harness's herdsman-blocks-declarative entry,
// built as that entry builds (esbuild, minified IIFE), with the herdsman-blocks
// compiler as an esbuild plugin, or without it.
//
//   bun jfb-entry.ts <js-framework-benchmark dir> <entry name> [--no-compile]
//
// Then (in the harness's webdriver-ts): isKeyed, checkCSP and the runner for
// keyed/<entry name>. The entry reuses herdsman-blocks-declarative's
// node_modules (esbuild) and package-lock.json, which the harness's server
// needs to list it.

import { cpSync, existsSync, mkdirSync, readFileSync, symlinkSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";

const [harness, name] = process.argv.slice(2);
if (!harness || !name)
  throw new Error("usage: bun jfb-entry.ts <js-framework-benchmark dir> <entry name> [--no-compile]");
const compile = !process.argv.includes("--no-compile");
const keyed = join(resolve(harness), "frameworks/keyed");
const decl = join(keyed, "herdsman-blocks-declarative");
const web = join(import.meta.dir, "../../web/src");
const out = join(keyed, name);

mkdirSync(join(out, "src"), { recursive: true });
cpSync(join(decl, "src/main.ts"), join(out, "src/main.ts"));
for (const f of [
  "herdsman-blocks.ts",
  "herdsman-blocks-view.ts",
  "herdsman-blocks-compiled.ts",
  "herdsman-blocks-compiler.ts",
])
  cpSync(join(web, f), join(out, "src", f));
cpSync(join(decl, "package-lock.json"), join(out, "package-lock.json"));
if (!existsSync(join(out, "node_modules")))
  symlinkSync("../herdsman-blocks-declarative/node_modules", join(out, "node_modules"));
writeFileSync(
  join(out, "index.html"),
  readFileSync(join(decl, "index.html"), "utf8").replaceAll("herdsman-blocks-declarative", name),
);
const pkg = JSON.parse(readFileSync(join(decl, "package.json"), "utf8")) as Record<string, unknown>;
pkg["name"] = "js-framework-benchmark-" + name;
pkg["description"] = compile
  ? "herdsman-blocks views compiled at build time by the herdsman-blocks compiler; the source of herdsman-blocks-declarative"
  : "herdsman-blocks-declarative, built by the same script without the compiler";
pkg["scripts"] = { dev: "bun build.ts", "build-prod": "bun build.ts" };
writeFileSync(join(out, "package.json"), JSON.stringify(pkg, null, 2) + "\n");
writeFileSync(
  join(out, "build.ts"),
  `// esbuild, as herdsman-blocks-declarative builds${compile ? ", with the herdsman-blocks compiler" : ""}.
import { build } from "esbuild";
import { readFileSync } from "node:fs";
import { transform } from "./src/herdsman-blocks-compiler.ts";
const runtime = import.meta.dir + "/src/herdsman-blocks-compiled.ts";
const plugin = {
  name: "herdsman-blocks",
  setup(b) {
    b.onLoad({ filter: /main\\.ts$/ }, (args) => {
      const r = transform(readFileSync(args.path, "utf8"), { runtime });
      if (r.errors.length) throw new Error(JSON.stringify(r.errors));
      return { contents: r.code, loader: "ts" };
    });
  },
};
await build({ entryPoints: ["src/main.ts"], bundle: true, format: "iife", minify: true, outfile: "dist/main.js", plugins: ${compile ? "[plugin]" : "[]"} });
`,
);
const p = Bun.spawnSync(["bun", "build.ts"], {
  cwd: out,
  stdout: "inherit",
  stderr: "inherit",
});
if (p.exitCode !== 0) process.exit(1);
console.log(`${name}: dist/main.js ${Bun.file(join(out, "dist/main.js")).size} bytes`);
