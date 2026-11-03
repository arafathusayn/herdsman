import { plugin } from "bun";

import { reactre, reactreSources } from "./reactre.ts";

plugin(reactreSources);

// HTML entries keep both pages and their assets in Go's embedded filesystem.
// Reactre compiles TSX to templates; tsconfig paths resolve its vendored source.
const result = await Bun.build({
  entrypoints: ["./index.html", "./docs/index.html"],
  outdir: "../internal/web/dist",
  minify: true,
  naming: {
    entry: "[dir]/[name].[ext]",
    asset: "[name].[ext]",
    chunk: "[dir]/[name].[ext]",
  },
  plugins: reactre(),
});

if (!result.success) throw new AggregateError(result.logs, "The web build failed");
