import { expect, test } from "bun:test";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

// A28: the app names no model and no effort level; models and efforts come
// from the harness's catalog or the user's choice.
const pattern =
  /gpt-[0-9]|\bopus\b|sonnet|haiku|\bfable\b|muse-spark|claude-[a-z]+-[0-9]|"(xhigh|high|medium|low|max|ultra)"/i;

function sourceHits(dir: string): string[] {
  const hits: string[] = [];
  for (const file of new Bun.Glob("**/*.{ts,tsx}").scanSync({ cwd: dir })) {
    if (/\.test\.tsx?$/.test(file)) continue;
    readFileSync(join(dir, file), "utf8")
      .split("\n")
      .forEach((line, i) => {
        if (pattern.test(line)) hits.push(`${file}:${i + 1}: ${line.trim()}`);
      });
  }
  return hits;
}

test("no source file names a model or an effort level", () => {
  expect(sourceHits(import.meta.dir)).toEqual([]);
});

test("the source guard checks TSX and excludes both TS and TSX tests", () => {
  const dir = mkdtempSync(join(tmpdir(), "herdsman-source-guard-"));
  try {
    writeFileSync(join(dir, "component.tsx"), 'export const choice = "high";');
    writeFileSync(join(dir, "component.test.tsx"), 'const choice = "low";');
    writeFileSync(join(dir, "component.test.ts"), 'const choice = "medium";');
    expect(sourceHits(dir)).toEqual(['component.tsx:1: export const choice = "high";']);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
