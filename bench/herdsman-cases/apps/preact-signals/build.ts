// Builds the preact-signals app for production: Bun bundles index.html (the shared
// stylesheet, TSX compiled against preact/jsx-runtime through ./tsconfig.json),
// minified, with process.env.NODE_ENV defined as "production". Writes dist/preact-signals/.
//
//   bun apps/preact-signals/build.ts      (run from bench/herdsman-cases, or anywhere)

import { rmSync } from "node:fs";
import { join } from "node:path";

const app = import.meta.dir;
const out = join(app, "../../dist/preact-signals");

rmSync(out, { recursive: true, force: true });
const r = await Bun.build({
  entrypoints: [join(app, "index.html")],
  outdir: out,
  minify: true,
  define: { HERDSMAN_DEV: "false", "process.env.NODE_ENV": JSON.stringify("production") },
});
if (!r.success) throw new Error(r.logs.join("\n"));
