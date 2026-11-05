import { expect, test } from "bun:test";

import { findChrome } from "./chrome.ts";

const none = () => false;
const noWhich = () => null;

test("CHROME wins over every install place", () => {
  expect(findChrome({ CHROME: "/opt/chrome/chrome" }, () => true, noWhich)).toBe("/opt/chrome/chrome");
});

test("Linux install places are found without a macOS path", () => {
  const exists = (p: string) => p === "/usr/bin/chromium";
  expect(findChrome({}, exists, noWhich)).toBe("/usr/bin/chromium");
});

test("a Chrome on PATH is found when no install place exists", () => {
  const which = (n: string) => (n === "google-chrome-stable" ? "/home/me/bin/google-chrome-stable" : null);
  expect(findChrome({}, none, which)).toBe("/home/me/bin/google-chrome-stable");
});

test("no Chrome anywhere names CHROME in the error", () => {
  expect(() => findChrome({}, none, noWhich)).toThrow(/set CHROME/);
});
