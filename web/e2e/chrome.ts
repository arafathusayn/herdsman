// Finds a Chrome or Chromium to drive headless: $CHROME, else the usual
// install places on macOS and Linux, else the first of the usual names on
// PATH. Never the user's open browser: puppeteer starts its own process with
// a fresh profile.
import { existsSync } from "node:fs";

const places = [
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
  "/Applications/Chromium.app/Contents/MacOS/Chromium",
  "/usr/bin/google-chrome",
  "/usr/bin/google-chrome-stable",
  "/usr/bin/chromium",
  "/usr/bin/chromium-browser",
];
const names = ["google-chrome", "google-chrome-stable", "chromium", "chromium-browser"];

export function findChrome(): string {
  const env = process.env["CHROME"];
  if (env) return env;
  for (const p of places) if (existsSync(p)) return p;
  for (const n of names) {
    const p = Bun.which(n);
    if (p) return p;
  }
  throw new Error("No Chrome found: install Chrome or Chromium, or set CHROME to its executable.");
}

/** Arguments for a headless run that also works in a CI container (no sandbox there). */
export function chromeArgs(): string[] {
  const args = ["--no-first-run", "--disable-extensions"];
  if (process.env["CI"]) args.push("--no-sandbox");
  return args;
}
