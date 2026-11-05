// Builds the Solid 2.0 (rc) app the way the official js-framework-benchmark entry does
// (rollup + @rollup/plugin-babel with babel-preset-solid, omitNestedClosingTags; here the 2.0 preset),
// with Bun in rollup's place: a Bun.build plugin runs @babel/core with the
// preset on the .jsx entry, so the compiled DOM output is Solid's own; Bun
// resolves solid-js and @solidjs/web through their browser production export (no dev build),
// bundles and minifies. Writes dist/solid/index.html with the shared page
// shell and the stylesheet bundled as the default index.html build bundles it.
//
//   bun apps/solid-2/build.ts      (run from bench/herdsman-cases, or anywhere)

import { rmSync } from "node:fs";
import { join } from "node:path";
import { transformAsync } from "@babel/core";
import type { BunPlugin } from "bun";

const app = import.meta.dir;
const name = "solid-2";
const out = join(app, "../../dist", name);

const solidJsx: BunPlugin = {
  name: "babel-preset-solid",
  setup(build) {
    build.onLoad({ filter: /\.jsx$/ }, async ({ path }) => {
      const r = await transformAsync(await Bun.file(path).text(), {
        filename: path,
        babelrc: false,
        configFile: false,
        presets: [[join(app, "node_modules/babel-preset-solid"), { omitNestedClosingTags: true }]],
      });
      return { contents: r!.code!, loader: "js" };
    });
  },
};

rmSync(out, { recursive: true, force: true });

const js = await Bun.build({
  entrypoints: [join(app, "main.jsx")],
  outdir: out,
  target: "browser",
  // Solid's browser production build: never the "development" condition.
  conditions: ["browser", "production"],
  define: { "process.env.NODE_ENV": '"production"' },
  minify: true,
  naming: "main.[ext]",
  plugins: [solidJsx],
});
if (!js.success) throw new Error(js.logs.join("\n"));

const css = await Bun.build({ entrypoints: [join(app, "../../../../web/styles/app.css")], outdir: out, minify: true, naming: "app.[ext]" });
if (!css.success) throw new Error(css.logs.join("\n"));

await Bun.write(
  join(out, "index.html"),
  `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>herdsman-cases: ${name}</title>
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
