// Builds the react app for production with the React Compiler (./compile.ts).
// Writes dist/react/.
//
//   bun apps/react/build.ts      (run from bench/herdsman-cases, or anywhere)

import { join } from "node:path";
import { buildReactApp } from "./compile.ts";

await buildReactApp(join(import.meta.dir, "index.html"), join(import.meta.dir, "../../dist/react"));
