import { expect, test } from "bun:test";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { reactre } from "./reactre.ts";
import { type FakeElement, installFakeDocument } from "./src/herdsman-blocks.testdom.ts";
import { dispatch, find } from "./src/testdom.ts";

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
    app.setSelected(2);
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
