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

test("parseSection numbers old and new lines", () => {
  const lines = parseSection(splitPatch(patch).get("keep.txt") ?? []);
  expect(lines).toEqual([
    { kind: "hunk", text: "@@ -1,3 +1,4 @@ func main() {" },
    { kind: "ctx", old: 1, new: 1, text: "one" },
    { kind: "del", old: 2, text: "two" },
    { kind: "add", new: 2, text: "2" },
    { kind: "ctx", old: 3, new: 3, text: "three" },
    { kind: "add", new: 4, text: "four" },
    { kind: "note", text: "No newline at end of file" },
  ]);
});

test("parseSection notes binary files", () => {
  expect(parseSection(splitPatch(patch).get("img.bin") ?? [])).toEqual([
    { kind: "note", text: "Binary file; no lines to show." },
  ]);
});

test("sideBySide pairs each removed run with the added run after it", () => {
  const lines: DiffLine[] = [
    { kind: "hunk", text: "@@ -1,5 +1,5 @@" },
    { kind: "ctx", old: 1, new: 1, text: "a" },
    { kind: "del", old: 2, text: "b" },
    { kind: "del", old: 3, text: "c" },
    { kind: "add", new: 2, text: "B" },
    { kind: "ctx", old: 4, new: 3, text: "d" },
    { kind: "add", new: 4, text: "e" },
    { kind: "add", new: 5, text: "f" },
    { kind: "del", old: 5, text: "g" },
    { kind: "note", text: "No newline at end of file" },
  ];
  const shape = sideBySide(lines).map((r) => ("kind" in r ? r.kind : `${r.left?.text ?? "_"}|${r.right?.text ?? "_"}`));
  expect(shape).toEqual(["hunk", "a|a", "b|B", "c|_", "d|d", "_|e", "_|f", "g|_", "note"]);
});
