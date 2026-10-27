// The production build of the web app: the same output as
//   bun build ./index.html ./docs/index.html --outdir <out> --minify \
//     --entry-naming '[dir]/[name].[ext]' --asset-naming '[name].[ext]' --chunk-naming '[dir]/[name].[ext]'
// plus the herdsman-blocks plugin, which compiles the views at build time
// (the bun build CLI takes no plugins).
//
//   bun build.ts [--outdir dir] [--no-compile]
//
// --no-compile builds without the plugin: the views compile at run time, as
// they do in tests.

import { herdsmanBlocks, type PluginStats } from "./src/herdsman-blocks-plugin.ts";

const args = process.argv.slice(2);
const at = args.indexOf("--outdir");
const outdir = at >= 0 ? args[at + 1]! : "../internal/web/dist";
const compile = !args.includes("--no-compile");

const stats: PluginStats = { files: 0, bytes: 0, changed: 0, views: 0, calls: 0, ms: 0 };
const t0 = performance.now();
const r = await Bun.build({
  entrypoints: ["./index.html", "./docs/index.html"],
  outdir,
  minify: true,
  naming: { entry: "[dir]/[name].[ext]", asset: "[name].[ext]", chunk: "[dir]/[name].[ext]" },
  plugins: compile ? [herdsmanBlocks({ stats })] : [],
});
if (!r.success) {
  for (const log of r.logs) console.error(log);
  process.exit(1);
}
const ms = performance.now() - t0;
if (compile) {
  const mb = stats.bytes / 1e6;
  console.log(
    `herdsman-blocks: ${stats.views} views and ${stats.calls} calls compiled in ${stats.changed} of ${stats.files} files ` +
      `(${(mb * 1000).toFixed(0)} kB) in ${stats.ms.toFixed(1)} ms, ${(mb / (stats.ms / 1000)).toFixed(0)} MB/s; build ${ms.toFixed(0)} ms`,
  );
}
