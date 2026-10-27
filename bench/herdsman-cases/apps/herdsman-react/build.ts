// The react app's own source (../react/main.tsx), unchanged and compiled by
// the React Compiler as apps/react is, with `react` and `react-dom` aliased to
// herdsman-react (web/src/herdsman-react.ts). Writes
// dist/herdsman-react/.

import { join } from "node:path";
import { reactAlias } from "../../react-alias.ts";
import { buildReactApp } from "../react/compile.ts";

await buildReactApp(join(import.meta.dir, "index.html"), join(import.meta.dir, "../../dist/herdsman-react"), [
  reactAlias("herdsman-react"),
]);
