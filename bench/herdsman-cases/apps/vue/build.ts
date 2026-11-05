// Builds the Vue app the way Vite + @vitejs/plugin-vue builds it for production,
// with Bun instead of Vite: each .vue file goes through @vue/compiler-sfc
// (compileScript with the template inlined, as plugin-vue does in production),
// "vue" resolves to dist/vue.runtime.esm-bundler.js (runtime only), and
// NODE_ENV and the feature flags are defined as a Vite production build defines
// them, with the Options API flag off (these components use script setup). Writes
// dist/<app>/index.html, main.js and app.css.
//
//   bun apps/vue/build.ts      (run from bench/herdsman-cases, or anywhere)

import { mkdirSync, rmSync } from "node:fs";
import { basename, join } from "node:path";
import { compileScript, parse } from "@vue/compiler-sfc";
import type { BunPlugin } from "bun";

const app = import.meta.dir;
const name = basename(app);
const out = join(app, "../../dist", name);

const sfc: BunPlugin = {
  name: "vue-sfc",
  setup(build) {
    build.onLoad({ filter: /\.vue$/ }, async ({ path }) => {
      const source = await Bun.file(path).text();
      const { descriptor, errors } = parse(source, { filename: path });
      if (errors.length) throw new Error(errors.map(String).join("\n"));
      const id = Bun.hash(path).toString(16).slice(0, 8);
      const script = compileScript(descriptor, { id, isProd: true, inlineTemplate: true, genDefaultAs: "_sfc_main" });
      const lang = descriptor.scriptSetup?.lang ?? descriptor.script?.lang;
      return { contents: `${script.content}\nexport default _sfc_main;\n`, loader: lang === "ts" ? "ts" : "js" };
    });
  },
};

rmSync(out, { recursive: true, force: true });
mkdirSync(out, { recursive: true });

const js = await Bun.build({
  entrypoints: [join(app, "main.ts")],
  outdir: out,
  target: "browser",
  minify: true,
  naming: "main.[ext]",
  plugins: [sfc],
  define: {
    "process.env.NODE_ENV": JSON.stringify("production"),
    __VUE_OPTIONS_API__: "false",
    __VUE_PROD_DEVTOOLS__: "false",
    __VUE_PROD_HYDRATION_MISMATCH_DETAILS__: "false",
  },
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
