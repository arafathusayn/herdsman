// Reads a unified git patch: one section per file, and each section's lines
// with their old and new line numbers.

export interface DiffLine {
  kind: "add" | "del" | "ctx" | "hunk" | "note";
  old?: number;
  new?: number;
  text: string;
}

const header = "diff --git a/";
const hunkPattern = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)$/;

/** The path a section's lines name: the new path, or the old one for a deleted file. */
function sectionPath(lines: string[]): string {
  let minus = "";
  for (const l of lines) {
    if (l.startsWith("@@")) break;
    if (l.startsWith("rename to ")) return l.slice("rename to ".length);
    if (l.startsWith("copy to ")) return l.slice("copy to ".length);
    if (l.startsWith("+++ b/")) return l.slice(6);
    if (l.startsWith("--- a/")) minus = l.slice(6);
  }
  if (minus) return minus;
  // No ---/+++ lines (a binary or mode-only change): "diff --git a/p b/p".
  const rest = (lines[0] ?? "").slice(header.length);
  const n = (rest.length - 3) / 2;
  if (Number.isInteger(n) && rest.slice(n, n + 3) === " b/" && rest.slice(0, n) === rest.slice(n + 3))
    return rest.slice(0, n);
  return rest;
}

/** Splits a patch into its file sections, keyed by path. */
export function splitPatch(patch: string): Map<string, string[]> {
  const out = new Map<string, string[]>();
  let cur: string[] | null = null;
  const flush = () => {
    if (cur) out.set(sectionPath(cur), cur);
  };
  for (const line of patch.split("\n")) {
    if (line.startsWith(header)) {
      flush();
      cur = [line];
    } else cur?.push(line);
  }
  flush();
  return out;
}

/** The lines of one file section, numbered; header lines other than notes are left out. */
export function parseSection(lines: string[]): DiffLine[] {
  const out: DiffLine[] = [];
  let o = 0;
  let n = 0;
  let inHunk = false;
  for (const l of lines) {
    const m = hunkPattern.exec(l);
    if (m) {
      o = Number(m[1]);
      n = Number(m[2]);
      inHunk = true;
      out.push({ kind: "hunk", text: l });
      continue;
    }
    if (!inHunk) {
      if (l.startsWith("Binary files")) out.push({ kind: "note", text: "Binary file; no lines to show." });
      continue;
    }
    if (l.startsWith("+")) out.push({ kind: "add", new: n++, text: l.slice(1) });
    else if (l.startsWith("-")) out.push({ kind: "del", old: o++, text: l.slice(1) });
    else if (l.startsWith(" ")) out.push({ kind: "ctx", old: o++, new: n++, text: l.slice(1) });
    else if (l.startsWith("\\")) out.push({ kind: "note", text: l.slice(2) });
  }
  return out;
}

/** One row of the side-by-side view: a hunk or note across both sides, or a line on either side. */
export type SplitRow = { kind: "hunk" | "note"; text: string } | { left: DiffLine | null; right: DiffLine | null };

/**
 * Lays diff lines out side by side, as editors show a two-pane diff: a
 * context line on both sides, and each run of removed lines next to the run
 * of added lines that follows it, line for line, with blanks where one run
 * is longer.
 */
export function sideBySide(lines: DiffLine[]): SplitRow[] {
  const out: SplitRow[] = [];
  let dels: DiffLine[] = [];
  let adds: DiffLine[] = [];
  const flush = () => {
    for (let i = 0; i < Math.max(dels.length, adds.length); i++)
      out.push({ left: dels[i] ?? null, right: adds[i] ?? null });
    dels = [];
    adds = [];
  };
  for (const l of lines) {
    if (l.kind === "del") {
      // A removal after additions starts a new change.
      if (adds.length) flush();
      dels.push(l);
    } else if (l.kind === "add") adds.push(l);
    else {
      flush();
      if (l.kind === "ctx") out.push({ left: l, right: l });
      else out.push({ kind: l.kind, text: l.text });
    }
  }
  flush();
  return out;
}
