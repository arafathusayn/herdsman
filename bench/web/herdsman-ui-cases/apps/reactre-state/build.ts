import { join } from "node:path";
import { plugin } from "bun";

import {
  reactreSources,
  sourceCompiler,
  stateCompiler,
} from "../../../../../web/reactre.ts";

plugin(reactreSources);
const result = await Bun.build({
  entrypoints: [join(import.meta.dir, "index.html")],
  outdir: join(import.meta.dir, "../../dist/reactre-state"),
  minify: true,
  plugins: [
    reactreSources,
    stateCompiler(false),
    sourceCompiler(/\/(?:apps\/reactre-state|shared)\/.*\.[jt]sx?$/),
  ],
});
if (!result.success)
  throw new AggregateError(result.logs, "reactre-state build failed");
