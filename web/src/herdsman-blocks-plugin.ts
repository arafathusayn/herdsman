// The Bun plugin that compiles herdsman-blocks views at build time (see
// herdsman-blocks-compiler.ts). Use it with Bun.build, or as a run-time
// plugin (Bun.plugin) for tests:
//
//   await Bun.build({ entrypoints, plugins: [herdsmanBlocks()] });
//
// Template mistakes fail the build with file:line:column. Literals the
// compiler leaves to run time are reported as warnings (unless quiet).

import type { BunPlugin } from "bun";

import { transform } from "./herdsman-blocks-compiler.ts";

export interface PluginOptions {
  /** Do not print warnings. */
  readonly quiet?: boolean;
  /** "error" (default): template mistakes fail the build. "runtime": leave them to run time, where they throw. */
  readonly mistakes?: "error" | "runtime";
  /** Filled with totals as files are compiled. */
  readonly stats?: PluginStats;
}

export interface PluginStats {
  files: number;
  bytes: number;
  changed: number;
  views: number;
  calls: number;
  /** Time spent in transform, milliseconds. */
  ms: number;
}

/** Where generated code imports its runtime from: this module's sibling. */
export const RUNTIME: string = import.meta.dir + "/herdsman-blocks-compiled.ts";

// The library's own modules are never rewritten.
const OWN = /\/herdsman-blocks(?:-view|-compiled|-compiler|-plugin)?\.ts$/;

export function herdsmanBlocks(options: PluginOptions = {}): BunPlugin {
  return {
    name: "herdsman-blocks",
    setup(build) {
      build.onLoad({ filter: /\.[cm]?[jt]sx?$/ }, async (args) => {
        const source = await Bun.file(args.path).text();
        // (Run-time plugins must return contents, so untouched files are returned as read.)
        if (args.path.includes("/node_modules/") || OWN.test(args.path))
          return { contents: source, loader: args.loader };
        const t0 = performance.now();
        // JSX and TSX are never rewritten (a warning says so when they use views).
        const r = transform(source, {
          runtime: RUNTIME,
          mistakes: options.mistakes ?? "error",
          jsx: args.path.endsWith("x"),
        });
        const st = options.stats;
        if (st) {
          st.ms += performance.now() - t0;
          st.files++;
          st.bytes += source.length;
          if (r.changed) st.changed++;
          st.views += r.views;
          st.calls += r.calls;
        }
        if (!options.quiet)
          for (const w of r.warnings) console.warn(`${args.path}:${w.line}:${w.column}: warning: ${w.message}`);
        if (r.errors.length > 0)
          throw new Error(r.errors.map((e) => `${args.path}:${e.line}:${e.column}: ${e.message}`).join("\n"));
        return { contents: r.code, loader: args.loader };
      });
    },
  };
}
