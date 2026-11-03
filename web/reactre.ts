import { join } from "node:path";

import { type BunPlugin, plugin } from "bun";

import { reactreCompiler } from "./vendor/reactre/packages/compiler/src/index.ts";

// The submodule has its own tsconfig, so its imports also need explicit source
// resolution. No package install or upstream dist directory is involved.
export const reactreSources: BunPlugin = {
  name: "reactre-sources",
  setup(build) {
    build.onResolve({ filter: /^@reactre\/(core|dom)(\/.*)?$/ }, ({ path }) => {
      const [, pkg, subpath] = /^@reactre\/(core|dom)(?:\/(.*))?$/.exec(path)!;
      return { path: join(import.meta.dir, "vendor/reactre/packages", pkg!, "src", `${subpath ?? "index"}.ts`) };
    });
  },
};

// The upstream compiler's whole-program walk uses Bun.resolveSync, which also
// needs this resolver at runtime, outside the bundler's onResolve callbacks.
plugin(reactreSources);

export function reactre(): BunPlugin[] {
  return [reactreSources, reactreCompiler()];
}
