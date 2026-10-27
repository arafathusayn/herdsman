// The compiler's scanner under fuzzing: random token soups, hard cases and
// mutations of every real module in web/src. For every input the transform
// must not throw, must keep the line count, and must change nothing but its
// own edits: the generated head on line 1, view literals, `Name([` ... `])`
// of direct calls, and import specifiers of the view module. Bun's own parser
// is the oracle that each edit sits in code (not in a string, comment,
// template text or regular expression) and that the output still parses.
import { describe, expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

import { transform, type TransformResult } from "./herdsman-blocks-compiler.ts";

const RT = "/rt/herdsman-blocks-compiled.ts";
const tr = new Bun.Transpiler({ loader: "ts" });
const parses = (s: string): boolean => {
  try {
    tr.transformSync(s);
    return true;
  } catch {
    return false;
  }
};
const lines = (s: string): number => s.split("\n").length;

let seed = Number(process.env["FUZZ_SEED"] ?? 0x5eed); // FUZZ_SEED=n bun test ... explores other inputs
const rnd = (n: number): number => {
  seed = (seed * 1103515245 + 12345) >>> 0;
  return (seed >>> 8) % n;
};
const pick = <T>(xs: readonly T[]): T => xs[rnd(xs.length)]!;

// A tag (maybe ns.html), then whitespace or comments, then the template.
const GAP = String.raw`(?:\s|//[^\n]*\n|/\*[\s\S]*?\*/)*`;
const LITERAL = new RegExp(String.raw`^[A-Za-z_$][\w$]*(?:${GAP}\.${GAP}(?:html|svg))?${GAP}\`[\s\S]*\`$`);
const CALL_HEAD = /^[A-Za-z_$][\w$]*\s*\(\s*\[$/;
const CALL_TAIL = /^\]\s*\)$/;
const SPEC = /^["'][^"'\n]*herdsman-blocks-view(?:\.ts|\.js)?["']$/;

/** Check the invariants of one transform; `deep` also asks Bun's parser where each edit sits. */
function check(src: string, deep: boolean): TransformResult {
  let r: TransformResult;
  try {
    r = transform(src, { runtime: RT, mistakes: "runtime" });
  } catch (e) {
    throw new Error(`transform threw on:\n${src}\n${String(e)}`);
  }
  if (!r.changed) {
    expect(r.code).toBe(src);
    return r;
  }
  expect(lines(r.code)).toBe(lines(src));
  // Rebuild the output from the source and the reported edits: nothing else moved.
  let out = src.slice(0, r.at) + r.head,
    at = r.at;
  for (const e of r.edits) {
    if (e.a < at) throw new Error(`overlapping edits at ${e.a}: ${JSON.stringify(src.slice(e.a, e.b))}`);
    out += src.slice(at, e.a) + e.text;
    at = e.b;
  }
  out += src.slice(at);
  expect(out).toBe(r.code);
  for (let i = 0; i < r.edits.length; i++) {
    const e = r.edits[i]!,
      was = src.slice(e.a, e.b);
    const kind = LITERAL.test(was)
      ? "literal"
      : CALL_HEAD.test(was)
        ? "head"
        : CALL_TAIL.test(was)
          ? "tail"
          : SPEC.test(was)
            ? "spec"
            : "";
    if (kind === "")
      throw new Error(`an edit outside the compiler's own kinds: ${JSON.stringify(was)} -> ${JSON.stringify(e.text)}`);
    // Each edit keeps its newlines, so later lines keep their numbers.
    expect(e.text.split("\n").length).toBe(was.split("\n").length);
  }
  if (!deep || !parses(src)) return r;
  expect(parses(r.code)).toBe(true);
  // An unbalanced parenthesis put in place of a literal (or a call's head)
  // breaks the module only if the span really is code.
  for (const e of r.edits) {
    const was = src.slice(e.a, e.b);
    if (!LITERAL.test(was)) continue;
    if (parses(src.slice(0, e.a) + "((0" + src.slice(e.b)))
      throw new Error(`a literal edit that is not code: ${JSON.stringify(was)}`);
  }
  return r;
}

const IMPORTS = [
  'import { html, svg, text, cls, attr, prop, on, child } from "./herdsman-blocks-view.ts";\n',
  'import * as V from "./herdsman-blocks-view";\n',
  'import { html as h, text as t } from "../herdsman-blocks-view.js";\n',
  'import { html as core, cls as ccls } from "./herdsman-blocks.ts";\n',
  'import type { View } from "./herdsman-blocks-view.ts";\n',
];

// Fragments chosen to stress the lexer: quotes, template parts, braces,
// slashes (regex or division), comments, keywords before a slash, tags.
const SOUP = [
  "html`<p>${text()}</p>`",
  "svg`<g class=${cls()}></g>`",
  "V.html`<b>${V.text()}</b>`",
  "h`<i>${t()}</i>`",
  "core`<p class=${cls()}>${0}</p>`",
  "html`<p>${html`<i>${text()}</i>`}</p>`",
  "`a${`b${'}'}`}c`",
  "`${{a:1}.a}`",
  "`",
  "${",
  "}",
  "{",
  "(",
  ")",
  "[",
  "]",
  "'`'",
  '"${"',
  "'\\''",
  '"\\""',
  "/`/g",
  "/[/`]/",
  "a / b / c",
  "x++ / 2",
  ") / 2",
  "] / 2",
  "return /x/",
  "typeof /x/",
  "// html`<p>${text()}</p>`\n",
  "/* ` ${ */",
  "/*",
  "*/",
  "\n",
  " ",
  ";",
  ",",
  "=",
  "=>",
  "const Row = ",
  "Row(['a'])",
  "Row([a, b])",
  "Row([...x])",
  "obj.html`<p>${0}</p>`",
  "String.raw`\\u{`",
  "#priv",
  "1e3",
  "0x1F",
  ".5",
  "html",
  "?.",
  "??",
  "<T,>(x: T) => x",
  "html`<p>\\${text()}</p>`",
  "html`<p>${0}</p>`",
  "html`<p onclick=${on<'click'>()}></p>`",
];

// Statements that parse on their own (given IMPORTS[0], [1] and [3]), and gaps between them.
const STATEMENTS = [
  "const a = x.map((y) => y / 2), b = /`/.test(s), c = html`<p>${text()}</p>`;",
  "if (a) /`/.test(s);",
  "while (a) /[`/]/.exec(s);",
  "for (;;) /x/;",
  "const r = (1) / 2 / html`<p>${text()}</p>`.length;",
  "const t = `x ${`y ${html`<p>${text()}</p>`} ${String.raw`z${1}`}`}`;",
  "const u = html`<p>${child()}</p>`([`${html`<i>${text()}</i>`}`]);",
  "const s1 = '${html`<p>${text()}</p>`}'; // html`<p>${text()}</p>`\n",
  'const s2 = "html`<p>${text()}</p>`";',
  "/* html`<p>${text()}</p>` */",
  "const o = `${(() => { return { x: `}` }; })().x}`;",
  "const d = b\n/ c / 2;",
  'const e = html`<p title="\\`">\\${not a hole}${text()}</p>`;',
  "class K { #html = 1; m() { return this.#html ?? html`<p>${text()}</p>`; } }",
  "const n = 1_000, v = o?.html, w = html`<p>${text()}</p>`;",
  "const f = <T,>(x: T) => x;",
  "const g = html`<p>${text<number>()}</p>` as unknown;",
  "Row(['a', 'b']);",
  "Row([f(1, [2]), `${g}`]);",
  "const S = svg`<g class=${attr()}><path d=${attr()} /></g>`;",
  "const N = V.html`<b>${V.text()}</b>`;",
  "const C = core`<p class=${ccls()}>${0}</p>`;",
  "const P = html`<input value=${prop<HTMLInputElement, 'value'>()} onclick=${on<'click'>()} />`;",
  "const q = obj.html`<u>${0}</u>`;",
  "label: for (const k of ks) { if (k) continue label; }",
  "x = y++ / 2;",
  "z = [1] / 2;",
  "function h() { return /`/; }",
  "const tg = typeof /`/;",
  "do /`/.test(s); while (0);",
  "if (a) {} else /`/.test(s);",
];
const GAPS = ["\n", " ", "\n\n", "\n// `\n", "/* ${ */", "\r\n", "\t"];

describe("scanner fuzzing", () => {
  test("random token soups: no throw, only the compiler's own edits, lines kept", () => {
    let changed = 0;
    for (let n = 0; n < 4000; n++) {
      let src = pick(IMPORTS) + (rnd(3) === 0 ? pick(IMPORTS) : "");
      const len = 1 + rnd(24);
      for (let i = 0; i < len; i++) src += pick(SOUP) + (rnd(4) === 0 ? "\n" : " ");
      if (check(src, false).changed) changed++;
    }
    expect(changed).toBeGreaterThan(500);
  });

  test("random valid modules: Bun's parser agrees on where every edit sits", () => {
    let changed = 0;
    for (let n = 0; n < 1500; n++) {
      let src = IMPORTS[0]! + (rnd(2) === 0 ? IMPORTS[1]! : "") + (rnd(2) === 0 ? IMPORTS[3]! : "");
      const len = 1 + rnd(12);
      src += "const Row = html`<tr class=${cls()}><td>${text()}</td></tr>`;\n";
      // Each statement in its own block, so names never clash.
      for (let i = 0; i < len; i++) src += "{ " + pick(STATEMENTS) + " }" + pick(GAPS);
      expect({ src, parses: parses(src) }).toEqual({ src, parses: true });
      if (check(src, true).changed) changed++;
    }
    expect(changed).toBeGreaterThan(1000);
  });

  test("hard cases: every literal the compiler touches is code, everything else stays", () => {
    const cases = [
      // A regex holding a backtick, after various tokens.
      "const a = x.map((y) => y / 2), b = /`/.test(s), c = html`<p>${text()}</p>`;",
      "if (a) /`/.test(s); const c = html`<p>${text()}</p>`;",
      "const r = (1) / 2 / html`<p>${text()}</p>`;",
      // Template literals nested in substitutions, tagged ones too.
      "const a = `x ${`y ${html`<p>${text()}</p>`} ${String.raw`z${1}`}`}`;",
      "const a = html`<p>${child()}</p>`([`${html`<i>${text()}</i>`}`]);",
      // Strings and comments that look like views.
      "const a = '${html`<p>${text()}</p>`}'; // html`<p>${text()}</p>`\nconst b = \"html`\";",
      "/* html`<p>${text()}</p>` */ const b = html`<b>${text()}</b>`;",
      // Braces in substitutions: object literals, blocks, arrow bodies.
      "const a = `${(() => { return { x: `}` }; })().x}`, b = html`<p>${text()}</p>`;",
      // A division that ends a line before a template.
      "const a = b\n/ c / html`<p>${text()}</p>`;",
      // Escapes and \\${ in the markup.
      'const a = html`<p title="\\`">\\${not a hole}${text()}</p>`;',
      // Private names, optional chaining, numeric separators around a view.
      "class K { #html = 1; m() { return this.#html ?? html`<p>${text()}</p>`; } }",
      "const n = 1_000, v = o?.html, w = html`<p>${text()}</p>`;",
      // Generics with arrows, satisfies, as.
      "const f = <T,>(x: T) => x; const v = html`<p>${text<number>()}</p>` as unknown;",
    ];
    for (const c of cases) {
      const src = IMPORTS[0] + c + "\n";
      expect({ c, parses: parses(src) }).toEqual({ c, parses: true });
      const r = check(src, true);
      expect({ c, changed: r.changed }).toEqual({ c, changed: true });
    }
  });

  test("mutations of every module in web/src: no throw, only the compiler's own edits", () => {
    const dir = import.meta.dir;
    const files = (readdirSync(dir, { recursive: true }) as string[]).filter(
      (f) => f.endsWith(".ts") && !f.includes("node_modules"),
    );
    let deep = 0;
    for (const f of files) {
      const src = readFileSync(join(dir, f), "utf8");
      check(src, true);
      deep++;
      for (let m = 0; m < 12; m++) {
        const a = rnd(src.length + 1),
          b = Math.min(src.length, a + rnd(80));
        let mut: string;
        switch (rnd(4)) {
          case 0:
            mut = src.slice(0, a) + src.slice(b); // delete a span
            break;
          case 1:
            mut = src.slice(0, b) + src.slice(a, b) + src.slice(b); // duplicate a span
            break;
          case 2:
            mut = src.slice(0, a) + pick(SOUP) + src.slice(a); // insert a fragment
            break;
          default:
            mut = src.slice(0, a) + src.slice(b, b + (b - a)) + src.slice(a, b) + src.slice(b + (b - a)); // swap two spans
        }
        check(mut, m === 0);
      }
    }
    expect(deep).toBeGreaterThan(80);
  });
});
