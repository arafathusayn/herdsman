// Builds the thane app with thane's own compiler (`thane build --prod`, as the
// official js-framework-benchmark entry builds), then writes
// dist/thane/index.html with the shared page shell and the stylesheet bundled
// by Bun the way the default index.html build bundles it.
//
//   bun apps/thane/build.ts      (run from bench/herdsman-cases, or anywhere)

import { existsSync, mkdirSync, readdirSync, rmSync } from "node:fs";
import { join } from "node:path";

const app = import.meta.dir;
const out = join(app, "../../dist/thane");
const tmp = join(app, ".thane-out");

// thane is pinned to a git commit (see package.json), which ships source only:
// build its dist/ once with its own tsconfig, as its `bun run build` does.
const pkg = join(app, "node_modules/thane");
const cli = join(pkg, "dist/compiler/cli/thane.js");
if (!existsSync(cli)) {
  const tsc = Bun.spawnSync(["bun", join(app, "node_modules/typescript/bin/tsc"), "-p", "tsconfig.build.json"], { cwd: pkg, stdout: "inherit", stderr: "inherit" });
  if (tsc.exitCode !== 0 || !existsSync(cli)) throw new Error(`thane: building its dist/ failed (${tsc.exitCode}); run bun install in apps/thane`);
}

rmSync(out, { recursive: true, force: true });
rmSync(tmp, { recursive: true, force: true });
mkdirSync(out, { recursive: true });

const thane = Bun.spawnSync(
  ["bun", cli, "build", "--prod", "--entry", "./main.ts", "--out", tmp, "--html", "./index.html", "--no-hash-file-names", "--no-splitting", "--quiet"],
  { cwd: app, stdout: "inherit", stderr: "inherit" },
);
if (thane.exitCode !== 0) throw new Error(`thane build exited ${thane.exitCode}`);

// thane's compiler writes the bundle as main.js; its HTML rewrite is not used.
const js = readdirSync(tmp).filter((f) => f.endsWith(".js"));
if (js.length !== 1) throw new Error(`thane build: expected one .js file, got ${js.join(", ")}`);
await Bun.write(join(out, "main.js"), Bun.file(join(tmp, js[0]!)));
rmSync(tmp, { recursive: true, force: true });

const css = await Bun.build({ entrypoints: [join(app, "../../../../web/styles/app.css")], outdir: out, minify: true, naming: "app.[ext]" });
if (!css.success) throw new Error(css.logs.join("\n"));

await Bun.write(
  join(out, "index.html"),
  `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>herdsman-cases: thane</title>
    <link rel="stylesheet" href="./app.css" />
  </head>
  <body>
    <div id="toolbar"></div>
    <div class="route-body" id="main"></div>
    <script type="module" src="./main.js"></script>
  </body>
</html>
`,
);
