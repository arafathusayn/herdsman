import { join } from "node:path";
import { plugin } from "bun";

import { reactreSources, sourceCompiler } from "../../../../../web/reactre.ts";

plugin(reactreSources);
const result = await Bun.build({
  entrypoints: [join(import.meta.dir, "index.html")],
  outdir: join(import.meta.dir, "../../dist/reactre-signals"),
  minify: true,
  plugins: [
    reactreSources,
    sourceCompiler(/\/(?:apps\/reactre-signals|shared)\/.*\.[jt]sx?$/),
  ],
});
if (!result.success)
  throw new AggregateError(result.logs, "reactre-signals build failed");
