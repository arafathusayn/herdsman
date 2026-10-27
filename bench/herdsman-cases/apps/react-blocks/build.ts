// The react app's own source (../react/main.tsx), unchanged and compiled by
// the React Compiler as apps/react is, with `react` and `react-dom` aliased to
// herdsman-blocks-react (web/src/herdsman-blocks-react.ts). Writes
// dist/react-blocks/.

import { join } from "node:path";
import { reactAlias } from "../../react-alias.ts";
import { buildReactApp } from "../react/compile.ts";

await buildReactApp(join(import.meta.dir, "index.html"), join(import.meta.dir, "../../dist/react-blocks"), [
  reactAlias("blocks"),
]);
