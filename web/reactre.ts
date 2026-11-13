import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";

import { type BunPlugin, plugin } from "bun";

import { reactreCompiler } from "./vendor/reactre/packages/compiler/src/index.ts";
import { isExternal } from "./vendor/reactre/packages/compiler/src/program.ts";
import { isIdent, literal, skipToken } from "./vendor/reactre/packages/compiler/src/scan.ts";
import { compile as compileState } from "./vendor/reactre/packages/state/compiler/index.ts";

const stateExports = JSON.parse(
  readFileSync(new URL("./vendor/reactre/packages/state/package.json", import.meta.url), "utf8"),
).exports as Record<string, { "reactre-source": string }>;
const imports = /^@reactre\/(core|dom|state)(\/.*)?$/;

function source(path: string): string {
  const [, pkg, subpath] = /^@reactre\/(core|dom|state)(?:\/(.*))?$/.exec(path)!;
  if (pkg === "state") {
    const key = subpath ? `./${subpath}` : ".";
    const entry = stateExports[key]?.["reactre-source"];
    if (!entry) throw new Error(`Unknown Reactre state entry: ${path}`);
    return join(import.meta.dir, "vendor/reactre/packages/state", entry);
  }
  const entry = pkg === "core" && subpath === "server" ? "server.browser" : (subpath ?? "index");
  return join(import.meta.dir, "vendor/reactre/packages", pkg!, "src", `${entry}.ts`);
}

// The submodule has its own tsconfig, so its imports also need explicit source
// resolution. No package install or upstream dist directory is involved.
export const reactreSources: BunPlugin = {
  name: "reactre-sources",
  setup(build) {
    build.onResolve({ filter: imports }, ({ path }) => ({
      path: source(path),
    }));
  },
};

function compiler(): BunPlugin {
  const root = join(import.meta.dir, "..").replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const filter = new RegExp(
    `^${root}/(?:web/(?:src|test-fixtures|e2e)|bench/web/herdsman-ui-cases/(?:apps/reactre-(?:state|signals)|shared/reactre))/(?!.*\\.machine\\.ts$).*\\.[jt]sx?$`,
  );
  return sourceCompiler(filter);
}

/** Program analysis must resolve the source submodule just as the bundle does.
 * Unknown imports otherwise make the compiler treat plain data as possible stores. */
export function sourceCompiler(filter: RegExp): BunPlugin {
  return {
    name: "herdsman-reactre-compiler",
    setup(build) {
      return reactreCompiler({
        filter,
        analysis: {
          // Runtime preloads have no entrypoints. Builds require complete facts;
          // tests retain upstream's conservative per-file compilation.
          strict: build.config?.entrypoints !== undefined,
          resolve(spec, importer) {
            if (isExternal(spec, build.config?.external)) return false;
            if (imports.test(spec)) return source(spec);
            try {
              return Bun.resolveSync(spec, dirname(importer));
            } catch {
              return null;
            }
          },
        },
      }).setup(build);
    },
  };
}

export function reactre(): BunPlugin[] {
  return [reactreSources, stateCompiler(false), compiler()];
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
      build.onLoad(
        {
          filter: /\/vendor\/reactre\/packages\/(?:core|dom|state)\/src\/.*\.ts$/,
        },
        async ({ path }) => ({
          contents: runtimeImports(await Bun.file(path).text()),
          loader: "ts",
        }),
      );
    },
  });
  plugin(stateCompiler(true));
  plugin(compiler());
}

// Machine modules contain no JSX. Both Bun.build and Bun's runtime load them
// through the same upstream compiler before the TSX plugin claims app files.
export function stateCompiler(dev: boolean): BunPlugin {
  return {
    name: "herdsman-state",
    setup(build) {
      build.onLoad({ filter: /\.machine\.ts$/ }, async ({ path }) => {
        const code = await Bun.file(path).text();
        return {
          contents: runtimeImports(compileState(path, code, { dev })?.code ?? code),
          loader: "ts",
        };
      });
    },
  };
}
