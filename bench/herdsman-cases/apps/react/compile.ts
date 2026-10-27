// Builds an app written for React for production, the way apps/react is
// built: Bun bundles its index.html (the shared stylesheet, TSX) with
// process.env.NODE_ENV defined as "production", after a Bun plugin runs the
// React Compiler (babel-plugin-react-compiler through @babel/core) on the
// app's own .tsx/.jsx. Babel only parses TypeScript and JSX and prints them
// back; Bun then strips the types and compiles the JSX. `plugins` run before
// the compiler's, so an alias plugin can swap the runtime React resolves to.
// The compiler and Babel are this app's dependencies, so every React-source
// app builds through here.

import { transformAsync } from "@babel/core";
import reactCompilerPlugin from "babel-plugin-react-compiler";
import type { BunPlugin } from "bun";
import { rmSync } from "node:fs";
import { dirname } from "node:path";

const reactCompiler: BunPlugin = {
  name: "react-compiler",
  setup(build) {
    build.onLoad({ filter: /\.[jt]sx$/ }, async ({ path }) => {
      if (path.includes("/node_modules/")) return undefined;
      const source = await Bun.file(path).text();
      const r = await transformAsync(source, {
        filename: path,
        babelrc: false,
        cwd: dirname(path),
        configFile: false,
        parserOpts: { plugins: ["typescript", "jsx"] },
        plugins: [[reactCompilerPlugin, { target: "19", panicThreshold: "all_errors" }]],
      });
      if (!r?.code) throw new Error(`react compiler: no output for ${path}`);
      return { contents: r.code, loader: path.endsWith(".tsx") ? "tsx" : "jsx" };
    });
  },
};

/** Build `html` into `out` with the React Compiler, after `plugins`. */
export async function buildReactApp(html: string, out: string, plugins: BunPlugin[] = []): Promise<void> {
  rmSync(out, { recursive: true, force: true });
  const r = await Bun.build({
    entrypoints: [html],
    outdir: out,
    minify: true,
    plugins: [...plugins, reactCompiler],
    define: { HERDSMAN_DEV: "false", "process.env.NODE_ENV": JSON.stringify("production") },
  });
  if (!r.success) throw new Error(r.logs.join("\n"));
}
