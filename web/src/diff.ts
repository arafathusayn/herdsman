// Reads a unified git patch: one section per file, and each section's lines
// with their old and new line numbers.

export interface DiffLine {
  kind: "add" | "del" | "ctx" | "hunk" | "note";
  old?: number;
  new?: number;
  text: string;
}

const header = "diff --git ";
const hunkPattern = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)$/;

const escapes: Record<string, number> = { a: 7, b: 8, t: 9, n: 10, v: 11, f: 12, r: 13, '"': 34, "\\": 92 };

/**
 * Reads one path as git writes it. Even with core.quotePath=false, git
 * C-quotes a name with a double quote, a backslash or a control character;
 * the escapes are bytes, so octal ones decode as UTF-8. `end` is where the
 * path stopped, for a header that has two.
 */
export function readQuoted(s: string): { path: string; end: number } {
  if (!s.startsWith('"')) return { path: s, end: s.length };
  const bytes: number[] = [];
  const enc = new TextEncoder();
  let i = 1;
  for (; i < s.length && s[i] !== '"'; i++) {
    const c = s[i] ?? "";
    if (c !== "\\") {
      bytes.push(...enc.encode(c));
      continue;
    }
    const next = s[++i] ?? "";
    const oct = /^[0-3][0-7]{2}/.exec(s.slice(i, i + 3));
    if (oct) {
      bytes.push(parseInt(oct[0], 8));
      i += 2;
    } else bytes.push(escapes[next] ?? next.charCodeAt(0));
  }
  return { path: new TextDecoder().decode(new Uint8Array(bytes)), end: i + 1 };
}

/** A ---/+++ path without its a/ or b/ side; "" for /dev/null. */
function sidePath(s: string, side: "a/" | "b/"): string {
  // Git ends an unquoted name that has a space with a tab; a name with a tab
  // of its own is always quoted, so the tab is never part of the name.
  const p = readQuoted(s.startsWith('"') ? s : s.replace(/\t$/, "")).path;
  return p.startsWith(side) ? p.slice(2) : "";
}

/** The path a section's lines name: the new path, or the old one for a deleted file. */
function sectionPath(lines: string[]): string {
  let minus = "";
  for (const l of lines) {
    if (l.startsWith("@@")) break;
    if (l.startsWith("rename to ")) return readQuoted(l.slice("rename to ".length)).path;
    if (l.startsWith("copy to ")) return readQuoted(l.slice("copy to ".length)).path;
    if (l.startsWith("+++ ")) {
      const p = sidePath(l.slice(4), "b/");
      if (p) return p;
    }
    if (l.startsWith("--- ")) minus = sidePath(l.slice(4), "a/") || minus;
  }
  if (minus) return minus;
  // No ---/+++ lines (a binary or mode-only change): "diff --git a/p b/p",
  // or with both sides quoted.
  const rest = (lines[0] ?? "").slice(header.length);
  if (rest.startsWith('"')) {
    const a = readQuoted(rest);
    return sidePath(rest.slice(a.end + 1), "b/") || a.path.slice(2);
  }
  const plain = rest.slice(2);
  const n = (plain.length - 3) / 2;
  if (Number.isInteger(n) && plain.slice(n, n + 3) === " b/" && plain.slice(0, n) === plain.slice(n + 3))
    return plain.slice(0, n);
  return plain;
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
