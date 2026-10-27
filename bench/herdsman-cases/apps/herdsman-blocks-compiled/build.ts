// The herdsman-blocks-declarative app built with the herdsman-blocks plugin:
// the same source, its views compiled at build time. Writes
// dist/herdsman-blocks-compiled/ (index.html and its assets), minified with
// HERDSMAN_DEV false like the other apps.
//
//   bun apps/herdsman-blocks-compiled/build.ts      (from bench/herdsman-cases, or anywhere)
//
// The declarative app itself (`bun build.ts herdsman-blocks-declarative`) is
// the same source without the plugin.

import { rmSync } from "node:fs";
import { basename, join } from "node:path";

import { herdsmanBlocks, type PluginStats } from "../../../../web/src/herdsman-blocks-plugin.ts";

const app = import.meta.dir;
const out = join(app, "../../dist", basename(app));
const stats: PluginStats = {
  files: 0,
  bytes: 0,
  changed: 0,
  views: 0,
  calls: 0,
  ms: 0,
};

rmSync(out, { recursive: true, force: true });
const r = await Bun.build({
  entrypoints: [join(app, "../herdsman-blocks-declarative/index.html")],
  outdir: out,
  minify: true,
  define: { HERDSMAN_DEV: "false" },
  plugins: [herdsmanBlocks({ stats })],
});
if (!r.success) {
  for (const log of r.logs) console.error(log);
  process.exit(1);
}
console.log(`herdsman-blocks-compiled: ${stats.views} views and ${stats.calls} calls compiled`);
