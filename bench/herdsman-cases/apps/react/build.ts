// Builds the react app for production: Bun bundles index.html (the shared
// stylesheet, TSX) as the default build does, with process.env.NODE_ENV defined
// as "production" so React's development checks drop out. A Bun plugin first
// runs the React Compiler (babel-plugin-react-compiler through @babel/core) on
// this app's .tsx/.jsx; Babel only parses TypeScript and JSX and prints them
// back, and Bun then strips the types and compiles the JSX. Writes dist/react/.
//
//   bun apps/react/build.ts      (run from bench/herdsman-cases, or anywhere)

import { transformAsync } from "@babel/core";
import reactCompilerPlugin from "babel-plugin-react-compiler";
import type { BunPlugin } from "bun";
import { rmSync } from "node:fs";
import { join } from "node:path";

const app = import.meta.dir;
const out = join(app, "../../dist/react");

const reactCompiler: BunPlugin = {
  name: "react-compiler",
  setup(build) {
    build.onLoad({ filter: /\.[jt]sx$/ }, async ({ path }) => {
      if (path.includes("/node_modules/")) return undefined;
      const source = await Bun.file(path).text();
      const r = await transformAsync(source, {
        filename: path,
        babelrc: false,
        cwd: app,
        configFile: false,
        parserOpts: { plugins: ["typescript", "jsx"] },
        plugins: [[reactCompilerPlugin, { target: "19", panicThreshold: "all_errors" }]],
      });
      if (!r?.code) throw new Error(`react compiler: no output for ${path}`);
      return { contents: r.code, loader: path.endsWith(".tsx") ? "tsx" : "jsx" };
    });
  },
};

rmSync(out, { recursive: true, force: true });
const r = await Bun.build({
  entrypoints: [join(app, "index.html")],
  outdir: out,
  minify: true,
  plugins: [reactCompiler],
  define: { HERDSMAN_DEV: "false", "process.env.NODE_ENV": JSON.stringify("production") },
});
if (!r.success) throw new Error(r.logs.join("\n"));
