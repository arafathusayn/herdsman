import { expect, test } from "bun:test";

import { languageOf, type Piece, tokenizeLine, tokenizeLines } from "./highlight.ts";

/** The non-plain pieces of a line, for short assertions. */
function marks(pieces: Piece[]): string[] {
  return pieces.filter(([k]) => k).map(([k, t]) => `${k}:${t}`);
}

test("languageOf reads extensions, names and fence infos", () => {
  expect(languageOf("internal/web/diff.go")).toBe("go");
  expect(languageOf("web/src/main.ts")).toBe("ts");
  expect(languageOf("Makefile")).toBe("make");
  expect(languageOf("Dockerfile.dev")).toBe("docker");
  expect(languageOf(".goreleaser.yaml")).toBe("yaml");
  expect(languageOf("bash")).toBe("sh");
  expect(languageOf("go.sum")).toBe("");
  expect(languageOf("notes.weird")).toBe("");
});

test("Go: keywords, types, strings, numbers, calls and comments", () => {
  const { pieces } = tokenizeLine(
    "go",
    'func Load(dir string) error { n := 0x1F; return fmt.Errorf("x %d", n) } // done',
    null,
  );
  expect(marks(pieces)).toEqual([
    "keyword:func",
    "func:Load",
    "type:string",
    "type:error",
    "number:0x1F",
    "keyword:return",
    "func:Errorf",
    'string:"x %d"',
    "comment:// done",
  ]);
});

test("block comments and raw strings carry over lines", () => {
  const lines = tokenizeLines("go", ["a := `one", "two` /* x", "y */ b"]);
  expect(marks(lines[0] ?? [])).toEqual(["string:`one"]);
  expect(marks(lines[1] ?? [])).toEqual(["string:two`", "comment:/* x"]);
  expect(marks(lines[2] ?? [])).toEqual(["comment:y */"]);
});

test("escaped quotes stay inside the string", () => {
  expect(marks(tokenizeLine("ts", 'const s = "a \\" b"; x', null).pieces)).toEqual([
    "keyword:const",
    'string:"a \\" b"',
  ]);
});

test("TypeScript: types after keywords and capitalized names", () => {
  expect(marks(tokenizeLine("ts", "class Hub extends Base { x: Map<string, Entry> }", null).pieces)).toEqual([
    "keyword:class",
    "type:Hub",
    "keyword:extends",
    "type:Base",
    "type:Map",
    "type:string",
    "type:Entry",
  ]);
});

test("JSON keys differ from values", () => {
  expect(marks(tokenizeLine("json", '{"name": "herdsman", "n": 2, "ok": true}', null).pieces)).toEqual([
    'property:"name"',
    'string:"herdsman"',
    'property:"n"',
    "number:2",
    'property:"ok"',
    "constant:true",
  ]);
});

test("YAML keys, comments and shell variables", () => {
  expect(marks(tokenizeLine("yaml", "  - name: build # the job", null).pieces)).toEqual([
    "property:name",
    "comment:# the job",
  ]);
  expect(marks(tokenizeLine("sh", 'echo "$HOME" ${X} # hi', null).pieces)).toEqual([
    "constant:echo",
    'string:"$HOME"',
    "variable:${X}",
    "comment:# hi",
  ]);
});

test("Markdown headings, code spans and links", () => {
  expect(marks(tokenizeLine("md", "## Plan", null).pieces)).toEqual(["keyword:## Plan"]);
  expect(marks(tokenizeLine("md", "See `make web` and [docs](x.md).", null).pieces)).toEqual([
    "string:`make web`",
    "func:docs",
    "property:x.md",
  ]);
});

test("an unknown language is one plain piece", () => {
  expect(tokenizeLine("", "<b>x</b>", null).pieces).toEqual([["", "<b>x</b>"]]);
});
