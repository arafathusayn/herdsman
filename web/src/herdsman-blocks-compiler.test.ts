// The build-time compiler: what its scanner recognizes and leaves alone, the
// code it generates (golden snapshots), and the template mistakes it turns
// into build errors with file:line:column.
import { describe, expect, test } from "bun:test";
import { mkdtempSync, readdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { compileView, transform } from "./herdsman-blocks-compiler.ts";
import { herdsmanBlocks } from "./herdsman-blocks-plugin.ts";

const RT = "/rt/herdsman-blocks-compiled.ts";
const run = (src: string) => transform(src, { runtime: RT });
const IMPORT = `import { attr, child, cls, html, on, prop, svg, text } from "./herdsman-blocks-view.ts";\n`;
const lines = (s: string): number => s.split("\n").length;

describe("scanner", () => {
  test("a module that never names the library is returned as is", () => {
    const src = "const html = (s: TemplateStringsArray) => s;\nexport const x = html`<p>${1}</p>`;\n";
    const r = run(src);
    expect(r.changed).toBe(false);
    expect(r.code).toBe(src);
  });

  test("a module that only mentions the library, or imports another html, is unchanged", () => {
    const src = `// herdsman-blocks is not imported here\nimport { html } from "lit";\nconst a = html\`<p>\${1}</p>\`;\n`;
    expect(run(src).code).toBe(src);
  });

  test("view literals compile; everything else stays byte for byte, on the same lines", () => {
    const body = [
      "const s1 = 'html`<p>${text()}</p>`';",
      'const s2 = "`" + `${"}"}`;',
      "// html`<b>${text()}</b>` in a comment",
      "/* html`<b>${text()}</b>` */",
      "const re = /`html`[`]/g, div = 4 / 2 / 1;",
      "const nested = `a ${`b ${{ c: 1 }.c} ${html`<i>${text()}</i>`}`} d`;",
      "const obj = { html: (s: unknown) => s };",
      "const notMine = obj.html`<u>${0}</u>`;",
      "const css = String.raw`a${1}b`;",
      "const View = html`<p class=${cls()}>",
      "  ${text()}",
      "</p>`;",
    ].join("\n");
    const src = IMPORT + body + "\n";
    const r = run(src);
    expect(r.errors).toEqual([]);
    expect(r.views).toBe(2);
    expect(lines(r.code)).toBe(lines(src));
    const out = r.code.split("\n");
    const inp = src.split("\n");
    // Line 1 gains the generated definitions and points the import at the runtime.
    expect(out[0]!.startsWith(`import * as $hb from ${JSON.stringify(RT)};var $hbv0=`)).toBe(true);
    expect(out[0]!.endsWith(`from ${JSON.stringify(RT)};`)).toBe(true);
    // Untouched lines are identical.
    for (const i of [1, 2, 3, 4, 5, 7, 8, 9]) expect(out[i]).toBe(inp[i]!);
    expect(out[6]).toBe("const nested = `a ${`b ${{ c: 1 }.c} ${$hbv0.i()}`} d`;");
    expect(out.slice(10, 13)).toEqual(["const View = $hbv1.i()", "", ";"]);
  });

  test("aliases and namespace imports are followed; herdsman-blocks' own templates are checked, not compiled", () => {
    const src = [
      'import { html as view, text as t } from "./herdsman-blocks-view.ts";',
      'import * as V from "../herdsman-blocks-view.ts";',
      'import { html as block, cls } from "./herdsman-blocks.ts";',
      "const A = view`<p>${t()}</p>`;",
      "const B = V.html`<p>${V.text()}</p>`;",
      "const C = block`<p class=${cls()}>${0}</p>`;",
    ].join("\n");
    const r = run(src);
    expect(r.views).toBe(2);
    expect(r.code).toContain("const A = $hbv0.i();");
    expect(r.code).toContain("const B = $hbv1.i();");
    expect(r.code).toContain("const C = block`<p class=${cls()}>${0}</p>`;");
    expect(r.code).toContain(`import * as V from ${JSON.stringify(RT)};`);
  });

  test("calls of a view bound in the module construct its description directly", () => {
    const src =
      IMPORT +
      [
        "const Row = html`<tr class=${cls()}><td>${text()}</td></tr>`;",
        "const a = Row(['x', 'y']);",
        "const b = Row([f(1, [2]), `${g}`,]);",
        "const c = Row([...pair]);",
        "const d = Row(pair);",
        "const e = Row(['only one']);",
        "type T = ViewNodeOf<typeof Row>;",
      ].join("\n");
    const r = run(src);
    expect(r.calls).toBe(2);
    expect(r.code).toContain("const a = new $hbD0('x', 'y');");
    expect(r.code).toContain("const b = new $hbD0(f(1, [2]), `${g}`,);");
    expect(r.code).toContain("const c = Row([...pair]);");
    expect(r.code).toContain("const d = Row(pair);");
    expect(r.code).toContain("const e = Row(['only one']);");
  });

  test("a view name that is shadowed, reassignable or declared twice keeps its calls", () => {
    const shadow =
      IMPORT + "const Row = html`<p>${text()}</p>`;\nfunction f(Row: unknown) { return Row; }\nRow(['a']);";
    expect(run(shadow).calls).toBe(0);
    const twice =
      IMPORT +
      "{ const Row = html`<p>${text()}</p>`; Row(['a']); }\n{ const Row = html`<b>${text()}</b>`; Row(['b']); }";
    expect(run(twice).calls).toBe(0);
    const reassign = IMPORT + "let Row = html`<p>${text()}</p>`;\nRow(['a']);";
    expect(run(reassign).calls).toBe(0);
  });

  test("markup the HTML model cannot be sure of is left to run time, with a warning", () => {
    for (const markup of [
      "<p><div>${text()}</div></p>",
      "<table><tr><td>${text()}</td></tr></table>",
      "<ul><li>a<li>${text()}</ul>",
    ]) {
      const r = run(IMPORT + "const V = html`" + markup + "`;");
      expect(r.views).toBe(0);
      expect(r.warnings.length).toBe(1);
      expect(r.code).toContain("html`" + markup + "`");
    }
  });

  test("every module in web/src transforms to code that parses, with the same line count", () => {
    const dir = import.meta.dir;
    const tr = new Bun.Transpiler({ loader: "ts" });
    let views = 0;
    for (const f of readdirSync(dir, { recursive: true }) as string[]) {
      if (!f.endsWith(".ts") || f.includes("herdsman-blocks")) continue;
      const src = require("node:fs").readFileSync(join(dir, f), "utf8") as string;
      const r = transform(src, { runtime: RT, mistakes: "runtime" });
      expect({ f, errors: r.errors }).toEqual({ f, errors: [] });
      expect({ f, lines: lines(r.code) }).toEqual({ f, lines: lines(src) });
      if (r.changed) tr.transformSync(r.code);
      views += r.views;
    }
    expect(views).toBeGreaterThan(200);
  });
});

describe("generated code", () => {
  const gen = (strings: string[], markers: number[], isSvg = false): string => {
    const r = compileView(strings, markers, isSvg);
    if (!r.ok) throw new Error(r.message);
    return r.code;
  };

  test("a benchmark row: text, class and delegated handler holes", () => {
    expect(
      gen(
        [
          "<tr class=",
          '><td class="col-md-1">',
          '</td><td class="col-md-4"><a onclick=',
          ">",
          '</a></td><td class="col-md-1"><a onclick=',
          '><span class="glyphicon glyphicon-remove" aria-hidden="true"></span></a></td><td class="col-md-6"></td></tr>',
        ],
        [-2, -1, -6, -1, -6],
      ),
    ).toMatchSnapshot();
  });

  test("child, property, attribute and direct handler holes, with formatting whitespace", () => {
    expect(
      gen(
        [
          "<label\n  >",
          "<input\n    class=",
          "\n    .value=",
          "\n    onfocus=",
          "\n    aria-invalid=",
          "\n/>",
          "</label>",
        ],
        [-5, -2, -3, -6, -4, -1],
      ),
    ).toMatchSnapshot();
  });

  test("an svg view: class is an attribute, self-closing elements", () => {
    expect(gen(["<g class=", '><circle r="1"/>', "<path d=", " /></g>"], [-4, -5, -4], true)).toMatchSnapshot();
  });
});

describe("build errors", () => {
  test("a misplaced marker is an error at the hole's line and column", () => {
    const src = IMPORT + "\nconst A = svg`<path class=${cls()}></path>`;\nconst B = html`<p title=${text()}></p>`;\n";
    const r = run(src);
    expect(r.changed).toBe(false);
    expect(r.errors).toEqual([
      {
        line: 3,
        column: 27,
        message:
          "herdsman-blocks: hole 0 is typed class, but its position makes it attr: class on an SVG element is an attribute, use attr()",
      },
      { line: 4, column: 25, message: "herdsman-blocks: hole 0 is typed text, but its position makes it attr" },
    ]);
  });

  test("an event position takes only on(), and on() only an event position", () => {
    const r = run(IMPORT + "const A = html`<b onclick=${attr()}></b>`;\nconst B = html`<b>${on<'click'>()}</b>`;");
    expect(r.errors.map((e) => [e.line, e.message])).toEqual([
      [2, "herdsman-blocks: hole 0 is typed attr, but its position makes it event: an on* name takes on()"],
      [3, "herdsman-blocks: hole 0 is typed event, but its position makes it text"],
    ]);
  });

  test("a hole inside a tag that is not an attribute value, and a template without one root", () => {
    const r = run(
      IMPORT +
        "const A = html`<b ${attr()}></b>`;\nconst B = html`<b></b><i></i>`;\nconst C = html`<textarea>${text()}</textarea>`;",
    );
    expect(r.errors.map((e) => [e.line, e.message])).toEqual([
      [2, "herdsman-blocks: hole 0 is inside a tag but not an attribute value"],
      [3, "herdsman-blocks: a template must have exactly one root element"],
      [4, "herdsman-blocks: hole 0 could not be located"],
    ]);
  });

  test("herdsman-blocks' own templates get the same check", () => {
    const r = run('import { html, cls } from "./herdsman-blocks.ts";\nconst T = html`<b>${cls()}</b>`;');
    expect(r.errors.map((e) => [e.line, e.column, e.message])).toEqual([
      [2, 19, "herdsman-blocks: hole 0 is typed class, but its position makes it text"],
    ]);
  });

  test("the plugin fails the build with file:line:column", async () => {
    const dir = mkdtempSync(join(tmpdir(), "hb-plugin-"));
    const file = join(dir, "entry.ts");
    writeFileSync(
      file,
      `import { cls, html, render } from ${JSON.stringify(join(import.meta.dir, "herdsman-blocks-view.ts"))};\n\nrender(document.body, html\`<b>\${cls()}</b>\`([""]));\n`,
    );
    let message = "";
    try {
      const r = await Bun.build({ entrypoints: [file], plugins: [herdsmanBlocks({ quiet: true })], throw: false });
      message = r.logs.map(String).join("\n");
      expect(r.success).toBe(false);
    } catch (e) {
      message = String(e);
    }
    expect(message).toContain(`${file}:3:31: herdsman-blocks: hole 0 is typed class, but its position makes it text`);
  });
});
