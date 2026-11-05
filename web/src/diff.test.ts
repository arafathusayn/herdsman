import { expect, test } from "bun:test";

import { type DiffLine, parseSection, sideBySide, splitPatch } from "./diff.ts";

const patch = [
  "diff --git a/keep.txt b/keep.txt",
  "index 1..2 100644",
  "--- a/keep.txt",
  "+++ b/keep.txt",
  "@@ -1,3 +1,4 @@ func main() {",
  " one",
  "-two",
  "+2",
  " three",
  "+four",
  "\\ No newline at end of file",
  "diff --git a/old name.txt b/new name.txt",
  "similarity index 90%",
  "rename from old name.txt",
  "rename to new name.txt",
  "diff --git a/gone.txt b/gone.txt",
  "deleted file mode 100644",
  "--- a/gone.txt",
  "+++ /dev/null",
  "@@ -1 +0,0 @@",
  "-bye",
  "diff --git a/img.bin b/img.bin",
  "new file mode 100644",
  "Binary files /dev/null and b/img.bin differ",
  "",
].join("\n");

test("splitPatch keys each section by its path", () => {
  expect([...splitPatch(patch).keys()]).toEqual(["keep.txt", "new name.txt", "gone.txt", "img.bin"]);
});

// Headers as git writes them with core.quotePath=false: names with a quote,
// a backslash or a tab are C-quoted; other names, UTF-8 included, are not.
test("splitPatch reads quoted paths", () => {
  const quoted = [
    'diff --git "a/back\\\\slash.txt" "b/new\\"name.txt"',
    "similarity index 100%",
    'rename from "back\\\\slash.txt"',
    'rename to "new\\"name.txt"',
    'diff --git "a/q\\"uote.txt" "b/q\\"uote.txt"',
    "index 1..2 100644",
    '--- "a/q\\"uote.txt"',
    '+++ "b/q\\"uote.txt"',
    "@@ -1 +1,2 @@",
    " x",
    "+b",
    'diff --git "a/ta\\tb.txt" "b/ta\\tb.txt"',
    "deleted file mode 100644",
    '--- "a/ta\\tb.txt"',
    "+++ /dev/null",
    "@@ -1 +0,0 @@",
    "-y",
    'diff --git "a/bell\\007.bin" "b/bell\\007.bin"',
    "new file mode 100644",
    'Binary files /dev/null and "b/bell\\007.bin" differ',
    "diff --git a/é.txt b/é.txt",
    "--- a/é.txt",
    "+++ b/é.txt",
    "@@ -1 +1,2 @@",
    " w",
    "+b",
    'diff --git "a/\\303\\251\\\\x.txt" "b/\\303\\251\\\\x.txt"',
    "new file mode 100644",
    "--- /dev/null",
    '+++ "b/\\303\\251\\\\x.txt"',
    "@@ -0,0 +1 @@",
    "+v",
    "",
  ].join("\n");
  const sections = splitPatch(quoted);
  expect([...sections.keys()]).toEqual([
    'new"name.txt',
    'q"uote.txt',
    "ta\tb.txt",
    "bell\x07.bin",
    "é.txt",
    "é\\x.txt",
  ]);
  expect(parseSection(sections.get('q"uote.txt') ?? []).filter((l) => l.kind === "add")).toEqual([
    { id: 2, kind: "add", new: 2, text: "b" },
  ]);
});

test("parseSection numbers old and new lines", () => {
  const lines = parseSection(splitPatch(patch).get("keep.txt") ?? []);
  expect(lines).toEqual([
    { id: 0, kind: "hunk", text: "@@ -1,3 +1,4 @@ func main() {" },
    { id: 1, kind: "ctx", old: 1, new: 1, text: "one" },
    { id: 2, kind: "del", old: 2, text: "two" },
    { id: 3, kind: "add", new: 2, text: "2" },
    { id: 4, kind: "ctx", old: 3, new: 3, text: "three" },
    { id: 5, kind: "add", new: 4, text: "four" },
    { id: 6, kind: "note", text: "No newline at end of file" },
  ]);
});

test("parseSection notes binary files", () => {
  expect(parseSection(splitPatch(patch).get("img.bin") ?? [])).toEqual([
    { id: 0, kind: "note", text: "Binary file; no lines to show." },
  ]);
});

test("sideBySide pairs each removed run with the added run after it", () => {
  const lines: DiffLine[] = [
    { id: 0, kind: "hunk", text: "@@ -1,5 +1,5 @@" },
    { id: 1, kind: "ctx", old: 1, new: 1, text: "a" },
    { id: 2, kind: "del", old: 2, text: "b" },
    { id: 3, kind: "del", old: 3, text: "c" },
    { id: 4, kind: "add", new: 2, text: "B" },
    { id: 5, kind: "ctx", old: 4, new: 3, text: "d" },
    { id: 6, kind: "add", new: 4, text: "e" },
    { id: 7, kind: "add", new: 5, text: "f" },
    { id: 8, kind: "del", old: 5, text: "g" },
    { id: 9, kind: "note", text: "No newline at end of file" },
  ];
  const shape = sideBySide(lines).map((r) => ("kind" in r ? r.kind : `${r.left?.text ?? "_"}|${r.right?.text ?? "_"}`));
  expect(shape).toEqual(["hunk", "a|a", "b|B", "c|_", "d|d", "_|e", "_|f", "g|_", "note"]);
});

// Git ends an unquoted ---/+++ name that has a space with a tab.
test("splitPatch drops the tab after a name with a space", () => {
  const spaced = [
    "diff --git a/gone file.txt b/gone file.txt",
    "deleted file mode 100644",
    "--- a/gone file.txt\t",
    "+++ /dev/null",
    "@@ -1 +0,0 @@",
    "-a",
    "diff --git a/my file.txt b/my file.txt",
    "--- a/my file.txt\t",
    "+++ b/my file.txt\t",
    "@@ -1 +1,2 @@",
    " a",
    "+b",
    "",
  ].join("\n");
  expect([...splitPatch(spaced).keys()]).toEqual(["gone file.txt", "my file.txt"]);
});

test("every line and every side-by-side row has its own id, notes and repeated hunk headers included", () => {
  const lines = parseSection([
    "diff --git a/x b/x",
    "@@ -1 +1 @@",
    "-a",
    "\\ No newline at end of file",
    "+b",
    "\\ No newline at end of file",
    "@@ -1 +1 @@",
    "-a",
    "+b",
  ]);
  const ids = lines.map((l) => l.id);
  expect(new Set(ids).size).toBe(lines.length);
  expect(parseSection(["@@ -1 +1 @@", "-a", "+b"]).map((l) => l.id)).toEqual([0, 1, 2]); // the same each parse
  const rows = sideBySide(lines);
  expect(new Set(rows.map((r) => r.id)).size).toBe(rows.length);
});
