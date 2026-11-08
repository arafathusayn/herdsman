// Bundles the SDK into dist/, keeping one output per module so that each
// contract's service table can be imported alone.
import { Glob } from "bun";

const root = new URL("./src/", import.meta.url).pathname;
const entrypoints = [root + "client.ts", root + "stream.ts"];
for await (const file of new Glob("generated/**/*.ts").scan(root)) {
  entrypoints.push(root + file);
}

const result = await Bun.build({
  entrypoints: entrypoints.sort(),
  root,
  outdir: new URL("./dist/", import.meta.url).pathname,
  target: "browser",
  splitting: true,
});

if (!result.success) {
  for (const log of result.logs) console.error(log);
  process.exit(1);
}
