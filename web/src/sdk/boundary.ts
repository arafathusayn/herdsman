// The rule that keeps the web client on the TypeScript SDK: a file reaches the
// server only through @herdsman/sdk. Run by `make check-web`.
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative, resolve, sep } from "node:path";

/**
 * Files that still call the older HTTP API, for screens the RPC services do
 * not cover yet. This list may only shrink: an entry that stops using that API
 * fails the check until it is removed.
 */
export const legacyClients: readonly string[] = [
  "actions.ts",
  "api.ts",
  "main.tsx",
  "views/agent.tsx",
  "views/artifacts.tsx",
  "views/diff.tsx",
  "views/files.tsx",
  "views/log-view.tsx",
  "views/messages.tsx",
  "views/new-route.tsx",
  "views/pair.tsx",
];

export interface Violation {
  file: string;
  rule: string;
}

export interface BoundaryResult {
  violations: Violation[];
  /** Allowlisted files that were not held to the rule. */
  skipped: string[];
}

// Files the bundle never contains, and this checker, which reads the disk.
const notClientCode = /(\.test\.tsx?|\/test-[^/]*|^test-[^/]*|(^|\/)testdom\.ts|^sdk\/boundary\.ts)$/;
const allowedPackage = /^(@herdsman\/sdk(\/.*)?|@reactre\/.*)$/;
// An object key such as the SDK's own `fetch:` option is not a use of the global.
const directCall = /(?<![\w$])(fetch|XMLHttpRequest|EventSource|WebSocket|sendBeacon)(?![\w$]|\s*:)/g;
const namesReact = /["'`]react["'`]/;
const literal = /"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'|`(?:[^`\\]|\\.)*`/g;

function sources(root: string): string[] {
  return readdirSync(root, { recursive: true, encoding: "utf8" })
    .map((name) => name.split(sep).join("/"))
    .filter((name) => /\.tsx?$/.test(name) && !notClientCode.test(name))
    .sort();
}

/** A resolved path as a module: what the bundler treats as the same file. */
function moduleOf(path: string): string {
  return path
    .replace(/\?.*$/, "")
    .replace(/\.[cm]?[jt]sx?$/, "")
    .replace(/\/index$/, "");
}

function importProblem(root: string, file: string, specifier: string): string | null {
  if (specifier.startsWith(".")) {
    if (/(^|\/)sdk\/ts\//.test(specifier)) return `reaches into the SDK by path: ${specifier}`;
    return moduleOf(resolve(root, dirname(file), specifier)) === join(root, "api")
      ? `imports the legacy client ${specifier}`
      : null;
  }
  return allowedPackage.test(specifier) ? null : `imports ${specifier} outside @herdsman/sdk`;
}

function problemsOf(root: string, file: string): string[] {
  const text = readFileSync(join(root, file), "utf8");
  const transpiler = new Bun.Transpiler({
    loader: file.endsWith("x") ? "tsx" : "ts",
    // Without this, Bun adds an import of its default JSX runtime to each TSX file.
    tsconfig: { compilerOptions: { jsx: "react-jsx", jsxImportSource: "@reactre/core" } },
  });
  const specifiers = new Set(transpiler.scanImports(text).map(({ path }) => path));
  // Bun lists "react" for any JSX, so only a file that names it has imported it.
  if (!namesReact.test(text)) specifiers.delete("react");
  const problems = [...specifiers]
    .map((specifier) => importProblem(root, file, specifier))
    .filter((problem) => problem !== null);
  // The transpiler drops comments and types and turns JSX text into strings.
  const code = transpiler.transformSync(text).replace(literal, '""');
  const called = new Set([...code.matchAll(directCall)].map((match) => match[1]));
  return [...problems, ...[...called].sort().map((name) => `calls ${name} directly`)];
}

/** Checks every client file under `root`, the web app's `src`. */
export function checkBoundary(root: string, legacy: readonly string[]): BoundaryResult {
  const violations: Violation[] = [];
  const skipped: string[] = [];
  for (const file of legacy) {
    if (!existsSync(join(root, file))) {
      violations.push({ file, rule: "is on the legacy list but does not exist; remove it from the list" });
    }
  }
  for (const file of sources(root)) {
    const problems = problemsOf(root, file);
    if (!legacy.includes(file)) violations.push(...problems.map((rule) => ({ file, rule })));
    else if (problems.length > 0) skipped.push(file);
    else violations.push({ file, rule: "is on the legacy list but uses no legacy client; remove it from the list" });
  }
  return { violations, skipped };
}

if (import.meta.main) {
  const root = join(import.meta.dir, "..");
  const { violations, skipped } = checkBoundary(root, legacyClients);
  for (const file of skipped)
    console.log(`allowlisted, still on the older API: ${relative(process.cwd(), join(root, file))}`);
  for (const { file, rule } of violations) console.error(`${relative(process.cwd(), join(root, file))}: ${rule}`);
  if (violations.length > 0) process.exit(1);
  console.log("every other client file reaches the server only through @herdsman/sdk");
}
