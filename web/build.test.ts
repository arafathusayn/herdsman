import { expect, test } from "bun:test";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { reactre, sourceCompiler } from "./reactre.ts";
import { type FakeElement, installFakeDocument } from "./src/test-dom-core.ts";
import { dispatch, find } from "./src/testdom.ts";

test("source analysis rejects opaque plugin imports unless explicitly external", async () => {
  const dir = await mkdtemp(join(tmpdir(), "herdsman-analysis-"));
  const entry = join(dir, "entry.ts");
  try {
    await writeFile(entry, 'import { value } from "opaque-test-module"; export { value };');
    const virtual = {
      name: "opaque-test-module",
      setup(build: import("bun").PluginBuilder) {
        build.onResolve({ filter: /^opaque-test-module$/ }, () => ({ path: "opaque", namespace: "test" }));
        build.onLoad({ filter: /.*/, namespace: "test" }, () => ({ contents: "export const value = 1", loader: "js" }));
      },
    } satisfies import("bun").BunPlugin;
    const compile = (external: string[]) =>
      Bun.build({
        entrypoints: [entry],
        target: "browser",
        external,
        plugins: [...(external.length ? [] : [virtual]), sourceCompiler(/\.ts$/)],
      });
    // Bun versions may reject the build promise or return failed logs.
    const rejected = await compile([]).then(
      (result) => ({ failed: !result.success, message: result.logs.map(String).join("\n") }),
      (error: unknown) => ({
        failed: true,
        message: error instanceof AggregateError ? error.errors.map(String).join("\n") : String(error),
      }),
    );
    expect(rejected.failed).toBe(true);
    expect(rejected.message).toContain("opaque-test-module");
    const external = await compile(["opaque-test-module"]);
    expect(external.success).toBe(true);
    expect(await external.outputs[0]!.text()).toContain('"opaque-test-module"');
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("vendored Reactre compiles TSX and updates keyed DOM without installed packages", async () => {
  const outdir = await mkdtemp(join(tmpdir(), "herdsman-reactre-"));
  try {
    const build = await Bun.build({
      entrypoints: [join(import.meta.dir, "test-fixtures/reactre.tsx")],
      outdir,
      target: "browser",
      plugins: reactre(),
    });
    expect(build.success).toBe(true);
    if (!build.success) throw new AggregateError(build.logs);
    const code = await build.outputs[0]!.text();
    expect(code).not.toContain('from "@reactre/');
    expect(code).not.toContain('from "react');

    installFakeDocument();
    const root = document.createElement("main");
    const { mount } = (await import(build.outputs[0]!.path)) as typeof import("./test-fixtures/reactre.tsx");
    const app = mount(root);
    const section = root.firstChild as unknown as FakeElement;
    const button = find(section, (e) => e.tagName === "BUTTON")!;
    const list = find(section, (e) => e.tagName === "UL")!;
    const first = list.firstChild as FakeElement;
    const second = list.lastChild as FakeElement;
    expect(button.textContent).toBe("0");
    dispatch(button, "click");
    expect(button.textContent).toBe("1");
    expect(find(section, (e) => e.tagName === "P")?.textContent).toBe("updated");
    const initialLabelReads = app.labelReads();
    app.setSelected(2);
    // Selection should touch the two classes, not re-read plain row labels.
    // This fails when unresolved imports disable whole-program store analysis.
    expect(app.labelReads()).toBe(initialLabelReads);
    expect(first.className).toBe("");
    expect(second.className).toBe("selected");
    app.setRows([
      { id: 2, label: "TWO" },
      { id: 1, label: "one" },
    ]);
    expect(list.firstChild).toBe(second);
    expect(list.lastChild).toBe(first);
    expect(second.textContent).toBe("TWO");
    app.dispose();
    expect((root as unknown as FakeElement).childNodes).toHaveLength(0);
  } finally {
    await rm(outdir, { recursive: true, force: true });
  }
});

test("browser bundles compile .ts memo options and select the browser server entry", async () => {
  const outdir = await mkdtemp(join(tmpdir(), "herdsman-reactre-memo-"));
  try {
    const build = await Bun.build({
      entrypoints: [join(import.meta.dir, "test-fixtures/memo.ts")],
      outdir,
      target: "browser",
      minify: true,
      plugins: reactre(),
    });
    if (!build.success) throw new AggregateError(build.logs);
    const { isServer, makeMemo } = (await import(build.outputs[0]!.path)) as typeof import("./test-fixtures/memo.ts");
    expect(isServer).toBe(false);
    const state = makeMemo();
    expect(state.runs).toBe(0);
    const dispose = state.subscribe();
    expect(state.seen).toBe(2);
    state.setValue(2);
    expect(state.seen).toBe(4);
    dispose();
    await Promise.resolve();
    expect(state.releases).toBe(1);
    state.setValue(3);
    expect(state.runs).toBe(2);
  } finally {
    await rm(outdir, { recursive: true, force: true });
  }
});

// Build-only upstreams have a separate budget from the Go modules. These are
// all published packages of one project; none is browser/runtime code.
const buildDependencyBudget = [
  "oxc-parser",
  "@oxc-project/types",
  ...["darwin-arm64", "darwin-x64", "linux-arm64-gnu", "linux-arm64-musl", "linux-x64-gnu", "linux-x64-musl"].map(
    (target) => `@oxc-parser/binding-${target}`,
  ),
];
test("the state compiler uses only the reviewed, checksummed local parser packages", async () => {
  const { readdirSync, readFileSync } = await import("node:fs");
  const { createHash } = await import("node:crypto");
  const root = join(import.meta.dir, "vendor/oxc");
  const pins = JSON.parse(readFileSync(join(root, "provenance.json"), "utf8")) as {
    name: string;
    version: string;
    directory: string;
    integrity: string;
  }[];
  expect(pins.map((pin) => pin.name).sort()).toEqual([...buildDependencyBudget].sort());
  expect(readFileSync(join(root, "LICENSE"), "utf8")).toBe(readFileSync(join(root, "oxc-parser/LICENSE"), "utf8"));
  const files = JSON.parse(readFileSync(join(root, "sha256.json"), "utf8")) as Record<string, string>;
  const actual = pins
    .flatMap((pin) =>
      readdirSync(join(root, pin.directory), { recursive: true, withFileTypes: true })
        .filter((entry) => entry.isFile())
        .map((entry) => join(entry.parentPath, entry.name).slice(root.length + 1)),
    )
    .sort();
  expect(actual).toEqual(Object.keys(files).sort());
  for (const [file, sum] of Object.entries(files))
    expect(
      createHash("sha256")
        .update(readFileSync(join(root, file)))
        .digest("hex"),
    ).toBe(sum);
  for (const pin of pins) {
    expect(pin.version).toBe("0.156.0");
    expect(pin.integrity).toMatch(/^sha512-/);
    expect(JSON.parse(readFileSync(join(root, pin.directory, "package.json"), "utf8")).license).toBe("MIT");
  }
  const manifest = JSON.parse(readFileSync(join(import.meta.dir, "package.json"), "utf8")) as {
    devDependencies: Record<string, string>;
    overrides: Record<string, string>;
  };
  expect(Object.keys(manifest.devDependencies)).toEqual(["oxc-parser"]);
  expect(Object.keys(manifest.overrides).sort()).toEqual(
    buildDependencyBudget.filter((name) => name !== "oxc-parser").sort(),
  );
  for (const [name, path] of Object.entries({ ...manifest.devDependencies, ...manifest.overrides })) {
    const pin = pins.find((pin) => pin.name === name)!;
    expect(path).toBe(`file:vendor/oxc/${pin.directory}`);
  }
});
