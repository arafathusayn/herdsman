import { join } from "node:path";

import { type BunPlugin, plugin } from "bun";

import { reactreCompiler } from "./vendor/reactre/packages/compiler/src/index.ts";
import { isIdent, literal, skipToken } from "./vendor/reactre/packages/compiler/src/scan.ts";

const imports = /^@reactre\/(core|dom)(\/.*)?$/;

function source(path: string): string {
  const [, pkg, subpath] = /^@reactre\/(core|dom)(?:\/(.*))?$/.exec(path)!;
  const entry = pkg === "core" && subpath === "server" ? "server.browser" : (subpath ?? "index");
  return join(import.meta.dir, "vendor/reactre/packages", pkg!, "src", `${entry}.ts`);
}

// The submodule has its own tsconfig, so its imports also need explicit source
// resolution. No package install or upstream dist directory is involved.
export const reactreSources: BunPlugin = {
  name: "reactre-sources",
  setup(build) {
    build.onResolve({ filter: imports }, ({ path }) => ({ path: source(path) }));
  },
};

function compiler(): BunPlugin {
  const dir = import.meta.dir.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const filter = new RegExp(`^${dir}/(?:src|test-fixtures|e2e)/.*\\.[jt]sx?$`);
  return reactreCompiler({ filter });
}

export function reactre(): BunPlugin[] {
  return [reactreSources, compiler()];
}

// Static runtime imports follow the submodule's tsconfig before Bun's resolver
// hooks. Resolve only import/export specifiers, leaving strings and comments
// untouched, so its sources share the same physical modules as app imports.
function runtimeImports(code: string): string {
  let out = "",
    start = 0,
    last = "";
  for (let i = 0; i < code.length;) {
    const c = code[i]!;
    if (/\s/.test(c)) {
      i++;
      continue;
    }
    const end = skipToken(code, i, last, last);
    if (end !== i) {
      if (c === '"' || c === "'") {
        const spec = literal(code.slice(i, end));
        if ((last === "from" || last === "import") && typeof spec === "string" && imports.test(spec)) {
          out += code.slice(start, i) + JSON.stringify(source(spec));
          start = end;
        }
        last = "literal";
      } else if (c !== "/") last = "literal";
      i = end;
    } else if (isIdent(c)) {
      const begin = i++;
      while (isIdent(code[i])) i++;
      last = code.slice(begin, i);
    } else {
      last = c;
      i++;
    }
  }
  return out + code.slice(start);
}

/** Load source imports and compile application TS/TSX during bun test. */
export function registerReactreTests(): void {
  plugin(reactreSources);
  plugin({
    name: "reactre-test-sources",
    setup(build) {
      build.onLoad({ filter: /\/vendor\/reactre\/packages\/(?:core|dom)\/src\/.*\.ts$/ }, async ({ path }) => ({
        contents: runtimeImports(await Bun.file(path).text()),
        loader: "ts",
      }));
    },
  });
  plugin(compiler());
}
