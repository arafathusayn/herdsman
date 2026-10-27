import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";

// A28: the app names no model and no effort level; models and efforts come
// from the harness's catalog or the user's choice.
const pattern =
  /gpt-[0-9]|\bopus\b|sonnet|haiku|\bfable\b|muse-spark|claude-[a-z]+-[0-9]|"(xhigh|high|medium|low|max|ultra)"/i;

test("no source file names a model or an effort level", () => {
  const hits: string[] = [];
  for (const file of new Bun.Glob("**/*.ts").scanSync({ cwd: import.meta.dir })) {
    if (file.endsWith(".test.ts")) continue;
    readFileSync(`${import.meta.dir}/${file}`, "utf8")
      .split("\n")
      .forEach((line, i) => {
        if (pattern.test(line)) hits.push(`${file}:${i + 1}: ${line.trim()}`);
      });
  }
  expect(hits).toEqual([]);
});
