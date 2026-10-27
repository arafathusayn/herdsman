// Resolves `react`, `react-dom` and their subpaths to a runtime at build
// time, as a bundler alias would in a real app: React itself (from
// apps/react), herdsman-blocks-react, or @tanstack/redact (from apps/redact).

import type { BunPlugin } from "bun";
import { join } from "node:path";

const here = import.meta.dir;
const reactDir = join(here, "apps/react");
const redactDir = join(here, "apps/redact");
const blocks = join(here, "../../web/src/herdsman-blocks-react.ts");

// Redact's own alias map (@tanstack/redact/vite), for the client.
const REDACT: Record<string, string> = {
  react: "@tanstack/redact",
  "react/jsx-runtime": "@tanstack/redact/jsx-runtime",
  "react/jsx-dev-runtime": "@tanstack/redact/jsx-runtime",
  "react/compiler-runtime": "@tanstack/redact/compiler-runtime",
  "react-dom": "@tanstack/redact/dom",
  "react-dom/client": "@tanstack/redact/dom-client",
};

export type Runtime = "react" | "blocks" | "redact";

export function reactAlias(runtime: Runtime): BunPlugin {
  return {
    name: "react-alias-" + runtime,
    setup(build) {
      build.onResolve({ filter: /^react(-dom)?(\/.*)?$/ }, (args) => {
        if (runtime === "react") return { path: Bun.resolveSync(args.path, reactDir) };
        if (runtime === "blocks") return { path: blocks };
        const to = REDACT[args.path];
        if (!to) throw new Error(`redact has no ${args.path}`);
        return { path: Bun.resolveSync(to, redactDir) };
      });
    },
  };
}
